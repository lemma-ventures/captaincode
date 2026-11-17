package captaincode

// Live qualification for the codex app-server transport (ROADMAP M3.8, Q14):
// one small real turn through the transport the exec default would move to,
// plus one note delivered mid-turn through the same channel /btw uses. The
// fake-server tests pin the JSON-RPC contract; this check proves the
// installed codex still speaks it - initialize, thread/start, turn/start,
// item events, token usage, turn/completed and turn/steer - before the
// default changes. It spends one or two real codex calls, so it runs only
// with CAPTAIN_LIVE_CLI=1:
//
//	CAPTAIN_LIVE_CLI=1 go test ./pkg/captaincode -run TestLiveCodexAppServer -v
//
// Prompts sent (named for the report): "Reply with exactly one word: pong.
// Do not use any tools." and "Run `sleep 8` with your shell tool first, then
// reply with exactly one word: pong. Do no other work." with the note
// "Acknowledge this note by ending your final reply with the word: steered."

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveCodexAppServer(t *testing.T) {
	if os.Getenv("CAPTAIN_LIVE_CLI") != "1" {
		t.Skip("live: set CAPTAIN_LIVE_CLI=1 to run the codex app-server transport for real")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not installed")
	}
	if home := os.Getenv("REAL_HOME"); home != "" {
		t.Setenv("HOME", home) // codex's login lives in the real home
	}
	t.Setenv("CAPTAIN_EUCLID", "0")
	t.Setenv("CAPTAIN_CODEX_TRANSPORT", "app-server")

	t.Run("schema gate", func(t *testing.T) {
		require.NoError(t, CheckCodexAppServer(), "the installed codex must still expose the methods and fields the transport uses")
	})

	t.Run("one turn", func(t *testing.T) {
		ws := Workspace{Dir: t.TempDir(), Effort: EffortLow}
		var streamed strings.Builder
		res, err := ws.RunWorkerStreamHooks(LegCodexCLI, "Reply with exactly one word: pong. Do not use any tools.", 0, func(d string) { streamed.WriteString(d) }, nil)
		require.NoError(t, err)
		t.Logf("one turn: %q in %d ms, session %q, tokens %d, first output %v", strings.TrimSpace(res.Text), res.DurationMs, res.SessionID, res.Tokens, deref(res.FirstOutputMs))
		assert.Contains(t, strings.ToLower(res.Text), "pong", "the answer is read from the turn's items")
		assert.Contains(t, strings.ToLower(streamed.String()), "pong", "…and streamed as it arrives")
		assert.NotEmpty(t, res.SessionID, "the thread id is read as the session id")
		assert.Positive(t, res.Tokens+tokensOf(res.TokenUsage), "usage is read")
		assert.True(t, res.FirstOutputMs != nil, "a time to first output is read")
	})

	t.Run("steer mid turn", func(t *testing.T) {
		dir := t.TempDir()
		steer := NewSteer(dir)
		ws := Workspace{Dir: dir, Effort: EffortLow, Steer: steer}
		type outcome struct {
			res Result
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			res, err := ws.RunWorkerStreamHooks(LegCodexCLI, "Run `sleep 8` with your shell tool first, then reply with exactly one word: pong. Do no other work.", 0, nil, nil)
			done <- outcome{res, err}
		}()
		attached := make(chan struct{})
		go func() {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				for _, l := range steer.Attached() {
					if l == LegCodexCLI {
						attached <- struct{}{}
						return
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			attached <- struct{}{}
		}()
		<-attached
		took, err := steer.Send("Acknowledge this note by ending your final reply with the word: steered.")
		require.NoError(t, err)
		assert.Contains(t, took, LegCodexCLI, "the note reaches the running turn, not the next one")
		select {
		case got := <-done:
			require.NoError(t, got.err)
			t.Logf("steered turn: %q in %d ms, %d notes took %v", strings.TrimSpace(got.res.Text), got.res.DurationMs, len(steer.Notes()), steer.Notes())
			lower := strings.ToLower(got.res.Text)
			assert.Contains(t, lower, "pong", "the original instruction still holds")
			assert.Contains(t, lower, "steered", "the note amended the same turn's answer")
		case <-time.After(3 * time.Minute):
			t.Fatal("the steered turn did not finish within 3 minutes")
		}
	})
}
