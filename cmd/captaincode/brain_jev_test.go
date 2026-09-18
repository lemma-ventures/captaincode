package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Triage tier 1 on the decision leg: jev answers the class/domain questions in
// a few hundred milliseconds with a calibrated confidence. Sure enough, it
// decides; unsure or down, the free-leg classify decides as before; either
// way the call is charged, and no task path ever reaches jev.

// jevServer answers the triage questions as told and anything else asked
// (the shadow questions) with a sensible default; see jevFakeServer.
func jevServer(t *testing.T, status int, class, domain string, conf float64) *captaincode.SystemOneClient {
	return jevFakeServer(t, status, map[string]string{"class": class, "domain": domain}, conf).client
}

func jevCharges(b *brain) int {
	n := 0
	for _, c := range b.ledger.Charges {
		if c.Leg == captaincode.LegJev && c.Label == "classify" && c.Kind == captaincode.KindCall {
			n++
		}
	}
	return n
}

func TestJevDecidesWhenItIsSure(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevServer(t, 200, "medium", "editorial", 0.9)
	b.classifyLLMFn = func(string) (captaincode.Class, captaincode.Domain, error) {
		t.Fatal("jev was sure: the free-leg classify must not be asked")
		return "", "", nil
	}
	resp := routeBody(t, b, "thoughts?", nil)
	assert.Equal(t, "medium", resp["class"])
	assert.Contains(t, resp["rationale"], "conf 0.90", "jev's calibrated confidence replaces the heuristic margin")
	assert.NotEqual(t, "claude", resp["modelID"])
	assert.NotEqual(t, "jev", resp["leg"], "jev decided the class; it never runs the task")
	assert.Equal(t, 1, jevCharges(b), "the call is charged to the turn")
}

func TestJevUnsureFallsBackToTheFreeLegClassify(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevServer(t, 200, "high", "code", 0.3)
	asked := false
	b.classifyLLMFn = func(string) (captaincode.Class, captaincode.Domain, error) {
		asked = true
		return captaincode.ClassMedium, captaincode.DomainEditorial, nil
	}
	resp := routeBody(t, b, "thoughts?", nil)
	assert.True(t, asked, "below the bar, jev's answer is dropped and the free leg decides")
	assert.Equal(t, "medium", resp["class"], "the unsure 'high' did not reach the director")
	assert.Equal(t, 1, jevCharges(b), "a dropped answer still cost tokens")
}

func TestJevFailureFallsBackToTheFreeLegClassify(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevServer(t, 422, "", "", 0) // not retryable: the request itself was refused
	asked := false
	b.classifyLLMFn = func(string) (captaincode.Class, captaincode.Domain, error) {
		asked = true
		return captaincode.ClassMedium, captaincode.DomainEditorial, nil
	}
	resp := routeBody(t, b, "thoughts?", nil)
	assert.True(t, asked)
	assert.NotEmpty(t, resp["modelID"], "tier 1 may only ever ADD signal - jev being down never blocks routing")
}

func TestJevConfidenceBarIsConfigurable(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE_JEV_CONF", "0.95")
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevServer(t, 200, "medium", "code", 0.9)
	asked := false
	b.classifyLLMFn = func(string) (captaincode.Class, captaincode.Domain, error) {
		asked = true
		return captaincode.ClassMedium, captaincode.DomainCode, nil
	}
	routeBody(t, b, "thoughts?", nil)
	assert.True(t, asked, "0.90 is under a 0.95 bar")
}

func TestForcedJevIsRefusedWithTheWayToAskIt(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": "write the tests", "forced": "jev"}))
	require.Equal(t, 400, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "decision leg")
	assert.Contains(t, rec.Body.String(), "captain jev classify")
}

func TestTheDecisionLegIsNotAModelOrAWorker(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.models(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assert.NotContains(t, rec.Body.String(), `"jev"`, "the TUI must never be able to send a turn to it")
	assert.Contains(t, rec.Body.String(), `"glm"`)
}

// The band: over the free-leg bar (0.6) the ~5s classify stays out, but a
// configured jev is still asked while the heuristic is under 0.9 - a sure
// answer is taken, a miss or a failure keeps the heuristic, and nothing else
// is called. A task the heuristics are certain about never reaches jev.

// bandTask triages as trivial/general at 0.65: two low-complexity hits, no
// domain evidence - sure enough to skip the free leg, not to skip jev.
const bandTask = "handle the edge cases we talked about and make it more robust"

func TestJevIsAskedInTheBandWithoutTheFreeLeg(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevServer(t, 200, "medium", "code", 0.9)
	b.classifyLLMFn = func(string) (captaincode.Class, captaincode.Domain, error) {
		t.Fatal("0.65 is over the free-leg bar: the free-leg classify must not be asked")
		return "", "", nil
	}
	resp := routeBody(t, b, bandTask, nil)
	assert.Equal(t, "medium", resp["class"], "jev's sure answer replaces the heuristic")
	assert.Contains(t, resp["rationale"], "conf 0.90")
	assert.Equal(t, 1, jevCharges(b))
}

func TestJevMissInTheBandKeepsTheHeuristic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		conf   float64
	}{
		{"unsure", 200, 0.3},
		{"failed", 401, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := teamBrain()
			noDirector(t, b)
			b.jev = jevServer(t, tc.status, "medium", "code", tc.conf)
			b.classifyLLMFn = func(string) (captaincode.Class, captaincode.Domain, error) {
				t.Fatal("a miss in the band keeps the heuristic: the free-leg classify must not be asked")
				return "", "", nil
			}
			resp := routeBody(t, b, bandTask, nil)
			assert.Equal(t, "trivial", resp["class"], "the heuristic stands")
			assert.Contains(t, resp["rationale"], "conf 0.65")
			assert.Equal(t, 1, jevCharges(b), "asked, so charged, taken or not")
		})
	}
}

func TestJevBandIsConfigurable(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE_JEV_BELOW", "0")
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevServer(t, 200, "medium", "code", 0.9)
	resp := routeBody(t, b, bandTask, nil)
	assert.Equal(t, "trivial", resp["class"], "band closed: the heuristic routes alone")
	assert.Equal(t, 0, jevCharges(b))
}

func TestACertainHeuristicNeverReachesJev(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevServer(t, 200, "medium", "editorial", 0.9)
	resp := routeBody(t, b, "fix the typo in README", nil) // trivial/code at 0.95
	assert.Equal(t, "trivial", resp["class"])
	assert.Equal(t, 0, jevCharges(b), "0.95 is over the band: no call, 0ms")
}
