package captaincode

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A snapshot captures the current routing policy with a unique fingerprint:
// two snapshots of the same state produce the same ID; different state does not.
func TestSnapshotPolicy_FingerprintStable(t *testing.T) {
	s1 := SnapshotPolicy("test")
	s2 := SnapshotPolicy("test")
	assert.Equal(t, s1.Fingerprint, s2.Fingerprint, "same state → same fingerprint")
	assert.Equal(t, s1.ID, s2.ID, "same state → same ID")
	assert.Equal(t, PolicySnapshotVersion, s1.Version)
	assert.Equal(t, Director, s1.Director)
	assert.Contains(t, s1.Fingerprint, "dir=")
	assert.NotEmpty(t, s1.Priors)
}

// Different director → different fingerprint.
func TestSnapshotPolicy_DifferentDirector(t *testing.T) {
	s1 := SnapshotPolicy("a")
	old := Director
	SetDirector(LegCodex)
	s2 := SnapshotPolicy("b")
	SetDirector(old)
	assert.NotEqual(t, s1.Fingerprint, s2.Fingerprint, "different director → different fingerprint")
	assert.NotEqual(t, s1.ID, s2.ID)
}

// Ledger stores and retrieves snapshots.
func TestLedger_RecordAndRetrieveSnapshot(t *testing.T) {
	l := &Ledger{}
	s := SnapshotPolicy("test")
	l.RecordSnapshot(s)
	got := l.SnapshotFor(s.ID)
	require.NotNil(t, got)
	assert.Equal(t, s.Name, got.Name)
}

// ActivateSnapshot marks one snapshot active and clears others.
func TestLedger_ActivateSnapshot(t *testing.T) {
	l := &Ledger{}
	s1 := SnapshotPolicy("a")
	s2 := SnapshotPolicy("b")
	l.RecordSnapshot(s1)
	l.RecordSnapshot(s2)
	l.ActivateSnapshot(s1.ID)
	assert.True(t, l.Snapshots[0].Active)
	assert.False(t, l.Snapshots[1].Active)
	l.ActivateSnapshot(s2.ID)
	assert.False(t, l.Snapshots[0].Active)
	assert.True(t, l.Snapshots[1].Active)
}

// AcceptSnapshot marks a snapshot accepted with a timestamp.
func TestLedger_AcceptSnapshot(t *testing.T) {
	l := &Ledger{}
	s := SnapshotPolicy("test")
	l.RecordSnapshot(s)
	at := time.Now()
	l.AcceptSnapshot(s.ID, at)
	got := l.SnapshotFor(s.ID)
	require.NotNil(t, got)
	assert.True(t, got.Accepted)
	assert.WithinDuration(t, at, got.AcceptedAt, time.Second)
}

// LastAcceptedSnapshot returns the most recently accepted.
func TestLedger_LastAcceptedSnapshot(t *testing.T) {
	l := &Ledger{}
	s1 := SnapshotPolicy("a")
	s2 := SnapshotPolicy("b")
	l.RecordSnapshot(s1)
	l.RecordSnapshot(s2)
	l.AcceptSnapshot(s1.ID, time.Now().Add(-1*time.Hour))
	l.AcceptSnapshot(s2.ID, time.Now())
	last := l.LastAcceptedSnapshot()
	require.NotNil(t, last)
	assert.Equal(t, s2.ID, last.ID, "most recently accepted wins")
}

// ActiveCanary returns the running canary; nil when none or stopped.
func TestLedger_ActiveCanary(t *testing.T) {
	l := &Ledger{}
	assert.Nil(t, l.ActiveCanary())
	c := Canary{
		ID:                "canary_1",
		CandidatePolicyID: "pol_abc",
		SampleRate:        0.5,
		MaxTasks:          10,
		StartedAt:         time.Now(),
	}
	l.RecordCanary(c)
	assert.NotNil(t, l.ActiveCanary(), "running canary is active")
	c.Stop("manual")
	l.RecordCanary(c)
	assert.Nil(t, l.ActiveCanary(), "stopped canary is not active")
}

// ShouldRoute respects sample rate, max tasks, and stop state.
func TestCanary_ShouldRoute(t *testing.T) {
	c := Canary{SampleRate: 1.0, MaxTasks: 3}
	rng := newRand(42)
	assert.True(t, c.ShouldRoute("t1", rng))
	assert.True(t, c.ShouldRoute("t2", rng))
	assert.True(t, c.ShouldRoute("t3", rng))
	c.TasksRouted = 3
	assert.False(t, c.ShouldRoute("t4", rng), "max tasks reached")

	c2 := Canary{SampleRate: 0.0}
	assert.False(t, c2.ShouldRoute("t1", rng), "zero rate → no routing")

	c3 := Canary{SampleRate: 1.0, MaxTasks: 5}
	c3.Stop("regression")
	assert.False(t, c3.ShouldRoute("t1", rng), "stopped → no routing")
}

// Stop records the first reason only.
func TestCanary_StopFirstReasonWins(t *testing.T) {
	c := Canary{SampleRate: 1.0}
	c.Stop("regression")
	c.Stop("manual")
	assert.Equal(t, "regression", c.StopReason, "first stop reason is kept")
}

// Holdout matches by domain, class, or task substring.
func TestHoldout_Matches(t *testing.T) {
	h := Holdout{
		Domains:    []Domain{DomainResearch},
		Classes:    []Class{ClassHigh},
		TaskSubstr: []string{"migration"},
	}
	assert.True(t, h.Matches(Decision{Domain: DomainResearch}), "domain match")
	assert.True(t, h.Matches(Decision{Class: ClassHigh}), "class match")
	assert.True(t, h.Matches(Decision{Task: "database migration plan"}), "task substring match")
	assert.False(t, h.Matches(Decision{Task: "fix typo", Class: ClassTrivial, Domain: DomainCode}), "no match")
}

// Promote produces a report: no regression when candidate equals active.
func TestPromote_NoRegression(t *testing.T) {
	active := SnapshotPolicy("active")
	cand := SnapshotPolicy("candidate")
	mk := func(i int) Decision {
		return Decision{Task: "fix bug", Class: ClassMedium, Domain: DomainCode, Chosen: LegGrok,
			Candidates: []Scored{{Leg: LegGrok, Quality: 8.0, Excluded: ""}}}
	}
	da := make([]Decision, 6)
	dc := make([]Decision, 6)
	for i := range da {
		da[i] = mk(i)
		dc[i] = mk(i)
	}
	ea := make([]Event, 6)
	ec := make([]Event, 6)
	for i := range ea {
		ea[i] = Event{Outcome: "ok"}
		ec[i] = Event{Outcome: "ok"}
	}
	report := Promote(active, cand, da, dc, ea, ec)
	assert.False(t, report.OverallRegressed, "same data → no regression")
	assert.Equal(t, "promote: no cohort regressed", report.Recommendation)
	assert.NotEmpty(t, report.Cohorts)
	assert.Equal(t, "overall", report.Cohorts[0].Label)
}

// Promote detects regression: candidate quality lower than active.
func TestPromote_DetectsRegression(t *testing.T) {
	active := SnapshotPolicy("active")
	cand := SnapshotPolicy("candidate")
	da := []Decision{
		{Task: "fix bug", Class: ClassMedium, Domain: DomainCode, Chosen: LegGrok,
			Candidates: []Scored{{Leg: LegGrok, Quality: 9.0, Excluded: ""}}},
	}
	dc := []Decision{
		{Task: "fix bug", Class: ClassMedium, Domain: DomainCode, Chosen: LegGrok,
			Candidates: []Scored{{Leg: LegGrok, Quality: 6.0, Excluded: ""}}},
	}
	ea := []Event{{Outcome: "ok"}}
	ec := []Event{{Outcome: "fail"}}
	report := Promote(active, cand, da, dc, ea, ec)
	assert.True(t, report.OverallRegressed, "candidate worse → regression")
	assert.Contains(t, report.Recommendation, "reject")
}

// Promote with fewer than 5 candidate decisions says insufficient.
func TestPromote_InsufficientData(t *testing.T) {
	active := SnapshotPolicy("active")
	cand := SnapshotPolicy("candidate")
	da := []Decision{{Task: "t", Class: ClassMedium, Domain: DomainCode, Chosen: LegGrok,
		Candidates: []Scored{{Leg: LegGrok, Quality: 8.0, Excluded: ""}}}}
	dc := da
	ea := []Event{{Outcome: "ok"}}
	ec := ea
	report := Promote(active, cand, da, dc, ea, ec)
	assert.False(t, report.OverallRegressed)
	assert.Contains(t, report.Recommendation, "insufficient")
}

// FormatSnapshot produces human-readable output.
func TestFormatSnapshot_NotEmpty(t *testing.T) {
	s := SnapshotPolicy("test")
	out := FormatSnapshot(s)
	assert.Contains(t, out, "test")
	assert.Contains(t, out, "director:")
	assert.Contains(t, out, "cost ref:")
}

// FormatCanary produces human-readable output.
func TestFormatCanary_NotEmpty(t *testing.T) {
	c := Canary{
		ID:                "canary_test",
		CandidatePolicyID: "pol_abc",
		ActivePolicyID:    "pol_xyz",
		SampleRate:        0.1,
		MaxTasks:          20,
		MaxCostUSD:        5.0,
		StartedAt:         time.Now(),
		TasksRouted:       5,
		CostUSD:           0.42,
	}
	out := FormatCanary(c)
	assert.Contains(t, out, "canary_test")
	assert.Contains(t, out, "10.0%")
	assert.Contains(t, out, "5 tasks")
}

// FormatPromotionReport produces human-readable output.
func TestFormatPromotionReport_NotEmpty(t *testing.T) {
	r := PromotionReport{
		ActivePolicyID:    "pol_a",
		CandidatePolicyID: "pol_b",
		Recommendation:    "promote: no cohort regressed",
		Cohorts:           []CohortResult{{Label: "overall", ActiveN: 10, CandN: 10, DeltaQ: 0.5}},
	}
	out := FormatPromotionReport(r)
	assert.Contains(t, out, "pol_a vs pol_b")
	assert.Contains(t, out, "promote")
	assert.Contains(t, out, "overall")
}

// RecentSnapshots returns newest-first.
func TestLedger_RecentSnapshotsNewestFirst(t *testing.T) {
	l := &Ledger{}
	s1 := SnapshotPolicy("old")
	s1.CreatedAt = time.Now().Add(-2 * time.Hour)
	s2 := SnapshotPolicy("new")
	s2.CreatedAt = time.Now()
	l.RecordSnapshot(s1)
	l.RecordSnapshot(s2)
	recent := l.RecentSnapshots()
	assert.Equal(t, s2.ID, recent[0].ID, "newest first")
	assert.Equal(t, s1.ID, recent[1].ID)
}

// Snapshot cap: old snapshots are evicted.
func TestLedger_SnapshotCap(t *testing.T) {
	l := &Ledger{}
	for i := 0; i < maxSnapshots+10; i++ {
		l.RecordSnapshot(SnapshotPolicy("s"))
	}
	assert.LessOrEqual(t, len(l.Snapshots), maxSnapshots, "snapshots are capped")
}

func newRand(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}
