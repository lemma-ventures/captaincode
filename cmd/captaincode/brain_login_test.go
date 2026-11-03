package main

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deadLogin(leg captaincode.Leg) error {
	return &captaincode.LoginError{Leg: leg, Cmd: string(leg) + " login", Detail: "Not signed in."}
}

// 2 Nov: a dead grok login read "the grok-cli leg's provider is temporarily
// unavailable (upstream outage) - retry shortly". It now says what to run
// and where, and is still provider-down so a routed task reroutes.
func TestLoginErrorSaysWhatToRun(t *testing.T) {
	err := deadLogin(captaincode.LegGrokCLI)
	assert.True(t, errors.Is(err, captaincode.ErrLoginNeeded))
	assert.True(t, errors.Is(err, captaincode.ErrProviderDown), "a routed task still reroutes")
	assert.Contains(t, err.Error(), "grok-cli is not logged in on ")
	assert.Contains(t, err.Error(), "run `grok-cli login`")
	assert.Equal(t, captaincode.CauseCredential, captaincode.FailCause(err.Error()), "off the model's reliability stats")

	_, why := benchPolicy(captaincode.LegGrokCLI, err)
	assert.Contains(t, why, "`captain legs reopen grok-cli`")
}

// A leg the user named that is not logged in fails; the same failure on a
// leg the brain chose reroutes, as before.
func TestNamedLegWithoutLoginDoesNotReroute(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "0")
	b := teamBrain()
	var mu sync.Mutex
	var tried []captaincode.Leg
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		tried = append(tried, leg)
		mu.Unlock()
		if leg == captaincode.LegCodexCLI {
			return leg, captaincode.Result{}, deadLogin(leg)
		}
		return leg, captaincode.Result{Text: "answer from " + string(leg), DurationMs: 5}, nil
	}
	prompt := "[user]\nrefactor the parser and keep its tests green\n\n"

	ws := defaultWorkspace()
	ws.Named = captaincode.LegCodexCLI
	ran, _, err := b.runWorkerRerouted(ws, captaincode.LegCodexCLI, prompt, nil, nil, "")
	require.ErrorIs(t, err, captaincode.ErrLoginNeeded)
	assert.Equal(t, captaincode.LegCodexCLI, ran)
	assert.Equal(t, []captaincode.Leg{captaincode.LegCodexCLI}, tried, "no other leg ran in its place")

	tried = nil
	ran, res, err := b.runWorkerRerouted(defaultWorkspace(), captaincode.LegCodexCLI, prompt, nil, nil, "")
	require.NoError(t, err)
	assert.NotEqual(t, captaincode.LegCodexCLI, ran, "a routed task reroutes")
	assert.Contains(t, res.Text, "answer from")
}

// A /repeat round whose named leg is not logged in ends the loop: every
// later round would fail the same way until the user logs in.
func TestRepeatStopsWhenANamedLegIsNotLoggedIn(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	var mu sync.Mutex
	runs := 0
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		return leg, captaincode.Result{}, deadLogin(leg)
	}
	go b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat 5 /codex-cli define the next items"))

	status := func() string {
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, repeatReq("/repeat status"))
		return rec.Body.String()
	}
	require.Eventually(t, func() bool { return strings.Contains(status(), "finished") }, 30*time.Second, 50*time.Millisecond)
	st := status()
	assert.Contains(t, st, "codex-cli is not logged in on ")
	assert.Contains(t, st, "run `codex-cli login`")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, runs, "one round, not five, and no other leg")
}
