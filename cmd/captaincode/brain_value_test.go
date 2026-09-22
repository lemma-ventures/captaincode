package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MM37 phases 3–4 in the brain: the cheap path ranks by value, a newly added
// model can win a domain by data alone, exploration tries the runner-up, and
// spend is estimated for API legs.

func routeLeg(t *testing.T, b *brain, task string) (routeResp, string) {
	t.Helper()
	body, _ := json.Marshal(routeReq{Task: task})
	rec := httptest.NewRecorder()
	b.route(rec, httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp, resp.Rationale
}

func TestRouteFastPathIsValueRanked(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "")
	t.Setenv("CAPTAIN_EXPLORE", "0")
	t.Setenv("CAPTAIN_VALUE_TAU", "5,7")
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGemini: true}
	resp, why := routeLeg(t, b, "tighten the wording of this paragraph and keep the argument intact")
	assert.Contains(t, why, "value-ranked")
	// gemini (7.8, $0.38/M) vs glm (8.2, $1.09/M): on a trivial editorial
	// task the price gap outweighs the quality gap with the default weights.
	assert.Equal(t, "gemini", resp.Leg, "gemini: near glm's quality, a third of the price - value wins")
	b.ledger.Cooldown(captaincode.LegGemini, time.Hour)
	resp, _ = routeLeg(t, b, "tighten the wording of this paragraph and keep the argument intact")
	assert.Equal(t, "glm", resp.Leg, "cooling legs drop out")
}

func TestRouteNewOverlayLegWinsItsDomainByData(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "")
	t.Setenv("CAPTAIN_EXPLORE", "0")
	t.Setenv("CAPTAIN_VALUE_TAU", "5,7")
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	path := filepath.Join(t.TempDir(), "legs.json")
	require.NoError(t, captaincode.AddLeg(path, captaincode.LegSpec{ID: "muse", Provider: "openrouter", Model: "meta/muse-spark-1.3-contributor",
		PriceIn: 1.0, PriceOut: 3.0, Prior: 7.2, DomainPrior: map[captaincode.Domain]float64{captaincode.DomainEditorial: 8.6}}))
	t.Cleanup(func() { captaincode.LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, "muse": true}
	resp, _ := routeLeg(t, b, "tighten the wording of this paragraph and keep the argument intact")
	assert.Equal(t, "muse", resp.Leg, "added by data, routed by data")
	resp, _ = routeLeg(t, b, "fix the nil pointer in the webhook retry loop and add a unit test")
	assert.Equal(t, "glm", resp.Leg, "on code its 7.2 loses to glm's 8.2 at the same price")
}

func TestRouteExploresTheRunnerUp(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "")
	t.Setenv("CAPTAIN_VALUE_TAU", "5,7")
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGemini: true}
	b.exploreFn = func(captaincode.Class) bool { return true }
	resp, why := routeLeg(t, b, "tighten the wording of this paragraph and keep the argument intact")
	assert.Equal(t, "glm", resp.Leg, "runner-up gets the turn")
	assert.Contains(t, why, "EXPLORE")
	b.mu.Lock()
	explored := b.wasExplored("tighten the wording of this paragraph and keep the argument intact")
	b.mu.Unlock()
	assert.True(t, explored)
	b.exploreFn = func(captaincode.Class) bool { return false }
	resp, _ = routeLeg(t, b, "tighten the wording of this paragraph and keep the argument intact")
	assert.Equal(t, "gemini", resp.Leg)
}

func TestExploreRateFromEnv(t *testing.T) {
	t.Setenv("CAPTAIN_EXPLORE", "")
	assert.Equal(t, 0.10, exploreRate(captaincode.ClassTrivial))
	assert.Equal(t, 0.05, exploreRate(captaincode.ClassMedium))
	t.Setenv("CAPTAIN_EXPLORE", "0")
	assert.Equal(t, 0.0, exploreRate(captaincode.ClassMedium))
	t.Setenv("CAPTAIN_EXPLORE", "0.2,0.1")
	assert.Equal(t, 0.2, exploreRate(captaincode.ClassTrivial))
	assert.Equal(t, 0.1, exploreRate(captaincode.ClassMedium))
}

func TestRecordRunEstimatesSpendAndTagsExploration(t *testing.T) {
	b := teamBrain()
	b.mu.Lock()
	b.markExplored("rewrite the intro")
	b.mu.Unlock()
	b.recordRun(captaincode.LegGLM, "[user]\nrewrite the intro\n\n", captaincode.Result{Text: "short", Tokens: 100_000, DurationMs: 1000}, "")
	b.mu.Lock()
	defer b.mu.Unlock()
	require.NotEmpty(t, b.ledger.Events)
	ev := b.ledger.Events[len(b.ledger.Events)-1]
	assert.InDelta(t, captaincode.EstimateCost(captaincode.LegGLM, 100_000), ev.CostUSD, 1e-9, "API spend estimated from the registry price")
	assert.Greater(t, ev.CostUSD, 0.0)
	assert.Equal(t, "explore", ev.Reason)
}

func TestRecordRunWritesTheChargeSpine(t *testing.T) {
	b := teamBrain()
	b.recordRun(captaincode.LegGLM, "[user]\nrewrite the intro\n\n", captaincode.Result{Text: "short", Tokens: 100_000, DurationMs: 1000}, "")
	b.mu.Lock()
	defer b.mu.Unlock()
	ev := b.ledger.Events[len(b.ledger.Events)-1]
	require.NotEmpty(t, ev.TaskID)
	assert.Equal(t, captaincode.UsageEstimated, ev.CostStatus, "a registry-priced run is not a bill")

	rows := captaincode.ChargeTree(b.ledger.Charges, ev.TaskID)
	require.Len(t, rows, 3)
	assert.Equal(t, captaincode.KindTask, rows[0].Kind)
	assert.Equal(t, captaincode.KindAttempt, rows[1].Kind)
	assert.Equal(t, captaincode.KindCall, rows[2].Kind)
	assert.Equal(t, ev.AttemptID, rows[2].Parent)

	totals := b.ledger.TaskTotals(ev.TaskID)
	assert.Equal(t, 1, totals.Calls, "only the call row is billed")
	assert.Equal(t, 100_000, totals.Tokens)
	assert.Equal(t, 1, totals.Estimated)
	assert.False(t, totals.Complete())
}

func TestPressureReadsRateLimits(t *testing.T) {
	b := teamBrain()
	assert.Equal(t, 0.0, b.pressure(captaincode.LegClaude))
	b.ledger.Events = append(b.ledger.Events, captaincode.Event{At: time.Now().Add(-time.Hour), Leg: captaincode.LegClaude, Outcome: "fail", Error: "rate limited: session limit"})
	assert.Equal(t, 0.5, b.pressure(captaincode.LegClaude), "rate-limited within the window → pressure")
	b.ledger.Cooldown(captaincode.LegClaude, time.Hour)
	assert.Equal(t, 1.0, b.pressure(captaincode.LegClaude), "cooling now → closed")
}

func TestDirectorMenuCarriesValueHints(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGemini: true, captaincode.LegCursor: true}
	var hints map[captaincode.Leg]string
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		hints = b.planHints
		return captaincode.Plan{Class: captaincode.ClassHigh, Workers: []captaincode.Worker{{Leg: captaincode.LegGLM, Brief: "b"}}}, nil
	}
	routeLeg(t, b, "redesign the ingestion pipeline for concurrency and add a migration plan")
	require.NotNil(t, hints)
	assert.Contains(t, hints[captaincode.LegGLM], "est $")
	assert.Contains(t, hints[captaincode.LegGLM], "value rank")
	assert.Contains(t, hints[captaincode.LegCursor], "subscription")
}

func TestPromptBudgetFromRegistryContext(t *testing.T) {
	t.Setenv("CAPTAIN_WRAPPER_MAX_PROMPT", "")
	assert.Equal(t, 700_000, promptBudget(captaincode.LegClaude))
	assert.Equal(t, 700_000, promptBudget(captaincode.LegFrontier))
	assert.Equal(t, 700_000, promptBudget(captaincode.LegCodex), "gpt-5.5-fast's 400k window × 3 chars, capped (spark's 128k left with spark, 2026-09-15)")
	assert.Equal(t, 131072*3, promptBudget(captaincode.LegMiniMax), "128k window × 3 chars")
	assert.Equal(t, 700_000, promptBudget(captaincode.LegGrok), "256k×3 capped")
}

func TestLegsAddWritesOverlayAndConfig(t *testing.T) {
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"meta/muse-spark-1.3","name":"Meta: Muse Spark 1.3","context_length":1048576,"pricing":{"prompt":"0.00000125","completion":"0.00000425"}}]}`))
	}))
	defer catalog.Close()
	old := openRouterCatalogURL
	openRouterCatalogURL = catalog.URL
	t.Cleanup(func() { openRouterCatalogURL = old; captaincode.LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	home := os.Getenv("HOME")
	cfgDir := filepath.Join(home, ".config", "opencode")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	cfgPath := filepath.Join(cfgDir, "opencode.jsonc")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{"provider":{"captain":{"models":{"claude":{"name":"Claude"}}},"openrouter":{"options":{"apiKey":"x"},"models":{}}},"command":{}}`), 0o600))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	_ = os.Remove(captaincode.RegistryOverlayPath())

	cmdLegsAdd([]string{"muse", "openrouter/meta/muse-spark-1.3", "--prior", "7.9", "--note", "Meta; value on prose"})

	assert.True(t, captaincode.KnownLeg("muse"))
	s, ok := captaincode.Spec("muse")
	require.True(t, ok)
	assert.InDelta(t, 1.25, s.PriceIn, 0.001, "price from the catalog")
	assert.Equal(t, 1048576, s.Ctx)
	assert.Equal(t, 7.9, s.Prior)
	raw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	cfg := string(raw)
	assert.Contains(t, cfg, `"muse"`, "captain model + command entries")
	assert.Contains(t, cfg, "meta/muse-spark-1.3", "listed under the provider block")
	ov, _ := os.ReadFile(captaincode.RegistryOverlayPath())
	assert.True(t, strings.Contains(string(ov), `"muse"`))
}

func TestRecordRunChargesTheRepairAttempt(t *testing.T) {
	b := teamBrain()
	// A nudged turn is two provider calls merged into one Result; the tokens
	// are the pair's, and the repair must appear without being billed twice.
	b.recordRun(captaincode.LegGLM, "[user]\nrewrite the intro\n\n",
		captaincode.Result{Text: "short", Tokens: 100_000, DurationMs: 1000, Nudged: true}, "")
	b.mu.Lock()
	defer b.mu.Unlock()
	ev := b.ledger.Events[len(b.ledger.Events)-1]

	labels := map[string]int{}
	for _, c := range captaincode.ChargeTree(b.ledger.Charges, ev.TaskID) {
		if c.Kind == captaincode.KindCall {
			labels[c.Label]++
		}
	}
	assert.Equal(t, map[string]int{"worker": 1, "repair": 1}, labels)

	totals := b.ledger.TaskTotals(ev.TaskID)
	assert.Equal(t, 2, totals.Calls)
	assert.Equal(t, 100_000, totals.Tokens, "the merged total is counted once, not per attempt")
	assert.Equal(t, 1, totals.Unknown, "the repair's own split was never measured")
	assert.False(t, totals.Complete())
}

func TestChargeAuxBillsReviewToTheSameTask(t *testing.T) {
	b := teamBrain()
	b.mu.Lock()
	taskID, _ := b.chargeTurn(captaincode.LegGLM, "rewrite the intro",
		captaincode.CallUsage(captaincode.LegGLM, 1000, 0, nil), 500, false)
	b.mu.Unlock()

	charge := b.chargeAux(taskID)
	require.NotNil(t, charge)
	// A director that answered, then a corrective retry that failed: both
	// spent quota, so both are charged.
	charge(captaincode.LegGrok, "review", captaincode.Result{Tokens: 300, DurationMs: 100}, nil)
	charge(captaincode.LegGrok, "review", captaincode.Result{}, assert.AnError)

	b.mu.Lock()
	defer b.mu.Unlock()
	totals := b.ledger.TaskTotals(taskID)
	assert.Equal(t, 3, totals.Calls, "the worker plus both review calls")
	assert.Equal(t, 1300, totals.Tokens)
	assert.Nil(t, b.chargeAux(""), "no task identity → nothing to bill it to")
}

// A leg that cannot run on this machine is rejected BEFORE ranking, with the
// reason `captain doctor` would print. Without this the brain ranked a leg it
// could not dispatch, and the run paid a round trip to find out
// (2026-09-22). A nil map is "no probe ran" and must filter nothing, which is
// what every other test in this file relies on.
func TestValueCandidatesRejectALegThatCannotRunHere(t *testing.T) {
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGemini: true}

	open, _ := b.valueCandidates(false)
	assert.Contains(t, open, captaincode.LegGemini, "no probe: nothing is filtered")

	b.legReady = map[captaincode.Leg]captaincode.Readiness{
		captaincode.LegGemini: {Leg: captaincode.LegGemini, Reason: "provider openrouter has no credential - `opencode auth login`"},
		captaincode.LegGLM:    {Leg: captaincode.LegGLM, OK: true},
	}
	open, excluded := b.valueCandidates(false)
	assert.NotContains(t, open, captaincode.LegGemini, "an unrunnable leg never reaches the ranking")
	assert.Contains(t, open, captaincode.LegGLM)

	var why string
	for _, e := range excluded {
		if e.Leg == captaincode.LegGemini {
			why = e.Excluded
		}
	}
	assert.Contains(t, why, "no credential", "`captain why` gets the reason, not just the absence")
}
