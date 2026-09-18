package captaincode

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live 2026-09-10 00:47: a 45-minute frontier run ended with "You've hit
// your session limit · resets 2:50am (Europe/Zurich)". The runner returned an
// EMPTY result (45 minutes of streamed text discarded), the ledger stored the
// bare word "rate limited", and the leg was benched a flat 30 minutes while
// the window actually reopened at 02:50.

func zurich(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Europe/Zurich")
	require.NoError(t, err)
	return loc
}

func TestParseResetAt(t *testing.T) {
	loc := zurich(t)
	now := time.Date(2026, 9, 10, 0, 47, 11, 0, loc)
	cases := []struct {
		msg  string
		want time.Time
		ok   bool
	}{
		{"You've hit your session limit · resets 2:50am (Europe/Zurich)", time.Date(2026, 9, 10, 2, 50, 0, 0, loc), true},
		{"resets 1:49pm", time.Date(2026, 9, 10, 13, 49, 0, 0, loc), true},
		{"limit reached, resets 11pm", time.Date(2026, 9, 10, 23, 0, 0, 0, loc), true},
		{"resets at 12:15am tomorrow", time.Date(2026, 9, 11, 0, 15, 0, 0, loc), true}, // 00:15 is already past → next day
		{"resets Sep 14 at 5:59am (Europe/Zurich)", time.Date(2026, 9, 14, 5, 59, 0, 0, loc), true},
		{"Try again at 14:00", time.Date(2026, 9, 10, 14, 0, 0, 0, loc), true},
		{"rate limit exceeded, retry in 2h 15m", now.Add(2*time.Hour + 15*time.Minute), true},
		{"please retry in 45 minutes", now.Add(45 * time.Minute), true},
		{"usage limit", time.Time{}, false},
		{"", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			got, ok := ParseResetAt(c.msg, now)
			assert.Equal(t, c.ok, ok)
			if c.ok {
				assert.True(t, got.Equal(c.want), "got %s want %s", got, c.want)
			}
		})
	}
	// A time-of-day with no zone uses NOW's zone, not UTC.
	got, ok := ParseResetAt("resets 9am", time.Date(2026, 9, 10, 8, 0, 0, 0, loc))
	require.True(t, ok)
	assert.Equal(t, "Europe/Zurich", got.Location().String())
}

func TestRateLimitErrorCarriesMessageAndReset(t *testing.T) {
	loc := zurich(t)
	now := time.Date(2026, 9, 10, 0, 47, 0, 0, loc)
	err := rateLimitedAt(LegClaude, "You've hit your session limit · resets 2:50am (Europe/Zurich)", now)
	assert.True(t, errors.Is(err, ErrRateLimited), "still the class every caller checks")
	var rl *RateLimitError
	require.True(t, errors.As(err, &rl))
	assert.Equal(t, LegClaude, rl.Leg)
	assert.Equal(t, 2, rl.ResetAt.In(loc).Hour())
	assert.Contains(t, err.Error(), "session limit", "the provider's own words survive into the ledger")
	assert.Contains(t, err.Error(), "02:50", "and so does the reset time")

	plain := rateLimitedAt(LegGrok, "429 too many requests", now)
	require.True(t, errors.As(plain, &rl))
	assert.True(t, rl.ResetAt.IsZero(), "no reset time known → zero, the caller falls back to its default")
}

func TestClaudeRateLimitKeepsPartialOutput(t *testing.T) {
	fakeBin(t, "claude", `#!/bin/sh
cat <<'EOF2'
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"D00 contracts written to docs/arc-implementation/D00-contracts.md. "}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Starting D01."}}}
{"type":"result","is_error":true,"result":"You've hit your session limit · resets 2:50am (Europe/Zurich)"}
EOF2
`)
	res, err := runClaudeStreamOpts("", "implement the plan", 30*time.Second, 0, nil, nil, false, "", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimited))
	assert.Contains(t, res.Text, "D00 contracts written", "45 minutes of narrative must not evaporate")
	assert.True(t, res.Partial)
	assert.Greater(t, res.DurationMs, int64(0))
	var rl *RateLimitError
	require.True(t, errors.As(err, &rl))
	assert.False(t, rl.ResetAt.IsZero())
}

func TestCursorRateLimitKeepsPartialOutput(t *testing.T) {
	fakeBin(t, "cursor-agent", `#!/bin/sh
cat <<'EOF2'
{"type":"assistant","message":{"content":[{"type":"text","text":"Refactored the retry loop."}]}}
{"type":"result","is_error":true,"result":"usage limit reached, resets 1:49pm"}
EOF2
`)
	res, err := runCursorStream("", "task", 30*time.Second, 0, nil, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimited))
	assert.Contains(t, res.Text, "Refactored")
	assert.True(t, res.Partial)
}

func TestCodexCLIRateLimitKeepsPartialOutput(t *testing.T) {
	fakeBin(t, "codex", `#!/bin/sh
cat <<'EOF2'
{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"Wrote the migration."}}
{"type":"turn.failed","error":{"message":"You've hit your usage limit. Try again at 14:00"}}
EOF2
exit 1
`)
	res, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, nil, "", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimited))
	assert.Contains(t, res.Text, "Wrote the migration")
	assert.True(t, res.Partial)
	var rl *RateLimitError
	require.True(t, errors.As(err, &rl))
	assert.Equal(t, 14, rl.ResetAt.Hour())
}

// A rate-limit hit after real work is a partial success; one hit at the
// first token is a dead leg. The threshold decides reroute vs salvage.
func TestWorthKeeping(t *testing.T) {
	rl := rateLimitedAt(LegClaude, "session limit", time.Now())
	report := strings.Repeat("The parser now handles escapes; tests pass. ", 40) // a real partial deliverable
	assert.True(t, WorthKeeping(Result{Text: report, DurationMs: 120_000}, rl))
	assert.False(t, WorthKeeping(Result{Text: "I'll start by", DurationMs: 3_000}, rl), "seconds in: reroute instead")
	assert.False(t, WorthKeeping(Result{Text: "Build passed in 8 s. Running the clippy gates now.", DurationMs: 1_000_000}, rl),
		"seventeen minutes of narration is not a deliverable: reroute (2026-09-17)")
	assert.False(t, WorthKeeping(Result{Text: "   ", DurationMs: 900_000}, rl), "no text → nothing to keep")
	assert.True(t, WorthKeeping(Result{Text: "x", DurationMs: 900_000}, ErrWorkerTimeout), "timeouts already carry their text")
	assert.False(t, WorthKeeping(Result{Text: "x", DurationMs: 900_000}, ErrProviderDown), "an outage keeps nothing: the text is whatever came before the crash")
}

// claude -p's `result` is only the LAST assistant message. A session that
// spawns subagents ends on a one-line reaction to their completion
// notifications, so an 11-minute Arc cohort delivered 293 chars while its
// 5k-char report sat one message earlier (2026-09-10).
func TestClaudeRunKeepsTheSubstantiveReportOverATrailer(t *testing.T) {
	report := strings.Repeat("All four packages are committed on main; suite green; D01 unfrozen. ", 20)
	fakeBin(t, "claude", `#!/bin/sh
cat <<'EOF2'
{"type":"assistant","message":{"content":[{"type":"text","text":"`+report+`"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git log -1"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"That notification is the completion signal for the run I already reported. Nothing changes."}]}}
{"type":"result","is_error":false,"result":"That notification is the completion signal for the run I already reported. Nothing changes."}
EOF2
`)
	res, err := runClaudeStreamOpts("", "cohort", 30*time.Second, 0, nil, nil, false, "", nil)
	require.NoError(t, err)
	assert.Contains(t, res.Text, "All four packages are committed", "the report is the deliverable")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(res.Text), "Nothing changes."), "the trailer is kept after it")

	// A genuinely short final answer stays as-is.
	assert.Equal(t, "ok", preferSubstantiveReport("ok", "ok"))
	assert.Equal(t, "final answer here", preferSubstantiveReport("final answer here", "a slightly longer intermediate note"))
	long := strings.Repeat("x", 900)
	assert.Equal(t, long, preferSubstantiveReport(long, strings.Repeat("y", 5000)), "a long final message is the report")
}

// "You've reached your Fable limit" closes one model tier, not the account:
// the CLI's other models still answer.
func TestModelTierLimitNamesTheTierOnly(t *testing.T) {
	assert.Equal(t, "Fable", ModelTierLimit("You've reached your Fable limit. Switch to another model, or manage usage credits at claude.ai/settings/usage"))
	assert.Equal(t, "", ModelTierLimit("You've reached your usage limit. Try again at 2:50am"))
	assert.Equal(t, "", ModelTierLimit("You have hit your session limit"))
	assert.Equal(t, "", ModelTierLimit("rate limit exceeded"))
	err := rateLimited(LegClaude, "You've reached your Fable limit. Switch to another model")
	var rl *RateLimitError
	require.ErrorAs(t, err, &rl)
	assert.Equal(t, "Fable", rl.Tier)
	assert.ErrorIs(t, err, ErrRateLimited)
}

// The CLI's monthly spend limit is the extra-usage cap on the frontier model,
// not an account window: opus, sonnet and haiku kept answering while fable
// was refused (2026-09-17). A frontier run that hits it must not bench the
// claude leg for 30 minutes - claude at standard settings is the fallback.
func TestSpendLimitOnAFrontierRunIsTheFrontiersToWear(t *testing.T) {
	msg := "You've hit your monthly spend limit. Switch to another model, or manage usage credits at claude.ai/settings/usage?from=cc_cli_limit_message, to continue."
	var rl *RateLimitError
	require.True(t, errors.As(rateLimitedFrontier(msg), &rl))
	assert.Equal(t, "frontier", rl.Tier)
	require.True(t, errors.As(rateLimited(LegClaude, msg), &rl))
	assert.Equal(t, "", rl.Tier, "the same message on an ordinary claude run is claude's")
	require.True(t, errors.As(rateLimitedFrontier("You've hit your usage limit · resets 5pm"), &rl))
	assert.Equal(t, "", rl.Tier, "an account window closes every model: claude wears it")
}
