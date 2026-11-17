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
		{Tool: "bash", Command: "git log --no-ext-diff --no-textconv -3 --oneline", Cwd: cwd},
		{Tool: "bash", Command: "git diff --no-ext-diff --no-textconv HEAD~1 HEAD", Cwd: cwd},
		{Tool: "bash", Command: "git show --no-ext-diff --no-textconv --stat HEAD", Cwd: cwd},
		{Tool: "bash", Command: "git branch --list", Cwd: cwd},
		{Tool: "bash", Command: "git branch --show-current", Cwd: cwd},
		{Tool: "bash", Command: "git remote -v", Cwd: cwd},
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
		{Tool: "bash", Command: "git branch new-branch", Cwd: cwd},
		{Tool: "bash", Command: "git branch -D feature", Cwd: cwd},
		{Tool: "bash", Command: "git remote add origin https://example.invalid/repo.git", Cwd: cwd},
		{Tool: "bash", Command: "git remote remove origin", Cwd: cwd},
		{Tool: "bash", Command: "git remote set-url origin https://example.invalid/repo.git", Cwd: cwd},
		{Tool: "bash", Command: "git diff --output=changes.patch", Cwd: cwd},
		{Tool: "bash", Command: "git log --output=history.txt", Cwd: cwd},
		{Tool: "bash", Command: "git show --output=commit.txt", Cwd: cwd},
		{Tool: "bash", Command: "git diff --ext-diff", Cwd: cwd},
		{Tool: "bash", Command: "git log --ext-diff", Cwd: cwd},
		{Tool: "bash", Command: "git show --ext-diff", Cwd: cwd},
		{Tool: "bash", Command: "git status --short", Cwd: cwd},
		{Tool: "bash", Command: "git blame README.md", Cwd: cwd},
		{Tool: "bash", Command: "git log -3 --oneline", Cwd: cwd},
		{Tool: "bash", Command: "git diff HEAD~1 HEAD", Cwd: cwd},
		{Tool: "bash", Command: "git show HEAD", Cwd: cwd},
		{Tool: "bash", Command: "git diff --no-ext-diff --no-textconv --no-index /etc/hosts /dev/null", Cwd: cwd},
		{Tool: "bash", Command: "git log --no-ext-diff --no-textconv --show-signature -1", Cwd: cwd},
		{Tool: "bash", Command: "git show --no-ext-diff --no-textconv --show-signature HEAD", Cwd: cwd},
		{Tool: "bash", Command: "git diff --no-ext-diff --no-textconv --textconv HEAD~1 HEAD", Cwd: cwd},
		{Tool: "bash", Command: "git log --no-ext-diff --no-textconv --textconv -p -1", Cwd: cwd},
		{Tool: "bash", Command: "git log --no-ext-diff --no-textconv --format=%G? -1", Cwd: cwd},
		{Tool: "bash", Command: "git show --no-ext-diff --no-textconv --format=%GK HEAD", Cwd: cwd},
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

func BenchmarkReadOnlyTurnGitDecision(b *testing.B) {
	cwd := b.TempDir()
	action := GateAction{Tool: "bash", Command: "git log --no-ext-diff --no-textconv -3 --oneline", Cwd: cwd}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if why, _ := ReadOnlyTurnDecision(action); why != "" {
			b.Fatal(why)
		}
	}
}
