package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The learning loop was half a loop: every run was journaled (49 entries in
// the main brain on 2026-09-11) and nothing ever distilled them, because
// distillation only ran when someone typed /euclid distill. Nobody did. The
// registers stayed templates. Consolidation has to happen on its own.

func journalN(t *testing.T, cwd string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := captaincode.JournalRun(cwd, captaincode.JournalEntry{Kind: "worker", Task: "fix flaky test " + string(rune('a'+i)), Leg: "grok", Outcome: "ok", Files: []string{"pkg/a.go"}})
		require.NoError(t, err)
	}
}

func distillingBrain(t *testing.T, summary string) *brain {
	t.Helper()
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: `{"summary":"` + summary + `","edits":[{"file":"BRAIN.md","mode":"replace_section","anchor":"## Current state","text":"- Active front: ` + summary + `","why":"journal"}]}`}, nil
	}
	return b
}

func TestAutoDistillConsolidatesOnceEnoughRunsAccumulate(t *testing.T) {
	home := euclidTestHome(t)
	root := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(root, "main")
	require.NoError(t, err)
	t.Setenv("CAPTAIN_EUCLID_AUTODISTILL", "3")
	b := distillingBrain(t, "flaky tests hunted down")

	journalN(t, home, 2)
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()
	brain, _ := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	assert.NotContains(t, string(brain), "flaky tests", "below the threshold nothing runs: a distill is a model call")

	journalN(t, home, 1)
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()
	brain, _ = os.ReadFile(filepath.Join(root, "BRAIN.md"))
	assert.Contains(t, string(brain), "- Active front: flaky tests hunted down", "at the threshold the journal is distilled into the registers")
	assert.False(t, captaincode.LastDistilledAt(captaincode.EuclidBrain{Root: root}).IsZero(), "the cursor advanced")
}

func TestAutoDistillIsOffWhenAskedAndNeverRunsTwiceAtOnce(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	t.Setenv("CAPTAIN_EUCLID_AUTODISTILL", "0")
	calls := 0
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		calls++
		return leg, captaincode.Result{Text: `{"summary":"x","edits":[]}`}, nil
	}
	journalN(t, home, 50)
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()
	assert.Zero(t, calls, "CAPTAIN_EUCLID_AUTODISTILL=0 means never")
}

func TestAutoDistillEchoesTheSummaryIntoTheMainBrain(t *testing.T) {
	// A repo brain makes the developer subtree the write brain, so the main
	// brain would never hear about that project again. The main brain is
	// meant to hold the high-level view across projects: each consolidation of
	// a repo brain leaves one dated line there.
	home := euclidTestHome(t)
	main := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(main, "main")
	require.NoError(t, err)
	repo := filepath.Join(home, "Gits", "widget")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run(), "RepoRoot asks git, a bare .git dir is not enough")
	_, err = captaincode.Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	// the write brain of a repo is the developer's subtree, as init --repo creates it
	_, err = captaincode.Scaffold(filepath.Join(repo, ".euclid", "developers", "dev"), "developer")
	require.NoError(t, err)
	t.Setenv("CAPTAIN_CWD", repo)
	t.Setenv("EUCLID_HANDLE", "dev")
	t.Setenv("CAPTAIN_EUCLID_AUTODISTILL", "1")
	b := distillingBrain(t, "widget: storage layer migrated")

	journalN(t, repo, 1)
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()

	mem, _ := os.ReadFile(filepath.Join(main, "memory", "MEMORIES.md"))
	assert.Contains(t, string(mem), "widget: storage layer migrated", "the main brain records what happened in the repo")
	assert.Contains(t, strings.ToLower(string(mem)), "widget", "…and which repo it was")
	mainBrain, _ := os.ReadFile(filepath.Join(main, "BRAIN.md"))
	assert.NotContains(t, string(mainBrain), "storage layer", "the repo's own BRAIN edit stays in the repo's write brain")
}

func TestNewRepoBrainInheritsMainDoctrine(t *testing.T) {
	// SOUL is doctrine, not project knowledge: a fresh repo brain starts from
	// the main brain's SOUL and WISDOM when those are no longer templates,
	// instead of the blank template every time.
	home := euclidTestHome(t)
	main := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(main, "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(main, "SOUL.md"), []byte("# SOUL\n\n- Retrieval before memory.\n- Ship through the gate.\n"), 0o644))
	repo := filepath.Join(home, "Gits", "widget")
	require.NoError(t, os.MkdirAll(repo, 0o755))

	_, err = captaincode.Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)

	soul, _ := os.ReadFile(filepath.Join(repo, ".euclid", "SOUL.md"))
	assert.Contains(t, string(soul), "Ship through the gate", "doctrine carried over")
	vision, _ := os.ReadFile(filepath.Join(repo, ".euclid", "VISION.md"))
	assert.NotContains(t, string(vision), "Ship through", "VISION is per project and stays a template until bootstrapped")
}

func TestBootstrapFillsAFreshRepoBrainFromItsDocs(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	repo := filepath.Join(home, "Gits", "widget")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "docs"), 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Widget\n\nWidget is a billing engine for co-ops.\n"), 0o644))
	_, err = captaincode.Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	t.Setenv("CAPTAIN_CWD", repo)

	b := teamBrain()
	var seen string
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		seen = brief
		return leg, captaincode.Result{Text: `{"summary":"a billing engine for co-ops","edits":[{"file":"VISION.md","mode":"append","text":"- What the project is: a billing engine for co-ops","why":"README"},{"file":"MAP.md","mode":"append","text":"| docs | docs/ | roadmap |","why":"README"}]}`}, nil
	}

	_, report, err := b.bootstrap(defaultWorkspace(), true)
	require.NoError(t, err)
	assert.Contains(t, seen, "billing engine for co-ops", "the model saw the README")
	assert.Contains(t, report, "written: VISION.md, MAP.md")
	vision, _ := os.ReadFile(filepath.Join(repo, ".euclid", "VISION.md"))
	assert.Contains(t, string(vision), "billing engine for co-ops")
	assert.Empty(t, b.ledger.Events, "bootstrap is bookkeeping, never scored")
}
