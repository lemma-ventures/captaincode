package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type statusLog struct {
	mu    sync.Mutex
	lines []string
}

func (s *statusLog) add(l string) { s.mu.Lock(); s.lines = append(s.lines, l); s.mu.Unlock() }
func (s *statusLog) all() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "\n")
}

// Fable 5.2 runs on the claude leg for high-complexity work at max effort,
// and nowhere else (fable.go).
func TestFableRunsForHighComplexityAtMaxEffort(t *testing.T) {
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegClaude: true}
	b.frontierFn = func(prompt string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		return captaincode.Result{Text: "done", DurationMs: 5}, nil
	}
	var st statusLog
	ws := captaincode.Workspace{Dir: t.TempDir(), Effort: captaincode.EffortMax, Class: captaincode.ClassHigh}
	_, _, err := b.runWorkerRerouted(ws, captaincode.LegFrontier, "prove the invariants", nil, st.add, "")
	require.NoError(t, err)
	assert.Contains(t, st.all(), "claude-cli:fable-5.2", "high complexity at max effort runs Fable")

	st = statusLog{}
	ws.Class = captaincode.ClassMedium
	_, _, err = b.runWorkerRerouted(ws, captaincode.LegFrontier, "tidy the README", nil, st.add, "")
	require.NoError(t, err)
	assert.NotContains(t, st.all(), "fable", "medium work stays on Opus")
}

// A Fable limit reruns the same turn on Opus and keeps Fable closed.
func TestFableLimitFallsBackToOpus(t *testing.T) {
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegClaude: true}
	calls := 0
	b.frontierFn = func(prompt string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		calls++
		if calls == 1 {
			return captaincode.Result{}, &captaincode.RateLimitError{Leg: captaincode.LegClaude, Msg: "You've reached your Fable limit. Switch to another model", Tier: "Fable"}
		}
		return captaincode.Result{Text: "done on opus", DurationMs: 5}, nil
	}
	var st statusLog
	ws := captaincode.Workspace{Dir: t.TempDir(), Effort: captaincode.EffortMax, Class: captaincode.ClassHigh, ClaudeModel: captaincode.FableModel()}
	_, res, err := b.runWorkerRerouted(ws, captaincode.LegFrontier, "prove the invariants", nil, st.add, "")
	require.NoError(t, err)
	assert.Equal(t, "done on opus", res.Text)
	assert.Equal(t, 2, calls, "the same turn, once more, on Opus")
	assert.Contains(t, st.all(), "Fable limited until")
	_, closed := b.fableClosed()
	assert.True(t, closed, "Fable stays closed until the limit resets")

	// While closed, the next high-complexity turn goes straight to Opus.
	st = statusLog{}
	_, _, err = b.runWorkerRerouted(ws, captaincode.LegFrontier, "prove more", nil, st.add, "")
	require.NoError(t, err)
	assert.Contains(t, st.all(), "Fable limited until")
	assert.Equal(t, 3, calls, "no knock on Fable while it is closed")
}

func TestFableLimitReadsTheSpendCap(t *testing.T) {
	assert.True(t, fableLimit(&captaincode.RateLimitError{Msg: "You've hit your monthly spend limit"}))
	assert.True(t, fableLimit(&captaincode.RateLimitError{Tier: "Fable"}))
	assert.False(t, fableLimit(&captaincode.RateLimitError{Msg: "You've hit your session limit · resets 2:50am"}), "an account window is the reroute net's")
}

// The frontier lane scores claude as Fable on high-complexity work, so it
// leads; with Fable closed it still leads as Opus, since Carbon is capped at
// medium work (its long runs failed with nothing kept, 11-14 Nov).
func TestFrontierLaneScoresClaudeAsFable(t *testing.T) {
	t.Setenv("CAPTAIN_LANES", "1")
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegCarbon: true, captaincode.LegClaude: true,
		captaincode.LegCodexCLI: true, captaincode.LegGrokMax: true}
	task := "audit the security architecture and prove the concurrency invariants"
	pick := b.frontierLead(task)
	assert.Equal(t, captaincode.LegClaude, pick.Leg)
	assert.Contains(t, pick.Reason, "on fable-5.2")
	b.ledger.Cooldowns[captaincode.FableCooldownKey] = time.Now().Add(time.Hour)
	pick = b.frontierLead(task)
	assert.Equal(t, captaincode.LegClaude, pick.Leg, "Fable closed: claude as Opus (59); Carbon (60) is capped at medium work")
	assert.NotContains(t, pick.Reason, "fable")
}

// /frontier /fable pins claude on Fable, whatever the lane would pick, and
// on a task the triage does not rate high.
func TestFrontierFableWordPinsClaude(t *testing.T) {
	t.Setenv("CAPTAIN_LANES", "1")
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegCarbon: true, captaincode.LegClaude: true}
	frontierRuns := 0
	b.frontierFn = func(prompt string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		frontierRuns++
		return captaincode.Result{Text: "fixed on fable", DurationMs: 5}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Errorf("%s ran - /frontier /fable is claude's", leg)
		return leg, captaincode.Result{Text: "x"}, nil
	}
	body, err := json.Marshal(map[string]any{"model": "frontier", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/frontier /fable tidy the README"}}})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set(workspaceHeader, t.TempDir())
	b.chatCompletions(rec, r)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "fixed on fable")
	assert.Equal(t, 1, frontierRuns)
}
