package captaincode

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The injected skill and the normative spec must not drift: the grammar the
// director is taught IS the grammar the parser enforces (spec §4.1).
func TestWorkflowSkillMatchesTheSpec(t *testing.T) {
	doc, err := os.ReadFile("../../docs/WORKFLOW_LANGUAGE.md")
	require.NoError(t, err, "the spec must exist next to the skill")
	spec, skill := string(doc), WorkflowSkill()

	// The grammar the director is taught IS the grammar the spec defines.
	for _, rule := range []string{
		"workflow    = stage { SEQ stage } ;",
		"stage       = leg { PAR leg } ;",
	} {
		assert.Contains(t, skill, rule, "skill teaches the grammar")
		assert.Contains(t, spec, rule, "spec defines the same rule")
	}

	// Limits: parser == skill == spec. Three places, one set of numbers.
	limits := []struct {
		specRow string
		skill   string
		code    int
	}{
		{"| stages |", "at most 4 stages", MaxWorkflowStages},
		{"| legs per stage |", "at most 4 legs", MaxStageWidth},
		{"| total worker runs |", "at most 8 worker runs", MaxWorkflowRuns},
	}
	num := regexp.MustCompile(`\d+`)
	for _, l := range limits {
		assert.Contains(t, skill, l.skill, "skill states the limit")
		row := ""
		for _, line := range strings.Split(spec, "\n") {
			if strings.HasPrefix(line, l.specRow) {
				row = line
				break
			}
		}
		require.NotEmpty(t, row, "spec has a %s row", l.specRow)
		m := num.FindString(row)
		require.NotEmpty(t, m, "spec row carries a number: %s", row)
		assert.Equal(t, strconv.Itoa(l.code), m, "spec and parser disagree on %s", l.specRow)
		assert.Contains(t, l.skill, strconv.Itoa(l.code), "skill and parser disagree on %s", l.specRow)
	}

	// Every legname the skill advertises must parse.
	for _, leg := range []string{"grok", "claude", "codex", "cursor", "free", "glm", "minimax", "qwen", "deepseek", "gemini", "kimi", "codex-cli", "frontier"} {
		assert.Contains(t, skill, `"`+leg+`"`, "skill lists %s", leg)
		_, err := ParseWorkflow("/" + leg + " do it > /claude review it")
		assert.NoError(t, err, "skill advertises an unparseable leg: %s", leg)
	}

	// Every expression in the skill's worked-examples table must parse.
	checked := 0
	for _, line := range strings.Split(skill, "\n") {
		if !strings.HasPrefix(line, "| \"") || !strings.Contains(line, "`/") {
			continue
		}
		expr := line[strings.Index(line, "`/")+1:]
		expr = expr[:strings.Index(expr, "`")]
		if strings.Contains(expr, "<") {
			continue // placeholder example
		}
		_, err := ParseWorkflow(expr)
		assert.NoError(t, err, "skill example must parse: %s", expr)
		checked++
	}
	assert.GreaterOrEqual(t, checked, 3, "the skill must carry worked examples")
}

func TestCompileRejectsAMismatchedDeclaration(t *testing.T) {
	wf, err := ParseWorkflow("/grok a > /cursor b")
	require.NoError(t, err)

	// Declared stages disagree with the expression: refuse rather than run
	// something the user never saw in the preview.
	reply := compileReply{Expression: "/grok a > /cursor b"}
	reply.Stages = []struct {
		Legs []struct {
			Leg    string `json:"leg"`
			Prompt string `json:"prompt"`
		} `json:"legs"`
		Purpose string `json:"purpose"`
	}{{}, {}, {}}
	err = matchesDeclaredStages(wf, reply)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 stages but it declared 3")

	// Same topology, wrong leg.
	reply.Stages = reply.Stages[:2]
	reply.Stages[0].Legs = []struct {
		Leg    string `json:"leg"`
		Prompt string `json:"prompt"`
	}{{Leg: "claude"}}
	reply.Stages[1].Legs = []struct {
		Leg    string `json:"leg"`
		Prompt string `json:"prompt"`
	}{{Leg: "cursor"}}
	err = matchesDeclaredStages(wf, reply)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expression says grok")
}

func TestEstimateWorkflowUsesStageMaxima(t *testing.T) {
	wf, err := ParseWorkflow("/free fast + /grok slow > /claude review")
	require.NoError(t, err)
	stats := map[Leg]LegStats{
		LegFree:   {N: 10, Scored: 5, AvgDurationMs: 5_000},
		LegGrok:   {N: 10, Scored: 5, AvgDurationMs: 60_000},
		LegClaude: {N: 10, Scored: 5, AvgDurationMs: 120_000},
	}
	low, high := EstimateWorkflow(wf, stats, 20*time.Second)
	// stage 1 is gated by grok (60s), stage 2 by claude (120s), plus review.
	assert.Greater(t, low, 100*time.Second)
	assert.Less(t, low, high)
	assert.Greater(t, high, 200*time.Second)

	// No evidence at all: still a usable, wider range.
	lo2, hi2 := EstimateWorkflow(wf, map[Leg]LegStats{}, 0)
	assert.Greater(t, lo2, time.Duration(0))
	assert.Greater(t, hi2, lo2*2-time.Second)
}

// Bare-leg assignment inheritance (2026-08-24, user request): repeating the
// task per stage was the top syntax complaint. "/grok review X > /codex"
// runs codex with the SAME prompt (plus, as always, the upstream output).
func TestBareLegInheritsAssignment(t *testing.T) {
	wf, err := ParseWorkflow("/grok review these content/ materials > /codex")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 2)
	assert.Equal(t, "review these content/ materials", wf.Stages[0].Legs[0].Prompt)
	assert.Equal(t, "review these content/ materials", wf.Stages[1].Legs[0].Prompt)

	wf, err = ParseWorkflow("/grok review the paper + /codex")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 1)
	require.Len(t, wf.Stages[0].Legs, 2)
	assert.Equal(t, "review the paper", wf.Stages[0].Legs[1].Prompt, "parallel bare leg mirrors its stage sibling")

	wf, err = ParseWorkflow("/grok analyse the repo > /codex polish it")
	require.NoError(t, err)
	assert.Equal(t, "polish it", wf.Stages[1].Legs[0].Prompt, "an explicit prompt is never overridden")

	wf, err = ParseWorkflow("/grok build it gate: go test ./... > /codex")
	require.NoError(t, err)
	assert.Equal(t, "build it", wf.Stages[1].Legs[0].Prompt, "inherits the prompt")
	assert.Empty(t, wf.Stages[1].Legs[0].Gate, "gates are NOT inherited")
}

// The trailing-task style: legs listed first, one prompt at the end fans out
// to every bare sibling in the stage (2026-08-25, live miss: a 4-leg opinion
// panel ran as solo codex).
func TestTrailingTaskFillsBareParallelLegs(t *testing.T) {
	wf, err := ParseWorkflow("/codex + /cursor + /grok + /glm what is your opinion about this.")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 1)
	require.Len(t, wf.Stages[0].Legs, 4)
	for _, l := range wf.Stages[0].Legs {
		assert.Equal(t, "what is your opinion about this.", l.Prompt, string(l.Leg))
	}
}

// An over-limit workflow ATTEMPT must be reported, never silently demoted to
// a solo forced run (the 2026-08-25 4-leg-panel incident).
func TestOverLimitWorkflowLooksLikeWorkflow(t *testing.T) {
	wide := "/grok a + /cursor b + /codex c + /claude d + /free e"
	_, err := ParseWorkflow(wide)
	require.Error(t, err)
	assert.False(t, IsWorkflowExpr(wide), "not executable")
	assert.True(t, LooksLikeWorkflow(wide), "but clearly an attempt - must be reported")
	assert.False(t, LooksLikeWorkflow("/grok just prose here"), "single leg stays on the forced path")
}
