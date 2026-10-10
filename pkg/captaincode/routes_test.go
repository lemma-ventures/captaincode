package captaincode

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The causes come from the errors the ledger recorded (2026-07..10).
func TestFailCauseSortsRecordedErrors(t *testing.T) {
	for msg, want := range map[string]string{
		"nim/deepseek-ai/deepseek-v4.1-flash: worker stalled - the model went quiet between steps for 1m30s": CauseHost,
		"huggingface/stepfun-ai/Step-3.5-Flash did not finish within 15m0s: worker timed out":                CauseHost,
		`opencode error: {"name":"APIError","data":{"message":"Provided authentication token is expired."}}`: CauseCredential,
		"cursor-agent -p: exit status 1: ActionRequiredError: You've hit your usage limit":                   CauseCredential,
		"codex exec: exit status 2: error: invalid UTF-8 was detected in one or more arguments":              CauseHarness,
		"claude -p start: chdir /tmp/global-fallback: no such file or directory":                             CauseHarness,
		"nim/z-ai/glm-5.3 stopped after 4m45s: interrupted by the user":                                      CauseUserStop,
		"grok-max: withdrawn before it started: interrupted by the user":                                     CauseUserStop,
		"kimi: worker returned an empty answer":                                                              CauseModel,
	} {
		assert.Equal(t, want, FailCause(msg), msg)
	}
	assert.True(t, harnessFault("interrupted by the user"), "a user's stop is not the model's failure")
	assert.False(t, harnessFault("the model went quiet between steps"), "a stall is")
}

func TestHostOf(t *testing.T) {
	assert.Equal(t, "nim", HostOf(Event{Leg: LegGLM, Route: "opencode:nim"}))
	assert.Equal(t, "huggingface", HostOf(Event{Leg: LegStep, Model: "huggingface/stepfun-ai/Step-3.5-Flash"}))
	assert.Equal(t, "unknown", HostOf(Event{Leg: LegStep, Model: "stepfun-ai/Step-3.5-Flash"}), "recorded before routes were")
	assert.Equal(t, "claude-cli", HostOf(Event{Leg: LegClaude}), "a CLI leg has one host")
}

// Step moved to Hugging Face and got twelve times slower: its speed must be
// the new host's, not an average over both.
func TestStatsRestartSpeedAndReliabilityOnANewHost(t *testing.T) {
	require.Equal(t, "huggingface", HostOfLeg(LegStep))
	t0 := time.Now().Add(-48 * time.Hour)
	var ev []Event
	for i := 0; i < 6; i++ {
		ev = append(ev, Event{At: t0, Leg: LegStep, Model: "stepfun-ai/Step-3.5-Flash", Outcome: "ok", Duration: 30_000, Quality: 7})
	}
	ev = append(ev, Event{At: t0, Leg: LegStep, Model: "stepfun-ai/Step-3.5-Flash", Outcome: "fail", Error: "worker timed out"})
	for i := 0; i < 2; i++ {
		ev = append(ev, Event{At: t0.Add(time.Hour), Leg: LegStep, Route: "opencode:huggingface", Outcome: "ok", Duration: 400_000, Quality: 9})
	}
	st := statsOf(ev)[LegStep]
	assert.Equal(t, int64(400_000), st.AvgDurationMs, "speed is the current host's")
	assert.Zero(t, st.Fails, "the old host's timeout is set aside")
	assert.Equal(t, 7, st.OtherHostRuns)
	assert.Equal(t, 8, st.N, "quality is the model's: every host counts")
	assert.InDelta(t, 7.5, st.AvgQuality, 0.01)
	assert.Contains(t, HostNote(st), "on huggingface")
	assert.Equal(t, 1, st.FailCauses[CauseHost])
}

func TestStatsKeepOurBugsAndUserStopsOffTheModel(t *testing.T) {
	ev := []Event{
		{Leg: LegCodexCLI, Outcome: "ok", Duration: 1000},
		{Leg: LegCodexCLI, Outcome: "fail", Error: "invalid UTF-8 was detected"},
		{Leg: LegCodexCLI, Outcome: "fail", Error: "stopped after 3s: interrupted by the user"},
		{Leg: LegCodexCLI, Outcome: "fail", Error: "the model refused"},
	}
	st := statsOf(ev)[LegCodexCLI]
	assert.Equal(t, 1, st.Fails)
	assert.Equal(t, 2, st.HarnessFails)
}

// NIM stalled three models in one week: three stalls in a row there, from
// more than one leg, cool every leg it serves.
func TestHostStallsBenchEveryLegOnTheHost(t *testing.T) {
	l := NewLedger(filepath.Join(t.TempDir(), "state.json"))
	stall := "worker stalled - the model went quiet between steps for 1m30s"
	l.Record(Event{Leg: LegDSFlash, Route: "opencode:nim", Outcome: "fail", Error: stall})
	l.Record(Event{Leg: LegGLM, Route: "opencode:nim", Outcome: "fail", Error: stall})
	assert.Empty(t, l.BenchedHosts, "two stalls are not a run")
	l.Record(Event{Leg: LegKimi, Route: "opencode:nim", Outcome: "fail", Error: stall})
	require.Contains(t, l.BenchedHosts, "nim")
	for _, leg := range []Leg{LegDSFlash, LegGLM, LegKimi} {
		assert.True(t, l.Cooldowns[leg].After(time.Now().Add(25*time.Minute)), "%s cools with its host", leg)
	}
	assert.True(t, l.Cooldowns[LegStep].IsZero(), "other hosts stay open")
}

func TestOneModelStallingDoesNotBenchItsHost(t *testing.T) {
	l := NewLedger(filepath.Join(t.TempDir(), "state.json"))
	for i := 0; i < 3; i++ {
		l.Record(Event{Leg: LegDeepSeek, Route: "opencode:openrouter", Outcome: "fail", Error: "worker timed out"})
	}
	assert.Empty(t, l.BenchedHosts, "OpenRouter serves eight legs; one stalling model is not the host")
	l.Record(Event{Leg: LegGemini, Route: "opencode:openrouter", Outcome: "ok"})
	t.Setenv("CAPTAIN_HOST_BENCH", "0")
	l.Record(Event{Leg: LegGLM, Route: "opencode:nim", Outcome: "fail", Error: "stalled"})
}
