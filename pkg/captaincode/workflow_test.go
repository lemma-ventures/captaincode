package captaincode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Captain Workflow Language - see docs/WORKFLOW_LANGUAGE.md.
// The safety of the whole grammar rests on one rule: a connector counts ONLY
// when the next token is a leg prefix. Everything else stays prose.

func TestParseWorkflowTopologies(t *testing.T) {
	t.Run("sequential chain", func(t *testing.T) {
		wf, err := ParseWorkflow("/grok analyse the file > /cursor review it > /codex ship it")
		require.NoError(t, err)
		require.Len(t, wf.Stages, 3)
		assert.Equal(t, LegGrok, wf.Stages[0].Legs[0].Leg)
		assert.Equal(t, "analyse the file", wf.Stages[0].Legs[0].Prompt)
		assert.Equal(t, LegCursor, wf.Stages[1].Legs[0].Leg)
		assert.Equal(t, "review it", wf.Stages[1].Legs[0].Prompt)
		assert.Equal(t, LegCodex, wf.Stages[2].Legs[0].Leg)
	})

	t.Run("parallel binds tighter than sequential", func(t *testing.T) {
		wf, err := ParseWorkflow("/grok p1 and /cursor p2 then /codex p3")
		require.NoError(t, err)
		require.Len(t, wf.Stages, 2)
		require.Len(t, wf.Stages[0].Legs, 2)
		assert.Equal(t, LegGrok, wf.Stages[0].Legs[0].Leg)
		assert.Equal(t, LegCursor, wf.Stages[0].Legs[1].Leg)
		require.Len(t, wf.Stages[1].Legs, 1)
		assert.Equal(t, LegCodex, wf.Stages[1].Legs[0].Leg)
	})

	t.Run("the users sentence", func(t *testing.T) {
		wf, err := ParseWorkflow("/grok analyse @queue.ts > /cursor review the analysis > /codex red-team it + /claude red-team it")
		require.NoError(t, err)
		require.Len(t, wf.Stages, 3)
		assert.Len(t, wf.Stages[2].Legs, 2)
		assert.Equal(t, 4, wf.Runs())
	})

	t.Run("bare stages repeat the previous assignment", func(t *testing.T) {
		// Semantic change 2026-08-24 (user request): a bare leg REPEATS the
		// upstream assignment (and sees its output) instead of carrying an
		// empty prompt for the fixed improve-instruction.
		wf, err := ParseWorkflow("/grok write the docstring > /cursor > /claude")
		require.NoError(t, err)
		require.Len(t, wf.Stages, 3)
		assert.Equal(t, "write the docstring", wf.Stages[1].Legs[0].Prompt)
		assert.Equal(t, "write the docstring", wf.Stages[2].Legs[0].Prompt)
	})

	t.Run("same leg twice is a self-review", func(t *testing.T) {
		wf, err := ParseWorkflow("/claude draft it > /claude critique your draft")
		require.NoError(t, err)
		assert.Len(t, wf.Stages, 2)
	})
}

// The connector rule: prose containing and/then/+/> must not become topology.
func TestConnectorsOnlyCountBeforeALegPrefix(t *testing.T) {
	proseOnly := []string{
		"/grok summarize this and explain why it matters",
		"/claude retry then fail loudly",
		"/codex assert n > 3 and n + 1 < 10",
		"/cursor compare /usr/bin/claude and /opt/homebrew/bin/claude",
		"/free write a haiku about > and + signs",
	}
	for _, in := range proseOnly {
		t.Run(in, func(t *testing.T) {
			wf, err := ParseWorkflow(in)
			require.NoError(t, err)
			require.Len(t, wf.Stages, 1, "prose connectors must not split stages")
			require.Len(t, wf.Stages[0].Legs, 1)
			assert.Equal(t, strings.SplitN(in, " ", 2)[1], wf.Stages[0].Legs[0].Prompt)
			assert.False(t, IsWorkflowExpr(in), "single-leg prompts stay on the normal forced path")
		})
	}
}

// A connector with no leg after it is prose, not a syntax error: the rule is
// deliberately conservative so a stray ">" can never break an ordinary prompt.
func TestDanglingConnectorIsProse(t *testing.T) {
	wf, err := ParseWorkflow("/grok summarize a > b")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 1)
	assert.Equal(t, "summarize a > b", wf.Stages[0].Legs[0].Prompt)
	assert.False(t, IsWorkflowExpr("/grok summarize a > b"))
}

func TestIsWorkflowExpr(t *testing.T) {
	yes := []string{
		"/grok a > /cursor b",
		"  /grok a and /cursor b",
		"/grok a > /cursor",
		"/grok a+/cursor b",
	}
	no := []string{
		"/grok just one leg",
		"do X then do Y",
		"/quality rewrite this",
		"not a workflow at all",
		"> /cursor missing first stage",
		"",
	}
	for _, s := range yes {
		assert.True(t, IsWorkflowExpr(s), "should be a workflow: %q", s)
	}
	for _, s := range no {
		assert.False(t, IsWorkflowExpr(s), "should NOT be a workflow: %q", s)
	}
}

func TestParseWorkflowRejections(t *testing.T) {
	cases := []struct{ name, in, wantErr string }{
		{"unknown leg", "/grok a > /gpt5 b", "unknown leg"},
		{"preference prefix", "/quality a > /cursor b", "preference"},
		{"team is not a leg", "/grok a > /team b", "unknown leg"},
		{"mid-expression preference", "/grok a > /quality b", "preference"},
		{"no leading leg", "analyse this > /cursor b", "must start with"},
		{"empty", "   ", "empty"},
		{"too many stages", "/grok a > /cursor b > /codex c > /claude d > /free e", "at most 4 stages"},
		{"stage too wide", "/grok a + /cursor b + /codex c + /claude d + /free e", "at most 4 legs"},
		{"too many runs", "/grok a + /cursor b + /codex c > /claude d + /free e + /glm f > /minimax g + /qwen h + /frontier i", "at most 8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseWorkflow(c.in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)
		})
	}
}

func TestWorkflowRoundTrips(t *testing.T) {
	for _, in := range []string{
		"/grok analyse the file > /cursor review it > /codex red-team it + /claude red-team it",
		"/grok draft > /claude",
		"/free a + /glm b > /claude merge them",
	} {
		wf, err := ParseWorkflow(in)
		require.NoError(t, err)
		again, err := ParseWorkflow(wf.String())
		require.NoError(t, err, "String() must re-parse")
		assert.Equal(t, wf, again, "round-trip must be lossless")
	}
}

func TestWorkflowKeyIsCanonical(t *testing.T) {
	a, err := ParseWorkflow("/grok a > /claude b + /codex c")
	require.NoError(t, err)
	b, err := ParseWorkflow("/grok a > /codex c + /claude b")
	require.NoError(t, err)
	assert.Equal(t, "grok>claude+codex", a.Key())
	assert.Equal(t, a.Key(), b.Key(), "leg order inside a stage must not change the key")
}

// The conversation the workers see must carry the user's instructions as prose,
// not as routing syntax (a worker that reads "/cursor review it" answers about
// a slash command - the /quality incident, 2026-07-29).
func TestPlainRequestStripsSyntax(t *testing.T) {
	wf, err := ParseWorkflow("/grok analyse @queue.ts > /cursor review the analysis > /codex red-team it + /claude red-team it")
	require.NoError(t, err)
	plain := wf.PlainRequest()
	assert.NotContains(t, plain, "/grok")
	assert.NotContains(t, plain, ">")
	assert.Contains(t, plain, "analyse @queue.ts")
	assert.Contains(t, plain, "review the analysis")
	assert.Contains(t, plain, "red-team it")
}

func TestFrontierIsALegalLeg(t *testing.T) {
	wf, err := ParseWorkflow("/grok draft > /frontier review it at full effort")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 2)
	assert.Equal(t, LegFrontier, wf.Stages[1].Legs[0].Leg)
	assert.True(t, IsFrontier(wf.Stages[1].Legs[0].Leg))
}

// R2 (PRIME_AGENT_NOTES): a stage may carry an objective GATE - a shell command
// that must pass before the stage counts as done. Structural fix for
// narration-only outputs (2026-08-06: 862 chars of "Checking… Applying…" slid
// past the heuristic detector; a `pytest -q` gate cannot be narrated past).
func TestParseWorkflowGates(t *testing.T) {
	wf, err := ParseWorkflow("/codex fix the failing tests gate: go test ./... > /claude review the fix")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 2)
	assert.Equal(t, "fix the failing tests", wf.Stages[0].Legs[0].Prompt)
	assert.Equal(t, "go test ./...", wf.Stages[0].Legs[0].Gate)
	assert.Equal(t, "", wf.Stages[1].Legs[0].Gate, "gates are per-leg, not inherited")

	// The LAST gate: marker wins; prose before it stays in the prompt.
	wf, err = ParseWorkflow("/grok explain the gate: mechanism gate: npm test")
	require.NoError(t, err)
	assert.Equal(t, "explain the gate: mechanism", wf.Stages[0].Legs[0].Prompt)
	assert.Equal(t, "npm test", wf.Stages[0].Legs[0].Gate)

	// Round-trip preserves gates.
	wf, err = ParseWorkflow("/codex fix it gate: make check + /grok fix docs")
	require.NoError(t, err)
	again, err := ParseWorkflow(wf.String())
	require.NoError(t, err)
	assert.Equal(t, wf, again)
}

// One task per line is how people write "do these three things". Before this,
// a three-line prompt ran on the FIRST leg only and handed the other lines to
// it as prose (live 2026-09-01).
func TestNewlineSeparatesLegs(t *testing.T) {
	wf, err := ParseWorkflow("/cursor fix the links\n/grok reword the date\n/codex verify the deploy")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 1, "lines fan out in parallel")
	require.Len(t, wf.Stages[0].Legs, 3)
	assert.Equal(t, LegCursor, wf.Stages[0].Legs[0].Leg)
	assert.Equal(t, LegGrok, wf.Stages[0].Legs[1].Leg)
	assert.Equal(t, LegCodex, wf.Stages[0].Legs[2].Leg)
	assert.Equal(t, "fix the links", wf.Stages[0].Legs[0].Prompt)
	assert.True(t, IsWorkflowExpr("/cursor a\n/grok b"))

	// Mixing with explicit connectors still works: lines fan out, ">" sequences.
	wf, err = ParseWorkflow("/cursor fix the links\n/grok reword the date > /codex verify")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 2)
	assert.Len(t, wf.Stages[0].Legs, 2)
	assert.Equal(t, LegCodex, wf.Stages[1].Legs[0].Leg)
}

// The connector rule still holds: a newline before ordinary prose, or before a
// path, is not topology.
func TestNewlineOnlyCountsBeforeALegPrefix(t *testing.T) {
	for _, in := range []string{
		"/grok summarize this\nand explain why it matters",
		"/grok check the file\n/opt/homebrew/bin/claude is the path",
		"/grok list them:\n- one\n- two",
	} {
		wf, err := ParseWorkflow(in)
		require.NoError(t, err, in)
		assert.Len(t, wf.Stages, 1, "prose after a newline is not a stage: %q", in)
		assert.Len(t, wf.Stages[0].Legs, 1)
	}
}

// Prose ABOUT captain's commands is not a workflow: "then a /team then a
// /workflow … then /speed" names pseudo-models and preferences after the
// connectors, and a connector only counts before a leg a stage can run
// (2026-09-19: a demo-script request was refused as workflow syntax).
func TestProseAboutCommandsIsNotAWorkflow(t *testing.T) {
	prose := "/cursor https://captaincode.ai/demo/ is great but show case more for the important commands: start with a bare prompt, then go for a /frontier model, then go for a /team then a /workflow, then a /deterministic explaining how it pulls the leg from the ADI, then a /cheap then /speed. Make it progressive."
	assert.False(t, LooksLikeWorkflow(prose), "no runnable leg follows any connector except /frontier")
	// …except that "then go for a /frontier model" IS a connector to a runnable pseudo-leg;
	// the expression must then still parse as the user's intent would read.
	wf, err := ParseWorkflow(prose)
	if err == nil {
		assert.LessOrEqual(t, len(wf.Stages), 2)
	}
	assert.True(t, LooksLikeWorkflow("/grok draft it then /claude review it"), "a real pipeline still is one")
	assert.False(t, LooksLikeWorkflow("/grok explain /quality and /speed to me"), "preferences after word connectors are prose")
	assert.False(t, LooksLikeWorkflow("/grok compare /team and /workflow modes"), "pseudo-models after word connectors are prose")
	assert.True(t, LooksLikeWorkflow("/grok a > /quality b"), "a symbol connector is deliberate syntax: still reported")
}

// A connector inside pasted content is not topology: the stage text before it
// spans a paragraph break, which a one-line workflow never does (live
// 2026-09-21: "/cursor move these sections…" followed by two paragraphs of
// website copy quoting a workflow example ran as cursor+grok>claude+codex).
func TestConnectorsAfterAParagraphBreakArePastedContent(t *testing.T) {
	pasted := "/cursor move these sections somewhere useful in /features subpage: ARCHITECTURE · HOW CAPTAIN WORKS\n\n" +
		"Four layers.\nLocal orchestration above all your models. MATCH THE INTELLIGENCE TO THE JOB\n\n" +
		"Write a patch, run its tests, get two reviews\n" +
		"/grok fix the retry bug gate: go test ./... > /claude review for duplicate charges + /codex review for lost payments\n" +
		"> next stage receives the outputs\n+ reviewers work in parallel\n\n" +
		"Replace these 3 sections with one summarizing with 3 tiles."
	assert.False(t, IsWorkflowExpr(pasted), "pasted copy is a solo /cursor prompt")
	assert.False(t, LooksLikeWorkflow(pasted), "…and not a workflow attempt to report")
	assert.Empty(t, boundariesOf(pasted))

	// One-line topology, and one-task-per-line topology, are untouched.
	for _, ok := range []string{
		"/grok fix the retry bug gate: go test ./... > /claude review for duplicate charges + /codex review for lost payments",
		"/grok do X\n\n/claude do Y",
		"/grok list:\n- a\n- b\n> /claude review the list",
		"/grok a\n/claude b\n/codex c",
	} {
		assert.True(t, IsWorkflowExpr(ok), ok)
	}
	wf, err := ParseWorkflow("/grok do X\n\n/claude do Y")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 1)
	assert.Len(t, wf.Stages[0].Legs, 2, "the newline connector survives its own blank line")
}
