package captaincode

// The grok-cli leg: xAI's own Grok Build CLI on this host, driven headless
// (`grok -p … --output-format streaming-json`). It is not the grok or
// grok-max leg, which reach xAI through opencode on the SuperGrok OAuth
// that opencode stores; the CLI keeps its own login (~/.grok/auth.json, or
// XAI_API_KEY), so one working proves nothing about the other.
//
// Event shapes are Grok Build 1.0.40's documented headless format
// (~/.grok/docs/user-guide/14-headless-mode.md):
//
//	{"type":"text","data":"Here's a summary"}
//	{"type":"thought","data":"Analyzing…"}
//	{"type":"tool_call","toolCallId":"call_1","title":"Read","kind":"read","status":"in_progress","toolName":"read_file","rawInput":{"path":"x"}}
//	{"type":"available_commands","tools":[…]}   ← the tool list, ignored
//
// Confirmed live on 1.0.40 (2026-10-14): text arrives as small deltas, the
// shell tool is run_terminal_command, and end carries usage and cost on the
// grok.com login too.
//	{"type":"tool_call_update","toolCallId":"call_1","status":"completed","rawOutput":{…}}
//	{"type":"end","stopReason":"end_turn","sessionId":"…","usage":{…},"total_cost_usd":0.01}
//	{"type":"error","message":"Not signed in. …"}

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// grokCLIModel is the model grok-cli pins: CAPTAIN_GROK_CLI_MODEL, else the
// registry's (the CLI's own default when the leg was added).
func grokCLIModel() string {
	if m := strings.TrimSpace(os.Getenv(specs[LegGrokCLI].EnvPrefix() + "_MODEL")); m != "" {
		return m
	}
	return specs[LegGrokCLI].Model
}

// grokBin is the grok binary: from PATH, else where its installer puts it
// (~/.grok/bin), which a LaunchAgent's PATH usually leaves out.
func grokBin() string {
	if p, err := exec.LookPath("grok"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(home, ".grok", "bin", "grok"); fileExists(p) {
			return p
		}
	}
	return "grok"
}

// grokCLICmdArgs builds the `grok` argument list for one task. The model is
// passed explicitly so a run is attributed to the version it ran.
// --reasoning-effort only when CAPTAIN_GROK_CLI_EFFORT names one: the CLI
// documents no value list, and a value it refuses would fail every run.
// --always-approve for the same reason claude, codex and cursor skip their
// prompts (a headless worker cannot answer one); a sent turn gets dontAsk
// instead, so whatever needs approval is refused (sentpolicy.go).
func grokCLICmdArgs(dir, task string) []string {
	args := []string{"-p", task, "--output-format", "streaming-json", "-m", grokCLIModel()}
	if dir != "" {
		args = append(args, "--cwd", dir)
	}
	if e := strings.TrimSpace(os.Getenv("CAPTAIN_GROK_CLI_EFFORT")); e != "" {
		args = append(args, "--reasoning-effort", e)
	}
	if IsSentTurn(task) {
		return append(args, "--permission-mode", "dontAsk")
	}
	return append(args, "--always-approve")
}

// grokEvent is one NDJSON line of streaming-json.
type grokEvent struct {
	Type       string          `json:"type"`
	Data       string          `json:"data"`
	Message    string          `json:"message"`
	Status     string          `json:"status"`
	Title      string          `json:"title"`
	ToolName   string          `json:"toolName"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	StopReason string          `json:"stopReason"`
	Usage      struct {
		Input      *int `json:"input_tokens"` // uncached only
		Output     *int `json:"output_tokens"`
		CacheRead  *int `json:"cache_read_input_tokens"`
		CacheWrite *int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// grokToolStatus is the progress line for a started tool call.
func grokToolStatus(ev grokEvent) string {
	tool := ev.Title
	if tool == "" {
		tool = ev.ToolName
	}
	detail := pickDetail(ev.RawInput, "command", "path", "file_path", "pattern", "query", "url")
	// Grok Build 1.0.40 names its shell tool run_terminal_command.
	if n := strings.ToLower(ev.ToolName); strings.Contains(n, "terminal") || strings.Contains(n, "bash") || strings.Contains(n, "shell") {
		return workerStatus("shell", unwrapShell(detail))
	}
	return workerStatus(strings.ToLower(tool), detail)
}

// grokCLIAuthError recognizes a dead Grok Build login.
func grokCLIAuthError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "not signed in") || strings.Contains(m, "grok login") ||
		strings.Contains(m, "unauthorized") || strings.Contains(m, "401") || strings.Contains(m, "xai_api_key")
}

// classifyGrokCLIFailure maps an error event or a failed exit to the leg
// error classes the reroute net understands.
func classifyGrokCLIFailure(msg string) error {
	switch {
	case codexCLIRateLimited(msg):
		return rateLimited(LegGrokCLI, msg)
	case grokCLIAuthError(msg):
		// Provider-down so the task reroutes; harnessFault() keeps a dead
		// login off the model's reliability stats.
		return fmt.Errorf("grok -p: %w: not signed in - run `grok login` (the Grok CLI keeps its own credential store) or set XAI_API_KEY: %s",
			ErrProviderDown, truncateStr(msg, 160))
	case transientProviderError(msg) || codexCLIOutage(msg):
		return fmt.Errorf("grok -p: %w: %s", ErrProviderDown, truncateStr(msg, 160))
	}
	return fmt.Errorf("grok -p error: %s", truncateStr(msg, 300))
}

// runGrokCLIStream runs one task through `grok -p` and folds its events into
// a Result: text streams to onDelta and forms the answer, started tool calls
// go to onStatus, `end` carries usage and cost, `error` is the failure. Time
// bounds follow the CLI legs' progress contract (progressCtx), as codex exec.
func runGrokCLIStream(dir, task string, base, ceil time.Duration, onDelta, onStatus func(string), steer *Steer) (result Result, runErr error) {
	start := time.Now()
	ctx := context.Background()
	prog := &progress{}
	var capped *atomic.Bool
	if base > 0 {
		var cancel context.CancelFunc
		ctx, cancel, prog, capped = progressCtx(base, ceil)
		defer cancel()
	}
	ctx, stopped, detachStop := interruptible(ctx, steer, LegGrokCLI) // /interrupt keeps what streamed
	defer detachStop()
	cmd := exec.CommandContext(ctx, grokBin(), grokCLICmdArgs(dir, task)...)
	cmd.Dir = dir
	cmd.Env = append(append(append(os.Environ(), "GROK_DISABLE_AUTOUPDATER=1"), GateWorkerEnv(LegGrokCLI, task, steer.Task())...), WorkerGuardEnv(task)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("grok pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Result{}, fmt.Errorf("grok -p: %w: the Grok CLI is not installed - `curl -fsSL https://x.ai/cli/install.sh | bash`", ErrProviderDown)
		}
		return Result{}, fmt.Errorf("grok start: %w", err)
	}
	prog.watch(cmd.Process.Pid, "grok-cli", onStatus)
	scanDone := make(chan struct{})
	defer close(scanDone)
	go func() { // keep the cap authoritative: a grandchild holding stdout must not outlive it
		select {
		case <-ctx.Done():
			stdout.Close()
		case <-scanDone:
		}
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var acc strings.Builder
	var detail *TokenUsage
	var firstOutput *int64
	tokens, cost := 0, 0.0
	defer func() {
		result.TokenUsage, result.FirstOutputMs = detail, firstOutput
		result.Tokens, result.CostUSD = tokens, cost
	}()
	failed, haveFailed := "", false
	afterTool := false // the next text is a new message: it starts on its own paragraph
	for sc.Scan() {
		prog.touch()
		var ev grokEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "text":
			if ev.Data == "" {
				continue
			}
			if firstOutput == nil {
				ms := time.Since(start).Milliseconds()
				firstOutput = &ms
			}
			chunk := ev.Data
			if afterTool && acc.Len() > 0 {
				chunk = "\n\n" + strings.TrimLeft(chunk, "\n")
			}
			afterTool = false
			acc.WriteString(chunk)
			if onDelta != nil {
				onDelta(chunk)
			}
		case "thought":
			if onStatus != nil {
				if s := Narration(ev.Data); s != "" {
					onStatus(s)
				}
			}
		case "tool_call":
			prog.toolStart()
			afterTool = true
			if onStatus != nil {
				if s := grokToolStatus(ev); s != "" {
					onStatus(s)
				}
			}
		case "tool_call_update":
			switch ev.Status {
			case "completed", "failed":
				prog.toolEnd()
				steer.RecordStep(Step{Tool: ev.ToolName, Output: string(ev.RawOutput), Failed: ev.Status == "failed"})
			}
		case "end":
			tokens = 0
			for _, n := range []*int{ev.Usage.Input, ev.Usage.Output, ev.Usage.CacheRead, ev.Usage.CacheWrite} {
				if n != nil && *n >= 0 {
					tokens += *n
				}
			}
			if ev.Usage.Input != nil || ev.Usage.Output != nil {
				detail = &TokenUsage{Input: ev.Usage.Input, Output: ev.Usage.Output, CacheRead: ev.Usage.CacheRead,
					CacheWrite: ev.Usage.CacheWrite, Source: "grok", Scope: "run"}
			}
			cost = ev.TotalCostUSD
			if ev.StopReason == "refusal" && acc.Len() == 0 {
				failed, haveFailed = "refusal", true
			}
		case "error":
			failed, haveFailed = ev.Message, true
		}
	}
	prog.snapshotKids()
	waitErr := waitBounded(cmd, 2*time.Second)
	reportLeftovers(prog, onStatus)
	if stopped.Load() {
		return Result{Text: strings.TrimSpace(acc.String()), Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
			fmt.Errorf("grok -p stopped after %s: %w", time.Since(start).Round(time.Second), ErrInterrupted)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (capped != nil && capped.Load()) {
		if partial := strings.TrimSpace(acc.String()); partial != "" {
			return Result{Text: partial, Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
				fmt.Errorf("grok -p did not finish within %s: %w", time.Since(start).Round(time.Second), ErrWorkerTimeout)
		}
		return Result{}, fmt.Errorf("grok -p did not finish within %s: %w", time.Since(start).Round(time.Second), ErrWorkerTimeout)
	}
	if haveFailed {
		err := classifyGrokCLIFailure(failed)
		if errors.Is(err, ErrRateLimited) {
			return partialResult(acc.String(), failed, start, onDelta), err
		}
		return Result{}, err
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return Result{}, classifyGrokCLIFailure(msg)
		}
		return Result{}, fmt.Errorf("grok -p: %w", waitErr)
	}
	return Result{Text: acc.String(), Tokens: tokens, TokenUsage: detail, CostUSD: cost, FirstOutputMs: firstOutput,
		DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil}, nil
}
