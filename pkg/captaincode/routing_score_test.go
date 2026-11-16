package captaincode

import (
	"testing"
	"time"
)

func scoreRow(task, leg string, at time.Time, label AcceptanceStatus, cost float64, status UsageStatus, attempt int) DatasetRow {
	r := DatasetRow{
		TaskID:     task,
		PromptSHA:  "sha-" + task,
		Leg:        Leg(leg),
		Attempt:    attempt,
		At:         at,
		Cost:       cost,
		CostStatus: status,
		Label:      label,
	}
	if label != "" {
		r.Settled = true
	}
	return r
}

func TestSplitDatasetByDateBoundary(t *testing.T) {
	cutoff := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rows := []DatasetRow{
		scoreRow("a", "codex", cutoff.Add(-24*time.Hour), "", 0, "", 1),
		scoreRow("b", "codex", cutoff, "", 0, "", 1),
		scoreRow("c", "codex", cutoff.Add(24*time.Hour), "", 0, "", 1),
	}
	before, after := SplitDatasetByDate(rows, cutoff)
	if len(before.Rows) != 1 || len(after.Rows) != 2 {
		t.Fatalf("split sizes: before=%d after=%d", len(before.Rows), len(after.Rows))
	}
	if before.Rows[0].TaskID != "a" {
		t.Errorf("before holds %s, want a", before.Rows[0].TaskID)
	}
	if !after.From.Equal(cutoff) {
		t.Errorf("held-out starts at %s, want the cutoff %s", after.From, cutoff)
	}
}

func TestDefaultDatasetCutoffFraction(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var rows []DatasetRow
	for day := 0; day <= 10; day++ {
		rows = append(rows, scoreRow("t", "codex", start.Add(time.Duration(day)*24*time.Hour), "", 0, "", 1))
	}
	got := DefaultDatasetCutoff(rows)
	want := start.Add(7 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("cutoff %s, want %s (70%% of a 10-day span)", got, want)
	}
	one := []DatasetRow{
		scoreRow("a", "codex", start, "", 0, "", 1),
		scoreRow("b", "codex", start.Add(time.Hour), "", 0, "", 1),
	}
	if !DefaultDatasetCutoff(one).IsZero() {
		t.Error("a one-day span must not split")
	}
}

func TestScoreDatasetSplitAcceptsAndCosts(t *testing.T) {
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rows := []DatasetRow{
		// task a: accepted, two attempts on two legs, each leg pays its own
		scoreRow("a", "grok", day, AcceptanceAccepted, 0.10, UsageMeasured, 1),
		scoreRow("a", "codex", day.Add(time.Hour), AcceptanceAccepted, 0.20, UsageMeasured, 2),
		// task b: rejected on codex
		scoreRow("b", "codex", day.Add(2*time.Hour), AcceptanceRejected, 0.30, UsageMeasured, 1),
		// task c: accepted on grok
		scoreRow("c", "grok", day.Add(3*time.Hour), AcceptanceAccepted, 0.05, UsageMeasured, 1),
		// task d: settled without a label - labeled nothing, costs nothing
		scoreRow("d", "codex", day.Add(4*time.Hour), "", 0.40, UsageMeasured, 1),
	}
	s := ScoreDatasetSplit(DatasetSplit{Name: "scoring", Rows: rows})
	if s.Tasks != 4 || s.Labeled != 3 || s.Accepted != 2 || s.Rejected != 1 {
		t.Fatalf("overall counts: %+v", s)
	}
	if s.AcceptanceRate != 2.0/3.0 {
		t.Errorf("acceptance rate %.3f, want 0.667", s.AcceptanceRate)
	}
	if s.CostUSD < 0.349 || s.CostUSD > 0.351 || !s.HasCost {
		t.Errorf("accepted cost $%.4f hasCost=%v, want $0.3500 (a+c, not the rejected or unlabeled)", s.CostUSD, s.HasCost)
	}
	if s.CostPerUSD < 0.1749 || s.CostPerUSD > 0.1751 {
		t.Errorf("cost per accepted $%.4f, want $0.1750", s.CostPerUSD)
	}
	byLeg := map[Leg]DatasetLegScore{}
	for _, l := range s.Legs {
		byLeg[l.Leg] = l
	}
	codex := byLeg["codex"]
	if codex.Labeled != 2 || codex.Accepted != 1 || codex.AcceptanceRate != 0.5 {
		t.Errorf("codex verdicts: %+v", codex)
	}
	// codex own cost on accepted tasks: its 0.20 attempt on task a only
	if codex.CostUSD != 0.20 || codex.CostPerUSD != 0.20 {
		t.Errorf("codex cost $%.4f per accepted $%.4f, want 0.2000 both (its own attempt only)", codex.CostUSD, codex.CostPerUSD)
	}
	grok := byLeg["grok"]
	// grok's verdict count comes from tasks it finished: only task c. Its
	// cost is its own attempts on accepted tasks: 0.10 on a plus 0.05 on c.
	if grok.Labeled != 1 || grok.Accepted != 1 || grok.AcceptanceRate != 1 {
		t.Errorf("grok verdicts: %+v", grok)
	}
	if grok.CostUSD < 0.1499 || grok.CostUSD > 0.1501 || grok.CostPerUSD < 0.1499 || grok.CostPerUSD > 0.1501 {
		t.Errorf("grok cost $%.4f per accepted $%.4f, want 0.1500 both (its own attempts on accepted tasks)", grok.CostUSD, grok.CostPerUSD)
	}
	if s.Legs[0].Leg != "grok" {
		t.Errorf("legs sort by acceptance first: %s leads, want grok", s.Legs[0].Leg)
	}
}

func TestScoreDatasetSplitBilledFallback(t *testing.T) {
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	rows := []DatasetRow{
		{TaskID: "x", Leg: "codex", At: day, Label: AcceptanceAccepted, Settled: true, TaskCost: 0.90, TaskBilled: true},
	}
	s := ScoreDatasetSplit(DatasetSplit{Name: "scoring", Rows: rows})
	if s.CostUSD != 0.90 || !s.HasCost {
		t.Fatalf("billed fallback: $%.4f hasCost=%v, want $0.9000 true", s.CostUSD, s.HasCost)
	}
	if s.Legs[0].HasCost {
		t.Error("per-leg cost must not invent attempt money: the leg has no attempt cost rows")
	}
}

func TestPriorConcordance(t *testing.T) {
	legs := []DatasetLegScore{
		{Leg: "claude", Labeled: 5, Accepted: 5, AcceptanceRate: 1.0},
		{Leg: "codex", Labeled: 5, Accepted: 3, AcceptanceRate: 0.6},
		{Leg: "grok", Labeled: 5, Accepted: 1, AcceptanceRate: 0.2},
		{Leg: "qwen", Labeled: 0}, // unlabeled: skipped
	}
	prior := func(l Leg) float64 {
		switch l {
		case "claude":
			return 9.5
		case "codex":
			return 6.0
		case "grok":
			return 6.0 // tied with codex: pair skipped
		case "qwen":
			return 5.0
		}
		return 0
	}
	conc, disc, pairs := PriorConcordance(legs, prior)
	// judged pairs: claude-codex and claude-grok (codex-grok tie their
	// priors and skip; qwen carries no verdict). Both are concordant.
	if conc != 2 || disc != 0 || pairs != 2 {
		t.Errorf("concordant=%d discordant=%d pairs=%d, want 2/0/2", conc, disc, pairs)
	}
	legs[0].AcceptanceRate = 0.5 // codex now beats claude despite a lower prior
	conc, disc, pairs = PriorConcordance(legs, prior)
	// claude-codex turns discordant; claude-grok stays concordant
	if conc != 1 || disc != 1 || pairs != 2 {
		t.Errorf("flipped: concordant=%d discordant=%d pairs=%d, want 1/1/2", conc, disc, pairs)
	}
}
