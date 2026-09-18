package captaincode

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBudgetReserveAndReconcile(t *testing.T) {
	b := NewBudget("t1", "test task", BudgetOpts{MaxAttempts: 3})

	if !b.Reserve(1) {
		t.Fatal("first reserve should succeed")
	}
	if !b.Reserve(1) {
		t.Fatal("second reserve should succeed")
	}
	if !b.Reserve(1) {
		t.Fatal("third reserve should succeed: settled=0 + reserved=2 + 1 = max=3")
	}
	if b.Reserve(1) {
		t.Fatal("fourth reserve should fail: settled=0 + reserved=3 + 1 > max=3")
	}

	b.Reconcile(1, 0.10)
	if b.SettledAttempts != 1 || b.ReservedAttempts != 2 {
		t.Fatalf("after reconcile: settled=%d reserved=%d, want 1/2", b.SettledAttempts, b.ReservedAttempts)
	}
	if b.SettledCostUSD != 0.10 {
		t.Fatalf("settled cost = %.4f, want 0.10", b.SettledCostUSD)
	}
}

func TestBudgetUnlimited(t *testing.T) {
	b := NewBudget("t2", "unlimited", BudgetOpts{MaxAttempts: 0})
	for i := 0; i < 100; i++ {
		if !b.Reserve(1) {
			t.Fatalf("reserve %d failed on unlimited budget", i+1)
		}
		b.Reconcile(1, 0)
	}
	if b.Exhausted() {
		t.Fatal("unlimited budget must never be exhausted")
	}
	if b.Remaining() != -1 {
		t.Fatalf("Remaining() = %d, want -1 for unlimited", b.Remaining())
	}
}

func TestBudgetExhausted(t *testing.T) {
	b := NewBudget("t3", "capped", BudgetOpts{MaxAttempts: 2})
	b.Reserve(1)
	b.Reconcile(1, 0)
	b.Reserve(1)
	b.Reconcile(1, 0)
	if !b.Exhausted() {
		t.Fatal("budget with settled=max should be exhausted")
	}
	if !b.isStopped() {
		t.Fatal("budget should be stopped with attempts_exhausted")
	}
	if b.StopReason != StopAttemptsExhausted {
		t.Fatalf("stop reason = %q, want %q", b.StopReason, StopAttemptsExhausted)
	}
}

func TestBudgetStopReasonFirstWins(t *testing.T) {
	b := NewBudget("t4", "first reason", BudgetOpts{MaxAttempts: 5})
	b.Stop(StopAllLegsFailed)
	b.Stop(StopAttemptsExhausted)
	if b.StopReason != StopAllLegsFailed {
		t.Fatalf("stop reason = %q, want %q (first reason wins)", b.StopReason, StopAllLegsFailed)
	}
}

func TestBudgetCanReserveWithoutReserving(t *testing.T) {
	b := NewBudget("t5", "peek", BudgetOpts{MaxAttempts: 3})
	b.Reserve(1)
	b.Reconcile(1, 0)

	if !b.CanReserve(1) {
		t.Fatal("CanReserve(1) should be true: 1 settled, 0 reserved, room for 1 more")
	}
	if b.CanReserve(3) {
		t.Fatal("CanReserve(3) should be false: 1 settled + 3 > max=3")
	}

	if b.ReservedAttempts != 0 {
		t.Fatalf("CanReserve should not mutate reserved: got %d", b.ReservedAttempts)
	}
}

func TestBudgetConcurrentReserve(t *testing.T) {
	b := NewBudget("t6", "concurrent", BudgetOpts{MaxAttempts: 10})
	var mu sync.Mutex
	var wg sync.WaitGroup
	allowed := 0
	var countMu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			ok := b.Reserve(1)
			mu.Unlock()
			if ok {
				countMu.Lock()
				allowed++
				countMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed > 10 {
		t.Fatalf("concurrent reserves allowed %d, should be <= max=10", allowed)
	}
	if allowed != 10 {
		t.Fatalf("concurrent reserves allowed %d, want exactly 10", allowed)
	}
}

func TestBudgetReconcileClampsReserved(t *testing.T) {
	b := NewBudget("t7", "clamp", BudgetOpts{MaxAttempts: 5})
	// Reconcile without a prior reserve: the money was real regardless
	b.Reconcile(1, 0.50)
	if b.SettledAttempts != 1 {
		t.Fatalf("settled = %d, want 1", b.SettledAttempts)
	}
	if b.ReservedAttempts != 0 {
		t.Fatalf("reserved = %d, want 0 (clamped)", b.ReservedAttempts)
	}
	if b.SettledCostUSD != 0.50 {
		t.Fatalf("settled cost = %.4f, want 0.50", b.SettledCostUSD)
	}
}

func TestBudgetWallTimeExceeded(t *testing.T) {
	b := NewBudget("t8", "timed", BudgetOpts{MaxWallMs: 1})
	time.Sleep(5 * time.Millisecond)
	if !b.Exhausted() {
		t.Fatal("budget with 1ms wall cap should be exhausted after 5ms")
	}
	if b.StopReason != StopTimeExhausted {
		t.Fatalf("stop reason = %q, want %q", b.StopReason, StopTimeExhausted)
	}
}

func TestBudgetCostCapStops(t *testing.T) {
	b := NewBudget("t9", "cost capped", BudgetOpts{MaxCostUSD: 1.00})
	b.Reserve(1)
	b.Reconcile(1, 0.60)
	if b.Exhausted() {
		t.Fatal("0.60 < 1.00 cap, should not be exhausted")
	}
	b.Reserve(1)
	b.Reconcile(1, 0.50)
	if !b.Exhausted() {
		t.Fatal("1.10 >= 1.00 cap, should be exhausted")
	}
	if b.StopReason != StopCostExhausted {
		t.Fatalf("stop reason = %q, want %q", b.StopReason, StopCostExhausted)
	}
}

func TestLedgerBudgetPersistence(t *testing.T) {
	l := &Ledger{Sessions: map[string]string{}, Threads: map[string]ThreadRef{}, Cooldowns: map[Leg]time.Time{}}
	b := NewBudget("t10", "persist test", BudgetOpts{MaxAttempts: 5})
	b.Reserve(2)
	b.Reconcile(1, 0.25)

	l.RecordBudget(b)
	stored := l.BudgetFor("t10")
	if stored == nil {
		t.Fatal("BudgetFor returned nil after RecordBudget")
	}
	if stored.SettledAttempts != 1 || stored.ReservedAttempts != 1 {
		t.Fatalf("stored: settled=%d reserved=%d, want 1/1", stored.SettledAttempts, stored.ReservedAttempts)
	}
	if stored.MaxAttempts != 5 {
		t.Fatalf("stored max = %d, want 5", stored.MaxAttempts)
	}

	// Update in place
	stored.Reserve(1)
	l.RecordBudget(stored)
	again := l.BudgetFor("t10")
	if again.ReservedAttempts != 2 {
		t.Fatalf("after update: reserved=%d, want 2", again.ReservedAttempts)
	}
	if len(l.Budgets) != 1 {
		t.Fatalf("budget count = %d, want 1 (update in place)", len(l.Budgets))
	}
}

func TestLedgerBudgetCap(t *testing.T) {
	l := &Ledger{Sessions: map[string]string{}, Threads: map[string]ThreadRef{}, Cooldowns: map[Leg]time.Time{}}
	for i := 0; i < maxBudgets+5; i++ {
		b := NewBudget("cap-"+itoa(i), "cap test", BudgetOpts{MaxAttempts: 1})
		l.RecordBudget(b)
	}
	if len(l.Budgets) > maxBudgets {
		t.Fatalf("budgets = %d, should be capped at %d", len(l.Budgets), maxBudgets)
	}
}

func TestLedgerRecentBudgets(t *testing.T) {
	l := &Ledger{Sessions: map[string]string{}, Threads: map[string]ThreadRef{}, Cooldowns: map[Leg]time.Time{}}
	b1 := NewBudget("old", "old task", BudgetOpts{})
	b1.StartedAt = time.Now().Add(-1 * time.Hour)
	l.RecordBudget(b1)
	b2 := NewBudget("new", "new task", BudgetOpts{})
	l.RecordBudget(b2)

	recent := l.RecentBudgets()
	if len(recent) != 2 || recent[0].TaskID != "new" {
		t.Fatalf("RecentBudgets[0] = %q, want %q (newest first)", recent[0].TaskID, "new")
	}
}

func TestLedgerBudgetCoverage(t *testing.T) {
	l := &Ledger{Sessions: map[string]string{}, Threads: map[string]ThreadRef{}, Cooldowns: map[Leg]time.Time{}}
	b1 := NewBudget("a", "task a", BudgetOpts{})
	l.RecordBudget(b1)
	b2 := NewBudget("b", "task b", BudgetOpts{})
	b2.Reserve(1)
	b2.Reconcile(1, 0.30)
	b2.Stop(StopAllLegsFailed)
	l.RecordBudget(b2)

	total, stopped, cost := l.BudgetCoverage()
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if stopped != 1 {
		t.Fatalf("stopped = %d, want 1", stopped)
	}
	if cost != 0.30 {
		t.Fatalf("cost = %.4f, want 0.30", cost)
	}
}

func TestStopReasonText(t *testing.T) {
	cases := map[string]string{
		StopAttemptsExhausted: "attempt cap reached",
		StopCostExhausted:     "cost cap reached",
		StopTimeExhausted:     "wall-time cap reached",
		StopAllLegsFailed:     "all legs failed or cooled down",
		StopObjectiveMet:      "objective check passed",
		StopObjectiveFailed:   "objective check failed after bounded retries",
		StopUserCancelled:     "user cancelled",
		"unknown":             "unknown",
	}
	for reason, want := range cases {
		if got := StopReasonText(reason); got != want {
			t.Errorf("StopReasonText(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestDefaultBudgetOpts(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "8")
	t.Setenv("CAPTAIN_MAX_COST", "5.00")
	t.Setenv("CAPTAIN_MAX_WALLTIME", "30m")

	opts := DefaultBudgetOpts()
	if opts.MaxAttempts != 8 {
		t.Fatalf("MaxAttempts = %d, want 8", opts.MaxAttempts)
	}
	if opts.MaxCostUSD != 5.00 {
		t.Fatalf("MaxCostUSD = %.2f, want 5.00", opts.MaxCostUSD)
	}
	if opts.MaxWallMs != (30 * 60 * 1000) {
		t.Fatalf("MaxWallMs = %d, want %d", opts.MaxWallMs, 30*60*1000)
	}
}

func TestDefaultBudgetOptsUnset(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_MAX_COST", "")
	t.Setenv("CAPTAIN_MAX_WALLTIME", "")

	opts := DefaultBudgetOpts()
	if opts.MaxAttempts != 0 {
		t.Fatalf("MaxAttempts = %d, want 0 (unlimited)", opts.MaxAttempts)
	}
	if opts.MaxCostUSD != 0 {
		t.Fatalf("MaxCostUSD = %.2f, want 0", opts.MaxCostUSD)
	}
	if opts.MaxWallMs != 0 {
		t.Fatalf("MaxWallMs = %d, want 0", opts.MaxWallMs)
	}
}

func TestBudgetSummary(t *testing.T) {
	b := NewBudget("t-sum", "summary test", BudgetOpts{MaxAttempts: 5})
	b.Reserve(2)
	b.Reconcile(1, 0.15)

	s := b.Summary()
	if s == "" {
		t.Fatal("Summary() returned empty string")
	}
}

func TestFormatBudget(t *testing.T) {
	b := NewBudget("t-fmt", "format test", BudgetOpts{MaxAttempts: 3, MaxCostUSD: 1.00})
	b.Reserve(1)
	b.Reconcile(1, 0.25)

	s := FormatBudget(*b)
	if s == "" {
		t.Fatal("FormatBudget returned empty string")
	}
}

// ── M2.4 fault tests: concurrent reservations, missing/late usage, cancellation ──

func TestBudgetConcurrentReservesRespectOutstanding(t *testing.T) {
	b := NewBudget("t-fault-1", "concurrent outstanding", BudgetOpts{MaxAttempts: 5})
	var mu sync.Mutex
	var wg sync.WaitGroup
	allowed := 0
	var countMu sync.Mutex

	// 10 goroutines each try to reserve 1; only 5 should succeed.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			ok := b.Reserve(1)
			mu.Unlock()
			if ok {
				countMu.Lock()
				allowed++
				countMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed > 5 {
		t.Fatalf("concurrent reserves allowed %d, should be <= max=5", allowed)
	}
	if allowed != 5 {
		t.Fatalf("concurrent reserves allowed %d, want exactly 5", allowed)
	}
	// Reserved but never reconciled: the outstanding count blocks further reserves.
	if b.Reserve(1) {
		t.Fatal("reserve after 5 outstanding should fail: settled=0 + reserved=5 + 1 > max=5")
	}
}

func TestBudgetLateUsageReconcile(t *testing.T) {
	b := NewBudget("t-fault-2", "late usage", BudgetOpts{MaxAttempts: 3})
	// Reserve and release the reservation, but usage arrives late.
	b.Reserve(1)
	b.Reconcile(1, 0.0) // reconcile with 0 cost (usage not yet reported)
	if b.SettledAttempts != 1 {
		t.Fatalf("settled = %d, want 1", b.SettledAttempts)
	}
	// Late usage arrives: reconcile again with actual cost.
	b.Reconcile(0, 0.15) // no new attempt, just cost update
	if b.SettledCostUSD != 0.15 {
		t.Fatalf("settled cost = %.4f, want 0.15 (late usage)", b.SettledCostUSD)
	}
	if b.SettledAttempts != 1 {
		t.Fatalf("settled attempts should stay 1, got %d", b.SettledAttempts)
	}
}

func TestBudgetDoubleReconcileReservedOnlyDecrementsOnce(t *testing.T) {
	b := NewBudget("t-fault-3", "double reconcile", BudgetOpts{MaxAttempts: 5})
	b.Reserve(1)
	b.Reconcile(1, 0.10)
	// Second reconcile with the same reservedAttempts: the reserved counter
	// is only decremented once (clamped to 0), but settled attempts and cost
	// accumulate — the money was real regardless of whether the bookkeeping
	// was correct (ROADMAP M2.4 design: reconcile handles a missing reserve
	// by settling directly).
	b.Reconcile(1, 0.10)
	if b.ReservedAttempts != 0 {
		t.Fatalf("reserved = %d, want 0 (only decremented once)", b.ReservedAttempts)
	}
	if b.SettledAttempts != 2 {
		t.Fatalf("settled = %d, want 2 (each reconcile settles)", b.SettledAttempts)
	}
	if b.SettledCostUSD != 0.20 {
		t.Fatalf("cost = %.4f, want 0.20", b.SettledCostUSD)
	}
}

func TestBudgetReservationLeakBlocksFutureDispatch(t *testing.T) {
	b := NewBudget("t-fault-4", "leaked reservation", BudgetOpts{MaxAttempts: 3})
	// Reserve 3 (the full cap) but never reconcile — simulates a crash.
	b.Reserve(1)
	b.Reserve(1)
	b.Reserve(1)
	if b.Reserve(1) {
		t.Fatal("reserve after 3 outstanding should fail (leaked reservations block)")
	}
	if !b.Exhausted() {
		t.Fatal("budget with reserved=max should be exhausted even without settling")
	}
}

func TestBudgetCancellationDoesNotRefund(t *testing.T) {
	b := NewBudget("t-fault-5", "cancelled task", BudgetOpts{MaxAttempts: 5})
	b.Reserve(1)
	b.Reconcile(1, 0.30)
	// Cancellation stops the budget but does not refund the spent cost.
	b.Stop(StopUserCancelled)
	if !b.isStopped() {
		t.Fatal("budget should be stopped after user cancellation")
	}
	if b.SettledCostUSD != 0.30 {
		t.Fatalf("cost = %.4f, want 0.30 (cancellation does not refund)", b.SettledCostUSD)
	}
	if b.SettledAttempts != 1 {
		t.Fatalf("settled = %d, want 1 (cancellation does not undo settled work)", b.SettledAttempts)
	}
	// A stopped budget refuses further dispatch.
	if b.Reserve(1) {
		t.Fatal("reserve after stop should fail")
	}
}

func TestBudgetConcurrentReserveAndReconcile(t *testing.T) {
	b := NewBudget("t-fault-6", "concurrent reserve+reconcile", BudgetOpts{MaxAttempts: 10})
	var mu sync.Mutex
	var wg sync.WaitGroup

	// 10 goroutines: each reserves, then immediately reconciles.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			ok := b.Reserve(1)
			if ok {
				b.Reconcile(1, 0.01)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if b.SettledAttempts != 10 {
		t.Fatalf("settled = %d, want 10", b.SettledAttempts)
	}
	if b.ReservedAttempts != 0 {
		t.Fatalf("reserved = %d, want 0 (all reconciled)", b.ReservedAttempts)
	}
	if b.SettledCostUSD < 0.099 || b.SettledCostUSD > 0.101 {
		t.Fatalf("cost = %.6f, want ~0.10", b.SettledCostUSD)
	}
}

// ── M2.4 admission/strict-mode distinction ──

func TestBudgetAdmissionModeByDefault(t *testing.T) {
	b := NewBudget("t-mode-1", "admission", BudgetOpts{MaxCostUSD: 1.00})
	if b.Mode != BudgetAdmission {
		t.Fatalf("mode = %q, want %q (default)", b.Mode, BudgetAdmission)
	}
	if b.IsStrict() {
		t.Fatal("admission budget should not be strict")
	}
}

func TestBudgetStrictModeRequiresCostCap(t *testing.T) {
	// Strict flag without a cost cap is still admission: there is nothing to
	// enforce, so rejecting cost-incapable legs would starve the field for
	// no reason.
	b := NewBudget("t-mode-2", "strict no cap", BudgetOpts{Strict: true, MaxCostUSD: 0})
	if b.Mode != BudgetAdmission {
		t.Fatalf("mode = %q, want %q (strict without cost cap is admission)", b.Mode, BudgetAdmission)
	}
}

func TestBudgetStrictModeWithCostCap(t *testing.T) {
	b := NewBudget("t-mode-3", "strict with cap", BudgetOpts{Strict: true, MaxCostUSD: 5.00})
	if b.Mode != BudgetStrict {
		t.Fatalf("mode = %q, want %q", b.Mode, BudgetStrict)
	}
	if !b.IsStrict() {
		t.Fatal("strict budget should be strict")
	}
}

func TestCostEnforceableClaudeCLI(t *testing.T) {
	// Claude CLI reports total_cost_usd, so it can enforce a hard cost cap.
	if reason := CostEnforceable(LegClaude); reason != "" {
		t.Fatalf("claude should be cost-enforceable, got: %s", reason)
	}
}

func TestCostEnforceableOpencodeLegsRejected(t *testing.T) {
	// opencode transport has CapCost=unknown (reported per provider, absent
	// for subscription rosters), so it cannot enforce a hard cost cap.
	reason := CostEnforceable(LegGLM)
	if reason == "" {
		t.Fatal("glm (opencode transport) should NOT be cost-enforceable")
	}
	if !strings.Contains(reason, "cost reporting") {
		t.Fatalf("reason should mention cost reporting, got: %s", reason)
	}
}

func TestCostEnforceableCursorRejected(t *testing.T) {
	// Cursor CLI has CapCost=no.
	reason := CostEnforceable(LegCursor)
	if reason == "" {
		t.Fatal("cursor should NOT be cost-enforceable")
	}
}

func TestDefaultBudgetOptsStrictFromEnv(t *testing.T) {
	t.Setenv("CAPTAIN_STRICT", "1")
	t.Setenv("CAPTAIN_MAX_COST", "2.00")
	opts := DefaultBudgetOpts()
	if !opts.Strict {
		t.Fatal("Strict should be true when CAPTAIN_STRICT=1")
	}
	if opts.MaxCostUSD != 2.00 {
		t.Fatalf("MaxCostUSD = %.2f, want 2.00", opts.MaxCostUSD)
	}
}

func TestDefaultBudgetOptsStrictUnset(t *testing.T) {
	t.Setenv("CAPTAIN_STRICT", "")
	opts := DefaultBudgetOpts()
	if opts.Strict {
		t.Fatal("Strict should be false when CAPTAIN_STRICT is unset")
	}
}

func TestFormatBudgetShowsMode(t *testing.T) {
	b := NewBudget("t-fmt-mode", "mode test", BudgetOpts{MaxCostUSD: 1.00, Strict: true})
	b.Reserve(1)
	b.Reconcile(1, 0.25)
	s := FormatBudget(*b)
	if !strings.Contains(s, "strict") {
		t.Fatalf("FormatBudget should mention strict mode, got:\n%s", s)
	}
}

func TestFormatBudgetAdmissionMode(t *testing.T) {
	b := NewBudget("t-fmt-adm", "admission test", BudgetOpts{MaxCostUSD: 1.00})
	b.Reserve(1)
	b.Reconcile(1, 0.25)
	s := FormatBudget(*b)
	if !strings.Contains(s, "admission") {
		t.Fatalf("FormatBudget should mention admission mode, got:\n%s", s)
	}
}
