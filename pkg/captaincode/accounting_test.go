package captaincode

import "testing"

func TestCallUsageSeparatesBilledFromPriced(t *testing.T) {
	billed := CallUsage(LegClaude, 1000, 0.42, nil)
	if billed.CostStatus != UsageMeasured || billed.PriceSource != "runtime" {
		t.Fatalf("runtime dollars must read measured: %+v", billed)
	}
	priced := CallUsage(LegGLM, 100_000, 0, nil)
	if priced.CostUSD <= 0 || priced.CostStatus != UsageEstimated {
		t.Fatalf("registry-priced tokens must read estimated: %+v", priced)
	}
	if priced.PriceSource != "registry:"+string(LegGLM) {
		t.Fatalf("estimate must name its price source: %+v", priced)
	}
	silent := CallUsage(LegClaude, 0, 0, nil)
	if silent.Status != UsageUnknown || silent.CostStatus != UsageUnknown {
		t.Fatalf("a leg reporting nothing must read unknown, not $0: %+v", silent)
	}
}

func TestNormalizeUsageDoesNotDoubleCountSubsets(t *testing.T) {
	u := NormalizeUsage(Usage{Input: 800, Output: 200, Cached: 600, Reasoning: 150})
	if u.Total != 1000 {
		t.Fatalf("cached/reasoning are subsets of the billed counts, total=%d", u.Total)
	}
	if u.Status != UsageMeasured {
		t.Fatalf("reported tokens are measured: %+v", u)
	}
}

func TestRecordChargeReconcilesLateUsageInPlace(t *testing.T) {
	l := &Ledger{}
	id := NewChargeID(KindCall)
	l.RecordCharge(Charge{ID: id, TaskID: "t-1", Kind: KindCall, Leg: LegGLM, Usage: Usage{Total: 500, CostStatus: UsageEstimated, CostUSD: 0.01, PriceSource: "registry:glm"}})
	if updated := l.RecordCharge(Charge{ID: id, TaskID: "t-1", Kind: KindCall, Usage: Usage{Total: 512, Status: UsageMeasured, CostUSD: 0.013, CostStatus: UsageMeasured, PriceSource: "runtime"}}); !updated {
		t.Fatal("late usage must reconcile the original charge, not add a second one")
	}
	if len(l.Charges) != 1 {
		t.Fatalf("want 1 charge, got %d", len(l.Charges))
	}
	if l.Charges[0].Usage.CostUSD != 0.013 || l.Charges[0].Usage.CostStatus != UsageMeasured {
		t.Fatalf("measured must replace estimated: %+v", l.Charges[0].Usage)
	}
	l.RecordCharge(Charge{ID: id, TaskID: "t-1", Kind: KindCall, Usage: Usage{Total: 9, Status: UsageEstimated, CostUSD: 99, CostStatus: UsageEstimated}})
	if l.Charges[0].Usage.CostUSD != 0.013 || l.Charges[0].Usage.Total != 512 {
		t.Fatalf("an estimate must never overwrite a measurement: %+v", l.Charges[0].Usage)
	}
}

func TestTaskTotalsBillLeavesOnlyAndReportCoverage(t *testing.T) {
	l := &Ledger{}
	l.RecordCharge(Charge{ID: "t-1", TaskID: "t-1", Kind: KindTask, Usage: Usage{CostUSD: 999, CostStatus: UsageMeasured}})
	l.RecordCharge(Charge{ID: "a-1", Parent: "t-1", TaskID: "t-1", Kind: KindAttempt, Usage: Usage{CostUSD: 999, CostStatus: UsageMeasured}})
	l.RecordCharge(Charge{Parent: "a-1", TaskID: "t-1", Kind: KindCall, Usage: Usage{Total: 100, CostUSD: 0.10, CostStatus: UsageMeasured}})
	l.RecordCharge(Charge{Parent: "a-1", TaskID: "t-1", Kind: KindCall, Usage: Usage{Total: 50, CostUSD: 0.05, CostStatus: UsageEstimated}})
	l.RecordCharge(Charge{Parent: "a-1", TaskID: "t-1", Kind: KindCall, Usage: Usage{}})
	l.RecordCharge(Charge{Parent: "a-9", TaskID: "t-2", Kind: KindCall, Usage: Usage{CostUSD: 5, CostStatus: UsageMeasured}})

	got := l.TaskTotals("t-1")
	if got.Calls != 3 || got.Tokens != 150 {
		t.Fatalf("parents and other tasks must not be billed: %+v", got)
	}
	if d := got.CostUSD - 0.15; d > 1e-9 || d < -1e-9 {
		t.Fatalf("want 0.15, got %v", got.CostUSD)
	}
	if got.Measured != 1 || got.Estimated != 1 || got.Unknown != 1 {
		t.Fatalf("coverage must be reported per status: %+v", got)
	}
	if got.Complete() {
		t.Fatal("a total with estimated/unknown calls is not a billed total")
	}
}

func TestChargeTreeOrdersParentsBeforeChildren(t *testing.T) {
	l := &Ledger{}
	l.RecordCharge(Charge{ID: "t-1", TaskID: "t-1", Kind: KindTask})
	l.RecordCharge(Charge{ID: "a-1", Parent: "t-1", TaskID: "t-1", Kind: KindAttempt})
	l.RecordCharge(Charge{ID: "c-1", Parent: "a-1", TaskID: "t-1", Kind: KindCall})
	rows := ChargeTree(l.Charges, "t-1")
	if len(rows) != 3 || rows[0].ID != "t-1" || rows[1].ID != "a-1" || rows[2].ID != "c-1" {
		t.Fatalf("tree order wrong: %+v", rows)
	}
}

func TestChargeVersionIsStamped(t *testing.T) {
	l := &Ledger{}
	l.RecordCharge(Charge{TaskID: "t-1", Kind: KindCall})
	if l.Charges[0].Version != AccountingVersion || l.Charges[0].ID == "" || l.Charges[0].At.IsZero() {
		t.Fatalf("every charge needs version, identity and a timestamp: %+v", l.Charges[0])
	}
}
