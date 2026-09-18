package captaincode

import (
	"testing"
	"time"
)

// A candidate below τ must be REPORTED as excluded, not dropped: the whole
// point of the decision record is that "not chosen" and "not eligible" are
// different answers (ROADMAP M2.1).
func TestValueRankKeepsSubThresholdAsExclusion(t *testing.T) {
	rows := ValueRank(ClassMedium, DomainCode, AllLegs, map[Leg]LegStats{}, 8000, nil)
	var excluded int
	for _, r := range rows {
		if !r.Eligible() {
			excluded++
			if r.Excluded == "" {
				t.Fatalf("%s marked ineligible with no reason", r.Leg)
			}
		}
	}
	if excluded == 0 {
		t.Skip("every known leg clears the medium bar in this registry")
	}
	// Excluded rows sort after every eligible one, and Legs() still yields the
	// try-order the router walks - the ranking's behaviour is unchanged.
	seenExcluded := false
	for _, r := range rows {
		if !r.Eligible() {
			seenExcluded = true
			continue
		}
		if seenExcluded {
			t.Fatal("an eligible candidate sorted after an excluded one")
		}
	}
	for _, l := range Legs(rows) {
		for _, r := range rows {
			if r.Leg == l && !r.Eligible() {
				t.Fatalf("Legs() offered the excluded %s as a try-order rung", l)
			}
		}
	}
}

// The quality number must arrive with its evidence attached.
func TestValueRankCarriesEvidence(t *testing.T) {
	leg := AllLegs[0]
	at := time.Now().Add(-90 * time.Minute)
	stats := map[Leg]LegStats{leg: {N: 7, Scored: 3, AvgQuality: 9.5, AvgDurationMs: 4200, LastAt: at}}
	for _, r := range ValueRank(ClassMedium, DomainCode, []Leg{leg}, stats, 8000, nil) {
		if r.Samples != 7 || r.ScoredRuns != 3 {
			t.Fatalf("sample counts not carried: %+v", r)
		}
		if r.Prior != QualityPriorFor(leg, DomainCode) {
			t.Fatalf("prior not carried: got %v", r.Prior)
		}
		if !r.LastRun.Equal(at) {
			t.Fatalf("freshness not carried: got %v", r.LastRun)
		}
	}
}

// Stats must date a leg's evidence from its FAILURES too: a leg whose last
// three runs all failed is not stale, it is freshly bad.
func TestStatsLastAtCountsFailures(t *testing.T) {
	leg := AllLegs[0]
	l := &Ledger{}
	l.Record(Event{Leg: leg, Outcome: "ok"})
	time.Sleep(2 * time.Millisecond)
	l.Record(Event{Leg: leg, Outcome: "fail", Error: "provider down"})
	st := l.Stats()[leg]
	if !st.LastAt.Equal(l.Events[1].At) {
		t.Fatalf("LastAt = %v, want the failure at %v", st.LastAt, l.Events[1].At)
	}
}

// One task has one decision: routing may revise itself inside a turn, and two
// rows for one identity would read as two tasks.
func TestRecordDecisionReplacesByTaskID(t *testing.T) {
	l := &Ledger{}
	l.RecordDecision(Decision{TaskID: "t1", Chosen: "a", Path: PathValue})
	l.RecordDecision(Decision{TaskID: "t1", Chosen: "b", Path: PathDirector})
	l.RecordDecision(Decision{TaskID: "t2", Chosen: "c", Path: PathValue})
	if len(l.Decisions) != 2 {
		t.Fatalf("want 2 decisions, got %d", len(l.Decisions))
	}
	d, ok := l.DecisionFor("t1")
	if !ok || d.Chosen != "b" || d.Path != PathDirector {
		t.Fatalf("revision not applied: %+v", d)
	}
	if d.Version != DecisionVersion || d.At.IsZero() {
		t.Fatalf("record not stamped: %+v", d)
	}
	if _, ok := l.DecisionFor(""); ok {
		t.Fatal("an empty task identity matched a decision")
	}
}

// A record written by a future schema is refused, not mis-read.
func TestDecisionForRefusesUnknownVersion(t *testing.T) {
	l := &Ledger{Decisions: []Decision{{TaskID: "t1", Version: DecisionVersion + 1, Chosen: "a"}}}
	if _, ok := l.DecisionFor("t1"); ok {
		t.Fatal("a decision from an unknown schema version was read")
	}
	if _, ok := l.LastDecision(); ok {
		t.Fatal("LastDecision read an unknown schema version")
	}
}

// The policy fingerprint must move when any tunable does - two installs with
// different CAPTAIN_VALUE_* settings produced indistinguishable rationales.
func TestPolicyVersionTracksTunables(t *testing.T) {
	a := PolicyFor(ClassMedium, 8000, 0.05)
	t.Setenv("CAPTAIN_VALUE_TAU", "1.0,9.9")
	b := PolicyFor(ClassMedium, 8000, 0.05)
	if a.Version == b.Version {
		t.Fatalf("policy version %q unchanged after τ moved", a.Version)
	}
	if b.Tau != 9.9 {
		t.Fatalf("τ not snapshotted: %v", b.Tau)
	}
	if c := PolicyFor(ClassTrivial, 8000, 0.05); c.Weights == b.Weights {
		t.Fatal("trivial and medium share a weight snapshot")
	}
}
