package captaincode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The lifecycle logs had no cap. state.json reached 11.4 MB - 5.2 MB of it
// attempt_states - and Save() re-read, merged, re-marshalled and rewrote the
// whole file under the brain's global lock on every worker completion, three
// sidebar polls a second queueing behind it, until the brain answered
// nothing at all (2026-09-24). Finished rows are history; unfinished ones are
// recovery state and are never dropped.
func TestSaveTrimsFinishedLifecycleRowsAndKeepsLiveOnes(t *testing.T) {
	dir := t.TempDir()
	l := &Ledger{path: filepath.Join(dir, "state.json")}
	for i := 0; i < maxAttemptStates+250; i++ {
		l.AttemptStates = append(l.AttemptStates, AttemptState{AttemptID: "a" + string(rune(i)), State: StateSucceeded})
	}
	l.AttemptStates = append(l.AttemptStates, AttemptState{AttemptID: "live-1", State: StateRunning})
	for i := 0; i < maxTaskStates+120; i++ {
		l.TaskStates = append(l.TaskStates, TaskState{TaskID: "t" + string(rune(i)), State: StateSucceeded})
	}
	require.NoError(t, l.Save())

	assert.Equal(t, maxAttemptStates+1, len(l.AttemptStates), "the cap, plus the running attempt")
	assert.Equal(t, maxTaskStates, len(l.TaskStates))
	var live bool
	for _, a := range l.AttemptStates {
		if a.AttemptID == "live-1" {
			live = true
		}
	}
	assert.True(t, live, "a running attempt is recovery state - never dropped")

	st, err := os.Stat(l.path)
	require.NoError(t, err)
	assert.Less(t, st.Size(), int64(2<<20), "the file stays small enough to write under a lock")
}

// A save with no other writer must not re-read the file: the merge exists for
// OTHER processes, and parsing megabytes to find nothing is the cost.
func TestSaveSkipsTheMergeReadWhenNobodyElseWrote(t *testing.T) {
	dir := t.TempDir()
	l := &Ledger{path: filepath.Join(dir, "state.json")}
	l.Record(Event{Leg: LegClaude, Outcome: "ok"})
	require.NoError(t, l.Save())

	before := mergeReads.Load()
	l.Record(Event{Leg: LegGrok, Outcome: "ok"})
	require.NoError(t, l.Save())
	assert.Equal(t, before, mergeReads.Load(), "nothing changed on disk: no re-read")

	// Another writer appends: the next save must read and merge again.
	raw, err := os.ReadFile(l.path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(l.path, raw, 0o600))
	require.NoError(t, os.Chtimes(l.path, time.Now().Add(time.Second), time.Now().Add(time.Second)))
	require.NoError(t, l.Save())
	assert.Greater(t, mergeReads.Load(), before, "a file someone else touched is read")
}
