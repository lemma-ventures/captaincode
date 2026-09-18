package captaincode

import (
	"testing"
	"time"
)

func TestEscalationPolicyDefaults(t *testing.T) {
	p := DefaultEscalationPolicy()
	if p.Version != EscalationVersion {
		t.Fatalf("version = %d, want %d", p.Version, EscalationVersion)
	}
	if p.MaxRepairs != 1 {
		t.Fatalf("MaxRepairs = %d, want 1 (default)", p.MaxRepairs)
	}
	if p.MaxEscalations != 1 {
		t.Fatalf("MaxEscalations = %d, want 1 (default)", p.MaxEscalations)
	}
}

func TestEscalationPolicyFromEnv(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_REPAIRS", "3")
	t.Setenv("CAPTAIN_MAX_ESCALATIONS", "2")
	p := DefaultEscalationPolicy()
	if p.MaxRepairs != 3 {
		t.Fatalf("MaxRepairs = %d, want 3", p.MaxRepairs)
	}
	if p.MaxEscalations != 2 {
		t.Fatalf("MaxEscalations = %d, want 2", p.MaxEscalations)
	}
}

func TestEscalationPolicyZeroMeansNone(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_REPAIRS", "0")
	t.Setenv("CAPTAIN_MAX_ESCALATIONS", "0")
	p := DefaultEscalationPolicy()
	if p.CanRepair(0) {
		t.Fatal("CanRepair(0) should be false when MaxRepairs=0")
	}
	if p.CanEscalate(0) {
		t.Fatal("CanEscalate(0) should be false when MaxEscalations=0")
	}
}

func TestEscalationPolicyCanRepair(t *testing.T) {
	p := EscalationPolicy{MaxRepairs: 2}
	if !p.CanRepair(0) {
		t.Fatal("CanRepair(0) should be true when MaxRepairs=2")
	}
	if !p.CanRepair(1) {
		t.Fatal("CanRepair(1) should be true when MaxRepairs=2")
	}
	if p.CanRepair(2) {
		t.Fatal("CanRepair(2) should be false when MaxRepairs=2")
	}
}

func TestEscalationPolicyCanEscalate(t *testing.T) {
	p := EscalationPolicy{MaxEscalations: 1}
	if !p.CanEscalate(0) {
		t.Fatal("CanEscalate(0) should be true when MaxEscalations=1")
	}
	if p.CanEscalate(1) {
		t.Fatal("CanEscalate(1) should be false when MaxEscalations=1")
	}
}

func TestNextEscalationFindsStrongerLeg(t *testing.T) {
	p := EscalationPolicy{MaxEscalations: 1}
	// Escalating from a cheap leg should find a stronger leg.
	// The FrontierChain starts with frontier legs by perf, then all legs by perf.
	// The failed leg is excluded from FrontierChain.
	now := time.Now()
	stronger, ok := p.NextEscalation(LegFree, nil, nil, map[Leg]time.Time{}, now)
	if !ok {
		t.Fatal("NextEscalation from LegFree should find a stronger leg")
	}
	if stronger == LegFree {
		t.Fatal("NextEscalation should not return the same leg")
	}
}

func TestNextEscalationSkipsTried(t *testing.T) {
	p := EscalationPolicy{MaxEscalations: 3}
	now := time.Now()
	// Find the first escalation target from LegFree
	first, ok := p.NextEscalation(LegFree, nil, nil, map[Leg]time.Time{}, now)
	if !ok {
		t.Fatal("should find at least one escalation target")
	}
	// Find the second, excluding the first
	second, ok := p.NextEscalation(LegFree, []Leg{first}, nil, map[Leg]time.Time{}, now)
	if !ok {
		t.Fatal("should find a second escalation target")
	}
	if second == first {
		t.Fatal("second escalation should skip the first tried leg")
	}
	if second == LegFree {
		t.Fatal("escalation should not return the failed leg")
	}
}

func TestNextEscalationNoStrongerLeg(t *testing.T) {
	p := EscalationPolicy{MaxEscalations: 1}
	now := time.Now()
	// Escalating from the strongest leg should find no stronger leg.
	// Try every leg; the strongest should return false when all others are tried.
	strongest := LegClaude
	for _, l := range AllLegs {
		if l != strongest && perfOrPrior(l) > perfOrPrior(strongest) {
			strongest = l
		}
	}
	// Exclude every other leg except the strongest itself.
	var tried []Leg
	for _, l := range AllLegs {
		if l != strongest {
			tried = append(tried, l)
		}
	}
	_, ok := p.NextEscalation(strongest, tried, nil, map[Leg]time.Time{}, now)
	if ok {
		t.Fatal("NextEscalation from the strongest leg with all others tried should return false")
	}
}

func TestNextEscalationRespectsAllowed(t *testing.T) {
	p := EscalationPolicy{MaxEscalations: 1}
	now := time.Now()
	// Only allow one leg (not the failed one).
	allowed := map[Leg]bool{LegFree: true}
	// Find a worker leg that is NOT LegFree to fail from.
	var failLeg Leg
	for _, l := range AllLegs {
		if l != LegFree && ServesTasks(l) {
			failLeg = l
			break
		}
	}
	if failLeg == "" {
		t.Fatal("need at least one non-free leg for this test")
	}
	got, ok := p.NextEscalation(failLeg, nil, allowed, map[Leg]time.Time{}, now)
	if !ok {
		t.Fatal("should find the allowed leg as escalation target")
	}
	if got != LegFree {
		t.Fatalf("escalation = %q, want %q (the only allowed leg)", got, LegFree)
	}
}

func TestNextEscalationSkipsCooldowns(t *testing.T) {
	p := EscalationPolicy{MaxEscalations: 1}
	now := time.Now()
	// Put all worker legs except one on cooldown.
	var open Leg
	for _, l := range AllLegs {
		if l != LegFree && ServesTasks(l) {
			open = l
			break
		}
	}
	if open == "" {
		t.Fatal("need at least one non-free leg for this test")
	}
	cooldowns := map[Leg]time.Time{}
	for _, l := range AllLegs {
		if l != LegFree && l != open {
			cooldowns[l] = now.Add(1 * time.Hour)
		}
	}
	got, ok := p.NextEscalation(LegFree, nil, nil, cooldowns, now)
	if !ok {
		t.Fatal("should find the one open leg")
	}
	if got != open {
		t.Fatalf("escalation = %q, want %q (the only non-cooled leg)", got, open)
	}
}

func TestEscalationOutcomeStopReason(t *testing.T) {
	met := EscalationOutcome{ObjectiveMet: true}
	if met.StopReason() != StopObjectiveMet {
		t.Fatalf("StopReason() = %q, want %q", met.StopReason(), StopObjectiveMet)
	}
	failed := EscalationOutcome{ObjectiveMet: false}
	if failed.StopReason() != StopObjectiveFailed {
		t.Fatalf("StopReason() = %q, want %q", failed.StopReason(), StopObjectiveFailed)
	}
}

func TestEscalationPolicyInvalidEnv(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_REPAIRS", "not-a-number")
	t.Setenv("CAPTAIN_MAX_ESCALATIONS", "-5")
	p := DefaultEscalationPolicy()
	if p.MaxRepairs != 1 {
		t.Fatalf("MaxRepairs = %d, want 1 (invalid env should fall back to default)", p.MaxRepairs)
	}
	if p.MaxEscalations != 1 {
		t.Fatalf("MaxEscalations = %d, want 1 (invalid env should fall back to default)", p.MaxEscalations)
	}
}
