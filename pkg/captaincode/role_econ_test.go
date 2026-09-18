package captaincode

import (
	"testing"
	"time"
)

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
