package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		if strings.Contains(brief, "[euclid crystallize]") {
			return leg, captaincode.Result{Text: `{"summary":"","edits":[]}`}, nil
		}
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
	t.Setenv("CAPTAIN_EUCLID_CRYSTALLIZE", "0")
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

func TestCrystallizeFoldsMainPersonaAfterEnoughEchoes(t *testing.T) {
	home := euclidTestHome(t)
	main := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(main, "main")
	require.NoError(t, err)
	repo := filepath.Join(home, "Gits", "widget")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	_, err = captaincode.Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	_, err = captaincode.Scaffold(filepath.Join(repo, ".euclid", "developers", "dev"), "developer")
	require.NoError(t, err)
	t.Setenv("CAPTAIN_CWD", repo)
	t.Setenv("EUCLID_HANDLE", "dev")
	t.Setenv("CAPTAIN_EUCLID_AUTODISTILL", "1")
	t.Setenv("CAPTAIN_EUCLID_CRYSTALLIZE", "1")
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if strings.Contains(brief, "[euclid crystallize]") {
			return leg, captaincode.Result{Text: `{"summary":"folded","edits":[{"file":"INTUITION.md","mode":"append","text":"- [open] widget storage is the risky path","why":"hunch"}]}`}, nil
		}
		return leg, captaincode.Result{Text: `{"summary":"widget: storage layer migrated","edits":[{"file":"BRAIN.md","mode":"replace_section","anchor":"## Current state","text":"- Active front: widget storage","why":"journal"}]}`}, nil
	}
	journalN(t, repo, 1)
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()
	intu, err := os.ReadFile(filepath.Join(main, "INTUITION.md"))
	require.NoError(t, err)
	assert.Contains(t, string(intu), "widget storage is the risky path")
	assert.Equal(t, 1, captaincode.LastCrystallizedCount(captaincode.EuclidBrain{Root: main}))
	mainBrain, _ := os.ReadFile(filepath.Join(main, "BRAIN.md"))
	assert.NotContains(t, string(mainBrain), "widget storage", "the repo distill does not write the main BRAIN; crystallize did not either")
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

// Live 2026-10-19: the learning state rejected every commit in a busy repo,
// and each finished task sent the same batch to the model again (180 calls).
// One failure still retries on the next task; from the second in a row a
// failure waits, longer each time.
func TestAutoDistillWaitsAfterRepeatedFailuresAndBacksOff(t *testing.T) {
	b := teamBrain()
	st := &b.autoDistill
	st.pending, st.last, st.failed, st.fails = map[string]captaincode.Workspace{}, map[string]time.Time{}, map[string]string{}, map[string]int{}
	st.wake = make(chan struct{}, 1)
	st.cooldown = time.Hour
	root := filepath.Join(t.TempDir(), "brain") // no write brain at ws.Dir: the run fails before any model call
	run := func() {
		st.mu.Lock()
		st.pending[root] = captaincode.Workspace{Dir: t.TempDir()}
		st.order = append(st.order, root)
		st.running = true
		st.mu.Unlock()
		st.wg.Add(1)
		go b.drainAutoDistill()
	}
	fails := func() int { st.mu.Lock(); defer st.mu.Unlock(); return st.fails[root] }

	run()
	st.wg.Wait()
	assert.Equal(t, 1, fails())
	assert.True(t, st.last[root].IsZero(), "one failure retries on the next task")
	run()
	st.wg.Wait()
	assert.Equal(t, 2, fails())
	assert.False(t, st.last[root].IsZero(), "the second failure in a row starts the wait")
	assert.Contains(t, b.autoDistillStatus(root), "failed 2 time(s); retry after")

	run() // the next finished task queues it again: it waits instead of running
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 2, fails(), "no third attempt inside the wait")
	t.Setenv("CAPTAIN_EUCLID", "0") // stop the waiting drain
	st.wake <- struct{}{}
	st.wg.Wait()

	assert.Equal(t, time.Hour, st.delayFor(root))
	st.fails[root] = 4
	assert.Equal(t, 4*time.Hour, st.delayFor(root))
	st.fails[root] = 40
	assert.Equal(t, 16*time.Hour, st.delayFor(root), "capped")
}
