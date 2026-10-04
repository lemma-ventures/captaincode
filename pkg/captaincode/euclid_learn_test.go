package captaincode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A persona that only moves inside a distillation of a busy repository stays
// behind: the explorer showed registers days old. The learn loop reads what no
// pass has read, folds it, reads its own output again, and stops.

func learnFixture(t *testing.T) (string, EuclidBrain) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".euclid")
	_, err := Scaffold(root, "main")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "journal"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "journal", "activity-2026-10-01.jsonl"),
		[]byte(`{"at":"2026-10-01T10:00:00Z","kind":"worker","task":"fix the parser","leg":"claude","outcome":"ok"}`+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "memory", "MEMORIES.md"),
		[]byte("# MEMORIES - append-only\n\n## 2026-10-01 - capt\n\n- the parser accepts a trailing slash\n"), 0o644))
	return root, EuclidBrain{Root: root, Kind: "main", Writable: true, Label: "main"}
}

func TestLearnLoopFoldsWhatNoPassHasSeenThenConverges(t *testing.T) {
	root, brain := learnFixture(t)
	calls := 0
	passes, err := LearnLoop(brain, 4, true, func(prompt string) (string, error) {
		calls++
		if calls == 1 {
			assert.Contains(t, prompt, "fix the parser", "the pass sees the journal")
			assert.Contains(t, prompt, "trailing slash", "…and the memory section no pass folded yet")
			return `{"summary":"parser learned","edits":[` +
				`{"file":"BRAIN.md","mode":"replace_section","anchor":"## Current state","text":"- Active front: parser accepts a trailing slash","why":"journal"},` +
				`{"file":"INTUITION.md","mode":"append","text":"- [open] trailing slash handling is load-bearing","why":"memory"}]}`, nil
		}
		return `{"summary":"","edits":[]}`, nil
	})
	require.NoError(t, err)
	require.Len(t, passes, 2, "one pass over the input, one that found nothing left")
	assert.Equal(t, 1, passes[0].Journal)
	assert.Equal(t, 1, passes[0].Memories)
	assert.Equal(t, []string{"BRAIN.md", "INTUITION.md"}, passes[0].Edits)
	assert.Equal(t, 0, passes[1].Journal, "the journal cursor moved after the pass that read it")
	assert.Equal(t, 0, passes[1].Memories)

	b, err := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "parser accepts a trailing slash")
	intu, err := os.ReadFile(filepath.Join(root, "INTUITION.md"))
	require.NoError(t, err)
	assert.Contains(t, string(intu), "load-bearing")

	assert.False(t, LastDistilledAt(brain).IsZero(), "the journal cursor advanced")
	assert.Equal(t, 1, LastCrystallizedCount(brain), "the memory cursor advanced by one section")
	assert.Equal(t, 2, calls, "the loop stops when a pass has no input and no edits")
}

func TestLearnDryRunWritesNothingAndLeavesTheCursors(t *testing.T) {
	root, brain := learnFixture(t)
	before, err := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	require.NoError(t, err)
	passes, err := LearnLoop(brain, 4, false, func(string) (string, error) {
		return `{"summary":"would write","edits":[{"file":"BRAIN.md","mode":"replace_section","anchor":"## Current state","text":"- Active front: dry run","why":"test"}]}`, nil
	})
	require.NoError(t, err)
	require.Len(t, passes, 1, "a dry run is one pass over the same input")
	assert.Equal(t, 1, passes[0].Journal)

	after, err := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a dry run writes nothing")
	assert.True(t, LastDistilledAt(brain).IsZero(), "…and neither cursor moves")
	assert.Zero(t, LastCrystallizedCount(brain))
}

func TestLearnPromptNamesEveryRegisterAndExplainsHowToStop(t *testing.T) {
	root, brain := learnFixture(t)
	prompt := LearnPrompt(brain, LearnInputsOf(brain))
	assert.Contains(t, prompt, learnMarker)
	for _, n := range learnRegisterNames {
		assert.Contains(t, prompt, "current "+n, "every register the pass may write is shown")
	}
	assert.Contains(t, prompt, `"file": "<one of BRAIN.md|`)
	assert.Contains(t, prompt, `{"summary":"","edits":[]}`, "the loop's stop condition is in the envelope")
	assert.Contains(t, LearnPrompt(brain, LearnInputs{}), "none: this pass only reviews the registers above", "an inputless review pass says so")

	// A pass that only cleans up must still be accepted, and only persona
	// files are accepted - a reply naming SOUL.md is dropped.
	d, err := ParseLearn(`{"summary":"x","edits":[
		{"file":"SOUL.md","mode":"append","text":"doctrine","why":"no"},
		{"file":"memory/FAILURES.md","mode":"append","text":"- close the socket before rotating","why":"yes"}]}`)
	require.NoError(t, err)
	require.Len(t, d.Edits, 1, "SOUL is never a learn target")
	assert.Equal(t, "memory/FAILURES.md", d.Edits[0].File)
	_ = root
}

func TestEnsurePersonaFilesCreatesTheMissingPersonaRegisters(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".euclid")
	require.NoError(t, os.MkdirAll(root, 0o755))
	EnsurePersonaFiles(root)
	for _, n := range []string{"INTUITION.md", "AFFECT.md"} {
		assert.FileExists(t, filepath.Join(root, n))
	}
	// Overwrites nothing: an existing register keeps its content.
	require.NoError(t, os.WriteFile(filepath.Join(root, "INTUITION.md"), []byte("> mine\n"), 0o644))
	EnsurePersonaFiles(root)
	b, err := os.ReadFile(filepath.Join(root, "INTUITION.md"))
	require.NoError(t, err)
	assert.Equal(t, "> mine\n", string(b))
}

func TestRenderLearnPassReadsForAHuman(t *testing.T) {
	p := LearnPass{Journal: 1, Memories: 3, Edits: []string{"WISDOM.md", "BRAIN.md"}}
	line := RenderLearnPass("main", 1, 2, p, false)
	assert.Contains(t, line, "learn: main · pass 1/2")
	assert.Contains(t, line, "1 journal entry")
	assert.Contains(t, line, "3 memory sections")
	assert.Contains(t, line, "2 edit(s) (BRAIN.md, WISDOM.md)", "files sorted and deduped")
	assert.Contains(t, strings.ToLower(RenderLearnPass("main", 2, 2, LearnPass{Journal: 1}, true)), "converged")
}

func TestLearnDrainsBacklogWithoutSkippingAndKeepsConcurrentInput(t *testing.T) {
	root, brain := learnFixture(t)
	var journal, memories strings.Builder
	for i := 0; i < 45; i++ {
		fmt.Fprintf(&journal, "{\"at\":\"2026-10-01T10:00:00Z\",\"kind\":\"worker\",\"task\":\"task-%d\",\"outcome\":\"ok\"}\n", i)
	}
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&memories, "## 2026-10-01 - memory-%d\n\n- lesson-%d\n\n", i, i)
	}
	jp := filepath.Join(root, "journal", "activity-2026-10-01.jsonl")
	mp := filepath.Join(root, "memory", "MEMORIES.md")
	require.NoError(t, os.WriteFile(jp, []byte(journal.String()), 0o644))
	require.NoError(t, os.WriteFile(mp, []byte(memories.String()), 0o644))
	calls := 0
	passes, err := LearnLoop(brain, 4, true, func(prompt string) (string, error) {
		calls++
		if calls == 1 {
			assert.Contains(t, prompt, "task-0\n")
			assert.NotContains(t, prompt, "task-44\n")
			f, err := os.OpenFile(jp, os.O_APPEND|os.O_WRONLY, 0o644)
			require.NoError(t, err)
			_, err = f.WriteString("{\"at\":\"2026-09-30T10:00:00Z\",\"kind\":\"worker\",\"task\":\"arrived-during-call\",\"outcome\":\"ok\"}\n")
			require.NoError(t, err)
			require.NoError(t, f.Close())
		}
		if calls == 2 {
			assert.Contains(t, prompt, "task-44\n")
			assert.Contains(t, prompt, "arrived-during-call")
		}
		return `{"summary":"","edits":[]}`, nil
	})
	require.NoError(t, err)
	require.Len(t, passes, 3)
	assert.Equal(t, 40, passes[0].Journal)
	assert.Equal(t, 12, passes[0].Memories)
	assert.Equal(t, 6, passes[1].Journal)
	assert.Equal(t, 3, passes[1].Memories)
	assert.Empty(t, LearnInputsOf(brain).Journal)
	assert.Empty(t, LearnInputsOf(brain).Memories)
}

func TestLearnDryRunDoesNotCreateMissingRegisters(t *testing.T) {
	root, brain := learnFixture(t)
	for _, name := range []string{"AFFECT.md", "INTUITION.md"} {
		require.NoError(t, os.Remove(filepath.Join(root, name)))
	}
	_, err := LearnLoop(brain, 1, false, func(string) (string, error) {
		return `{"summary":"","edits":[]}`, nil
	})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(root, "AFFECT.md"))
	assert.NoFileExists(t, filepath.Join(root, "INTUITION.md"))
}

func TestLearnRejectsRegisterChangedDuringCall(t *testing.T) {
	root, brain := learnFixture(t)
	_, err := LearnLoop(brain, 1, true, func(string) (string, error) {
		require.NoError(t, os.WriteFile(filepath.Join(root, "BRAIN.md"), []byte("human edit\n"), 0o644))
		return `{"summary":"","edits":[{"file":"BRAIN.md","mode":"append","text":"stale change"}]}`, nil
	})
	require.Error(t, err)
	b, err := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	require.NoError(t, err)
	assert.Equal(t, "human edit\n", string(b))
	assert.Len(t, LearnInputsOf(brain).Journal, 1)
}

func TestOrientationSelectsStateAndLessons(t *testing.T) {
	root, brain := learnFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "BRAIN.md"), []byte("<!-- freshness -->\n# BRAIN\n\nA \"you are here\" snapshot, not a log.\n\n## Current state\n\n- Active front: lossless learning\n- Next gate:\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "WISDOM.md"), []byte("# WISDOM\n\nJudgment proven across repos.\n\n## Lessons\n\n- Advance only consumed input.\n"), 0o644))
	got := renderOrientation([]EuclidBrain{brain}, 2500)
	assert.Contains(t, got, "lossless learning")
	assert.Contains(t, got, "Advance only consumed input")
	assert.NotContains(t, got, "freshness")
	assert.NotContains(t, got, "snapshot, not a log")
	assert.NotContains(t, got, "Judgment proven")
	assert.NotContains(t, got, "Next gate:")
}

func TestOrientationIncludesAcceptedLearningStateLessons(t *testing.T) {
	root, brain := learnFixture(t)
	learningDir := filepath.Join(root, "learning")
	require.NoError(t, os.MkdirAll(learningDir, 0o700))
	stateJSON := `{"version":1,"events":[],"cursors":{},"lessons":{"lesson:1":{"id":"lesson:1","text":"Keep tests hermetic.","status":"accepted","evidence":["ev1"]},"lesson:2":{"id":"lesson:2","text":"Draft only.","status":"candidate","evidence":["ev2"]}}}`
	require.NoError(t, os.WriteFile(filepath.Join(learningDir, "state.json"), []byte(stateJSON), 0o600))
	got := renderOrientation([]EuclidBrain{brain}, 2500)
	assert.Contains(t, got, "Keep tests hermetic.")
	assert.NotContains(t, got, "Draft only.")
}

func TestLearnRecoversInterruptedTransaction(t *testing.T) {
	root, brain := learnFixture(t)
	in := LearnInputsOf(brain)
	require.NoError(t, in.Err)
	writes := map[string][]byte{"BRAIN.md": []byte("new state\n"), "WISDOM.md": []byte("new lesson\n")}
	data, err := json.Marshal(brainTransaction{Before: in.base, After: writes})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".pending-edits.json"), data, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "BRAIN.md"), writes["BRAIN.md"], 0o600))
	require.Error(t, LearnInputsOf(brain).Err)
	require.NoError(t, RecoverBrainEdits(brain))
	for name, want := range writes {
		got, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	assert.NoFileExists(t, filepath.Join(root, ".pending-edits.json"))
}

func TestLearnRejectsSymlinkWithoutPartialWrites(t *testing.T) {
	root, brain := learnFixture(t)
	before, err := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	require.NoError(t, err)
	outside := filepath.Join(t.TempDir(), "outside.md")
	require.NoError(t, os.WriteFile(outside, []byte("private\n"), 0o600))
	require.NoError(t, os.Remove(filepath.Join(root, "WISDOM.md")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "WISDOM.md")))
	_, err = ApplyEdits(brain, []RegisterEdit{{File: "BRAIN.md", Mode: "append", Text: "change"}, {File: "WISDOM.md", Mode: "append", Text: "change"}})
	require.Error(t, err)
	got, err := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	require.NoError(t, err)
	assert.Equal(t, before, got)
	got, err = os.ReadFile(outside)
	require.NoError(t, err)
	assert.Equal(t, "private\n", string(got))
}
