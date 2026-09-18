package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live 2026-09-09 23:24: "/team /frontier continue the work of the other
// agent…" - the fork parsed /team, the brain ignored /frontier (teamPrefer
// only knows preference words), the director picked codex (excluded by
// CAPTAIN_LEGS, so not on its menu), Plan rejected the pick, and the turn
// ended with no answer at all. Three fixes, three tests.

// 1. A leg or /frontier named right after /team is a BINDING team member.
func TestTeamRequired(t *testing.T) {
	assert.Equal(t, []captaincode.Leg{captaincode.LegFrontier}, teamRequired("/team /frontier continue the work"))
	assert.Equal(t, []captaincode.Leg{captaincode.LegCodexCLI, captaincode.LegClaude}, teamRequired("/team /codex-cli /claude compare notes"))
	assert.Equal(t, []captaincode.Leg{captaincode.LegFrontier}, teamRequired("/team /quality /frontier do it"), "preferences and legs mix")
	assert.Equal(t, "quality", teamPrefer("/team /frontier /quality do it"), "and the preference still parses past a leg")
	assert.Nil(t, teamRequired("/team build it"))
	assert.Nil(t, teamRequired("/team /speed build it"))
}

func TestTeamPlanFor_RequiredLegsReachTheDirectorAndTheMenu(t *testing.T) {
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGrok: true, captaincode.LegCursor: true}
	var gotOpen []captaincode.Leg
	var gotRequired []captaincode.Leg
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		gotOpen = open
		return captaincode.Plan{Class: captaincode.ClassHigh, Workers: []captaincode.Worker{{Leg: captaincode.LegFrontier, Brief: "deep"}, {Leg: captaincode.LegGrok, Brief: "fast"}}}, nil
	}
	b.planRequiredFn = func(required []captaincode.Leg) { gotRequired = required }
	plan, err := b.teamPlanFor(defaultWorkspace(), "continue the work", "", []captaincode.Leg{captaincode.LegFrontier})
	require.NoError(t, err)
	assert.Equal(t, captaincode.LegFrontier, gotOpen[0], "the required leg heads the director's menu even though it is not a CAPTAIN_LEGS leg")
	assert.Equal(t, []captaincode.Leg{captaincode.LegFrontier}, gotRequired, "and the director is told it is binding")
	assert.Len(t, plan.Workers, 2)

	// /team /quality /frontier: TopQuality has no prior for frontier and would
	// drop it - required legs survive the quality binding.
	_, err = b.teamPlanFor(defaultWorkspace(), "continue the work", "quality", []captaincode.Leg{captaincode.LegFrontier})
	require.NoError(t, err)
	assert.Equal(t, captaincode.LegFrontier, gotOpen[0])
}

// The frontier pseudo-leg executes inside a team exactly as /frontier does:
// claude at max effort, recorded under claude.
func TestTeamChat_FrontierWorkerRunsTheFrontierRunner(t *testing.T) {
	b := teamBrain()
	// Team workers run in PARALLEL, so the collector needs a lock: without one
	// the two appends race and the test drops a leg at random (seen 2026-09-11).
	var ranMu sync.Mutex
	ran := []string{}
	record := func(leg string) {
		ranMu.Lock()
		defer ranMu.Unlock()
		ran = append(ran, leg)
	}
	b.frontierFn = func(task string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		record("frontier")
		return captaincode.Result{Text: "FRONTIER-DEEP-ANSWER with enough substance to pass the gates", DurationMs: 9000}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		record(string(leg))
		return leg, captaincode.Result{Text: "grok answer with enough words to count as a real deliverable", DurationMs: 3000}, nil
	}
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "r", Workers: []captaincode.Worker{{Leg: captaincode.LegFrontier, Brief: "deep"}, {Leg: captaincode.LegGrok, Brief: "fast"}}}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "SYNTH"}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/team /frontier continue the work of the other agent"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "SYNTH")
	ranMu.Lock()
	got := append([]string(nil), ran...)
	ranMu.Unlock()
	assert.ElementsMatch(t, []string{"frontier", "grok"}, got, "the frontier runner ran, not an opencode dispatch of a leg named 'frontier'")
	b.mu.Lock()
	defer b.mu.Unlock()
	legs := []captaincode.Leg{}
	for _, ev := range b.ledger.Events {
		if ev.Leg != "" {
			legs = append(legs, ev.Leg)
		}
	}
	assert.Contains(t, legs, captaincode.LegClaude, "frontier work builds claude's scorecard")
	assert.NotContains(t, legs, captaincode.LegFrontier)
}

// 3. An error raised after the SSE stream has started must still reach the
// user as text - writing a JSON error body into a committed event stream
// produced "superfluous WriteHeader" in the log and a silent, empty turn.
func TestWorkerErrorAfterStreamStartedIsDeliveredInline(t *testing.T) {
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{}, assert.AnError
	}
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": true,
		"messages": []map[string]string{{"role": "user", "content": "/team do the thing"}}})
	rec := httptest.NewRecorder()
	// Simulate the keepalive having committed the stream before the plan
	// failed: wrap like chatCompletions does and touch the writer first.
	tw := trackWriter(rec)
	tw.WriteHeader(200)
	b.teamChat(tw, oaiChatReq{Model: "team", Stream: true, Messages: nil}, "[user]\ndo the thing\n\n")
	out := rec.Body.String()
	assert.Contains(t, out, "data: ", "delivered as SSE, not as a JSON body pasted into the stream")
	assert.Contains(t, out, "[captain] team failed", "the failure is spoken, not swallowed")
	assert.Contains(t, out, "[DONE]")
	_ = body
}

func TestChatCompletionsErrorBeforeStreamStaysAnHTTPError(t *testing.T) {
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{}, assert.AnError
	}
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": true,
		"messages": []map[string]string{{"role": "user", "content": "/team do the thing"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	assert.Equal(t, http.StatusBadGateway, rec.Code, "nothing streamed yet → a real HTTP error the SDK can act on")
	assert.True(t, strings.Contains(rec.Body.String(), `"error"`))
}
