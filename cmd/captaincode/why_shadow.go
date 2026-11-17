package main

// `captain why --shadow <file>` - the offline shadow router, P7 step 3 (P7
// ownership decision dq-261117-f0191b). It splits an exported dataset by the
// same date rule as `--score`, trains a routing policy on the scoring slice,
// asks three policies for a leg on each task, and prints what those legs did
// on the held-out slice.
//
// A read over rows that already exist: no model call, no routing change, no
// spend, and a claim only where the held-out slice has enough tasks to carry
// one.

import (
	"errors"
	"fmt"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// whyShadowFlags refuses the combinations the shadow must not run in. The
// export and the score read the same journal differently, the score already
// owns the share guard, and the shadow always reads one dataset file.
func whyShadowFlags(share bool, export, score, shadow string) error {
	if err := whyScoreFlags(share, export, score); err != nil {
		return err
	}
	if shadow == "" {
		return nil
	}
	switch {
	case export != "":
		return errors.New("--shadow does not combine with --export")
	case score != "":
		return errors.New("--shadow does not combine with --score")
	case share:
		return errors.New("--shadow does not combine with --share")
	}
	return nil
}

// shadowDataset prints the report: one summary line of what trained, one
// table per slice, the head to head, and the claim those numbers carry.
func shadowDataset(rows []captaincode.DatasetRow, cutoff time.Time) string {
	if cutoff.IsZero() {
		cutoff = captaincode.DefaultDatasetCutoff(rows)
		if cutoff.IsZero() {
			return "captain: dataset spans one day or less - name a --cutoff YYYY-MM-DD to split it\n"
		}
	}
	train, held := captaincode.SplitDatasetByDate(rows, cutoff)
	if len(train.Rows) == 0 || len(held.Rows) == 0 {
		return fmt.Sprintf("captain: cutoff %s leaves an empty slice (%d and %d rows) - move the date\n",
			cutoff.Format("2006-01-02"), len(train.Rows), len(held.Rows))
	}
	report := captaincode.EvaluateShadow(train.Rows, held.Rows, cutoff)
	out := fmt.Sprintf("captain: routing dataset shadow-evaluated, cutoff %s\n", cutoff.Format("2006-01-02"))
	out += fmt.Sprintf("model: %d training tasks, %d legs, %d domains lifted from the scoring slice; the held-out slice never trains it\n",
		report.TrainedTasks, report.TrainedLegs, report.TrainedDomains)
	out += formatShadowSlice(report.Train)
	out += formatShadowSlice(report.HeldOut)
	out += "  head to head, where a policy and the director chose different legs and both ran:\n"
	for _, c := range report.HeldOut.Compares {
		out += fmt.Sprintf("    %-8s %4d tasks, policy ok %d, director ok %d\n",
			c.Policy, c.Tasks, c.PolicyOk, c.DirectorOk)
	}
	out += "  claim: " + report.Claim + "\n"
	return out
}

// formatShadowSlice is one slice's table. Every column counts tasks in that
// slice; a pick the policy did not have is unpick, a pick that never ran is
// unrun, and pair order is the ranking accuracy over the observed
// preferences inside one task.
func formatShadowSlice(eval captaincode.ShadowSliceEval) string {
	out := fmt.Sprintf("\n%s slice: %d tasks\n", eval.Name, eval.Tasks)
	out += "  policy     agree  covered  first  later  unrun  unpick  labelled  accepted  acceptance  cost/accept  fallback  try-ok     pair-order\n"
	for _, r := range eval.Runs {
		acceptance := "-"
		if r.Labeled > 0 {
			acceptance = fmt.Sprintf("%.1f%%", r.Acceptance*100)
		}
		costPer := "-"
		if r.HasCost && r.Accepted > 0 {
			costPer = fmt.Sprintf("$%.4f", r.CostPer)
		}
		fallback := "-"
		if r.Policy == captaincode.ShadowPolicyLearned {
			fallback = fmt.Sprintf("%d", r.Fallbacks)
		}
		tryOk := "-"
		if r.Attempts > 0 {
			tryOk = fmt.Sprintf("%d/%d", r.AttemptsOk, r.Attempts)
		}
		order := r.Rank.String()
		if r.Rank.Judged > 0 {
			order = fmt.Sprintf("%s (%d)", order, r.Rank.Judged)
		}
		out += fmt.Sprintf("  %-8s %6d %8d %6d %6d %5d %6d %8d %9d  %10s  %11s %9s %9s  %s\n",
			r.Policy, r.Agreement, r.Covered, r.First, r.Later, r.Uncovered, r.NoPick,
			r.Labeled, r.Accepted, acceptance, costPer, fallback, tryOk, order)
	}
	return out
}
