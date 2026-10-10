package captaincode

// The live check behind Toolchain's Tested versions: one small real run
// through each CLI leg's own runner, asserting what captain reads from the
// CLI - the streamed text, the model it reports, the usage and the time to
// first output. A CLI upgrade that changes its event format, its flags or its
// model aliases fails here before it fails a user's turn. It spends a few
// calls of the logins on this machine, so it runs only with
// CAPTAIN_LIVE_CLI=1:
//
//	CAPTAIN_LIVE_CLI=1 go test ./pkg/captaincode -run TestLiveCLILegs -v
//
// When it passes on a new version, raise that pin's Tested and Rollback.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveCLILegs(t *testing.T) {
	if os.Getenv("CAPTAIN_LIVE_CLI") != "1" {
		t.Skip("live: set CAPTAIN_LIVE_CLI=1 to run the CLI legs for real")
	}
	if home := os.Getenv("REAL_HOME"); home != "" {
		t.Setenv("HOME", home) // the CLIs' logins live in the real home
	}
	// Euclid's MCP server is this binary in production; under `go test` it is
	// the test binary, which serves nothing, and claude waited ~25 s on it
	// before answering. The CLI contract is what is under test here.
	t.Setenv("CAPTAIN_EUCLID", "0")
	const ask = "Reply with exactly one word: pong. Do not use any tools."
	cases := []struct {
		name   string
		leg    Leg
		effort Effort
		model  string // a substring of the model the CLI must report ("" when it reports none)
	}{
		{"claude/sonnet at low", LegClaude, EffortLow, "sonnet"},
		{"claude/opus at high", LegClaude, EffortHigh, "opus-5-5"},
		{"codex-cli/sol at low", LegCodexCLI, EffortLow, ""},
		{"codex-cli/astra at high", LegCodexCLI, EffortHigh, ""},
	}
	t.Run("claude/frontier at high", func(t *testing.T) {
		if _, err := exec.LookPath("claude"); err != nil {
			t.Skip("claude not installed")
		}
		res, err := Workspace{Dir: t.TempDir(), Effort: EffortHigh}.RunClaudeFrontierStream(ask, nil, nil)
		require.NoError(t, err)
		t.Logf("frontier: %q, model %q, ttft %v", strings.TrimSpace(res.Text), res.Model, deref(res.TTFTMs))
		assert.Contains(t, strings.ToLower(res.Text), "pong")
		assert.Contains(t, strings.ToLower(res.Model), "opus-5-5", "/frontier's pinned model")
	})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := exec.LookPath(toolFor(c.leg)); err != nil {
				t.Skipf("%s not installed", toolFor(c.leg))
			}
			ws := Workspace{Dir: t.TempDir(), Effort: c.effort}
			var streamed strings.Builder
			start := time.Now()
			res, err := ws.RunWorkerStreamHooks(c.leg, ask, 0, func(d string) { streamed.WriteString(d) }, nil)
			require.NoError(t, err)
			t.Logf("%s: %q in %s, model %q, tokens %d, ttft %v, first output %v, cost $%.4f",
				c.name, strings.TrimSpace(res.Text), time.Since(start).Round(time.Millisecond), res.Model, res.Tokens, deref(res.TTFTMs), deref(res.FirstOutputMs), res.CostUSD)
			assert.Contains(t, strings.ToLower(res.Text), "pong", "the answer is read from the CLI's events")
			assert.Contains(t, strings.ToLower(streamed.String()), "pong", "…and streamed as it arrives")
			assert.Positive(t, res.Tokens+tokensOf(res.TokenUsage), "usage is read")
			assert.True(t, res.TTFTMs != nil || res.FirstOutputMs != nil, "a time to first output is read")
			if c.model != "" {
				assert.Contains(t, strings.ToLower(res.Model), c.model, "the CLI resolves the alias to the expected model")
			}
		})
	}
}

func toolFor(l Leg) string {
	switch specs[l].Transport {
	case TransportClaudeCLI:
		return "claude"
	case TransportCodexCLI:
		return "codex"
	}
	return string(l)
}

func tokensOf(u *TokenUsage) int {
	if u == nil {
		return 0
	}
	n := 0
	for _, v := range []*int{u.Input, u.Output, u.CacheRead} {
		if v != nil {
			n += *v
		}
	}
	return n
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
