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

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Naming a session is housekeeping, not work. It was being recorded as a real
// free-leg run: today's history held entries whose OUTPUT was a title ("Say ok
// phrase"), which inflates the free leg's run count and feeds the learning
// loop with work nobody asked for. Worse, a title carries the user's prompt as
// its task, so the run log reads as if the free leg answered the real turn -
// which is exactly how it looked when /frontier appeared to run on free.

func titleRequest(t *testing.T, text string) *http.Request {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model": "frontier", "stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a title generator. Give this conversation a short title."},
			{"role": "user", "content": text},
		},
	})
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
}

func TestTitleRequestIsNotRecordedAsWork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "Arc Proof Sizes", Tokens: 3, DurationMs: 400}, nil
	}
	assessed := false
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		assessed = true
		return captaincode.Assessment{Quality: 9, Verdict: "good"}, nil
	}

	rec := httptest.NewRecorder()
	b.chatCompletions(rec, titleRequest(t, "we are using arc as the IVC and proof system for all proofs in lemma"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	entries, _ := filepath.Glob(filepath.Join(home, ".captaincode", "history", "*.jsonl"))
	assert.Empty(t, entries, "a session title must not land in the run history")
	assert.Empty(t, b.ledger.Events, "…nor in the ledger the router learns from")
	assert.False(t, assessed, "…nor cost an assessment call")
}

func TestRealTurnIsStillRecorded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "here is the answer to your question about proof sizes", Tokens: 40, DurationMs: 900}, nil
	}
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 8, Verdict: "good"}, nil
	}

	body, _ := json.Marshal(map[string]any{
		"model": "free", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "how do we handle gigabyte-size proofs?"}},
	})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	entries, _ := filepath.Glob(filepath.Join(home, ".captaincode", "history", "*.jsonl"))
	require.Len(t, entries, 1, "real work is still recorded")
	body2, err := os.ReadFile(entries[0])
	require.NoError(t, err)
	assert.Contains(t, string(body2), "gigabyte-size proofs")
}

func TestTitleRunIsNeverRerouted(t *testing.T) {
	b := teamBrain()
	legs := []captaincode.Leg{}
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		legs = append(legs, leg)
		return leg, captaincode.Result{}, captaincode.ErrProviderDown
	}

	_, _, err := b.runWorkerRerouted(defaultWorkspace(), captaincode.LegFree,
		"[system]\nYou are a title generator\n[user]\nrewrite the storage layer", nil, nil, "")

	assert.Error(t, err, "the title simply fails")
	assert.Len(t, legs, 1, "a failed title must not spend a second leg (live: free stalled, ds-flash picked it up)")
}

func TestTitleRequestGetsAMinimalPromptAndNoRepoContext(t *testing.T) {
	// The title probe's worker started an "Explore repo structure" subagent in
	// the user's repo: the title request had been wrapped in the full worker
	// scaffolding (working context, Euclid orientation, deliverable contract),
	// which tells the model it is a software engineer with a task. A title is
	// six words. It gets six words' worth of prompt.
	b := teamBrain()
	var seen string
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		seen = brief
		return leg, captaincode.Result{Text: "Storage layer migration"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, titleRequest(t, "rewrite the storage layer and migrate every caller"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	assert.NotContains(t, seen, "Working context", "no engineer framing")
	assert.NotContains(t, seen, "<euclid>", "no repository brain")
	assert.NotContains(t, seen, "End-of-turn contract", "no deliverable contract")
	assert.Contains(t, seen, "rewrite the storage layer", "the text to name is still there")
	assert.Contains(t, strings.ToLower(seen), "title", "…and the instruction is to name it, not do it")
	assert.Less(t, len(seen), 600, "a title prompt is small: %d chars", len(seen))
}

// A session that inherited a worker transcript (a `-c` that resumed a worker
// before the umbrella fix) carries "You are a title generator" inside an OLD
// user message. Only the request's own framing may make a turn a title:
// scanning the conversation body turned every prompt typed in that session -
// "/frontier", "/codex-cli" - into a six-word title from the free leg (live
// 2026-09-12: "Arc Performance Optimization" was the whole answer).
func TestTitleMarkerInsideOldConversationDoesNotMakeATitleTurn(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	t.Setenv("CAPTAIN_WORKER_LOGS", "0")
	b := teamBrain()
	var ranLeg captaincode.Leg
	var ranPrompt string
	b.frontierFn = func(task string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		ranLeg, ranPrompt = captaincode.LegFrontier, task
		return captaincode.Result{Text: "Next: shared Merkle multiproofs, then proof encoding."}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ranLeg, ranPrompt = leg, brief
		return leg, captaincode.Result{Text: "Arc Performance Optimization"}, nil
	}
	body, _ := json.Marshal(map[string]any{
		"model": "frontier", "stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": "You are opencode, an interactive CLI tool."},
			{"role": "user", "content": "[system]\nYou are opencode...\n\n[system]\nYou are a title generator. Give this conversation a short title.\n\n[user]\nwe are using arc as the IVC"},
			{"role": "assistant", "content": "Arc Proof Sizes"},
			{"role": "user", "content": "what's next in the optimization of arc (performance, proof size)"},
		},
	})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.NotEqual(t, captaincode.LegFree, ranLeg, "the real turn must not be demoted to the title leg")
	assert.NotContains(t, ranPrompt, "Reply with ONLY a short title", "…nor rewritten into a title prompt")
	assert.Contains(t, rec.Body.String(), "Merkle", "the user gets the work, not a title")

	assert.True(t, isTitleTurn([]oaiMessage{
		{Role: "system", Content: json.RawMessage(`"You are a title generator. Give this conversation a short title."`)},
		{Role: "user", Content: json.RawMessage(`"we are using arc"`)},
	}), "opencode's own title call is still recognized")
	assert.False(t, captaincode.IsTitlePrompt("[user]\n[system]\nYou are a title generator\n\n[user]\nreal work"), "a marker buried in the body is not a title")
	assert.True(t, captaincode.IsTitlePrompt("[system]\nYou are a title generator\n\n[user]\nname this"), "a leading [system] block is")
}
