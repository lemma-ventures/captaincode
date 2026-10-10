package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scorecards forgot everything older than state.json's 500 events. They
// now read the journal's last 90 days, each event once.
func TestStatsReadTheJournalNotOnlyTheRingBuffer(t *testing.T) {
	dir := t.TempDir()
	l := NewLedger(filepath.Join(dir, "state.json"))
	for i := 0; i < 3; i++ {
		l.Record(Event{Leg: LegCursor, Outcome: "ok", Duration: 1000})
	}
	l.Events = l.Events[2:] // the ring buffer forgot two
	st := l.Stats()[LegCursor]
	assert.Equal(t, 3, st.N, "the journal remembers what the buffer dropped; the kept one counts once")

	old := Event{At: time.Now().Add(-200 * 24 * time.Hour), Leg: LegCursor, Outcome: "ok", Duration: 1000}
	appendRecord(t, filepath.Join(dir, "routing.jsonl"), old)
	assert.Equal(t, 3, l.Stats()[LegCursor].N, "older than CAPTAIN_STATS_DAYS: out of the window")
	t.Setenv("CAPTAIN_STATS_DAYS", "365")
	assert.Equal(t, 4, l.Stats()[LegCursor].N)
}

func TestJournalRotatesByMonthAndKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routing.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(`{"kind":"event"}`+"\n"), 0o600))
	sept := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	require.NoError(t, os.Chtimes(p, sept, sept))
	rotateJournal(p, sept.Add(48*time.Hour))
	_, err := os.Stat(filepath.Join(dir, "routing-2026-09.jsonl"))
	require.NoError(t, err, "September's journal is archived, whole")
	_, err = os.Stat(p)
	assert.True(t, os.IsNotExist(err), "October starts a new file")

	require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o600))
	rotateJournal(p, time.Now())
	_, err = os.Stat(p)
	assert.NoError(t, err, "the current month stays live")
	assert.Len(t, journalFiles(p), 2)
}

func TestImportAddsOnlyTheEventsTheJournalLacks(t *testing.T) {
	dir := t.TempDir()
	l := NewLedger(filepath.Join(dir, "state.json"))
	l.Record(Event{Leg: LegGrok, Outcome: "ok", Task: "kept"})
	backup := filepath.Join(dir, "backup.json")
	raw, _ := json.Marshal(map[string]any{"events": []Event{
		l.Events[0],
		{At: time.Now().Add(-30 * 24 * time.Hour), Leg: LegFree, Outcome: "ok", Task: "august"},
	}})
	require.NoError(t, os.WriteFile(backup, raw, 0o600))
	n, err := l.ImportEvents(backup)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, _ = l.ImportEvents(backup)
	assert.Zero(t, n, "a second import adds nothing")
	assert.Equal(t, 1, l.Stats()[LegFree].N, "the scorecards see the imported run")
}

func TestRecordNamesTheEffortTheTransportChose(t *testing.T) {
	l := NewLedger("")
	l.Record(Event{Leg: LegGrok, Outcome: "ok"})
	l.Record(Event{Leg: LegCursor, Outcome: "ok", Effort: EffortHigh})
	assert.Equal(t, EffortDefault, l.Events[0].Effort, "no blank effort: the run's tier is never a guess")
	assert.Equal(t, EffortHigh, l.Events[1].Effort)
	assert.Equal(t, TierQuality, TierOf(EffortDefault))
}

func appendRecord(t *testing.T, p string, e Event) {
	t.Helper()
	line, _ := json.Marshal(RoutingRecord{Kind: RoutingKindEvent, At: e.At, Event: &e})
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, _ = f.Write(append(line, '\n'))
	require.NoError(t, f.Close())
}
