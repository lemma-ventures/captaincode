package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// /oss narrows the route to open-weight legs, wherever the word stands;
// /deterministic to the legs whose serving tuple is green in ADI, and the
// run is pinned to that tuple through the proxy.
func TestPoolWordsNarrowTheRoute(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	old := captaincode.ADISnapshot()
	defer captaincode.SetADIFeed(old)
	captaincode.SetADIFeed(&captaincode.ADIFeed{RunStamp: "x", Tuples: []captaincode.ADITuple{
		{Provider: "openrouter", Model: "openai/gpt-oss-120b", Label: "Cerebras via OpenRouter", Rank: 1, Streak: 10, MeanModeShare: 1, Green: true,
			ProviderPrefs: map[string]any{"order": []any{"cerebras"}, "allow_fallbacks": false}},
	}})
	b := teamBrain()
	var menus [][]captaincode.Leg
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		menus = append(menus, open)
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: open[0], Brief: task}}}, nil
	}
	for _, task := range []string{"/oss refactor the lexer", "refactor the lexer /oss", "/repeat 1 /oss refactor the lexer"} {
		rec := httptest.NewRecorder()
		b.route(rec, jsonReq("/v1/route", map[string]any{"task": task}))
		require.Equal(t, 200, rec.Code, rec.Body.String())
	}
	require.Len(t, menus, 3)
	for _, m := range menus {
		require.NotEmpty(t, m)
		for _, l := range m {
			assert.True(t, captaincode.OpenWeights(l), "%s is not open-weight", l)
		}
	}

	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": "/deterministic summarise the spec"}))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"leg":"gpt-oss"`, "the one green leg is the only option, no director needed")
}

// Nothing in the pool → the turn still runs, and the feed says why.
func TestEmptyPoolFallsBackAndSaysWhy(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	old := captaincode.ADISnapshot()
	defer captaincode.SetADIFeed(old)
	captaincode.SetADIFeed(&captaincode.ADIFeed{RunStamp: "x", Tuples: []captaincode.ADITuple{
		{Provider: "openrouter", Model: "meta-llama/llama-3.1-8b-instruct", Label: "Groq via OpenRouter", Rank: 1, Streak: 3, Green: true},
	}})
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: open[0], Brief: task}}}, nil
	}
	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": "/deterministic summarise the spec"}))
	require.Equal(t, 200, rec.Code)
	b.amu.Lock()
	feed := append([]activity(nil), b.acts...)
	b.amu.Unlock()
	var found bool
	for _, a := range feed {
		if a.Leg == "pool" && bytes.Contains([]byte(a.Text), []byte("llama-3.1-8b")) {
			found = true
		}
	}
	assert.True(t, found, "the feed names the green tuple no leg serves: %+v", feed)
}

// The proxy pin: a request for the pinned model gets the ADI provider_prefs
// and temperature 0; other models are untouched; the pin lifts with the run.
func TestProxyPinRewritesOnlyThePinnedModel(t *testing.T) {
	release := proxyPins.hold("openai/gpt-oss-120b", map[string]any{"order": []any{"cerebras"}, "allow_fallbacks": false})
	body := []byte(`{"model":"openai/gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`)
	out, ok := proxyPins.apply(body)
	require.True(t, ok)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(out, &obj))
	assert.Equal(t, []any{"cerebras"}, obj["provider"].(map[string]any)["order"])
	assert.Equal(t, float64(0), obj["temperature"])

	other := []byte(`{"model":"z-ai/glm-5.3","messages":[]}`)
	_, ok = proxyPins.apply(other)
	assert.False(t, ok)

	withTemp := []byte(`{"model":"OPENAI/gpt-oss-120b","temperature":0.7}`)
	out, ok = proxyPins.apply(withTemp)
	require.True(t, ok, "model ids match case-insensitively")
	require.NoError(t, json.Unmarshal(out, &obj))
	assert.Equal(t, 0.7, obj["temperature"], "an explicit temperature is the caller's")

	release()
	_, ok = proxyPins.apply(body)
	assert.False(t, ok, "released with the run")
}

// Modifiers before a control word are hoisted past it, so "/oss /repeat 1
// <task>" starts the loop with the pool intact, and "/oss /team <task>" runs
// as a team although the plugin forced nothing.
func TestModifiersBeforeAControlWordStillCompose(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	var teamMenu []captaincode.Leg
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		teamMenu = open
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: open[0], Brief: task}}}, nil
	}
	var prompts []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		prompts = append(prompts, prompt)
		return leg, captaincode.Result{Text: "done", DurationMs: 5}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "auto", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/oss /team review the lexer"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.NotEmpty(t, teamMenu, "the turn ran as a team")
	for _, l := range teamMenu {
		assert.True(t, captaincode.OpenWeights(l), "team menu under /oss: %v", teamMenu)
	}

	body, _ = json.Marshal(map[string]any{"model": "auto", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/oss /repeat 1 review the lexer"}}})
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "repeat thread rp_", "the loop started with the modifier hoisted past /repeat")
}

// A preference at the HEAD of an auto-routed turn ("/quality fix …") never
// reached the route: promptFrom strips leading directives and the route read
// the stripped task, so only a mid-prompt "/quality" counted (2026-09-17).
func TestLeadingPreferenceReachesTheRouteInAutoMode(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	seen := "unset"
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		seen = prefer
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: open[0], Brief: task}}}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		assert.NotContains(t, prompt, "/quality", "the worker never sees the directive")
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "auto", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/quality refactor the lexer"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, "quality", seen)
}
