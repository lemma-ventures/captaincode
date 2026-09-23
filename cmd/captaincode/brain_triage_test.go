package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The triage gate (usage analysis I1/I3): trivial/medium tasks route on the
// deterministic domain ladder with NO director call - the 17.5s-median claude
// plan is reserved for high complexity, fan-out, /quality and named legs. And
// claude never appears in a fast-path menu: no bazooka for prose edits.

func routeBody(t *testing.T, b *brain, task string, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"task": task}
	for k, v := range extra {
		body[k] = v
	}
	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", body))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp map[string]any
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func noDirector(t *testing.T, b *brain) {
	t.Helper()
	b.planFn = func(string, captaincode.Class, string, []captaincode.Leg, map[captaincode.Leg]captaincode.LegStats, map[string]captaincode.TeamStat, bool) (captaincode.Plan, error) {
		t.Fatal("the director must not be called for this route")
		return captaincode.Plan{}, nil
	}
}

func TestTriageFastPathSkipsTheDirector(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "0") // exercises the hand-written FastLadder (MM37 value routing has its own tests)
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	noDirector(t, b)

	resp := routeBody(t, b, "keep it in my writing style, condense this paragraph to 200 words max", nil)
	assert.NotEqual(t, "claude", resp["modelID"], "no bazooka for a prose edit")
	assert.Equal(t, "medium", resp["class"])
	assert.Contains(t, resp["rationale"], "triage", "the rationale names the mechanism")

	resp = routeBody(t, b, "fix typo in README", nil)
	assert.Equal(t, "trivial", resp["class"])
	assert.Equal(t, "free", resp["modelID"], "trivial starts at the zero-cost leg")
}

func TestTriageSendsHighComplexityToTheDirector(t *testing.T) {
	b := teamBrain()
	called := false
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		called = true
		return captaincode.Plan{Class: captaincode.ClassHigh, Workers: []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "x"}}}, nil
	}
	routeBody(t, b, "audit the security of the auth proxy across the codebase", nil)
	assert.True(t, called, "high complexity still gets the director (and may get claude)")
}

// Preferences, named legs, and vision keep their existing (director) semantics.
// With lanes on, /quality is the quality lane instead - also never the triage
// fast path (brain_lanes_test.go).
func TestTriageDoesNotHijackSpecialRoutes(t *testing.T) {
	t.Setenv("CAPTAIN_LANES", "0")
	for name, body := range map[string]map[string]any{
		"quality": {"task": "polish this sentence", "prefer": "quality"},
		"named":   {"task": "have grok and codex review this sentence"},
		"vision":  {"task": "look at the screenshot in /tmp/bug.png and describe it"},
	} {
		t.Run(name, func(t *testing.T) {
			b := teamBrain()
			called := false
			b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
				called = true
				return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: "x"}}}, nil
			}
			routeBody(t, b, body["task"].(string), body)
			assert.True(t, called, "%s must keep the director path", name)
		})
	}
}

// Tier 1: a low-confidence triage asks the FREE leg - never the director - and
// the refined verdict decides the path.
func TestLowConfidenceUsesTheCheapClassifier(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	asked := ""
	b.classifyLLMFn = func(task string) (captaincode.Class, captaincode.Domain, error) {
		asked = task
		return captaincode.ClassMedium, captaincode.DomainEditorial, nil
	}
	resp := routeBody(t, b, "thoughts?", nil)
	assert.Equal(t, "thoughts?", asked, "the ambiguous fragment goes to tier 1")
	assert.Equal(t, "medium", resp["class"])
	assert.NotEqual(t, "claude", resp["modelID"])
}

func TestLowConfidenceEscalatesWhenTheClassifierSaysHigh(t *testing.T) {
	b := teamBrain()
	planned := false
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		planned = true
		return captaincode.Plan{Class: captaincode.ClassHigh, Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: "x"}}}, nil
	}
	b.classifyLLMFn = func(task string) (captaincode.Class, captaincode.Domain, error) {
		return captaincode.ClassHigh, captaincode.DomainResearch, nil
	}
	routeBody(t, b, "thoughts?", nil)
	assert.True(t, planned, "tier 1 saying high hands over to the director")
}

func TestTriageClassifierFailureFallsBackToHeuristics(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	b.classifyLLMFn = func(task string) (captaincode.Class, captaincode.Domain, error) {
		return "", "", fmt.Errorf("free leg down")
	}
	resp := routeBody(t, b, "thoughts?", nil)
	assert.NotEmpty(t, resp["modelID"], "tier 1 may only ever ADD signal - its failure never blocks routing")
}

func TestTriageKillSwitchRestoresTheDirector(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	called := false
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		called = true
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: "x"}}}, nil
	}
	routeBody(t, b, "fix typo in README", nil)
	assert.True(t, called)
}

func TestFastPathHonorsCooldownsAndAllowlist(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGrok: true, captaincode.LegMiniMax: true}
	b.ledger.Cooldown(captaincode.LegGrok, 30*60*1e9)
	resp := routeBody(t, b, "condense this paragraph, keep my style", nil)
	assert.Equal(t, "minimax", resp["modelID"], "benched grok skipped, allowlist honored")
}

// I2: replay budgets scale with triaged class - a trivial turn must not drag
// 331k tokens of conversation behind it (usage analysis F3).
func TestReplayBudgetScalesWithClass(t *testing.T) {
	b := teamBrain()
	var got int
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		got = len(prompt)
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	long := strings.Repeat("earlier conversation turn about the paper. ", 5000) // ~215k chars
	turns := []string{long, "noted.", "fix typo in README"}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, turns...))
	require.Equal(t, 200, rec.Code)
	assert.Less(t, got, 60_000, "a trivial turn is windowed hard (got %d chars)", got)

	// High-complexity turns keep the leg's full budget.
	var got2 int
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		got2 = len(prompt)
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	turns2 := []string{long, "noted.", "audit the security of the auth proxy across the codebase"}
	rec2 := httptest.NewRecorder()
	b.chatCompletions(rec2, wfReq(false, turns2...))
	require.Equal(t, 200, rec2.Code)
	assert.Greater(t, got2, 200_000, "high keeps the full context (got %d chars)", got2)
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// A reroute is a fresh routing decision and must be domain-aware too: when grok
// died mid-stream on an editorial task, the rung ladder handed it to codex -
// the weakest prose leg (6.2) - instead of another strong prose leg
// (live 2026-08-01, "Address improvements.").
func TestRerouteTargetPrefersTheDomainLadder(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "0") // exercises the hand-written FastLadder (MM37 value routing has its own tests)
	b := teamBrain()
	// Trivial editorial → the zero-cost leg.
	leg, ok := b.rerouteTarget(captaincode.LegGrok, "[user]\ncondense this paragraph of the paper, keep my style\n\n")
	require.True(t, ok)
	assert.Equal(t, captaincode.LegFree, leg)

	// Medium editorial → the next PROSE leg after grok, never codex (6.2).
	leg, ok = b.rerouteTarget(captaincode.LegGrok, "[user]\nkeep it in my writing style, condense the abstract to 200 words max and preserve the flow of the argument\n\n")
	require.True(t, ok)
	assert.Equal(t, captaincode.LegMiniMax, leg,
		"an editorial task reroutes to the next prose leg, not to codex")

	// A code task keeps code-shaped targets.
	leg, ok = b.rerouteTarget(captaincode.LegCursor, "[user]\nwrite a unit test for the ledger Save function\n\n")
	require.True(t, ok)
	assert.Equal(t, captaincode.LegCodex, leg, "code work reroutes to the code ladder")
}

func TestRerouteTargetFallsBackToTheRungLadder(t *testing.T) {
	b := teamBrain()
	// Bench everything the editorial ladder would offer; the rung ladder is the
	// backstop so a reroute still finds SOME open leg.
	for _, l := range []captaincode.Leg{captaincode.LegMiniMax, captaincode.LegGLM, captaincode.LegFree} {
		b.ledger.Cooldown(l, 30*60*1e9)
	}
	leg, ok := b.rerouteTarget(captaincode.LegGrok, "[user]\ncondense this paragraph, keep my style\n\n")
	require.True(t, ok)
	assert.NotEqual(t, captaincode.LegGrok, leg)
	assert.NotEmpty(t, leg)
}

// The OPSIS misroute: a mid-prompt /quality must reach the quality path, not
// the triage fast path - the quality lane (lanes.go), or with lanes off the
// director with the top-quality menu.
func TestMidPromptQualityReachesTheDirector(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	resp := routeBody(t, b, "I changed the paper title, it will be called OPSIS. /quality review the paper", nil)
	assert.Contains(t, resp["rationale"], "quality lane:", "mid-prompt /quality routes like a leading /quality")

	t.Setenv("CAPTAIN_LANES", "0")
	prefer := ""
	b.planFn = func(task string, class captaincode.Class, p string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		prefer = p
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: "x"}}}, nil
	}
	routeBody(t, b, "I changed the paper title, it will be called OPSIS. /quality review the paper", nil)
	assert.Equal(t, "quality", prefer, "mid-prompt /quality routes like a leading /quality")
}

// An explicit leading prefix beats a different mid-prompt token.
func TestLeadingPreferBeatsMidPrompt(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	resp := routeBody(t, b, "do it /cheap if you must", map[string]any{"prefer": "quality"})
	assert.Contains(t, resp["rationale"], "quality lane:", "the leading /quality wins over a mid-prompt /cheap")

	t.Setenv("CAPTAIN_LANES", "0")
	prefer := ""
	b.planFn = func(task string, class captaincode.Class, p string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		prefer = p
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: "x"}}}, nil
	}
	routeBody(t, b, "do it /cheap if you must", map[string]any{"prefer": "quality"})
	assert.Equal(t, "quality", prefer)
}
