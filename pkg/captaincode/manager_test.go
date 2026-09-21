package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelForDirectorOverride(t *testing.T) {
	// grok-build-0.1 (the LegGrok worker model) is xAI's agentic coding
	// model: observed live to keep narrating tool calls as prose even with
	// tools disabled, breaking JSON-only director prompts. grok-4.7 is a
	// plain reasoning model that complies. The director path must use the
	// override, and worker dispatch (ModelID, non-director Run) must not.
	worker, ok := modelFor(LegGrok, false)
	require.True(t, ok)
	assert.Equal(t, "grok-build-0.1", worker.Model, "worker leg keeps the agentic model")

	director, ok := modelFor(LegGrok, true)
	require.True(t, ok)
	assert.Equal(t, "grok-4.7", director.Model, "director path must use the non-agentic override")
	assert.NotEqual(t, worker.Model, director.Model)

	frontier, ok := modelForEffort(LegGrok, false, EffortMax)
	require.True(t, ok)
	assert.Equal(t, "grok-4.7", frontier.Model, "/frontier /grok upgrades the burner to the flagship")

	assert.Equal(t, "grok-4.7-xhigh", cursorModel(EffortMax), "/frontier /cursor pins Grok 4.7 Extra High")
	assert.Empty(t, cursorModel(""), "bare cursor leaves the CLI default")
	assert.Equal(t, "grok-4.7-xhigh", ModelIDAt(LegCursor, EffortMax))
	assert.Equal(t, "composer-2.5", ModelID(LegCursor), "routine cursor stays Composer in the registry")

	// Codex as director must use the full-effort model, never the fast lane:
	// the latency-optimized variant is too weak to plan/assess (see
	// directorModels). 2026-09-15: the ChatGPT route refused spark and lost
	// gpt-5.3-codex; the pair is gpt-5.5-fast / gpt-5.5 now.
	codexWorker, _ := modelFor(LegCodex, false)
	codexDirector, _ := modelFor(LegCodex, true)
	assert.Equal(t, "gpt-5.5-fast", codexWorker.Model, "worker leg keeps the fast variant")
	assert.Equal(t, "gpt-5.5", codexDirector.Model, "director path must use the full-effort model")
	codexFrontier, _ := modelForEffort(LegCodex, false, EffortMax)
	assert.Equal(t, "gpt-5.5", codexFrontier.Model, "/frontier /codex also takes the full-effort twin")

	// A leg with no director override (free) falls back to its regular
	// worker model either way.
	freeAsWorker, _ := modelFor(LegFree, false)
	freeAsDirector, _ := modelFor(LegFree, true)
	assert.Equal(t, freeAsWorker, freeAsDirector, "no override defined -> same model both ways")
}

func TestExtractJSON(t *testing.T) {
	t.Run("plain JSON", func(t *testing.T) {
		var got struct {
			Quality float64 `json:"quality"`
		}
		require.NoError(t, extractJSON(`{"quality": 8.5}`, &got))
		assert.Equal(t, 8.5, got.Quality)
	})

	t.Run("JSON wrapped in prose", func(t *testing.T) {
		var got struct {
			Quality float64 `json:"quality"`
		}
		require.NoError(t, extractJSON("Here's my verdict:\n"+`{"quality": 7}`+"\nDone.", &got))
		assert.Equal(t, 7.0, got.Quality)
	})

	t.Run("worker output with braces precedes the real verdict (the bug from live testing)", func(t *testing.T) {
		// Reproduces the exact failure: the assessor's prompt embeds the
		// worker's output (a Go struct with its own braces); if the director
		// echoes any of it before its verdict, first-'{'-to-last-'}' grabs
		// the wrong span. extractJSON must find the LAST balanced span that
		// actually parses.
		reply := "The worker found:\n```go\n" +
			`type Project struct {
	ID   string ` + "`json:\"id\"`" + `
	Name string ` + "`json:\"name\"`" + `
}` +
			"\n```\nVerdict: " + `{"quality": 9, "verdict": "good", "notes": "correct struct"}`
		var got Assessment
		require.NoError(t, extractJSON(reply, &got))
		assert.Equal(t, 9.0, got.Quality)
		assert.Equal(t, "good", got.Verdict)
	})

	t.Run("no JSON at all", func(t *testing.T) {
		var got struct{}
		assert.Error(t, extractJSON("I refuse to answer in JSON.", &got))
	})

	t.Run("multiple JSON-shaped spans: last valid one wins", func(t *testing.T) {
		var got struct {
			Quality float64 `json:"quality"`
		}
		// first span isn't the real verdict but IS syntactically valid JSON -
		// extractJSON should prefer the LAST parseable span (the director's
		// actual final answer), not the first.
		reply := `{"leg":"free"}` + "\nActually: " + `{"quality": 6}`
		require.NoError(t, extractJSON(reply, &got))
		assert.Equal(t, 6.0, got.Quality)
	})

	t.Run("class field parsed as plain text", func(t *testing.T) {
		var got struct {
			Class string `json:"class"`
		}
		require.NoError(t, extractJSON(`{"class":"medium","workers":[]}`, &got))
		assert.Equal(t, "medium", got.Class)
	})
}

// The director's menu must expose the class dimension and past ensembles -
// flat averages hide that a leg (or team) is strong on one class of task and
// weak on another.
func TestPlanPromptIncludesClassBreakdownAndTeams(t *testing.T) {
	stats := map[Leg]LegStats{
		LegCodex: {N: 5, Scored: 5, AvgQuality: 7.0, ByClass: map[Class]ClassStat{
			ClassMedium: {N: 3, Scored: 3, AvgQuality: 8.5},
			ClassHigh:   {N: 2, Scored: 2, AvgQuality: 4.8},
		}},
	}
	teams := map[string]TeamStat{
		"codex+glm": {N: 3, Scored: 3, AvgQuality: 8.7, ByClass: map[Class]ClassStat{
			ClassMedium: {N: 3, Scored: 3, AvgQuality: 8.7},
		}},
	}
	p := buildPlanPrompt("integrate the auth seam", "", "", []Leg{LegCodex}, stats, teams, true)
	assert.Contains(t, p, "medium 8.5", "per-class quality must be visible")
	assert.Contains(t, p, "high 4.8", "so the director can avoid a leg's weak class")
	assert.Contains(t, p, "codex+glm", "past ensembles are part of the menu")
	assert.Contains(t, p, "8.7")

	solo := buildPlanPrompt("task", "", "", []Leg{LegCodex}, stats, teams, false)
	assert.NotContains(t, solo, "codex+glm", "no fan-out → team stats are noise")
}

// The director's menu must show reliability, not just quality - a 9.0 leg
// that stalls half the time is not a 9.0 leg.
func TestPlanPromptShowsFailures(t *testing.T) {
	stats := map[Leg]LegStats{LegGLM: {N: 3, Scored: 3, AvgQuality: 9.0, Fails: 4}}
	p := buildPlanPrompt("task", "", "", []Leg{LegGLM}, stats, nil, false)
	assert.Contains(t, p, "FAILED_RUNS=4", "reliability must be in the menu")
	stats[LegGLM] = LegStats{N: 3, Scored: 3, AvgQuality: 9.0}
	p = buildPlanPrompt("task", "", "", []Leg{LegGLM}, stats, nil, false)
	assert.NotContains(t, p, "FAILED_RUNS", "no noise when a leg is clean")
}

// /quality is a command, not a suggestion: when the user explicitly asks for
// maximum quality, the director must be told cost is off the table (live
// 2026-07-25: prefer=quality still routed trivial work to the free leg).
func TestPlanPromptPreferenceIsBinding(t *testing.T) {
	p := buildPlanPrompt("task", "", "quality", []Leg{LegCursor}, nil, nil, false)
	assert.Contains(t, p, "EXPLICITLY requested maximum quality")
	p = buildPlanPrompt("task", "", "save", []Leg{LegCursor}, nil, nil, false)
	assert.Contains(t, p, "cheapest")
	p = buildPlanPrompt("task", "", "", []Leg{LegCursor}, nil, nil, false)
	assert.NotContains(t, p, "EXPLICITLY requested")
}

// 2. The director named a leg that is not on its menu (live 2026-09-09: codex,
// excluded by CAPTAIN_LEGS). Failing the whole turn over one off-menu pick
// throws away a plan that is otherwise fine: substitute the strongest open
// leg, say so in the rationale, and never duplicate a worker.
func TestValidatePlanSubstitutesOffMenuLegs(t *testing.T) {
	open := []Leg{LegCursor, LegGrok, LegFree}
	p := Plan{Class: ClassHigh, Rationale: "r", Workers: []Worker{{Leg: LegCodex, Brief: "a"}, {Leg: LegGrok, Brief: "b"}}}
	got, err := validatePlan(p, "task", open, true)
	require.NoError(t, err)
	assert.Equal(t, LegCursor, got.Workers[0].Leg, "strongest open leg replaces the off-menu pick")
	assert.Equal(t, LegGrok, got.Workers[1].Leg)
	assert.Contains(t, got.Rationale, "codex", "the substitution is visible")

	// A second off-menu pick must not collapse onto the same substitute.
	p = Plan{Workers: []Worker{{Leg: LegCodex, Brief: "a"}, {Leg: LegGLM, Brief: "b"}, {Leg: LegCursor, Brief: "c"}}}
	got, err = validatePlan(p, "task", open, true)
	require.NoError(t, err)
	assert.Equal(t, []Leg{LegGrok, LegFree, LegCursor}, []Leg{got.Workers[0].Leg, got.Workers[1].Leg, got.Workers[2].Leg},
		"cursor is already taken by the director's own pick, so the substitutes are the next open legs")

	// Nothing open to substitute → still an error, not a silent drop.
	_, err = validatePlan(Plan{Workers: []Worker{{Leg: LegCodex, Brief: "a"}}}, "task", nil, true)
	require.Error(t, err)

	// Worker-count rules are unchanged.
	_, err = validatePlan(Plan{Workers: []Worker{{Leg: LegGrok}, {Leg: LegFree}}}, "task", open, false)
	require.Error(t, err, "fan-out disallowed")
	got, err = validatePlan(Plan{Workers: []Worker{{Leg: LegGrok, Brief: "  "}}}, "task", open, true)
	require.NoError(t, err)
	assert.Equal(t, "task", got.Workers[0].Brief, "empty brief → the task")
}

func TestPlanPromptCarriesRequiredLegsAsBinding(t *testing.T) {
	out := buildPlanPrompt("continue the work", "", "", []Leg{LegFrontier, LegGrok}, nil, nil, true, LegFrontier)
	assert.Contains(t, out, "BINDING")
	assert.Contains(t, out, "frontier")
	assert.Contains(t, out, legDescription(LegFrontier), "the menu describes the frontier pseudo-leg")
	assert.NotEmpty(t, legDescription(LegFrontier))
}
