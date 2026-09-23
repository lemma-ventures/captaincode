package captaincode

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		task string
		want Class
	}{
		{"fix typo in README", ClassTrivial},
		{"bump the version and update changelog", ClassTrivial},
		{"add a null check in the sms webhook", ClassMedium},
		{"redesign the trigger engine architecture", ClassHigh},
		{"debug the race in worker drain", ClassHigh},
		{"refactor auth across the codebase", ClassHigh},
	}
	for _, tt := range tests {
		t.Run(tt.task, func(t *testing.T) {
			assert.Equal(t, tt.want, Classify(tt.task))
		})
	}
}

func TestParseClass(t *testing.T) {
	got, ok := ParseClass("trivial")
	assert.True(t, ok)
	assert.Equal(t, ClassTrivial, got)

	got, ok = ParseClass("  MEDIUM ")
	assert.True(t, ok)
	assert.Equal(t, ClassMedium, got)

	got, ok = ParseClass("high")
	assert.True(t, ok)
	assert.Equal(t, ClassHigh, got)

	_, ok = ParseClass("unknown")
	assert.False(t, ok)
}

func TestResolveRoute_DirectorClassAuthoritative(t *testing.T) {
	ladder := []Leg{LegFree, LegCodex, LegClaude}
	// Heuristic would say trivial; director says high and picks claude.
	plan := Plan{
		Class:     ClassHigh,
		Rationale: "needs architecture judgment",
		Workers:   []Worker{{Leg: LegClaude, Brief: "redesign the auth seam"}},
	}
	rr := ResolveRoute("fix typo in README", ClassTrivial, ladder, plan, nil)
	assert.True(t, rr.Managed)
	assert.False(t, rr.FanOut)
	assert.Equal(t, ClassHigh, rr.Class, "director class overrides heuristic")
	assert.Equal(t, []Leg{LegClaude, LegFree, LegCodex}, rr.Order)
	assert.Equal(t, "redesign the auth seam", rr.Brief)
	assert.Contains(t, rr.Reason, "manager:")
}

func TestResolveRoute_FallbackOnDirectorError(t *testing.T) {
	ladder := []Leg{LegFree, LegCodex}
	rr := ResolveRoute("add pagination", ClassMedium, ladder, Plan{}, assertErr("director down"))
	assert.False(t, rr.Managed)
	assert.Equal(t, ClassMedium, rr.Class, "heuristic classHint on failure")
	assert.Equal(t, ladder, rr.Order)
	assert.Equal(t, "add pagination", rr.Brief)
	assert.Equal(t, "director unavailable → ladder", rr.Reason)
}

func TestResolveRoute_FallbackOnEmptyWorkers(t *testing.T) {
	ladder := []Leg{LegFree}
	rr := ResolveRoute("task", ClassHigh, ladder, Plan{Class: ClassTrivial, Workers: nil}, nil)
	assert.False(t, rr.Managed)
	assert.Equal(t, ClassHigh, rr.Class)
	assert.Equal(t, ladder, rr.Order)
}

func TestResolveRoute_FanOut(t *testing.T) {
	plan := Plan{
		Class: ClassMedium,
		Workers: []Worker{
			{Leg: LegFree, Brief: "part A"},
			{Leg: LegCodex, Brief: "part B"},
		},
		Rationale: "independent subtasks",
	}
	rr := ResolveRoute("do both", ClassTrivial, []Leg{LegFree, LegCodex}, plan, nil)
	assert.True(t, rr.Managed)
	assert.True(t, rr.FanOut)
	assert.Equal(t, ClassMedium, rr.Class)
	assert.Equal(t, 2, len(rr.Plan.Workers))
}

func TestResolveRoute_EmptyBriefUsesTask(t *testing.T) {
	plan := Plan{Class: ClassMedium, Workers: []Worker{{Leg: LegFree, Brief: "  "}}}
	rr := ResolveRoute("original task", ClassMedium, []Leg{LegFree}, plan, nil)
	assert.Equal(t, "original task", rr.Brief)
}

// assertErr is a tiny error helper so ResolveRoute tests stay readable.
type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestStartRung(t *testing.T) {
	assert.Equal(t, 0, StartRung(ClassTrivial, ""))
	assert.Equal(t, 1, StartRung(ClassMedium, ""))
	assert.Equal(t, len(Rungs)-1, StartRung(ClassHigh, ""))
	assert.Equal(t, 0, StartRung(ClassHigh, "save"))
	assert.Equal(t, len(Rungs)-1, StartRung(ClassTrivial, "quality"))
	assert.Equal(t, 1, StartRung(ClassTrivial, "speed"))
}

func TestDirectorExcludedFromWorkerLadder(t *testing.T) {
	defer SetDirector(LegGrok) // restore default
	SetDirector(LegGrok)
	assert.Equal(t, []Leg{LegFree, LegQwen, LegStep, LegGPTOSS, LegLuna, LegDS4Flash, LegMiniMax, LegDeepSeek, LegGemini, LegKimi, LegCursor, LegGLM, LegCodex, LegGrokMax, LegCodexCLI, LegClaude}, Rungs, "grok director → grok not a worker; cursor sits below codex-cli and claude")
	assert.NotContains(t, Rungs, LegGrok)
	SetDirector(LegClaude)
	assert.Equal(t, []Leg{LegFree, LegQwen, LegStep, LegGPTOSS, LegGrok, LegLuna, LegDS4Flash, LegMiniMax, LegDeepSeek, LegGemini, LegKimi, LegCursor, LegGLM, LegCodex, LegGrokMax, LegCodexCLI}, Rungs, "claude director → claude not a worker; codex-cli is the top worker rung")
}

func TestPickSkipsCooldownsAndFallsBack(t *testing.T) {
	// Pin the director so the worker ladder is deterministic: claude director →
	// Rungs = {free, qwen, step, gpt-oss, grok, luna, ds4-flash, minimax, deepseek, gemini, kimi, cursor, glm, codex, grok-max, codex-cli}.
	defer SetDirector(LegGrok)
	SetDirector(LegClaude)
	now := time.Now()
	cool := map[Leg]time.Time{LegLuna: now.Add(time.Hour)}

	got := Pick(5, cool, now) // start at luna's rung (index 5)
	assert.Equal(t, []Leg{LegDS4Flash, LegMiniMax, LegDeepSeek, LegGemini, LegKimi, LegCursor, LegGLM, LegCodex, LegGrokMax, LegCodexCLI, LegGrok, LegGPTOSS, LegStep, LegQwen, LegFree}, got, "luna cooling: take ds4-flash above, then fall back down the ladder")

	got = Pick(0, map[Leg]time.Time{}, now)
	assert.Equal(t, []Leg{LegFree, LegQwen, LegStep, LegGPTOSS, LegGrok, LegLuna, LegDS4Flash, LegMiniMax, LegDeepSeek, LegGemini, LegKimi, LegCursor, LegGLM, LegCodex, LegGrokMax, LegCodexCLI}, got)

	expired := map[Leg]time.Time{LegFree: now.Add(-time.Minute)}
	got = Pick(0, expired, now)
	assert.Equal(t, []Leg{LegFree, LegQwen, LegStep, LegGPTOSS, LegGrok, LegLuna, LegDS4Flash, LegMiniMax, LegDeepSeek, LegGemini, LegKimi, LegCursor, LegGLM, LegCodex, LegGrokMax, LegCodexCLI}, got, "expired cooldown reopens the leg")
}

func TestModelSpecQwenAndGLM(t *testing.T) {
	p, m, ok := ModelSpec(LegQwen)
	assert.True(t, ok)
	assert.Equal(t, "openrouter", p, "Scaleway instance stopped 2026-08-24; qwen is hosted OSS now")
	assert.Contains(t, m, "qwen3.5") // any policy-eligible qwen3.5 variant

	p, m, ok = ModelSpec(LegGLM)
	assert.True(t, ok)
	assert.Equal(t, "openrouter", p, "NIM retired its whole GLM line (410 Gone, 2026-08-24)")
	assert.Contains(t, m, "glm")

	p, m, ok = ModelSpec(LegMiniMax)
	assert.True(t, ok)
	assert.Equal(t, "openrouter", p, "NIM retired every MiniMax model too (410 Gone, 2026-09-09)")
	assert.Contains(t, m, "minimax")
}

func TestCompileLoop(t *testing.T) {
	p := CompileLoop("fix the flaky auth test until tests pass", "", 0)
	assert.Equal(t, "go test ./...", p.Until)
	assert.Equal(t, 5, p.MaxIters)

	p = CompileLoop("clean up imports", "", 3)
	assert.Empty(t, p.Until, "no loop intent -> single shot")

	p = CompileLoop("keep going until the build passes", "make lint", 2)
	assert.Equal(t, "make lint", p.Until, "explicit --until wins over NL detection")
	assert.Equal(t, 2, p.MaxIters)
}

// TestTaskNeedsVision: tasks that reference an image (path or screenshot
// language) must be detectable so routing can exclude blind legs - live
// 2026-07-19: "logo not readable ... see screens/bug-logo.png" was routed to
// the free leg (deepseek-flash, text-only), which could only reply "I can't
// view the image".
func TestTaskNeedsVision(t *testing.T) {
	yes := []string{
		"logo is still not readable, see screens/bug-logo.png",
		"logo is still not readable as captain code, the typo is not exact, sse screen capture in screens/bug-logo.png",
		"can you inspect this sse screen-shot from /tmp/bug-logo.png",
		"compare with design.JPEG please",
		"here is a screenshot of the failure",
		"attached a screen capture of the TUI",
		"can you read the sse screen shot in /tmp/bug-logo.jpeg",
		"fix the layout per mockup.webp",
	}
	for _, task := range yes {
		assert.True(t, TaskNeedsVision(task), task)
	}
	no := []string{
		"fix typo in README",
		"the png encoder test is failing", // mentions png as a word, not a file
		"refactor image_test.go helpers",
		"debug the race in worker drain",
	}
	for _, task := range no {
		assert.False(t, TaskNeedsVision(task), task)
	}
}

// Vision capability: claude/codex/cursor/grok accept images; the OSS text
// legs (free/qwen/glm/minimax) don't. CAPTAIN_VISION_LEGS overrides.
func TestLegSupportsVisionAndFilter(t *testing.T) {
	assert.True(t, LegSupportsVision(LegClaude))
	assert.True(t, LegSupportsVision(LegCodex))
	assert.False(t, LegSupportsVision(LegFree))
	assert.False(t, LegSupportsVision(LegGLM))

	got := FilterVision([]Leg{LegFree, LegQwen, LegCodex, LegGLM, LegClaude})
	assert.Equal(t, []Leg{LegCodex, LegClaude}, got)
}

func TestVisionLegsEnvOverride(t *testing.T) {
	t.Setenv("CAPTAIN_VISION_LEGS", "glm,claude")
	reloadVisionLegs()
	t.Cleanup(func() { os.Unsetenv("CAPTAIN_VISION_LEGS"); reloadVisionLegs() })
	assert.True(t, LegSupportsVision(LegGLM))
	assert.False(t, LegSupportsVision(LegCodex))
}

// /quality constrains the MENU, not just the prompt: the director once read
// "maximum quality" and still picked the free leg because its trivial-class
// average was 9.0 (2026-07-25). TopQuality keeps only the strongest legs by
// overall blended quality, so /quality structurally cannot land on a budget leg.
func TestTopQuality(t *testing.T) {
	order := []Leg{LegFree, LegGrok, LegLuna, LegCodex, LegMiniMax, LegGLM, LegCursor}
	got := TopQuality(order, nil, 2) // no local stats → priors: codex (GPT-6 Sol) 8.4, glm 8.2
	assert.Equal(t, []Leg{LegCodex, LegGLM}, got)
	assert.Equal(t, []Leg{LegCursor}, TopQuality([]Leg{LegCursor}, nil, 2), "fewer legs than n is fine")
	assert.Empty(t, TopQuality(nil, nil, 2))

	// The strongest prior with a record of stalls is not the best (kimi,
	// 2026-09-18): it sorts after every reliable leg.
	stats := map[Leg]LegStats{LegKimi: {N: 0, Fails: 11}}
	got = TopQuality([]Leg{LegFree, LegKimi, LegGLM, LegCursor}, stats, 2)
	assert.Equal(t, []Leg{LegGLM, LegCursor}, got, "kimi (prior 8.0) is unreliable and drops out of the top two")
	got = TopQuality([]Leg{LegFree, LegKimi}, stats, 2)
	assert.Equal(t, []Leg{LegFree, LegKimi}, got, "…but still makes the menu when nothing reliable outranks it")
}
