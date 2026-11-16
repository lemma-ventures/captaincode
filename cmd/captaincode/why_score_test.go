package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func TestWhyScoreFlagsRefuseCombos(t *testing.T) {
	if err := whyScoreFlags(true, "", ""); err != nil {
		t.Error("share alone is fine")
	}
	if err := whyScoreFlags(false, "out.jsonl", ""); err != nil {
		t.Error("export alone is fine")
	}
	if err := whyScoreFlags(true, "out.jsonl", ""); err == nil {
		t.Error("share+export must refuse")
	}
	if err := whyScoreFlags(true, "", "in.jsonl"); err == nil {
		t.Error("share+score must refuse")
	}
	if err := whyScoreFlags(false, "out.jsonl", "in.jsonl"); err == nil {
		t.Error("export+score must refuse")
	}
	if err := whyScoreFlags(false, "", "in.jsonl"); err != nil {
		t.Error("score alone is fine")
	}
}

func TestParseScoreCutoff(t *testing.T) {
	at, err := parseScoreCutoff("2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC); !at.Equal(want) {
		t.Fatalf("cutoff %s, want %s", at, want)
	}
	if at, err := parseScoreCutoff(""); err != nil || !at.IsZero() {
		t.Fatal("an empty cutoff keeps the default")
	}
	if _, err := parseScoreCutoff("november 10"); err == nil {
		t.Fatal("a non-date must fail")
	}
}

func writeScoreFixture(t *testing.T, rows []captaincode.DatasetRow) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dataset.jsonl")
	if err := captaincode.WriteRoutingDataset(path, rows); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadDatasetRows(t *testing.T) {
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	path := writeScoreFixture(t, []captaincode.DatasetRow{
		{TaskID: "a", Leg: "codex", At: day, Label: captaincode.AcceptanceAccepted, Settled: true},
		{TaskID: "b", Leg: "grok", At: day.Add(48 * time.Hour)},
	})
	rows, err := readDatasetRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].TaskID != "a" || rows[1].Leg != "grok" {
		t.Fatalf("rows: %+v", rows)
	}
	bad := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{\"task_id\": \"a\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDatasetRows(bad); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("malformed line must fail with its line number, got %v", err)
	}
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDatasetRows(empty); err == nil {
		t.Fatal("an empty file is not a dataset")
	}
}

func TestScoreDatasetReport(t *testing.T) {
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rows := []captaincode.DatasetRow{
		{TaskID: "a", Leg: "codex", At: day, Label: captaincode.AcceptanceAccepted, Settled: true, Cost: 0.10, CostStatus: captaincode.UsageMeasured},
		{TaskID: "b", Leg: "grok", At: day.Add(5 * 24 * time.Hour), Label: captaincode.AcceptanceRejected, Settled: true, Cost: 0.20, CostStatus: captaincode.UsageMeasured},
		{TaskID: "c", Leg: "grok", At: day.Add(6 * 24 * time.Hour), Label: captaincode.AcceptanceAccepted, Settled: true, Cost: 0.30, CostStatus: captaincode.UsageMeasured},
	}
	out := scoreDataset(rows, time.Time{})
	for _, want := range []string{
		"cutoff 2026-11-05",
		"scoring split: 1 rows, 1 tasks",
		"held-out split: 2 rows, 2 tasks",
		"cost per accepted task $0.1000",
		"$0.3000",
		"priors:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q:\n%s", want, out)
		}
	}
	fixed := scoreDataset(rows, day.Add(24*time.Hour))
	if !strings.Contains(fixed, "cutoff 2026-11-02") {
		t.Errorf("an explicit cutoff overrides the default:\n%s", fixed)
	}
	oneDay := scoreDataset(rows[:1], time.Time{})
	if !strings.Contains(oneDay, "spans one day or less") {
		t.Errorf("a one-day dataset must ask for a cutoff:\n%s", oneDay)
	}
	edge := scoreDataset(rows, day) // everything lands held-out
	if !strings.Contains(edge, "empty split") {
		t.Errorf("a cutoff that empties a split must say so:\n%s", edge)
	}
}
