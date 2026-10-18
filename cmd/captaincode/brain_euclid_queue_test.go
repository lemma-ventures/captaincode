package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func queuedWorkspace(t *testing.T, home, name string) captaincode.Workspace {
	t.Helper()
	repo := filepath.Join(home, name)
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	_, err := captaincode.Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	_, err = captaincode.Scaffold(filepath.Join(repo, ".euclid", "developers", "tester"), "developer")
	require.NoError(t, err)
	return captaincode.Workspace{Dir: repo}
}

func TestAutoDistillQueuesOtherWorkspaces(t *testing.T) {
	home := euclidTestHome(t)
	t.Setenv("CAPTAIN_EUCLID_AUTODISTILL", "1")
	t.Setenv("CAPTAIN_EUCLID_CRYSTALLIZE", "0")
	first := queuedWorkspace(t, home, "project-a")
	second := queuedWorkspace(t, home, "project-b")
	journalN(t, first.Dir, 1)
	journalN(t, second.Dir, 1)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	b := teamBrain()
	var calls []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		calls = append(calls, prompt)
		if len(calls) == 1 {
			close(started)
			<-release
		}
		return leg, captaincode.Result{Text: `{"summary":"done","edits":[]}`}, nil
	}
	b.maybeAutoDistill(first)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first workspace did not start")
	}
	for i := 0; i < 3; i++ {
		b.maybeAutoDistill(second)
	}
	secondBrain, _ := captaincode.WriteBrain(second.Dir)
	assert.Equal(t, "queued", b.autoDistillStatus(secondBrain.Root))
	unblock()
	b.awaitAutoDistill()
	require.Len(t, calls, 2, "the second workspace must run without another task completion")
	assert.Contains(t, calls[0], first.Dir)
	assert.Contains(t, calls[1], second.Dir)
	for _, ws := range []captaincode.Workspace{first, second} {
		wb, ok := captaincode.WriteBrain(ws.Dir)
		require.True(t, ok)
		assert.Zero(t, captaincode.LearnInputsOf(wb).PendingJournal)
	}
}

func TestAutoDistillFailureDoesNotStartSuccessCooldown(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	t.Setenv("CAPTAIN_EUCLID_AUTODISTILL", "1")
	journalN(t, home, 1)
	b := teamBrain()
	calls := 0
	b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		calls++
		if calls == 1 {
			return leg, captaincode.Result{}, fmt.Errorf("temporary provider failure")
		}
		return leg, captaincode.Result{Text: `{"summary":"done","edits":[]}`}, nil
	}
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()
	wb, _ := captaincode.WriteBrain(home)
	assert.Contains(t, b.autoDistillStatus(wb.Root), "temporary provider failure")
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()
	assert.Equal(t, 2, calls)
	assert.Zero(t, captaincode.LearnInputsOf(wb).PendingJournal)
	assert.Contains(t, b.autoDistillStatus(wb.Root), "last completed")
}

func TestAutoDistillResumesAfterCooldownWithoutAnotherTask(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	t.Setenv("CAPTAIN_EUCLID_AUTODISTILL", "1")
	b := distillingBrain(t, "updated")
	b.autoDistill.cooldown = 40 * time.Millisecond
	journalN(t, home, 1)
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()
	journalN(t, home, 1)
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()
	wb, _ := captaincode.WriteBrain(home)
	assert.Zero(t, captaincode.LearnInputsOf(wb).PendingJournal)
}

func TestEuclidStatusExplainsCandidateLearning(t *testing.T) {
	st := euclidStatus{
		Brains:       []captaincode.EuclidBrain{{Root: "/example/brain", Label: "developer", Writable: true}},
		Journal:      map[string]int{"developer": 7},
		Learning:     map[string]string{"developer": "queued"},
		LearningMode: "mcp_candidates",
	}
	text := renderEuclidStatus(st)
	assert.Contains(t, text, "7 pending learning events")
	assert.Contains(t, text, "learning queued")
	assert.Contains(t, text, "does not rewrite the Markdown persona cards")
	st.LearningErrors = map[string]string{"developer": "connection unavailable"}
	assert.Contains(t, renderEuclidStatus(st), "learning inputs failed: connection unavailable")
}

func TestAutoDistillDrainsBacklogAcrossWindows(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	t.Setenv("CAPTAIN_EUCLID_AUTODISTILL", "1")
	b := distillingBrain(t, "updated")
	b.autoDistill.cooldown = time.Millisecond
	journalN(t, home, 45)
	b.maybeAutoDistill(defaultWorkspace())
	b.awaitAutoDistill()
	wb, _ := captaincode.WriteBrain(home)
	assert.Zero(t, captaincode.LearnInputsOf(wb).PendingJournal)
}
