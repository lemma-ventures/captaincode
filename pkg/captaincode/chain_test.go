package captaincode

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chainRegistry(t *testing.T) {
	t.Helper()
	LoadRegistry(filepath.Join(t.TempDir(), "none.json"))
}

// The shape that failed (2026-10-05): a multi-line /frontier brief, then a
// repeating implementation loop behind ">".
func TestChainSplitsASoloBriefFromARepeatLoop(t *testing.T) {
	chainRegistry(t)
	brief := "/frontier create detailed specs and roadmap items for: Direction\n" +
		"Build one complete research-repair experience (F3). When a source is challenged, show the affected claim.\n" +
		"Make the tool installable, then find real users. Contacting outside teams is your decision."
	steps, ok := SplitChain(brief + "  > /repeat 5 /quality implement next items following specs")
	require.True(t, ok)
	assert.Equal(t, []string{brief, "/repeat 5 /quality implement next items following specs"}, steps)
}

func TestChainJoinsAnyCommands(t *testing.T) {
	chainRegistry(t)
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"/claude draft it > /team review it", []string{"/claude draft it", "/team review it"}},
		{"/team audit the deck > /claude fix what it found", []string{"/team audit the deck", "/claude fix what it found"}},
		{"/grok a + /codex b > /repeat 2 /quality c", []string{"/grok a + /codex b", "/repeat 2 /quality c"}},
		{"/frontier plan -> /quality build", []string{"/frontier plan", "/quality build"}},
	} {
		steps, ok := SplitChain(tc.in)
		require.True(t, ok, tc.in)
		assert.Equal(t, tc.want, steps, tc.in)
	}
}

func TestChainNestsAWorkflowInParentheses(t *testing.T) {
	chainRegistry(t)
	steps, ok := SplitChain("/frontier plan it > (/team build it > /repeat 3 /quality polish it)")
	require.True(t, ok)
	assert.Equal(t, []string{"/frontier plan it", "(/team build it > /repeat 3 /quality polish it)"}, steps)
	assert.True(t, IsGroupStep(steps[1]))

	// The group runs as its own chain when its turn comes.
	inner, ok := SplitChain(steps[1])
	require.True(t, ok)
	assert.Equal(t, []string{"/team build it", "/repeat 3 /quality polish it"}, inner)

	// A group of plain legs is a CWL workflow inside the chain.
	steps, ok = SplitChain("/team scope it > (/grok draft + /codex draft > /claude merge)")
	require.True(t, ok)
	assert.Equal(t, "(/grok draft + /codex draft > /claude merge)", steps[1])
	_, isChain := SplitChain(steps[1])
	assert.False(t, isChain, "unwrapped, it is CWL")
	assert.True(t, IsWorkflowExpr(UnwrapGroup(steps[1])))
}

func TestChainLeavesCWLAndProseAlone(t *testing.T) {
	chainRegistry(t)
	for _, in := range []string{
		"/grok analyse it > /cursor review it",            // plain leg stages: CWL
		"/frontier draft > /codex review",                 // frontier is a CWL stage too
		"/repeat 5 /quality implement the next item",      // one command
		"/frontier check that x > y holds for all n",      // ">" in prose
		"/frontier show (a) > (b) in the table",           // parentheses in prose
		"/frontier compare /usr/bin/claude > /opt/claude", // paths
		"fix the build > /repeat 3",                       // must open with a command
		"/frontier fix it (see /usr/lib) > then stop",     // a path inside prose parentheses
	} {
		_, ok := SplitChain(in)
		assert.False(t, ok, in)
	}
}

func TestChainRepeatOfAGroupIsOneCommand(t *testing.T) {
	chainRegistry(t)
	// /repeat applies to the whole group; each round then runs the group.
	_, ok := SplitChain("/repeat 3 (/frontier plan it > /team build it)")
	assert.False(t, ok, "no top-level >: it is a /repeat whose task is a chain")
	steps, ok := SplitChain("(/frontier plan it > /team build it)")
	require.True(t, ok, "the round's task unwraps to the chain")
	assert.Equal(t, []string{"/frontier plan it", "/team build it"}, steps)
}

func TestChainLimits(t *testing.T) {
	chainRegistry(t)
	_, ok := SplitChain("/team a > (/team b > (/team c > (/team d > (/team e > /claude f))))")
	assert.False(t, ok, "nesting deeper than MaxChainDepth")
	_, ok = SplitChain("/team a > (/team b > /claude c")
	assert.False(t, ok, "an unclosed group")
}
