package captaincode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFableIsAClaudeModelNotALeg(t *testing.T) {
	assert.False(t, KnownLeg("fable"), "Fable 5.2 is a model of the claude leg")
	assert.Equal(t, "claude-fable-5-2", FableModel())
	t.Setenv("CAPTAIN_FABLE_MODEL", "claude-fable-5-3")
	assert.Equal(t, "claude-fable-5-3", FableModel())
	assert.Equal(t, "fable-5.2", ModelShort("claude-fable-5-2"), "the run line reads claude-cli:fable-5.2")
	p, ok := FablePerf()
	require.True(t, ok)
	assert.Equal(t, 65.0, p, "the compiled snapshot of 2026-11-10")
}

func TestFableForVeryHighComplexityOnly(t *testing.T) {
	assert.True(t, FableFor(ClassHigh, EffortMax))
	assert.False(t, FableFor(ClassHigh, EffortXHigh), "below max stays on Opus")
	assert.False(t, FableFor(ClassMedium, EffortMax), "a medium task at max stays on Opus")
	t.Setenv("CAPTAIN_FABLE", "0")
	assert.False(t, FableFor(ClassHigh, EffortMax), "CAPTAIN_FABLE=0 turns the automatic pick off")
}

func TestFableWord(t *testing.T) {
	for text, want := range map[string]bool{
		"/frontier /fable prove the invariant": true,
		"/claude-cli /fable review it":         true,
		"/fable fix it":                        true,
		"read /fable/notes.md":                 false,
		"a fable about routing":                false,
		"/fabled fix":                          false,
	} {
		assert.Equal(t, want, MidPromptFable(text), text)
	}
	assert.Contains(t, SlashCommands(), "fable", "the plugin accepts a turn that starts with /fable")
	assert.Equal(t, "/claude /fable fix it", HoistLeading("/fable /claude fix it"), "a leading /fable hoists past the leg")
}

// The pinned model reaches claude -p, at the request's effort; the frontier
// configuration keeps its effort and takes the pinned model.
func TestClaudeRunPinsTheTurnModel(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_CLAUDE_ARGS", argsFile)
	fakeBin(t, "claude", `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_CLAUDE_ARGS"
cat <<'EOF2'
{"type":"result","is_error":false,"result":"ok"}
EOF2
`)
	args := func() string {
		b, err := os.ReadFile(argsFile)
		require.NoError(t, err)
		return strings.ReplaceAll(string(b), "\n", " ")
	}
	_, err := runClaudeStreamModel("", "x", 30*time.Second, 0, nil, nil, true, EffortMax, nil, FableModel())
	require.NoError(t, err)
	assert.Contains(t, args(), "--model claude-fable-5-2 --effort max")
	assert.NotContains(t, args(), "--model opus")

	_, err = runClaudeStreamModel("", "x", 30*time.Second, 0, nil, nil, false, EffortHigh, nil, FableModel())
	require.NoError(t, err)
	assert.Contains(t, args(), "--model claude-fable-5-2 --effort high", "/claude-cli /fable at the request's effort")

	_, err = runClaudeStreamModel("", "x", 30*time.Second, 0, nil, nil, true, EffortMax, nil, "")
	require.NoError(t, err)
	assert.Contains(t, args(), "--model opus --effort max", "no pin: /frontier stays on the opus alias")
}
