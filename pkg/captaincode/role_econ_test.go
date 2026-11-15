package captaincode

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRoleEconomicsUsesVerdict(t *testing.T) {
	l := &Ledger{}
	for _, item := range []struct {
		id      string
		state   LifecycleState
		outcome *OutcomeEvidence
	}{
		{id: "accepted", state: StateSucceeded, outcome: &OutcomeEvidence{TaskID: "accepted", Status: AcceptanceAccepted}},
		{id: "rejected", state: StateSucceeded, outcome: &OutcomeEvidence{TaskID: "rejected", Status: AcceptanceRejected}},
		{id: "unreviewed", state: StateSucceeded},
		{id: "failed", state: StateFailed},
	} {
		l.Charges = append(l.Charges,
			Charge{ID: item.id, TaskID: item.id, Kind: KindTask},
			Charge{ID: item.id + "-call", Parent: item.id, TaskID: item.id, Kind: KindCall, Leg: LegGrok, Label: "worker", DurationMs: 1000, Usage: Usage{CostUSD: 0.25}},
		)
		l.TaskStates = append(l.TaskStates, TaskState{TaskID: item.id, State: item.state})
		if item.outcome != nil {
			l.Outcomes = append(l.Outcomes, *item.outcome)
		}
	}

	got := RoleEconomics(l)
	if len(got) != 1 {
		t.Fatalf("expected one role, got %d", len(got))
	}
	r := got[0]
	if r.Tasks != 4 || r.Finished != 4 || r.Accepted != 1 || r.Rejected != 1 || r.Unreviewed != 2 {
		t.Fatalf("unexpected verdict counts: %+v", r)
	}
	if r.CostPerAccepted() != 1 || r.TimePerAccepted() != 4*time.Second {
		t.Fatalf("accepted denominators use all finished cost and time: %+v", r)
	}
}

func TestRoleEconomicsCountsPendingAndRegressed(t *testing.T) {
	l := &Ledger{
		Charges: []Charge{
			{ID: "pending", TaskID: "pending", Kind: KindTask},
			{ID: "regressed", TaskID: "regressed", Kind: KindTask},
		},
		TaskStates: []TaskState{
			{TaskID: "pending", State: StateSucceeded},
			{TaskID: "regressed", State: StateSucceeded},
		},
		Outcomes: []OutcomeEvidence{
			{TaskID: "pending", Status: AcceptancePending},
			{TaskID: "regressed", Status: AcceptanceRegressed},
		},
	}
	r := RoleEconomics(l)[0]
	if r.Accepted != 0 || r.Rejected != 1 || r.Unreviewed != 1 {
		t.Fatalf("unexpected verdict counts: %+v", r)
	}
}

func TestRoleEconomicsEmpty(t *testing.T) {
	l := &Ledger{}
	summaries := RoleEconomics(l)
	if len(summaries) != 0 {
		t.Fatalf("expected 0 summaries for empty ledger, got %d", len(summaries))
	}
}

func TestRoleEconomicsClassifiesSolo(t *testing.T) {
	l := &Ledger{
		Charges: []Charge{
			{ID: "t1", TaskID: "t1", Kind: KindTask, Label: "test task"},
			{ID: "a1", Parent: "t1", TaskID: "t1", Kind: KindAttempt, Leg: LegGrok, Label: "worker"},
			{ID: "c1", Parent: "a1", TaskID: "t1", Kind: KindCall, Leg: LegGrok, Label: "worker", DurationMs: 5000},
		},
		TaskStates: []TaskState{{TaskID: "t1", State: StateSucceeded, StartedAt: time.Now()}},
		Outcomes:   []OutcomeEvidence{{TaskID: "t1", Status: AcceptanceAccepted}},
	}
	summaries := RoleEconomics(l)
	if len(summaries) != 1 {
		t.Fatalf("expected 1 role, got %d", len(summaries))
	}
	if summaries[0].Role != RoleEconomicalSolo {
		t.Fatalf("expected economical_solo, got %s", summaries[0].Role)
	}
	if summaries[0].Tasks != 1 {
		t.Fatalf("expected 1 task, got %d", summaries[0].Tasks)
	}
	if summaries[0].Accepted != 1 {
		t.Fatalf("expected 1 accepted, got %d", summaries[0].Accepted)
	}
	if summaries[0].TotalMs != 5000 {
		t.Fatalf("expected 5000ms total, got %d", summaries[0].TotalMs)
	}
}

func TestRoleEconomicsClassifiesFrontier(t *testing.T) {
	l := &Ledger{
		Charges: []Charge{
			{ID: "t1", TaskID: "t1", Kind: KindTask, Label: "test task"},
			{ID: "a1", Parent: "t1", TaskID: "t1", Kind: KindAttempt, Leg: LegFrontier, Label: "worker"},
			{ID: "c1", Parent: "a1", TaskID: "t1", Kind: KindCall, Leg: LegFrontier, Label: "worker", DurationMs: 10000},
		},
		TaskStates: []TaskState{{TaskID: "t1", State: StateSucceeded, StartedAt: time.Now()}},
	}
	summaries := RoleEconomics(l)
	if len(summaries) != 1 {
		t.Fatalf("expected 1 role, got %d", len(summaries))
	}
	if summaries[0].Role != RoleFrontierSolo {
		t.Fatalf("expected frontier_solo, got %s", summaries[0].Role)
	}
}

func TestRoleEconomicsClassifiesRepair(t *testing.T) {
	l := &Ledger{
		Charges: []Charge{
			{ID: "t1", TaskID: "t1", Kind: KindTask, Label: "test task"},
			{ID: "a1", Parent: "t1", TaskID: "t1", Kind: KindAttempt, Leg: LegGrok, Label: "worker"},
			{ID: "c1", Parent: "a1", TaskID: "t1", Kind: KindCall, Leg: LegGrok, Label: "worker", DurationMs: 5000},
			{ID: "a2", Parent: "t1", TaskID: "t1", Kind: KindAttempt, Leg: LegGrok, Label: "gate-repair"},
			{ID: "c2", Parent: "a2", TaskID: "t1", Kind: KindCall, Leg: LegGrok, Label: "gate-repair", DurationMs: 3000},
		},
		TaskStates: []TaskState{{TaskID: "t1", State: StateFailed, StartedAt: time.Now()}},
	}
	summaries := RoleEconomics(l)
	if len(summaries) != 1 {
		t.Fatalf("expected 1 role, got %d", len(summaries))
	}
	if summaries[0].Role != RoleRepairEscalation {
		t.Fatalf("expected repair_escalation, got %s", summaries[0].Role)
	}
	if summaries[0].Accepted != 0 {
		t.Fatalf("expected 0 accepted (failed task), got %d", summaries[0].Accepted)
	}
}

func TestRoleEconomicsClassifiesTeams(t *testing.T) {
	l := &Ledger{
		Charges: []Charge{
			{ID: "t1", TaskID: "t1", Kind: KindTask, Label: "test task"},
			{ID: "s1", Parent: "t1", TaskID: "t1", Kind: KindStage, Label: "stage 1/1"},
			{ID: "a1", Parent: "s1", TaskID: "t1", Kind: KindAttempt, Leg: LegGrok, Label: "worker"},
			{ID: "c1", Parent: "a1", TaskID: "t1", Kind: KindCall, Leg: LegGrok, Label: "worker", DurationMs: 5000},
			{ID: "a2", Parent: "s1", TaskID: "t1", Kind: KindAttempt, Leg: LegCodex, Label: "worker"},
			{ID: "c2", Parent: "a2", TaskID: "t1", Kind: KindCall, Leg: LegCodex, Label: "worker", DurationMs: 3000},
		},
		TaskStates: []TaskState{{TaskID: "t1", State: StateSucceeded, StartedAt: time.Now()}},
	}
	summaries := RoleEconomics(l)
	if len(summaries) != 1 {
		t.Fatalf("expected 1 role, got %d", len(summaries))
	}
	if summaries[0].Role != RoleTeams {
		t.Fatalf("expected teams, got %s", summaries[0].Role)
	}
	if summaries[0].TotalMs != 8000 {
		t.Fatalf("expected 8000ms total, got %d", summaries[0].TotalMs)
	}
}

func TestRoleEconomicsMixedRoles(t *testing.T) {
	l := &Ledger{
		Charges: []Charge{
			{ID: "t1", TaskID: "t1", Kind: KindTask, Label: "solo task"},
			{ID: "a1", Parent: "t1", TaskID: "t1", Kind: KindAttempt, Leg: LegGrok, Label: "worker"},
			{ID: "c1", Parent: "a1", TaskID: "t1", Kind: KindCall, Leg: LegGrok, Label: "worker", DurationMs: 2000},
			{ID: "t2", TaskID: "t2", Kind: KindTask, Label: "frontier task"},
			{ID: "a2", Parent: "t2", TaskID: "t2", Kind: KindAttempt, Leg: LegFrontier, Label: "worker"},
			{ID: "c2", Parent: "a2", TaskID: "t2", Kind: KindCall, Leg: LegFrontier, Label: "worker", DurationMs: 10000},
		},
		TaskStates: []TaskState{
			{TaskID: "t1", State: StateSucceeded, StartedAt: time.Now()},
			{TaskID: "t2", State: StateSucceeded, StartedAt: time.Now()},
		},
	}
	summaries := RoleEconomics(l)
	if len(summaries) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(summaries))
	}
}

func TestFormatRoleEconomicsShowsVerdictCoverage(t *testing.T) {
	out := FormatRoleEconomics([]RoleSummary{{
		Role: RoleEconomicalSolo, Tasks: 4, Finished: 4, Accepted: 1, Rejected: 1, Unreviewed: 2,
		TotalCostUSD: 1, TotalMs: 4000,
	}})
	for _, want := range []string{"done", "accpt", "rejct", "unrev", "    4     4     1     1     2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q: %s", want, out)
		}
	}
}

func BenchmarkRoleEconomics(b *testing.B) {
	l := &Ledger{}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("task-%04d", i)
		l.Charges = append(l.Charges,
			Charge{ID: id, TaskID: id, Kind: KindTask},
			Charge{ID: id + "-call", Parent: id, TaskID: id, Kind: KindCall, Leg: LegGrok, Label: "worker", DurationMs: 1000, Usage: Usage{CostUSD: 0.25}},
		)
		l.TaskStates = append(l.TaskStates, TaskState{TaskID: id, State: StateSucceeded})
		status := AcceptancePending
		if i%3 == 0 {
			status = AcceptanceAccepted
		}
		l.Outcomes = append(l.Outcomes, OutcomeEvidence{TaskID: id, Status: status})
	}
	b.ReportAllocs()
	for b.Loop() {
		RoleEconomics(l)
	}
}

func TestFormatRoleEconomicsEmpty(t *testing.T) {
	if got := FormatRoleEconomics(nil); got != "no task data.\n" {
		t.Fatalf("expected empty message, got %q", got)
	}
}

func TestFormatRoleEconomicsNoAcceptedShowsDash(t *testing.T) {
	summaries := []RoleSummary{
		{Role: RoleEconomicalSolo, Tasks: 3, Accepted: 0, TotalMs: 5000},
	}
	out := FormatRoleEconomics(summaries)
	for i := 0; i < len(out); i++ {
		if out[i] == '-' {
			return
		}
	}
	t.Fatalf("expected dash for no accepted, got %q", out)
}
