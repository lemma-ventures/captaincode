package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MM38 in the brain: orientation into worker prompts, a journal line per run,
// /euclid status + distill. All opt-in: no brain → no change.

func euclidTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_HOME", filepath.Join(home, ".euclid"))
	t.Setenv("EUCLID_HANDLE", "tester")
	t.Setenv("EUCLID_TEMPLATE_DIR", filepath.Join(home, "none"))
	t.Setenv("CAPTAIN_EUCLID", "")
	t.Setenv("CAPTAIN_CWD", home) // a non-repo project dir: only the main brain applies
	return home
}

func TestWorkerContextWithoutABrainIsUnchanged(t *testing.T) {
	euclidTestHome(t)
	ctx := workerContext(defaultWorkspace())
	assert.NotContains(t, ctx, "<euclid>")
}

func TestWorkerContextCarriesEuclidOrientation(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".euclid", "BRAIN.md"), []byte("# BRAIN\n\nThe arc plan lives in ~/Gits/arc, not DLM.\n"), 0o644))
	ctx := workerContext(defaultWorkspace())
	assert.Contains(t, ctx, "<euclid>")
	assert.Contains(t, ctx, "The arc plan lives in ~/Gits/arc")
}

func TestSoloRunIsJournaledIntoTheWriteBrain(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if onStatus != nil {
			onStatus("⚙ edit docs/plan.md")
		}
		return leg, captaincode.Result{Text: "done with the plan update, all sections rewritten as asked", DurationMs: 4000}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "grok", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "update the plan doc"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	entries, err := captaincode.ReadJournal(captaincode.EuclidBrain{Root: filepath.Join(home, ".euclid")}, timeZero())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "update the plan doc", entries[0].Task)
	assert.Equal(t, "grok", entries[0].Leg)
	assert.Equal(t, "ok", entries[0].Outcome)
	assert.Equal(t, []string{"docs/plan.md"}, entries[0].Files, "files from the worker log's tool activity")
	assert.NotEmpty(t, entries[0].Log)
}

func TestEuclidStatusControlWord(t *testing.T) {
	home := euclidTestHome(t)
	b := teamBrain()
	body, _ := json.Marshal(map[string]any{"model": "grok", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/euclid"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "no brain", "before init: says how to start")

	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "WRITE")
	assert.Contains(t, rec.Body.String(), "main")
}

func TestDistillAppliesToTheWriteBrainOnly(t *testing.T) {
	home := euclidTestHome(t)
	root := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(root, "main")
	require.NoError(t, err)
	_, err = captaincode.JournalRun(home, captaincode.JournalEntry{Kind: "worker", Task: "add value routing", Leg: "gemini", Outcome: "ok", Files: []string{"pkg/value.go"}})
	require.NoError(t, err)
	b := teamBrain()
	var sawPrompt string
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		sawPrompt = brief
		return leg, captaincode.Result{Text: `{"summary":"value routing landed","edits":[{"file":"BRAIN.md","mode":"replace_section","anchor":"## Current state","text":"- Active front: value routing\n- Last landed: pkg/value.go","why":"journal"}]}`, DurationMs: 3000}, nil
	}
	// dry run
	body, _ := json.Marshal(map[string]any{"apply": false})
	rec := httptest.NewRecorder()
	b.euclidDistillHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/euclid/distill", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.True(t, captaincode.IsDistillRequest(sawPrompt), "the distiller prompt carries its marker")
	assert.Contains(t, sawPrompt, "pkg/value.go")
	assert.Contains(t, rec.Body.String(), "dry run")
	brain, _ := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	assert.NotContains(t, string(brain), "value routing", "dry run writes nothing")
	// apply
	body, _ = json.Marshal(map[string]any{"apply": true})
	rec = httptest.NewRecorder()
	b.euclidDistillHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/euclid/distill", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	brain, _ = os.ReadFile(filepath.Join(root, "BRAIN.md"))
	assert.Contains(t, string(brain), "- Active front: value routing")
	// the cursor advanced: a second distill has nothing new
	rec = httptest.NewRecorder()
	b.euclidDistillHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/euclid/distill", bytes.NewReader(body)))
	assert.Contains(t, rec.Body.String(), "nothing new")
	// distill runs are neither journaled nor scored
	entries, _ := captaincode.ReadJournal(captaincode.EuclidBrain{Root: root}, timeZero())
	assert.Len(t, entries, 1, "the distill call itself never lands in the journal")
	b.mu.Lock()
	defer b.mu.Unlock()
	assert.Empty(t, b.ledger.Events, "nor on the scorecards")
}

func TestDistillWithoutABrainExplains(t *testing.T) {
	euclidTestHome(t)
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.euclidDistillHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/euclid/distill", strings.NewReader(`{"apply":true}`)))
	assert.Equal(t, 500, rec.Code)
	assert.Contains(t, rec.Body.String(), "captain euclid init")
}

func timeZero() (t time.Time) { return }

// The director's grade of a run is memory too: the score, the verdict and
// the reviewer's notes are journaled (kind=review), and a poor verdict is a
// failure note - so the reasoning behind a grade outlives the number in the
// ledger (2026-09-16).
func TestDirectorGradesAreJournaledAndPoorOnesBecomeFailureNotes(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	b := newRecordBrain(3.0, nil)
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 3.0, Verdict: "poor", Notes: "answered a different question; ignored the file the user named"}, nil
	}
	b.recordRun(captaincode.LegGLM, "[user]\nfix the parser in lexer.go\n\n", bigResult(), home)

	brain := captaincode.EuclidBrain{Root: filepath.Join(home, ".euclid")}
	entries, err := captaincode.ReadJournal(brain, timeZero())
	require.NoError(t, err)
	var reviews []captaincode.JournalEntry
	for _, e := range entries {
		if e.Kind == "review" {
			reviews = append(reviews, e)
		}
	}
	require.Len(t, reviews, 1)
	assert.Equal(t, "glm", reviews[0].Leg)
	assert.Equal(t, "poor", reviews[0].Verdict)
	assert.Equal(t, 3.0, reviews[0].Score)
	assert.NotEmpty(t, reviews[0].Reviewer)
	assert.Contains(t, reviews[0].Summary, "ignored the file the user named")

	fails, err := os.ReadFile(filepath.Join(home, ".euclid", "memory", "FAILURES.md"))
	require.NoError(t, err)
	assert.Contains(t, string(fails), "glm graded poor by the director")
	assert.Contains(t, string(fails), "_(director)_")

	// A good grade is journaled but is no failure.
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 8.5, Verdict: "good", Notes: "complete and tested"}, nil
	}
	b.recordRun(captaincode.LegGLM, "[user]\nadd the changelog entry\n\n", bigResult(), home)
	fails, _ = os.ReadFile(filepath.Join(home, ".euclid", "memory", "FAILURES.md"))
	assert.Equal(t, 1, strings.Count(string(fails), "graded poor"))
}
