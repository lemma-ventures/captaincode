package captaincode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadOnlyTurnPolicy(t *testing.T) {
	cwd := t.TempDir()
	for _, a := range []GateAction{
		{Tool: "read", Path: "README.md", Cwd: cwd},
		{Tool: "grep", Path: cwd, Cwd: cwd},
		{Tool: "glob", Path: cwd, Cwd: cwd},
		{Tool: "bash", Command: "git status --short", Cwd: cwd},
		{Tool: "bash", Command: "git log -3 --oneline", Cwd: cwd},
	} {
		why, _ := ReadOnlyTurnDecision(a)
		assert.Empty(t, why, "%s %s", a.Tool, a.Command)
	}
	for _, a := range []GateAction{
		{Tool: "write", Path: "notes.md", Cwd: cwd},
		{Tool: "edit", Path: "notes.md", Cwd: cwd},
		{Tool: "todowrite", Cwd: cwd},
		{Tool: "webfetch", Cwd: cwd},
		{Tool: "bash", Command: "go test ./...", Cwd: cwd},
		{Tool: "bash", Command: "git push", Cwd: cwd},
		{Tool: "bash", Command: "curl https://example.invalid", Cwd: cwd},
		{Tool: "bash", Command: "cat README.md", Cwd: cwd},
		{Tool: "read", Path: "../outside", Cwd: cwd},
		{Tool: "grep", Path: "/etc", Cwd: cwd},
		{Tool: "bash", Command: "git status; touch x", Cwd: cwd},
	} {
		why, jail := ReadOnlyTurnDecision(a)
		assert.NotEmpty(t, why, "%s %s", a.Tool, a.Command)
		assert.False(t, jail)
	}
}

func TestReadOnlyTurnUsesReadOnlyCodexSandbox(t *testing.T) {
	t.Setenv("CAPTAIN_CODEX_CLI_SANDBOX", "")
	t.Setenv("CAPTAIN_SERENA_MCP", "1")
	task := "inspect\n\n" + SentTurnMarker + "\n" + ReadOnlyTurnMarker
	args := strings.Join(codexCLICmdArgs("/w", task, ""), " ")
	assert.Contains(t, args, "--sandbox read-only")
	assert.NotContains(t, args, "workspace-write")
	assert.NotContains(t, args, "dangerously-bypass")
	assert.NotContains(t, args, "serena")
}

func BenchmarkReadOnlyTurnDecision(b *testing.B) {
	cwd := b.TempDir()
	action := GateAction{Tool: "read", Path: "README.md", Cwd: cwd}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if why, _ := ReadOnlyTurnDecision(action); why != "" {
			b.Fatal(why)
		}
	}
}
