package captaincode

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The TUI showed NOTHING while a worker ran (live 2026-07-29): the wrapper only
// forwards answer text, and a worker that thinks or runs tools for minutes
// emits none. The status channel carries "what is it doing" out-of-band, so
// these lines must never leak into the answer stream.

func TestWorkerStatusLine(t *testing.T) {
	cases := []struct{ name, tool, detail, want string }{
		{"tool only", "Bash", "", "⚙ Bash"},
		{"tool and target", "Read", "pkg/captaincode/legs.go", "⚙ Read pkg/captaincode/legs.go"},
		{"collapses whitespace", "Bash", "go test ./...\n\n   -run X", "⚙ Bash go test ./... -run X"},
		{"detail only", "", "planning", "⚙ planning"},
		{"nothing to say", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, workerStatus(c.tool, c.detail))
		})
	}
	long := workerStatus("Read", strings.Repeat("x", 400))
	assert.LessOrEqual(t, len(long), statusMaxDetail+16, "status lines stay one line")
	assert.True(t, strings.HasSuffix(long, "…"), "truncation is visible")
}

func TestClaudeToolDetail(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"file tools", `{"file_path":"pkg/x.go","limit":10}`, "pkg/x.go"},
		{"bash prefers command", `{"command":"go test ./...","description":"run tests"}`, "go test ./..."},
		{"grep pattern", `{"pattern":"func Run","path":"pkg"}`, "func Run"},
		{"web fetch url", `{"url":"https://example.com","prompt":"summarize"}`, "https://example.com"},
		{"falls back to description", `{"description":"explore the repo"}`, "explore the repo"},
		{"unknown shape", `{"weird":1}`, ""},
		{"not an object", `"nope"`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, claudeToolDetail([]byte(c.input)))
		})
	}
	assert.Equal(t, "", claudeToolDetail(nil))
}

// cursor-agent's stream-json wraps every call in a per-tool key
// ("shellToolCall", "readToolCall", …) whose args carry the human-readable
// description - captured live 2026-07-29 from `cursor-agent -p --output-format
// stream-json`.
func TestCursorToolStatus(t *testing.T) {
	shell := `{"shellToolCall":{"args":{"command":"echo ok","description":"Print ok to verify shell"}},"description":"Print ok"}`
	assert.Equal(t, "⚙ shell Print ok to verify shell", cursorToolStatus([]byte(shell)))

	read := `{"readToolCall":{"args":{"path":"pkg/captaincode/legs.go"}}}`
	assert.Equal(t, "⚙ read pkg/captaincode/legs.go", cursorToolStatus([]byte(read)))

	assert.Equal(t, "", cursorToolStatus([]byte(`{}`)))
	assert.Equal(t, "", cursorToolStatus(nil))
}

// fakeBin installs an executable `name` on PATH for the duration of the test.
func fakeBin(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestClaudeStreamReportsToolActivity(t *testing.T) {
	fakeBin(t, "claude", `#!/bin/sh
cat <<'EOF'
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"pkg/x.go"}}]}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"result","is_error":false,"result":"hello"}
EOF
`)
	var statuses []string
	var answer strings.Builder
	res, err := runClaudeStreamOpts("", "task", 30*time.Second, 0,
		func(d string) { answer.WriteString(d) },
		func(s string) { statuses = append(statuses, s) }, false, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "hello", res.Text)
	assert.Equal(t, []string{"⚙ Read pkg/x.go", "⚙ Bash go test ./..."}, statuses)
	assert.Equal(t, "hello", answer.String(), "tool activity must stay off the answer stream")
}

func TestCursorStreamReportsToolActivity(t *testing.T) {
	fakeBin(t, "cursor-agent", `#!/bin/sh
cat <<'EOF'
{"type":"tool_call","subtype":"started","tool_call":{"shellToolCall":{"args":{"command":"echo ok","description":"Print ok"}}}}
{"type":"tool_call","subtype":"completed","tool_call":{"shellToolCall":{"result":{}}}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"ok"}
EOF
`)
	var statuses []string
	var answer strings.Builder
	res, err := runCursorStream("", "task", 30*time.Second, 0,
		func(d string) { answer.WriteString(d) },
		func(s string) { statuses = append(statuses, s) }, nil, "")
	require.NoError(t, err)
	assert.Equal(t, "ok", res.Text)
	assert.Equal(t, []string{"⚙ shell Print ok"}, statuses, "only tool STARTS are reported, once each")
	assert.Equal(t, "ok", answer.String())
}

// "grok wrapper done in 1m30s (0 chars)" - a run that succeeds with no text is
// not an answer; the user waited 90 seconds for silence (live 2026-07-31).
func TestEmptyAnswerIsAFailure(t *testing.T) {
	fakeBin(t, "claude", "#!/bin/sh\ncat <<'EOF2'\n{\"type\":\"result\",\"is_error\":false,\"result\":\"   \"}\nEOF2\n")
	_, err := RunWorkerStreamHooks(LegClaude, "task", 0, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEmptyOutput), "an empty answer must be reroutable: %v", err)
}

func TestNonEmptyAnswerIsNotAFailure(t *testing.T) {
	fakeBin(t, "claude", "#!/bin/sh\ncat <<'EOF2'\n{\"type\":\"result\",\"is_error\":false,\"result\":\"a real answer\"}\nEOF2\n")
	res, err := RunWorkerStreamHooks(LegClaude, "task", 0, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "a real answer", res.Text)
}

// "RetriableError: Connection stalled repeatedly" - cursor's backend died and
// the error surfaced as a generic exit status 1: not reroutable, no bench, no
// answer after 7m10s (live 2026-08-06). Transient connection failures from the
// CLI legs must classify as provider-down so the reroute machinery engages.
func TestCursorTransientConnectionErrorIsProviderDown(t *testing.T) {
	fakeBin(t, "cursor-agent", "#!/bin/sh\necho 'RetriableError: Connection stalled repeatedly' >&2\nexit 1\n")
	_, err := RunWorkerStreamHooks(LegCursor, "task", 0, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrProviderDown), "a stalled connection is the provider's fault: %v", err)
}

func TestClaudeTransientConnectionErrorIsProviderDown(t *testing.T) {
	fakeBin(t, "claude", "#!/bin/sh\ncat <<'EOF2'\n{\"type\":\"result\",\"is_error\":true,\"result\":\"API Error: Connection error.\"}\nEOF2\n")
	_, err := RunWorkerStreamHooks(LegClaude, "task", 0, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrProviderDown), "%v", err)
}

// A real task failure stays generic - rerouting can't fix a broken request.
func TestGenuineCLIErrorStaysGeneric(t *testing.T) {
	fakeBin(t, "cursor-agent", "#!/bin/sh\necho 'invalid flag --output-format' >&2\nexit 1\n")
	_, err := RunWorkerStreamHooks(LegCursor, "task", 0, nil, nil)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrProviderDown))
	assert.False(t, errors.Is(err, ErrWorkerStalled))
}

// A status feed of "⚙ Bash …" and "still working (1h20m)" says the worker is
// alive, not what it does (2026-09-13). The worker's own words and what its
// commands came back with are the readable part.
func TestNarrationAndOutcomeLines(t *testing.T) {
	assert.Equal(t, "💬 Bounded recursion advanced one concrete step; the milestone itself remains open.",
		Narration("Bounded recursion advanced one concrete step; the milestone itself remains open.\n\n**What was done** …"))
	assert.Equal(t, "💬 The decider now uses an exact NTT product, with coefficient checks against schoolbook multiplication in both extension fields.",
		Narration("## Status\n\nThe decider now uses an exact NTT product, with coefficient checks against schoolbook multiplication in both extension fields. Next I add path checks."),
		"first sentence of the first prose line, headings skipped")
	assert.Equal(t, "", Narration("ok"), "a fragment is not narration")
	assert.Equal(t, "", Narration("```rust\nfn x() {}\n```"), "code is not narration")

	assert.Equal(t, "↳ test result: ok. 40 passed; 0 failed", Outcome("running 40 tests\n...\ntest result: ok. 40 passed; 0 failed\n\nfinished in 3.2s", 0, false), "the verdict line, not the last line")
	assert.Equal(t, "↳ ✗ exit 101: error[E0425]: cannot find value `x`", Outcome("   Compiling arc\nerror[E0425]: cannot find value `x`\nwarning: unused", 101, true))
	assert.Equal(t, "", Outcome("", 0, false), "silent success stays silent")
	assert.Equal(t, "↳ ✗ exit 1", Outcome("", 1, true))
}
