package captaincode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A freshly created repo brain knew nothing about the repo: VISION, MAP and
// BRAIN stayed templates, and distillation is forbidden from writing them.
// The repo already says what it is - README, docs/, CLAUDE.md, AGENTS.md - so
// a new brain is bootstrapped from those once, the one time a model may write
// the strategy registers.

func repoWithDocs(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Widget\n\nWidget is a billing engine for co-ops. It serves treasurers.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "docs", "ROADMAP.md"), []byte("# Roadmap\n\nQ4: multi-currency ledgers.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "docs", "notes.txt"), []byte("not markdown"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".env"), []byte("API_KEY=sk-secret\n"), 0o600))
	_, err := Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	return repo
}

func TestBootstrapPromptCarriesTheReposOwnDocs(t *testing.T) {
	repo := repoWithDocs(t)
	brain := EuclidBrain{Root: filepath.Join(repo, ".euclid"), Kind: "repo", Label: "repo:widget"}

	prompt, docs := BootstrapPrompt(brain, repo)

	assert.Contains(t, prompt, "billing engine for co-ops", "README content is in the prompt")
	assert.Contains(t, prompt, "multi-currency ledgers", "docs/*.md too")
	assert.NotContains(t, prompt, "not markdown", "only markdown is read")
	assert.NotContains(t, prompt, "sk-secret", "never a dotfile, never a secret")
	assert.Contains(t, prompt, "VISION.md", "…and it asks for the strategy registers")
	assert.True(t, IsDistillRequest(prompt), "bootstrap is bookkeeping: never journaled, never scored")
	assert.Len(t, docs, 2)
}

func TestBootstrapEditsMayWriteStrategyRegistersOnlyWhileTheyAreTemplates(t *testing.T) {
	repo := repoWithDocs(t)
	brain := EuclidBrain{Root: filepath.Join(repo, ".euclid"), Kind: "repo", Label: "repo:widget"}
	edits := []RegisterEdit{
		{File: "VISION.md", Mode: "append", Text: "- What the project is: a billing engine for co-ops"},
		{File: "MAP.md", Mode: "append", Text: "| docs | docs/ | roadmap and notes |"},
		{File: "SOUL.md", Mode: "append", Text: "- rewrite doctrine"},
	}

	touched, err := ApplyBootstrap(brain, edits)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"VISION.md", "MAP.md"}, touched, "SOUL is doctrine and off limits even to bootstrap")
	vision, _ := os.ReadFile(filepath.Join(brain.Root, "VISION.md"))
	assert.Contains(t, string(vision), "billing engine for co-ops")

	// A second bootstrap must not overwrite a VISION someone has since curated.
	require.NoError(t, os.WriteFile(filepath.Join(brain.Root, "VISION.md"), []byte("# VISION\n\n- What the project is: curated by a human\n"), 0o644))
	touched, err = ApplyBootstrap(brain, edits[:1])
	require.NoError(t, err)
	assert.Empty(t, touched, "VISION has moved past the template: bootstrap leaves it alone")
}

func TestAppliedEditsDoNotDuplicateHeadingsOrKeepPlaceholders(t *testing.T) {
	// Seen on the first live consolidation of the main brain (2026-09-11):
	// BRAIN.md ended up with "## Current state" twice because the model's
	// replacement text began with the heading it was replacing, and WISDOM
	// kept "- (add the first heuristic…)" above the real heuristics.
	root := t.TempDir()
	brain := EuclidBrain{Root: root, Kind: "main", Label: "main", Writable: true}
	require.NoError(t, os.WriteFile(filepath.Join(root, "BRAIN.md"), []byte("# BRAIN\n\n## Current state\n\n- Active front:\n- Last landed:\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "WISDOM.md"), []byte("# WISDOM\n\n- (add the first heuristic your work here earns)\n"), 0o644))

	_, err := ApplyEdits(brain, []RegisterEdit{
		{File: "BRAIN.md", Mode: "replace_section", Anchor: "## Current state", Text: "## Current state\n- Active front: arc cohort\n- Last landed: deck"},
		{File: "WISDOM.md", Mode: "append", Text: "- Cross-check every slide when the round changes."},
	})
	require.NoError(t, err)

	b, _ := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	assert.Equal(t, 1, strings.Count(string(b), "## Current state"), "the heading appears once")
	assert.Contains(t, string(b), "- Active front: arc cohort")
	w, _ := os.ReadFile(filepath.Join(root, "WISDOM.md"))
	assert.NotContains(t, string(w), "(add the first heuristic", "placeholder retired once real content exists")
	assert.Contains(t, string(w), "Cross-check every slide")
}
