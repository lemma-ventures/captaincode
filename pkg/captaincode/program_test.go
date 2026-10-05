package captaincode

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Text that uses no program syntax keeps its old path: a leg, a workflow, a
// plain /repeat, and prose that only mentions the operators.
func TestParseProgramLeavesOldSyntaxAlone(t *testing.T) {
	for _, s := range []string{
		"fix the parser",
		"/codex fix the parser",
		"/grok analyse the queue > /cursor review it > /codex red-team it + /claude red-team it",
		"/codex fix the parser gate: go test ./...",
		"/repeat 3 tidy the changelog",
		"/repeat 3 /grok draft > /claude review",
		"/repeat 4 /quality implement the next item",
		"/repeat 3 /openshell fix the parser",
		"/openshell fix A > /openshell --review check A",
		"/repeat status",
		"/repeat watch the queue",
		"/team audit the deck",
		"/quality implement the next item",
		"/codex explain why x || y holds",
		"/codex compare /usr/bin/claude || /opt/homebrew/bin/claude",
		"/codex write a demo script: first a /frontier, then a /team then a /workflow then /speed",
		"/codex document `(/repeat 3 /claude x) > /team y` in the README",
		"/cursor rework these sections:\n\nIntro text that quotes (/repeat 3 /claude x) > /team y as an example.",
		"/codex keep going until: the build is green",
		"/codex check that the output > /tmp is empty",
	} {
		p, ok, err := ParseProgram(s)
		assert.NoError(t, err, "%q", s)
		assert.False(t, ok, "%q is not a program (parsed as %s)", s, p.String())
	}
}

func turn(kind TurnKind, text string) Program {
	return Program{Kind: ProgramTurn, Turn: kind, Text: text}
}

func TestParseProgramShapes(t *testing.T) {
	cases := []struct {
		in   string
		want Program
	}{
		{"/team research the API > /codex implement it", Program{Kind: ProgramChain, Steps: []Program{
			turn(TurnTeam, "/team research the API"),
			turn(TurnWorkflow, "/codex implement it"),
		}}},
		{"/frontier write the spec > (/repeat 4 /codex implement the next item gate: go test ./...) > /claude review the diff",
			Program{Kind: ProgramChain, Steps: []Program{
				turn(TurnWorkflow, "/frontier write the spec"),
				{Kind: ProgramLoop, Count: 4, Group: true, Steps: []Program{
					turn(TurnWorkflow, "/codex implement the next item gate: go test ./..."),
				}},
				turn(TurnWorkflow, "/claude review the diff"),
			}}},
		// || is the loosest operator: the loop is the first alternative.
		{"/repeat 10 /codex fix the failing tests until: go test ./... || /claude explain why they still fail",
			Program{Kind: ProgramAlt, Steps: []Program{
				{Kind: ProgramLoop, Count: 10, Until: "go test ./...", Steps: []Program{
					turn(TurnWorkflow, "/codex fix the failing tests"),
				}},
				turn(TurnWorkflow, "/claude explain why they still fail"),
			}}},
		// A loop runs the rest of its chain: here, the whole group.
		{"/repeat 5 (/frontier pick the next item > /team implement it > /claude review the diff)",
			Program{Kind: ProgramLoop, Count: 5, Steps: []Program{
				{Kind: ProgramChain, Group: true, Steps: []Program{
					turn(TurnWorkflow, "/frontier pick the next item"),
					turn(TurnTeam, "/team implement it"),
					turn(TurnWorkflow, "/claude review the diff"),
				}},
			}}},
		// Legs next to each other stay ONE workflow, with one review.
		{"/team research the problem > (/repeat 4 /quality implement the next item gate: make test) > /claude + /codex audit the change > /frontier write the release notes",
			Program{Kind: ProgramChain, Steps: []Program{
				turn(TurnTeam, "/team research the problem"),
				{Kind: ProgramLoop, Count: 4, Group: true, Steps: []Program{
					{Kind: ProgramTurn, Turn: TurnLane, Text: "/quality implement the next item", Gate: "make test"},
				}},
				turn(TurnWorkflow, "/claude + /codex audit the change > /frontier write the release notes"),
			}}},
		// A gate on a lane turn needs the runner: the solo path ignored it.
		{"/quality implement the next item gate: make test",
			Program{Kind: ProgramTurn, Turn: TurnLane, Text: "/quality implement the next item", Gate: "make test"}},
		{"/codex fix the parser gate: go test ./... || /claude diagnose the failure",
			Program{Kind: ProgramAlt, Steps: []Program{
				turn(TurnWorkflow, "/codex fix the parser gate: go test ./..."),
				turn(TurnWorkflow, "/claude diagnose the failure"),
			}}},
		// until: belongs to the innermost loop of its group.
		{"/repeat 3 /codex plan > /repeat 2 /claude fix until: go test ./...",
			Program{Kind: ProgramLoop, Count: 3, Steps: []Program{
				{Kind: ProgramChain, Steps: []Program{
					turn(TurnWorkflow, "/codex plan"),
					{Kind: ProgramLoop, Count: 2, Until: "go test ./...", Steps: []Program{turn(TurnWorkflow, "/claude fix")}},
				}},
			}}},
		{"/repeat 3 (/repeat 2 /claude fix until: go vet ./...) until: go test ./...",
			Program{Kind: ProgramLoop, Count: 3, Until: "go test ./...", Steps: []Program{
				{Kind: ProgramLoop, Count: 2, Until: "go vet ./...", Group: true, Steps: []Program{turn(TurnWorkflow, "/claude fix")}},
			}}},
		// Lane words in front of /repeat are hoisted into its body.
		{"/oss /repeat 5 /codex fix the tests until: go test ./...",
			Program{Kind: ProgramLoop, Count: 5, Until: "go test ./...", Steps: []Program{
				turn(TurnWorkflow, "/oss /codex fix the tests"),
			}}},
		// Plain text may open a loop body; a gate there runs on the runner.
		{"/repeat fix the flaky test gate: go test -count=5 ./...",
			Program{Kind: ProgramLoop, Steps: []Program{
				{Kind: ProgramTurn, Turn: TurnAuto, Text: "fix the flaky test", Gate: "go test -count=5 ./..."},
			}}},
		// Parentheses split a workflow into separate commands.
		{"(/codex draft the API) > /claude review it", Program{Kind: ProgramChain, Steps: []Program{
			{Kind: ProgramTurn, Turn: TurnWorkflow, Text: "/codex draft the API", Group: true},
			turn(TurnWorkflow, "/claude review it"),
		}}},
		// Shell syntax in a check stays in the check.
		{"/repeat 5 /codex fix it until: make test > /dev/null 2>&1 || test -f done.flag",
			Program{Kind: ProgramLoop, Count: 5, Until: "make test > /dev/null 2>&1 || test -f done.flag", Steps: []Program{
				turn(TurnWorkflow, "/codex fix it"),
			}}},
		// Prose parentheses inside a group are counted, not misread.
		{"(/codex fix the parser (it fails on empty input) > /team review it)", Program{Kind: ProgramChain, Group: true, Steps: []Program{
			turn(TurnWorkflow, "/codex fix the parser (it fails on empty input)"),
			turn(TurnTeam, "/team review it"),
		}}},
	}
	for _, c := range cases {
		p, ok, err := ParseProgram(c.in)
		require.NoError(t, err, "%q", c.in)
		require.True(t, ok, "%q is a program", c.in)
		assert.Equal(t, c.want, p, "%q", c.in)

		// Round trip: the canonical rendering parses back to the same program.
		back, ok2, err2 := ParseProgram(p.String())
		require.NoError(t, err2, "render of %q: %q", c.in, p.String())
		require.True(t, ok2)
		assert.Equal(t, p, back, "round trip of %q via %q", c.in, p.String())
	}
}

func TestParseProgramErrors(t *testing.T) {
	cases := []struct{ in, want string }{
		{"(/codex fix the parser > /claude review it", "never closed"},
		{"/team research it > /fontier implement it", "unknown command /fontier"},
		{"/team research it > /wf run nightly", "/wf is a control word"},
		{"/repeat 5 /codex fix it until: go test ./... > /claude review it", "put it in parentheses"},
		{"/repeat 5 /codex fix it until:", "until: needs a command"},
		{"/quality (/codex draft > /team review)", "cannot apply to a whole (group)"},
		{"(/repeat 2 (/repeat 2 (/codex fix it)))", "at most 3 levels"},
		{"/team a > /team b > /team c > /team d > /team e > /team f > /team g > /team h > /team i", "at most 8 steps"},
		{"/team research it > /openshell implement it", "/openshell cannot be a step"},
		{"/repeat 3 /openshell fix it until: go test ./...", "/openshell cannot be a step"},
		{"/team research it + /codex draft it > /claude review", "joins legs inside one stage"},
		{"/team research it > /codex a + /cursor b + /grok c + /gemini d + /glm e", "at most 4 legs"},
		{"(/codex fix it) and then more text", "unexpected"},
		{"/codex draft the API > /team", "has no task"},
		{"/codex draft the API > /quality /team", "has no task"},
	}
	for _, c := range cases {
		_, ok, err := ParseProgram(c.in)
		require.Error(t, err, "%q must be refused", c.in)
		assert.True(t, ok, "%q is a program attempt, reported - never run as prose", c.in)
		assert.Contains(t, err.Error(), c.want, "%q", c.in)
	}
}

func TestProgramMaxTurns(t *testing.T) {
	p, ok, err := ParseProgram("/team research it > (/repeat 4 /quality implement the next item gate: make test) > /claude audit it")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 1+4*2+1, p.MaxTurns(100), "a gated lane turn may add one repair per round")

	open, _, err := ParseProgram("/repeat /codex fix it until: go test ./...")
	require.NoError(t, err)
	assert.Equal(t, 100, open.MaxTurns(100), "an open loop runs until the budget ends")

	alt, _, err := ParseProgram("/codex fix it gate: make test || /claude diagnose it || /grok diagnose it")
	require.NoError(t, err)
	assert.Equal(t, 3, alt.MaxTurns(100), "a fallback may run every alternative")
}

func TestProgramOutline(t *testing.T) {
	p, _, err := ParseProgram("/team research it > (/repeat 4 /quality implement the next item gate: make test) > /claude audit it || /grok explain what failed")
	require.NoError(t, err)
	want := strings.Join([]string{
		"try:",
		"   1. /team research it",
		"   2. repeat 4 times:",
		"      /quality implement the next item   [gate: make test]",
		"   3. /claude audit it",
		"if that fails:",
		"   /grok explain what failed",
	}, "\n")
	assert.Equal(t, want, p.Outline())

	loop, _, err := ParseProgram("/repeat 10 /codex fix the failing tests until: go test ./...")
	require.NoError(t, err)
	assert.Contains(t, loop.Outline(), "repeat 10 times, until `go test ./...` passes:")
}

// The gate ends at the first blank line. A /repeat round appends its contract
// after one, and the gate swallowed it: the check failed in every round.
func TestSplitGateEndsAtTheParagraph(t *testing.T) {
	cases := []struct{ in, prompt, gate string }{
		{"fix the parser gate: go test ./...", "fix the parser", "go test ./..."},
		{"fix the parser gate: true\n\n[captain] This is one round (it may take an hour).", "fix the parser\n\n[captain] This is one round (it may take an hour).", "true"},
		{"explain what a gate: is", "explain what a", "is"},
		{"no check here", "no check here", ""},
		{"fix it gate:", "fix it gate:", ""},
	}
	for _, c := range cases {
		p, g := SplitGate(c.in)
		assert.Equal(t, c.prompt, p, "%q", c.in)
		assert.Equal(t, c.gate, g, "%q", c.in)
	}

	wf, err := ParseWorkflow("/codex fix the parser gate: go test ./...\n\n[captain] This is one round of a repeating task.")
	require.NoError(t, err)
	leg := wf.Stages[0].Legs[0]
	assert.Equal(t, "go test ./...", leg.Gate, "the round contract is not part of the command")
	assert.Contains(t, leg.Prompt, "[captain] This is one round", "the contract reaches the worker")
}

// Every program example in the docs parses. A previous answer described a
// language with parentheses and chains that did not exist; this keeps the
// docs and the parser in step.
func TestDocumentedProgramsParse(t *testing.T) {
	fence := regexp.MustCompile("(?s)```captain\n(.*?)```")
	seen := 0
	for _, path := range []string{"../../docs/WORKFLOW_LANGUAGE.md", "../../docs/TUI.md", "../../docs/ORCHESTRATION_MAPPING.md"} {
		doc, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, m := range fence.FindAllStringSubmatch(string(doc), -1) {
			for _, line := range strings.Split(m[1], "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				seen++
				if _, _, err := ParseProgram(line); err != nil {
					t.Errorf("%s: %q does not parse: %v", path, line, err)
					continue
				}
				// A line with no program syntax (a workflow, a plain loop)
				// must still parse with the full grammar.
				if _, err := parseAlt(line, 0); err != nil {
					t.Errorf("%s: %q does not parse: %v", path, line, err)
				}
			}
		}
	}
	assert.Greater(t, seen, 10, "the docs carry runnable examples")
}
