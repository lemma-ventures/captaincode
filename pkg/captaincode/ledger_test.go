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

func TestThreadKeyIsScopedByLegAndDirectory(t *testing.T) {
	a := ThreadKey(LegCodex, "/repo/a")
	b := ThreadKey(LegCodex, "/repo/b")
	c := ThreadKey(LegClaude, "/repo/a")
	assert.NotEqual(t, a, b, "same leg, different project -> different thread")
	assert.NotEqual(t, a, c, "same project, different leg -> different thread")
}

func TestResetThreadsOnlyDropsCurrentDirectory(t *testing.T) {
	l := &Ledger{Threads: map[string]ThreadRef{
		ThreadKey(LegFree, "/repo/a"):  {SessionID: "ses_a_free"},
		ThreadKey(LegCodex, "/repo/a"): {SessionID: "ses_a_codex"},
		ThreadKey(LegFree, "/repo/b"):  {SessionID: "ses_b_free"},
	}}
	l.ResetThreads("/repo/a")
	assert.Empty(t, l.Threads[ThreadKey(LegFree, "/repo/a")].SessionID)
	assert.Empty(t, l.Threads[ThreadKey(LegCodex, "/repo/a")].SessionID)
	assert.Equal(t, "ses_b_free", l.Threads[ThreadKey(LegFree, "/repo/b")].SessionID, "other project's threads survive")
}

// ── team + class-conditioned scorecards (2026-07-19) ──
//
// Per-leg flat averages can't express "codex+glm fan-out beats claude solo on
// medium integration tasks": the ensemble needs its own scorecard, and both
// leg and team stats need a per-class dimension.

func TestTeamKeyIsCanonical(t *testing.T) {
	assert.Equal(t, "codex+glm", TeamKey([]Leg{LegGLM, LegCodex}), "sorted, order-independent")
	assert.Equal(t, "codex+glm", TeamKey([]Leg{LegCodex, LegGLM, LegCodex}), "duplicates collapse")
	assert.Equal(t, "", TeamKey([]Leg{LegCodex}), "a single leg is not a team")
}

func TestStatsSkipAggregateTeamEventsAndBreakDownByClass(t *testing.T) {
	l := &Ledger{Cooldowns: map[Leg]time.Time{}}
	l.Record(Event{Leg: LegCodex, Class: ClassMedium, Outcome: "ok", Quality: 8})
	l.Record(Event{Leg: LegCodex, Class: ClassHigh, Outcome: "ok", Quality: 5})
	l.Record(Event{Team: "codex+glm", Class: ClassMedium, Outcome: "ok", Quality: 9}) // aggregate: no leg
	s := l.Stats()
	_, hasEmpty := s[Leg("")]
	assert.False(t, hasEmpty, "aggregate team events must not create a phantom '' leg")
	codex := s[LegCodex]
	assert.Equal(t, 2, codex.N)
	if assert.NotNil(t, codex.ByClass) {
		assert.Equal(t, 8.0, codex.ByClass[ClassMedium].AvgQuality)
		assert.Equal(t, 5.0, codex.ByClass[ClassHigh].AvgQuality)
	}
}

func TestTeamStatsAggregate(t *testing.T) {
	l := &Ledger{Cooldowns: map[Leg]time.Time{}}
	l.Record(Event{Team: "codex+glm", Class: ClassMedium, Outcome: "ok", Quality: 9})
	l.Record(Event{Team: "codex+glm", Class: ClassMedium, Outcome: "ok", Quality: 7})
	l.Record(Event{Team: "free+glm", Class: ClassTrivial, Outcome: "ok", Quality: 6})
	l.Record(Event{Leg: LegCodex, Team: "codex+glm", Class: ClassMedium, Outcome: "ok", Quality: 8}) // per-worker event: not a team aggregate
	ts := l.TeamStats()
	if assert.Contains(t, ts, "codex+glm") {
		assert.Equal(t, 2, ts["codex+glm"].N, "only aggregate events (no leg) count as team runs")
		assert.InDelta(t, 8.0, ts["codex+glm"].AvgQuality, 0.01)
		assert.InDelta(t, 8.0, ts["codex+glm"].ByClass[ClassMedium].AvgQuality, 0.01)
	}
	assert.Contains(t, ts, "free+glm")
}

// Failures must be VISIBLE on the scorecard: glm stalled repeatedly on
// 2026-07-24/25 yet kept a spotless q9.0 because only ok-runs were recorded -
// so the director kept picking it ("GLM is top-scored"). Non-ok events count
// into Fails without polluting the quality/duration averages.
func TestStatsCountFailures(t *testing.T) {
	l := &Ledger{Cooldowns: map[Leg]time.Time{}}
	l.Record(Event{Leg: LegGLM, Class: ClassHigh, Outcome: "ok", Quality: 9})
	l.Record(Event{Leg: LegGLM, Class: ClassHigh, Outcome: "fail", Error: "worker stalled"})
	l.Record(Event{Leg: LegGLM, Class: ClassHigh, Outcome: "fail", Error: "worker stalled"})
	s := l.Stats()[LegGLM]
	assert.Equal(t, 1, s.N, "N stays ok-runs only - averages unpolluted")
	assert.Equal(t, 2, s.Fails, "failures visible")
	assert.Equal(t, 9.0, s.AvgQuality)
}

// Harness faults (our bugs) must never read as model unreliability: claude
// "failed" 15× during the keychain incident while being the best-rated leg
// (usage analysis F1/I4).
func TestStatsSeparateHarnessFromProviderFaults(t *testing.T) {
	l := &Ledger{Cooldowns: map[Leg]time.Time{}}
	l.Record(Event{Leg: LegClaude, Outcome: "fail", Error: "claude error: Not logged in · Please run /login"})
	l.Record(Event{Leg: LegCursor, Outcome: "fail", Error: "cursor-agent -p: signal: segmentation fault: ERROR: SecItemCopyMatching failed -50"})
	l.Record(Event{Leg: LegGrok, Outcome: "fail", Error: "xai/grok-build-0.1: worker stalled - no session activity for 4m0s"})
	l.Record(Event{Leg: LegGrok, Outcome: "ok", Task: "condense the abstract", Duration: 100})

	st := l.Stats()
	assert.Equal(t, 0, st[LegClaude].Fails, "auth failure is ours, not claude's")
	assert.Equal(t, 1, st[LegClaude].HarnessFails)
	assert.Equal(t, 0, st[LegCursor].Fails, "keychain segfault is ours")
	assert.Equal(t, 1, st[LegGrok].Fails, "a stall IS the provider's fault")
	assert.Equal(t, 0, st[LegGrok].HarnessFails)
}

// Every recorded event gets a triaged domain, so per-domain priors can be
// built from the ledger without re-parsing logs.
func TestRecordStampsTheDomain(t *testing.T) {
	l := &Ledger{Cooldowns: map[Leg]time.Time{}}
	l.Record(Event{Leg: LegGrok, Outcome: "ok", Task: "condense this paragraph, keep my style"})
	l.Record(Event{Leg: LegCodex, Outcome: "ok", Task: "write a unit test for the ledger Save function"})
	assert.Equal(t, "editorial", l.Events[0].Domain)
	assert.Equal(t, "code", l.Events[1].Domain)
}

// The brain and a CLI invocation both hold state.json. A CLI that loaded the
// file before the brain recorded a charge must not erase that charge when it
// saves its own stale copy - the eval's cost attribution depends on it.
func TestSaveMergesChargesAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	brain := &Ledger{Cooldowns: map[Leg]time.Time{}, path: path}
	brain.RecordCharge(Charge{ID: "brain-1", TaskID: "t1", Kind: KindCall, Leg: LegClaude})
	require.NoError(t, brain.Save())

	cli := &Ledger{Cooldowns: map[Leg]time.Time{}, path: path} // stale: loaded before brain.Save
	cli.RecordCharge(Charge{ID: "cli-1", TaskID: "t2", Kind: KindCall, Leg: LegGLM})
	require.NoError(t, cli.Save())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var disk Ledger
	require.NoError(t, json.Unmarshal(data, &disk))

	ids := map[string]bool{}
	for _, c := range disk.Charges {
		ids[c.ID] = true
	}
	assert.True(t, ids["brain-1"], "the brain's charge survives a stale CLI save")
	assert.True(t, ids["cli-1"], "the CLI's own charge is written")
	_, leftover := os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(leftover), "the atomic temp file is renamed away")
}

// An in-memory ledger (tests, throwaway brains) has no file and must not try
// to write to an empty path.
func TestSaveWithoutPathIsANoop(t *testing.T) {
	l := &Ledger{Cooldowns: map[Leg]time.Time{}}
	assert.NoError(t, l.Save())
}
