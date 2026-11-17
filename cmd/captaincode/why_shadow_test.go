package main

import (
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func TestWhyShadowFlagsRefuseCombos(t *testing.T) {
	if err := whyShadowFlags(false, "", "", "in.jsonl"); err != nil {
		t.Error("shadow alone is fine:", err)
	}
	if err := whyShadowFlags(false, "out.jsonl", "", "in.jsonl"); err == nil {
		t.Error("export+shadow must refuse")
	}
	if err := whyShadowFlags(false, "", "in.jsonl", "in2.jsonl"); err == nil {
		t.Error("score+shadow must refuse")
	}
	if err := whyShadowFlags(true, "", "", "in.jsonl"); err == nil {
		t.Error("share+shadow must refuse")
	}
	if err := whyShadowFlags(true, "out.jsonl", "", ""); err == nil {
		t.Error("share+export must still refuse")
	}
	if err := whyShadowFlags(false, "", "in.jsonl", ""); err != nil {
		t.Error("score alone is fine:", err)
	}
}

// shadowDatasetRows builds a two-slice dataset: four tasks before the
// cutoff and two after it, with one observed preference each way.
func shadowDatasetRows(day time.Time) []captaincode.DatasetRow {
	rows := []captaincode.DatasetRow{
		{TaskID: "t1", PromptSHA: "s1", Leg: captaincode.LegGrok, Chosen: captaincode.LegGrok, Attempt: 1, RunOutcome: "ok", Domain: "code", Label: captaincode.AcceptanceAccepted, Settled: true, Cost: 0.10, CostStatus: captaincode.UsageMeasured, At: day},
		{TaskID: "t2", PromptSHA: "s2", Leg: captaincode.LegGrok, Chosen: captaincode.LegGrok, Attempt: 1, RunOutcome: "ok", Domain: "code", Label: captaincode.AcceptanceAccepted, Settled: true, Cost: 0.10, CostStatus: captaincode.UsageMeasured, At: day.Add(time.Hour)},
		{TaskID: "t3", PromptSHA: "s3", Leg: captaincode.LegGrok, Chosen: captaincode.LegGrok, Attempt: 1, RunOutcome: "ok", Domain: "code", Label: captaincode.AcceptanceAccepted, Settled: true, Cost: 0.10, CostStatus: captaincode.UsageMeasured, At: day.Add(2 * time.Hour)},
		{TaskID: "t4", PromptSHA: "s4", Leg: captaincode.LegClaude, Chosen: captaincode.LegClaude, Attempt: 1, RunOutcome: "fail", Domain: "code", Label: captaincode.AcceptanceRejected, Settled: true, At: day.Add(3 * time.Hour)},
		{TaskID: "t4", PromptSHA: "s4", Leg: captaincode.LegGrok, Chosen: captaincode.LegClaude, Attempt: 2, RunOutcome: "ok", Domain: "code", Label: captaincode.AcceptanceRejected, Settled: true, At: day.Add(4 * time.Hour)},
		{TaskID: "h1", PromptSHA: "s5", Leg: captaincode.LegClaude, Chosen: captaincode.LegClaude, Attempt: 1, RunOutcome: "fail", Domain: "code", Label: captaincode.AcceptanceAccepted, Settled: true, At: day.Add(6 * 24 * time.Hour)},
		{TaskID: "h1", PromptSHA: "s5", Leg: captaincode.LegGrok, Chosen: captaincode.LegClaude, Attempt: 2, RunOutcome: "ok", Domain: "code", Label: captaincode.AcceptanceAccepted, Settled: true, At: day.Add(6*24*time.Hour + time.Hour)},
		{TaskID: "h2", PromptSHA: "s6", Leg: captaincode.LegGrok, Chosen: captaincode.LegGrok, Attempt: 1, RunOutcome: "ok", Domain: "code", Label: captaincode.AcceptanceAccepted, Settled: true, Cost: 0.20, CostStatus: captaincode.UsageMeasured, At: day.Add(7 * 24 * time.Hour)},
	}
	return rows
}

func TestShadowDatasetReport(t *testing.T) {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	out := shadowDataset(shadowDatasetRows(day), time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"routing dataset shadow-evaluated, cutoff 2026-10-05",
		"model: 4 training tasks",
		"the held-out slice never trains it",
		"training slice: 4 tasks",
		"held-out slice: 2 tasks",
		"policy     agree  covered  first  later  unrun  unpick  labelled  accepted  acceptance  cost/accept  fallback  try-ok     pair-order",
		"head to head, where a policy and the director chose different legs and both ran:",
		"claim: no claim",
		"10 needed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q:\n%s", want, out)
		}
	}
	// The director agrees with itself on every task it had a leg for.
	if !strings.Contains(out, "director") {
		t.Errorf("report must name the director:\n%s", out)
	}
}

func TestShadowDatasetRefusals(t *testing.T) {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if out := shadowDataset(shadowDatasetRows(day)[:1], time.Time{}); !strings.Contains(out, "spans one day or less") {
		t.Errorf("a one-day dataset must ask for a cutoff:\n%s", out)
	}
	all := shadowDatasetRows(day)
	out := shadowDataset(all, day.Add(90*24*time.Hour))
	if !strings.Contains(out, "empty slice") {
		t.Errorf("a cutoff that empties a slice must say so:\n%s", out)
	}
	out = shadowDataset(all, day.Add(-time.Hour))
	if !strings.Contains(out, "empty slice") {
		t.Errorf("a cutoff before every task must say so:\n%s", out)
	}
}

func TestShadowDatasetDefaultCutoff(t *testing.T) {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rows := shadowDatasetRows(day)
	out := shadowDataset(rows, time.Time{})
	want := captaincode.DefaultDatasetCutoff(rows).Format("2006-01-02")
	if want == "0001-01-01" {
		t.Fatal("the fixture must span more than one day, so the default split applies")
	}
	if !strings.Contains(out, "cutoff "+want) {
		t.Errorf("the default cutoff is %s (70%% of the span):\n%s", want, out)
	}
	if !strings.Contains(out, "training slice: 4 tasks") || !strings.Contains(out, "held-out slice: 2 tasks") {
		t.Errorf("the default split must leave both slices populated:\n%s", out)
	}
}
