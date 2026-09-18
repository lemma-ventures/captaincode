package captaincode

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The codex-cli leg is the Codex CLI (`codex exec`) driving gpt-6-astra at xhigh
// reasoning - a SECOND frontier-class leg beside /frontier (claude). It is a
// real leg with its own scorecard, not a mode of the codex leg: the codex leg
// is opencode → gpt-5.3-codex-spark (a latency-optimized model on a different
// credential store), and blending the two would make both scorecards lie.

func TestCodexCLIIsARealLegAtTheTopOfTheLadder(t *testing.T) {
	assert.True(t, KnownLeg(LegCodexCLI))
	assert.Equal(t, LegClaude, AllLegs[len(AllLegs)-1], "claude stays the apex rung")
	assert.Equal(t, LegCodexCLI, AllLegs[len(AllLegs)-2], "codex-cli sits directly below claude")
	assert.Less(t, QualityPrior(LegCodexCLI), QualityPrior(LegClaude), "claude remains the quality apex")
	assert.Greater(t, QualityPrior(LegCodexCLI), QualityPrior(LegCursor), "frontier-class outranks the standard tier")
	assert.True(t, IsFrontierClass(LegCodexCLI))
	assert.True(t, IsFrontierClass(LegFrontier))
	assert.False(t, IsFrontierClass(LegClaude), "plain claude is a normal worker; /frontier is its max-effort mode")
	assert.False(t, IsFrontierClass(LegCodex))
}

func TestCodexCLICmdConfigDefaults(t *testing.T) {
	t.Setenv("CAPTAIN_CODEX_CLI_MODEL", "")
	t.Setenv("CAPTAIN_CODEX_CLI_EFFORT", "")
	t.Setenv("CAPTAIN_CODEX_CLI_SANDBOX", "")
	args := codexCLICmdArgs("/tmp/somewhere", "do the thing", "")
	joined := strings.Join(args, " ")
	assert.Equal(t, "exec", args[0], "headless codex is `codex exec`")
	assert.Contains(t, joined, "--json", "events are parsed from JSONL")
	assert.Contains(t, joined, "-m gpt-6-astra")
	assert.Contains(t, joined, "-c model_reasoning_effort=xhigh", "frontier-class effort, matching /frontier")
	assert.Contains(t, joined, "--skip-git-repo-check", "the workspace need not be a git repo")
	assert.Contains(t, joined, "-C /tmp/somewhere", "the worker runs in the request's workspace")
	assert.Contains(t, joined, "--dangerously-bypass-approvals-and-sandbox",
		"parity with claude --dangerously-skip-permissions: an approval prompt wedges a headless worker forever")
	assert.Equal(t, "do the thing", args[len(args)-1], "the prompt is the trailing positional")
}

func TestCodexCLICmdConfigOverrides(t *testing.T) {
	t.Setenv("CAPTAIN_CODEX_CLI_MODEL", "gpt-6-pro")
	t.Setenv("CAPTAIN_CODEX_CLI_EFFORT", "high")
	t.Setenv("CAPTAIN_CODEX_CLI_SANDBOX", "workspace-write")
	joined := strings.Join(codexCLICmdArgs("", "x", ""), " ")
	assert.Contains(t, joined, "-m gpt-6-pro")
	assert.Contains(t, joined, "-c model_reasoning_effort=high")
	assert.Contains(t, joined, "--sandbox workspace-write")
	assert.Contains(t, joined, "-c approval_policy=never", "a sandboxed run must still never ask")
	assert.NotContains(t, joined, "--dangerously-bypass-approvals-and-sandbox")
	assert.NotContains(t, joined, "-C ", "no workspace → inherit the brain's cwd")
}

// Event shapes captured live from `codex exec --json` (codex-cli 0.153.4,
// 2026-09-09): agent_message items carry the answer, command_execution items
// are the visible activity, turn.completed carries usage.
const codexCLIProbeEvents = `#!/bin/sh
cat <<'EOF'
{"type":"thread.started","thread_id":"01a087d8-4625-7ed3-9a0d-4aca2e5d76f5"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I’ll read note.txt.\n"}}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'cat note.txt'","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'cat note.txt'","aggregated_output":"hello\n","exit_code":0,"status":"completed"}}
{"type":"item.started","item":{"id":"item_2","type":"file_change","changes":[{"path":"pkg/x.go","kind":"update"}],"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"hello"}}
{"type":"turn.completed","usage":{"input_tokens":33968,"cached_input_tokens":28288,"cache_write_input_tokens":0,"output_tokens":54,"reasoning_output_tokens":0}}
EOF
`

func TestCodexCLIStreamParsesCodexEvents(t *testing.T) {
	fakeBin(t, "codex", codexCLIProbeEvents)
	var deltas []string
	var statuses []string
	res, err := runCodexCLIStream("", "task", 30*time.Second, 0,
		func(d string) { deltas = append(deltas, d) },
		func(s string) { statuses = append(statuses, s) }, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "I’ll read note.txt.\n\nhello", res.Text, "every agent message, in order, blank-line separated")
	assert.Equal(t, []string{"I’ll read note.txt.\n", "\nhello"}, deltas, "messages stream as they land")
	assert.Equal(t, []string{"⚙ shell cat note.txt", "⚙ edit pkg/x.go"}, statuses,
		"only the started edge, with the zsh -lc wrapper stripped; completions would double every line")
	assert.Equal(t, 33968+54, res.Tokens, "input + output (cached is a subset of input)")
	assert.Greater(t, res.DurationMs, int64(0), "a zero duration exempts the leg from grading (cursor 2026-07-25)")
	assert.True(t, res.Streamed)
}

func TestCodexCLIStreamBufferedModeStillReturnsText(t *testing.T) {
	fakeBin(t, "codex", codexCLIProbeEvents)
	res, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, nil, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "I’ll read note.txt.\n\nhello", res.Text)
	assert.False(t, res.Streamed)
}

// Error classification: only turn.failed is authoritative. The "error" events
// codex emits while reconnecting are noise on a run that then succeeds.
func TestCodexCLIStreamClassifiesTurnFailures(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    error
	}{
		{"rate limit", "You've hit your usage limit. Try again at 14:00.", ErrRateLimited},
		{"429", "unexpected status 429 Too Many Requests", ErrRateLimited},
		{"auth", "unexpected status 401 Unauthorized: Missing bearer or basic authentication in header", ErrProviderDown},
		{"transient", "stream disconnected before completion", ErrProviderDown},
		{"outage", "unexpected status 503 Service Unavailable", ErrProviderDown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeBin(t, "codex", `#!/bin/sh
cat <<'EOF'
{"type":"thread.started","thread_id":"t"}
{"type":"turn.started"}
{"type":"error","message":"Reconnecting... 1/5 (`+c.message+`)"}
{"type":"turn.failed","error":{"message":"`+c.message+`"}}
EOF
exit 1
`)
			_, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, nil, "", nil)
			require.Error(t, err)
			assert.True(t, errors.Is(err, c.want), "got %v, want %v", err, c.want)
		})
	}
}

func TestCodexCLIStreamAuthFailureNamesTheFix(t *testing.T) {
	fakeBin(t, "codex", `#!/bin/sh
cat <<'EOF'
{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized: Missing bearer or basic authentication in header"}}
EOF
exit 1
`)
	_, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, nil, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex login", "the operator must learn WHICH credential store is dead (codex ≠ opencode)")
	assert.True(t, harnessFault(err.Error()), "a dead login is our fault, not the provider's - keep it off the reliability stats")
}

func TestCodexCLIStreamReconnectNoiseIsNotAnError(t *testing.T) {
	fakeBin(t, "codex", `#!/bin/sh
cat <<'EOF'
{"type":"turn.started"}
{"type":"error","message":"Reconnecting... 2/5 (unexpected status 401 Unauthorized)"}
{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Falling back from WebSockets to HTTPS transport."}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done"}}
{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":1}}
EOF
`)
	res, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, nil, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "done", res.Text)
}

func TestCodexCLIStreamGenericFailureIsNotReroutable(t *testing.T) {
	fakeBin(t, "codex", `#!/bin/sh
cat <<'EOF'
{"type":"turn.failed","error":{"message":"unknown model gpt-7"}}
EOF
exit 1
`)
	_, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, nil, "", nil)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrProviderDown))
	assert.False(t, errors.Is(err, ErrRateLimited))
	assert.Contains(t, err.Error(), "unknown model gpt-7")
}

func TestCodexCLIStreamStderrOnExitClassifies(t *testing.T) {
	fakeBin(t, "codex", "#!/bin/sh\necho 'error: connection reset by peer' >&2\nexit 1\n")
	_, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, nil, "", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrProviderDown))
}

func TestCodexCLITimeoutKeepsWhatItProduced(t *testing.T) {
	// A run is cut only when QUIET past its base cap (the progress contract):
	// the idle window is what ends a silent one.
	t.Setenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT", "200ms")
	fakeBin(t, "codex", `#!/bin/sh
cat <<'EOF'
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Finding 1: the threat model omits X."}}
EOF
sleep 30
`)
	start := time.Now()
	res, err := runCodexCLIStream("", "audit", 400*time.Millisecond, 0, nil, nil, "", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerTimeout))
	assert.True(t, res.Partial)
	assert.Contains(t, res.Text, "Finding 1")
	assert.Less(t, time.Since(start), 20*time.Second, "the cap still bounds the run")
}

func TestCodexCLITimeoutWithNoOutputStaysAnError(t *testing.T) {
	// A run is cut only when QUIET past its base cap (the progress contract):
	// the idle window is what ends a silent one.
	t.Setenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT", "200ms")
	fakeBin(t, "codex", "#!/bin/sh\nsleep 30\n")
	res, err := runCodexCLIStream("", "audit", 300*time.Millisecond, 0, nil, nil, "", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerTimeout))
	assert.Empty(t, strings.TrimSpace(res.Text))
	assert.False(t, res.Partial)
}

// Through the one choke point every path crosses: codex-cli runs with the
// frontier time budget (2×) and an empty answer is a failure like any leg.
func TestRunWorkerStreamHooksDispatchesCodexCLI(t *testing.T) {
	fakeBin(t, "codex", codexCLIProbeEvents)
	res, err := RunWorkerStreamHooks(LegCodexCLI, "task", 0, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, res.Text, "hello")

	fakeBin(t, "codex", "#!/bin/sh\necho '{\"type\":\"turn.completed\",\"usage\":{}}'\n")
	_, err = RunWorkerStreamHooks(LegCodexCLI, "task", 0, nil, nil)
	assert.True(t, errors.Is(err, ErrEmptyOutput))
}

func TestFrontierClassLegsGetDoubleTheBudget(t *testing.T) {
	assert.Equal(t, time.Duration(2), legBudgetMultiplier(LegCodexCLI))
	assert.Equal(t, time.Duration(1), legBudgetMultiplier(LegClaude))
	assert.Equal(t, time.Duration(1), legBudgetMultiplier(LegGrok))
}

func TestCodexCLICodexToolStatus(t *testing.T) {
	assert.Equal(t, "⚙ shell go test ./...", codexItemStatus("command_execution", []byte(`{"command":"/bin/zsh -lc 'go test ./...'"}`)))
	assert.Equal(t, "⚙ shell ls", codexItemStatus("command_execution", []byte(`{"command":"ls"}`)))
	assert.Equal(t, "⚙ edit a.go, b.go", codexItemStatus("file_change", []byte(`{"changes":[{"path":"a.go"},{"path":"b.go"}]}`)))
	assert.Equal(t, "⚙ mcp stripe.list_customers", codexItemStatus("mcp_tool_call", []byte(`{"server":"stripe","tool":"list_customers"}`)))
	assert.Equal(t, "⚙ search rust async runtimes", codexItemStatus("web_search", []byte(`{"query":"rust async runtimes"}`)))
	assert.Equal(t, "", codexItemStatus("agent_message", []byte(`{"text":"hi"}`)), "messages are the answer, not activity")
	assert.Equal(t, "", codexItemStatus("reasoning", []byte(`{"text":"thinking"}`)))
}

// Routing rules: frontier-class legs are never AUTO-assigned by the cheap
// paths. They enter via the director's menu, /quality, a forced prefix, a
// named assignment or a workflow - and earn their place through scorecards.
func TestFastLadderNeverContainsFrontierClass(t *testing.T) {
	SetDirector(LegClaude)
	t.Cleanup(func() { SetDirector(LegGrok) })
	for _, d := range []Domain{DomainEditorial, DomainCode, DomainResearch, DomainGeneral} {
		for _, c := range []Class{ClassTrivial, ClassMedium} {
			for _, l := range FastLadder(c, d) {
				assert.False(t, IsFrontierClass(l), "%s/%s ladder auto-assigns %s", c, d, l)
			}
		}
	}
}

func TestCodexCLISupportsVision(t *testing.T) {
	t.Setenv("CAPTAIN_VISION_LEGS", "")
	reloadVisionLegs()
	assert.True(t, LegSupportsVision(LegCodexCLI), "gpt-6-astra is multimodal; codex reads images with its own tools")
}

func TestCodexCLIIsADistinctiveNamedLeg(t *testing.T) {
	assert.Equal(t, []Leg{LegCodexCLI, LegGrok}, NamedAssignees("have codex-cli and grok review this design"))
	assert.Equal(t, []Leg{LegCodex, LegCodexCLI}, NamedAssignees("have codex and codex-cli both review it"), "the hyphenated name is not mistaken for its prefix")
	assert.Equal(t, "codex-cli", modelWordFor("/codex-cli think hard"))
}

func TestCodexCLIParsesInWorkflows(t *testing.T) {
	wf, err := ParseWorkflow("/codex-cli design the migration > /claude review it")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 2)
	assert.Equal(t, LegCodexCLI, wf.Stages[0].Legs[0].Leg)

	// Estimate: frontier-class doubles the stage budget, as /frontier does.
	stats := map[Leg]LegStats{LegCodexCLI: {N: 5, Scored: 5, AvgDurationMs: 60_000}, LegGrok: {N: 5, Scored: 5, AvgDurationMs: 60_000}}
	loA, hiA := EstimateWorkflow(mustParse(t, "/codex-cli x"), stats, time.Second)
	loG, hiG := EstimateWorkflow(mustParse(t, "/grok x"), stats, time.Second)
	assert.Equal(t, 2*(loG-time.Second), loA-time.Second)
	assert.Equal(t, 2*(hiG-time.Second), hiA-time.Second)
}

// modelWordFor: the leg a forced-prefix parser reads off a turn (the fork's
// and the brain's regexes must both prefer codex-cli over codex).
func modelWordFor(raw string) string {
	wf, err := ParseWorkflow(raw)
	if err != nil || len(wf.Stages) == 0 || len(wf.Stages[0].Legs) == 0 {
		return ""
	}
	return string(wf.Stages[0].Legs[0].Leg)
}

func mustParse(t *testing.T, expr string) Workflow {
	t.Helper()
	wf, err := ParseWorkflow(expr)
	require.NoError(t, err)
	return wf
}

func TestCodexCLIDirectorBriefingIsHonestAboutCost(t *testing.T) {
	d := strings.ToLower(legDescription(LegCodexCLI))
	assert.Contains(t, d, "gpt-6-astra")
	assert.Contains(t, d, "slow", "the director must know it pays minutes, not seconds")
	assert.Contains(t, d, "chatgpt", "and whose quota it burns")
}

func TestCodexCLIPriorsSyncPattern(t *testing.T) {
	models := []AAModel{{Slug: "gpt-6-astra", Name: "GPT-6 Astra", CodingIndex: 70}}
	m, ok := MatchAA(models, LegCodexCLI)
	require.True(t, ok)
	assert.Equal(t, "gpt-6-astra", m.Slug)
}
