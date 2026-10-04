package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `captain euclid learn` is the persona loop: passes over the registers until
// a pass has no new input and no edits, one model call per pass on the distill
// leg, then the dashboard data is rebuilt through the brain's own bin copy.

func TestLearnBrainFoldsThenStopsAndSaysWhatItDid(t *testing.T) {
	home := euclidTestHome(t)
	root := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(root, "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "memory", "MEMORIES.md"),
		[]byte("# MEMORIES - append-only\n\n## 2026-10-01 - capt\n\n- the parser keeps its tests green\n"), 0o644))

	b := teamBrain()
	calls := 0
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		calls++
		if calls == 1 {
			assert.Contains(t, brief, "[euclid learn]", "the loop's own marker is on the prompt")
			assert.Contains(t, brief, "tests green", "the unread memory section is in front of the model")
			return leg, captaincode.Result{Text: `{"summary":"learned","edits":[{"file":"WISDOM.md","mode":"append","text":"- tests green beats tests modeled","why":"memory"}]}`}, nil
		}
		return leg, captaincode.Result{Text: `{"summary":"","edits":[]}`}, nil
	}

	var out bytes.Buffer
	eu := captaincode.EuclidBrain{Root: root, Kind: "main", Writable: true, Label: "main"}
	passes, err := b.learnBrain(eu, 4, true, &out)
	require.NoError(t, err)
	require.Len(t, passes, 2)
	assert.Equal(t, 2, calls)

	wisdom, err := os.ReadFile(filepath.Join(root, "WISDOM.md"))
	require.NoError(t, err)
	assert.Contains(t, string(wisdom), "tests green beats tests modeled")

	text := out.String()
	assert.Contains(t, text, "learn: main · pass 1/2: 1 memory section")
	assert.Contains(t, text, "1 edit(s) (WISDOM.md)")
	assert.Contains(t, text, "learn: main · pass 2/2: no new input → no edits (converged)")
}

func TestLearnBrainDryRunReportsInsteadOfWriting(t *testing.T) {
	home := euclidTestHome(t)
	root := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(root, "main")
	require.NoError(t, err)
	before, err := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	require.NoError(t, err)

	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: `{"summary":"x","edits":[{"file":"BRAIN.md","mode":"replace_section","anchor":"## Current state","text":"- Active front: dry","why":"dry"}]}`}, nil
	}
	var out bytes.Buffer
	_, err = b.learnBrain(captaincode.EuclidBrain{Root: root, Kind: "main", Writable: true, Label: "main"}, 4, false, &out)
	require.NoError(t, err)

	after, err := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a dry run writes nothing")
	assert.Contains(t, out.String(), "dry run, nothing written")
}

func TestLearnRespectsTheWriteBrainAndItsIndexRoot(t *testing.T) {
	home := euclidTestHome(t)
	repo := filepath.Join(home, "Gits", "widget")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	_, err := captaincode.Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	_, err = captaincode.Scaffold(filepath.Join(repo, ".euclid", "developers", "tester"), "developer")
	require.NoError(t, err)

	shared := learnBrainAt(repo, filepath.Join(repo, ".euclid"))
	assert.Equal(t, "repo", shared.Kind, "the shared brain belongs to `captain euclid share`, not to learn")
	assert.False(t, shared.Writable)

	dev := learnBrainAt(repo, filepath.Join(repo, ".euclid", "developers", "tester"))
	assert.Equal(t, "developer", dev.Kind)
	assert.True(t, dev.Writable, "a developer subtree is a write brain")
	assert.Equal(t, filepath.Join(repo, ".euclid"), indexRootOfRoot(dev.Root), "its dashboard data lives with the repo brain")
}
