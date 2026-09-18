package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Until now a solo or team turn left NOTHING on disk: an abandoned request, a
// brain crash or a worker answering into the void took the work with it, and
// the user could not ask "what did it actually say?" (live 2026-07-31).

func TestHistoryRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id1 := recordRunHistory(runRecord{Kind: "solo", Legs: []string{"grok"}, Task: "first task", Output: "first answer", DurationMs: 1200})
	time.Sleep(2 * time.Millisecond)
	id2 := recordRunHistory(runRecord{Kind: "workflow", Legs: []string{"grok", "claude"}, Task: "second task",
		Output: "second answer", DurationMs: 4300, Transcript: "/tmp/wf.md"})
	require.NotEmpty(t, id1)
	require.NotEmpty(t, id2)

	recs, err := readHistory(0)
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, id2, recs[0].ID, "newest first")
	assert.Equal(t, "second answer", recs[0].Output)

	got, ok := findRun("last")
	require.True(t, ok)
	assert.Equal(t, id2, got.ID)

	got, ok = findRun(id1)
	require.True(t, ok)
	assert.Equal(t, "first answer", got.Output)

	// A prefix resolves to the NEWEST matching run - ids share their timestamp
	// part, so "r0731-10" means "the latest run in that minute", not an error.
	got, ok = findRun(id2[:len(id2)-1])
	require.True(t, ok, "an id prefix resolves")
	assert.True(t, strings.HasPrefix(got.ID, id2[:len(id2)-1]))
	assert.Equal(t, id2, got.ID, "newest match wins")

	_, ok = findRun("nope")
	assert.False(t, ok)
}

func TestHistorySearchAndLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	recordRunHistory(runRecord{Kind: "solo", Task: "condense the abstract", Output: "short version"})
	recordRunHistory(runRecord{Kind: "solo", Task: "unrelated", Output: "nothing here"})
	recordRunHistory(runRecord{Kind: "team", Task: "grade the abstract", Output: "B+"})

	assert.Len(t, searchRuns("abstract", 0), 2, "matches the task text")
	assert.Len(t, searchRuns("short version", 0), 1, "and the output text")
	assert.Len(t, searchRuns("abstract", 1), 1, "limit is honored")

	recs, err := readHistory(2)
	require.NoError(t, err)
	assert.Len(t, recs, 2)
}

// A failed turn is exactly what the user needs to look up afterwards.
func TestFailedRunsAreRecorded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	recordRunHistory(runRecord{Kind: "solo", Legs: []string{"grok"}, Task: "the lost turn",
		Error: "grok: worker returned an empty answer", DurationMs: 90000})
	recs, err := readHistory(0)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Contains(t, recs[0].Error, "empty answer")
	assert.Contains(t, runLine(recs[0]), "✗")
}

// The wrapper must record what it served, so a turn the TUI dropped is still
// recoverable with `captain show`.
func TestSoloTurnIsRecordedByTheWrapper(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "THE ANSWER THE TUI LOST", DurationMs: 10}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "a question worth keeping"))
	require.Equal(t, 200, rec.Code)

	files, _ := filepath.Glob(filepath.Join(home, ".captaincode", "history", "*.jsonl"))
	require.Len(t, files, 1)
	body, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Contains(t, string(body), "THE ANSWER THE TUI LOST")
	assert.Contains(t, string(body), "a question worth keeping")

	got, ok := findRun("last")
	require.True(t, ok)
	assert.Equal(t, "solo", got.Kind)
	assert.Equal(t, "THE ANSWER THE TUI LOST", got.Output)
}

func TestWorkflowRunIsRecordedWithItsWorkers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "review by " + string(leg), DurationMs: 10}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGGREGATE"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok analyse it > /claude audit it"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	got, ok := findRun("last")
	require.True(t, ok)
	assert.Equal(t, "workflow", got.Kind)
	assert.Equal(t, "AGGREGATE", got.Output)
	assert.Contains(t, got.Legs, "grok")
	assert.Contains(t, got.Legs, "claude")
	require.NotEmpty(t, got.Workers)
	assert.Contains(t, got.Workers[0].Text, "review by claude", "the terminal worker's own text is kept")
	assert.NotEmpty(t, got.Transcript, "and points at the full transcript")
}
