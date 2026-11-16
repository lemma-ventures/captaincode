package main

// `captain why --score <file>` - the offline router evaluation, P7 step 2
// (owner decision dq-261116-a61645). It reads a dataset that `captain why
// --export` wrote, splits it by date, and scores the director's actual
// routing and the hand priors on acceptance rate and cost per accepted
// task. A read over an existing file: no model call, no routing change.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// scoreLineLimit bounds one dataset row: a real row is well under 1 KiB,
// so a longer line is a file that is not a dataset.
const scoreLineLimit = 1 << 20

// whyScoreFlags refuses the flag combinations the score must not run in:
// the share report and an export answer different questions on the same
// journal, and the score reads a file instead.
func whyScoreFlags(share bool, export, score string) error {
	if err := whyExportFlags(share, export); err != nil {
		return err
	}
	if score != "" && export != "" {
		return errors.New("--score does not combine with --export")
	}
	if score != "" && share {
		return errors.New("--score does not combine with --share")
	}
	return nil
}

// parseScoreCutoff reads --cutoff as a UTC date (YYYY-MM-DD). An empty
// value keeps the default split.
func parseScoreCutoff(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, errors.New("--cutoff wants YYYY-MM-DD")
	}
	return t.UTC(), nil
}

// readDatasetRows reads a JSONL dataset. A malformed line fails with its
// line number; an empty file is not a dataset.
func readDatasetRows(path string) ([]captaincode.DatasetRow, error) {
	var r io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("dataset: %w", err)
		}
		defer f.Close()
		r = f
	}
	var rows []captaincode.DatasetRow
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), scoreLineLimit)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Bytes()
		if len(text) == 0 {
			return nil, fmt.Errorf("dataset line %d: empty line", line)
		}
		var row captaincode.DatasetRow
		if err := json.Unmarshal(text, &row); err != nil {
			return nil, fmt.Errorf("dataset line %d: %w", line, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("dataset: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("dataset is empty")
	}
	return rows, nil
}

// formatSplitScore renders one split: the director's overall figures, the
// per-leg table and the hand priors' agreement with the observed order.
func formatSplitScore(s captaincode.DatasetSplitScore, prior func(captaincode.Leg) float64) string {
	out := fmt.Sprintf("\n%s split: %d rows, %d tasks", s.Split.Name, len(s.Split.Rows), s.Tasks)
	if !s.Split.From.IsZero() {
		out += fmt.Sprintf(" (%s to %s)",
			s.Split.From.Format("2006-01-02"), s.Split.Until.Format("2006-01-02"))
	}
	out += "\n"
	if s.Labeled == 0 {
		out += "  no labeled verdicts: acceptance and cost figures need judged tasks\n"
		return out
	}
	out += fmt.Sprintf("  director: acceptance %.1f%% (%d/%d labeled), cost per accepted task $%.4f over %d accepted\n",
		s.AcceptanceRate*100, s.Accepted, s.Labeled, s.CostPerUSD, s.Accepted)
	out += "  leg      attem  tasks  labeled  accepted  acceptance  cost/accepted  prior\n"
	for _, l := range s.Legs {
		p := prior(l.Leg)
		priorCell := "-"
		if p > 0 {
			priorCell = fmt.Sprintf("%.1f", p)
		}
		costCell := "-"
		if l.Accepted > 0 && l.HasCost {
			costCell = fmt.Sprintf("$%.4f", l.CostPerUSD)
		}
		out += fmt.Sprintf("  %-8s %5d  %5d  %7d  %8d  %10.1f%%  %12s  %6s\n",
			string(l.Leg), l.Attempts, l.Tasks, l.Labeled, l.Accepted, l.AcceptanceRate*100, costCell, priorCell)
	}
	conc, disc, pairs := captaincode.PriorConcordance(s.Legs, prior)
	if pairs == 0 {
		out += "  priors: no judged leg pair with distinct priors\n"
	} else {
		out += fmt.Sprintf("  priors: %d/%d judged pairs concordant with observed acceptance (%d discordant)\n",
			conc, pairs, disc)
	}
	return out
}

// scoreDataset prints the split report. The default cutoff puts the first
// 70% of the observed time span in the scoring split; --cutoff overrides
// it with a UTC date.
func scoreDataset(rows []captaincode.DatasetRow, cutoff time.Time) string {
	if cutoff.IsZero() {
		cutoff = captaincode.DefaultDatasetCutoff(rows)
		if cutoff.IsZero() {
			return "captain: dataset spans one day or less - name a --cutoff YYYY-MM-DD to split it\n"
		}
	}
	before, after := captaincode.SplitDatasetByDate(rows, cutoff)
	if len(before.Rows) == 0 || len(after.Rows) == 0 {
		return fmt.Sprintf("captain: cutoff %s leaves an empty split (%d and %d rows) - move the date\n",
			cutoff.Format("2006-01-02"), len(before.Rows), len(after.Rows))
	}
	out := fmt.Sprintf("captain: routing dataset scored, cutoff %s\n", cutoff.Format("2006-01-02"))
	out += formatSplitScore(captaincode.ScoreDatasetSplit(before), captaincode.QualityPrior)
	out += formatSplitScore(captaincode.ScoreDatasetSplit(after), captaincode.QualityPrior)
	return out
}
