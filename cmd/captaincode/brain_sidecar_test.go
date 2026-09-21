package main

import (
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An open sidecar beside the primary backend: what it answers, what it is
// allowed to decide, and how its rows stay apart from the primary's.

// sidecarBrain gives b a primary backend and a sidecar, each a fake server of
// its own, and promotes the sidecar for whatever the spec names.
func sidecarBrain(t *testing.T, promotions string, sidecarConf float64) (*brain, *captaincode.SystemOneClient) {
	t.Helper()
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevServer(t, 200, "medium", "code", 0.95)
	open := jevFakeServer(t, 200, map[string]string{"class": "trivial", "domain": "editorial"}, sidecarConf).client
	open.ContextTokens = captaincode.SystemOneOpenContext
	bars, refused := map[string]float64(nil), []string(nil)
	if promotions != "" {
		t.Setenv(captaincode.SystemOneOpenForEnv, promotions)
		t.Setenv(captaincode.SystemOneOpenURLEnv, open.BaseURL)
		env := captaincode.SystemOneBackendsFromEnv()
		bars, refused = env.Bars, env.Refused
	}
	b.jevBackends = captaincode.SystemOneBackends{Primary: b.jev, Open: open, Bars: bars, Refused: refused}
	return b, open
}

// jevCallRows counts every charge row written for a decision-leg call,
// whichever backend made it.
func jevCallRows(b *brain) int {
	n := 0
	for _, c := range b.ledger.Charges {
		if c.Leg == captaincode.LegJev && c.Kind == captaincode.KindCall {
			n++
		}
	}
	return n
}

// openRows are the standalone shadow rows the sidecar left behind. The brain
// collects them after the route has already answered - a row nobody waits for
// must not put a wedged sidecar's timeout on the fast path - so a test waits
// for one rather than assuming it has landed.
func openRows(b *brain, backend string) []captaincode.ShadowRecord {
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.mu.Lock()
		rows := captaincode.ShadowsFromBackend(b.ledger.Shadows, backend)
		b.mu.Unlock()
		if len(rows) > 0 || time.Now().After(deadline) {
			return rows
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// noOpenRows asserts the sidecar left nothing behind, after giving the
// collector long enough to have done so if it were going to.
func noOpenRows(t *testing.T, b *brain, backend string) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	b.mu.Lock()
	defer b.mu.Unlock()
	assert.Empty(t, captaincode.ShadowsFromBackend(b.ledger.Shadows, backend))
}

func TestAnUnpromotedSidecarLeavesRowsAndChangesNothing(t *testing.T) {
	b, open := sidecarBrain(t, "", 0.99)
	resp := routeBody(t, b, bandTask, nil)
	assert.Equal(t, "medium", resp["class"], "the primary decided: the sidecar answers beside it and is not consulted")

	rows := openRows(b, open.Backend())
	require.Len(t, rows, 1, "the sidecar's answers land on a row of their own")
	assert.Equal(t, open.Backend(), rows[0].Backend, "stamped with who answered, never pooled onto the decision")
	assert.Equal(t, "trivial", rows[0].Answers[captaincode.PointClass].Choice)
	assert.Equal(t, "medium", rows[0].Answers[captaincode.PointClass].Actual, "compared against what captain did")
	assert.False(t, rows[0].Answers[captaincode.PointClass].Agree)
	assert.Equal(t, 1, jevCallRows(b), "the primary's call is charged and the sidecar's is not: a local read off this disk bills nobody")
}

func TestAPromotedSidecarDecidesInTheBandAtItsOwnBar(t *testing.T) {
	b, open := sidecarBrain(t, "triage=0.8", 0.9)
	b.classifyLLMFn = func(string) (captaincode.Class, captaincode.Domain, error) {
		t.Fatal("the sidecar was sure: the ~5s free-leg classify must not be asked")
		return "", "", nil
	}
	resp := routeBody(t, b, bandTask, nil)
	// The heuristic says trivial/general and the primary says medium/code, so
	// "editorial" could only have come from the sidecar.
	assert.Contains(t, resp["rationale"], "trivial/editorial", "the promoted sidecar's answer is the one acted on")
	assert.Equal(t, "trivial", resp["class"])
	assert.Zero(t, jevCallRows(b), "the primary was never asked, and the sidecar costs nothing")
	noOpenRows(t, b, open.Backend()) // the decider's answers ride on the decision, not on a row of their own
}

func TestASidecarBelowItsOwnBarKeepsTheHeuristicRatherThanBorrowingThePrimarys(t *testing.T) {
	// 0.75 clears the primary's tuned bar (CAPTAIN_TRIAGE_CONF, 0.6) and
	// misses the one this backend actually earned. The whole point of a
	// per-backend bar is that the first number is not available to it.
	b, _ := sidecarBrain(t, "triage=0.8", 0.75)
	resp := routeBody(t, b, bandTask, nil)
	assert.NotContains(t, resp["rationale"], "editorial",
		"under its own bar the answer is dropped, as it would be for the primary - and the primary's 0.6 is not available to it")
}

func TestASidecarIsNotAskedWhenTheHeuristicIsCertain(t *testing.T) {
	b, open := sidecarBrain(t, "", 0.99)
	routeBody(t, b, "fix the typo in the README: recieve should be receive", nil)
	// A calibration built from the turns captain never had to think about
	// says more about the heuristic than about the backend.
	noOpenRows(t, b, open.Backend())
}

func TestTheSidecarsRowsReadApartFromThePrimarys(t *testing.T) {
	b, open := sidecarBrain(t, "", 0.99)
	routeBody(t, b, bandTask, nil)
	require.Len(t, openRows(b, open.Backend()), 1)
	b.mu.Lock()
	rows := append([]captaincode.ShadowRecord(nil), b.ledger.Shadows...)
	decisions := append([]captaincode.Decision(nil), b.ledger.Decisions...)
	b.mu.Unlock()
	counts := captaincode.ShadowBackends(decisions, rows)
	assert.Equal(t, 1, counts[open.Backend()])
	assert.Empty(t, captaincode.ShadowsFromBackend(rows, "typesafe"), "the vendor's name must never collect a sidecar's row")
}

// The director path is where the leg question has a real menu and a prose
// answer to be compared against, so it is where the rows that could promote a
// sidecar for `route` come from.
func TestTheSidecarAnswersBesideTheDirectorToo(t *testing.T) {
	b, open := sidecarBrain(t, "", 0.93)
	b.planFn = nil
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	var menu []captaincode.Leg
	b.planFn = func(task string, class captaincode.Class, prefer string, openLegs []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, fanOut bool) (captaincode.Plan, error) {
		menu = openLegs
		return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "audit",
			Workers: []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "audit the pool"}}}, nil
	}
	routeBody(t, b, "audit the connection pool for data races under concurrent shutdown and prove the lock ordering is sound across all five call sites", nil)
	require.NotEmpty(t, menu, "the fixture must actually reach the director")

	rows := openRows(b, open.Backend())
	require.Len(t, rows, 1)
	assert.NotEmpty(t, rows[0].Menu, "the leg question was asked over the menu the director was given")
	leg := rows[0].Answers[captaincode.PointLeg]
	assert.NotEmpty(t, leg.Choice)
	assert.Equal(t, string(captaincode.LegClaude), leg.Actual, "compared against the leg the director actually chose")
	assert.Equal(t, captaincode.PathDirector, leg.By)
}
