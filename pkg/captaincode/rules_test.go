package captaincode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeRule(t *testing.T, base, name, text string) {
	t.Helper()
	dir := filepath.Join(base, ".captaincode", "rules")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644))
}

func TestRulesComeFromHomeAndWorkspaceAndTheWorkspaceWins(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeRule(t, home, "no-self-promotion.md", "Commits name the operator only.\n")
	writeRule(t, home, "style.md", "home style")
	writeRule(t, ws, "style.md", "project style")
	writeRule(t, ws, "notes.txt", "not a rule")
	writeRule(t, ws, ".hidden.md", "not a rule")

	rules, dropped := LoadRules(ws)
	require.Len(t, rules, 2)
	assert.Zero(t, dropped)
	assert.Equal(t, Rule{Name: "no-self-promotion", Source: "~/.captaincode/rules/no-self-promotion.md", Text: "Commits name the operator only."}, rules[0])
	assert.Equal(t, Rule{Name: "style", Source: ".captaincode/rules/style.md", Text: "project style"}, rules[1])

	block := RulesContract(rules, dropped)
	assert.Contains(t, block, "[captain] Standing rules")
	assert.Contains(t, block, "--- rule no-self-promotion (~/.captaincode/rules/no-self-promotion.md) ---\nCommits name the operator only.")
	assert.NotContains(t, block, "home style")
	assert.NotContains(t, block, home, "the block names no absolute path")

	only, _ := LoadRules("")
	assert.Len(t, only, 2, "no workspace: the user's rules only")
}

func TestNoRulesNoBlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rules, dropped := LoadRules(t.TempDir())
	assert.Empty(t, rules)
	assert.Empty(t, RulesContract(rules, dropped))
}

// A cloned repository must not turn a private file into prompt text: a rule
// that is a symlink is skipped, and so is a rules folder that links out of
// the workspace.
func TestRulesSkipSymlinks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret := filepath.Join(t.TempDir(), "id_ed25519.md")
	require.NoError(t, os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600))

	ws := t.TempDir()
	writeRule(t, ws, "ok.md", "kept")
	require.NoError(t, os.Symlink(secret, filepath.Join(ws, ".captaincode", "rules", "key.md")))
	rules, _ := LoadRules(ws)
	require.Len(t, rules, 1)
	assert.Equal(t, "kept", rules[0].Text)

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "note.md"), []byte("PRIVATE NOTE"), 0o600))
	ws2 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws2, ".captaincode"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws2, ".captaincode", "rules")))
	rules, _ = LoadRules(ws2)
	assert.Empty(t, rules)
}

func TestRulesAreCapped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	writeRule(t, ws, "a-long.md", strings.Repeat("a", ruleFileMax+100))
	for _, n := range []string{"b.md", "c.md", "d.md", "e.md"} {
		writeRule(t, ws, n, strings.Repeat("b", ruleFileMax-100))
	}
	rules, dropped := LoadRules(ws)
	require.NotEmpty(t, rules)
	assert.Contains(t, rules[0].Text, "[captain: rule cut at 8 KiB]")
	assert.LessOrEqual(t, len(rules[0].Text), ruleFileMax+40)
	total := 0
	for _, r := range rules {
		total += len(r.Text)
	}
	assert.LessOrEqual(t, total, rulesMax)
	assert.Positive(t, dropped)
	assert.Contains(t, RulesContract(rules, dropped), "more rules left out")
}
