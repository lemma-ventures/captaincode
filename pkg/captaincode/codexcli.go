package captaincode

// The codex-cli leg: OpenAI's frontier model (gpt-6-astra) at xhigh reasoning,
// driven headless through the Codex CLI (`codex exec --json`). It is the
// second frontier-class leg beside /frontier (claude at max effort), and a
// REAL leg with its own scorecard - not a mode of the codex leg, which runs
// gpt-5.3-codex-spark through opencode on a different credential store
// (~/.local/share/opencode/auth.json vs ~/.codex/auth.json). Conflating the
// two would make both scorecards lie about a model they never ran.
//
// Frontier-class semantics, shared with /frontier: double the worker time
// budget (deep reasoning is slow by design), never auto-assigned by the cheap
// routing tiers (it enters via the director's menu, /quality, a forced /codex-cli
// prefix, a named assignment or a workflow), and a reroute target only when
// nothing ordinary is open.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// IsFrontierClass reports whether a leg carries frontier semantics: the
// /frontier pseudo-leg (claude at max effort) and codex-cli. Plain claude is a
// normal worker leg - its max-effort mode is what /frontier names.
func IsFrontierClass(l Leg) bool { return IsFrontier(l) || specs[l].Frontier }

// legBudgetMultiplier scales the worker time budget: frontier-class legs get
// 2× (the same allowance RunClaudeFrontierStream gives /frontier).
func legBudgetMultiplier(l Leg) time.Duration {
	if IsFrontierClass(l) {
		return 2
	}
	return 1
}

// codexCLICmdArgs builds the `codex exec` argument list for one task.
//
//   - CAPTAIN_CODEX_CLI_MODEL (default gpt-6-astra) - the frontier model id.
//   - CAPTAIN_CODEX_CLI_EFFORT (default xhigh) - model_reasoning_effort; xhigh is
//     the same deliberate second-to-best choice /frontier makes for claude.
//   - CAPTAIN_CODEX_CLI_SANDBOX - unset/"skip": bypass approvals AND the sandbox
//     (parity with claude --dangerously-skip-permissions and cursor --trust:
//     the repo is the blast radius, and an approval prompt wedges a headless
//     worker forever). Any other value is passed as `--sandbox <v>`
//     (read-only | workspace-write | danger-full-access) with approvals set
//     to never, so a sandboxed run still cannot stop to ask.
//
// --skip-git-repo-check because the workspace need not be a git repo, and -C
// pins the worker to the request's workspace (cmd.Dir does too; the
// flag is what codex reports as its working root).
func codexCLICmdArgs(dir, task string, effort Effort) []string {
	model := strings.TrimSpace(os.Getenv("CAPTAIN_CODEX_CLI_MODEL"))
	if model == "" {
		model = "gpt-6-astra"
	}
	// The request's effort (effort.go), xhigh when none was decided - the
	// leg is frontier-class and used to run at xhigh always; a pinned
	// CAPTAIN_CODEX_CLI_EFFORT still wins.
	flag := strings.TrimSpace(os.Getenv("CAPTAIN_CODEX_CLI_EFFORT"))
	if flag == "" {
		flag = "xhigh"
		if effort != "" {
			flag = effort.CodexFlag()
		}
	}
	args := []string{"exec", "--json", "--skip-git-repo-check",
		"-m", model, "-c", "model_reasoning_effort=" + flag}
	if dir != "" {
		args = append(args, "-C", dir)
	}
	args = append(args, CodexMCPArgs(dir)...) // Euclid memory tools, same as the opencode workers get
	args = append(args, CodexProxyArgs()...)  // the model turn crosses the egress proxy while it is up
	switch sb := strings.TrimSpace(os.Getenv("CAPTAIN_CODEX_CLI_SANDBOX")); sb {
	case "", "skip":
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	default:
		args = append(args, "--sandbox", sb, "-c", "approval_policy=never")
	}
	return append(args, task)
}

// codexEvent is one JSONL line from `codex exec --json` (codex-cli 0.153.4).
// Shapes captured live 2026-09-09:
//
//	{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"…"}}
//	{"type":"item.started","item":{"type":"command_execution","command":"/bin/zsh -lc 'cat x'","status":"in_progress"}}
//	{"type":"turn.completed","usage":{"input_tokens":33968,"cached_input_tokens":28288,"output_tokens":54}}
//	{"type":"turn.failed","error":{"message":"…"}}
//	{"type":"error","message":"Reconnecting... 2/5 (…)"}   ← transport noise, not a verdict
type codexEvent struct {
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Item    json.RawMessage `json:"item"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Text             string `json:"text"`
	Status           string `json:"status"`            // command_execution: completed | failed
	ExitCode         *int   `json:"exit_code"`         // command_execution
	AggregatedOutput string `json:"aggregated_output"` // command_execution
}

// shellWrapperRe strips codex's `/bin/zsh -lc '…'` / `bash -c "…"` wrapper so
// the status line shows the command the model meant, not the shell.
var shellWrapperRe = regexp.MustCompile(`^(?:/\S*/)?(?:zsh|bash|sh)\s+-l?c\s+(?:'(.*)'|"(.*)"|(\S.*))$`)

func unwrapShell(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if m := shellWrapperRe.FindStringSubmatch(cmd); m != nil {
		for _, g := range m[1:] {
			if g != "" {
				return g
			}
		}
	}
	return cmd
}

// codexItemIsTool says whether a codex item is an action the run waits on
// (as opposed to a message or reasoning, which are the answer itself).
func codexItemIsTool(itemType string) bool {
	switch itemType {
	case "command_execution", "file_change", "mcp_tool_call", "web_search":
		return true
	}
	return false
}

// codexItemStatus renders one progress line for a started codex item. Only
// activity items produce a line; messages and reasoning are the answer (or
// its thinking) and stay off the status channel.
func codexItemStatus(itemType string, raw json.RawMessage) string {
	switch itemType {
	case "command_execution":
		return workerStatus("shell", unwrapShell(pickDetail(raw, "command")))
	case "file_change":
		var fc struct {
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		}
		_ = json.Unmarshal(raw, &fc)
		paths := make([]string, 0, len(fc.Changes))
		for _, c := range fc.Changes {
			if c.Path != "" {
				paths = append(paths, c.Path)
			}
		}
		return workerStatus("edit", strings.Join(paths, ", "))
	case "mcp_tool_call":
		server, tool := pickDetail(raw, "server"), pickDetail(raw, "tool")
		if server != "" && tool != "" {
			return workerStatus("mcp", server+"."+tool)
		}
		return workerStatus("mcp", server+tool)
	case "web_search":
		return workerStatus("search", pickDetail(raw, "query"))
	}
	return ""
}

// codexCLIAuthError recognizes a dead Codex login. The CLI keeps its own
// credential store (~/.codex/auth.json) - a working codex LEG (opencode's
// store) proves nothing about it, and vice versa.
func codexCLIAuthError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "401") || strings.Contains(m, "unauthorized") ||
		strings.Contains(m, "missing bearer") || strings.Contains(m, "not logged in") ||
		strings.Contains(m, "codex login")
}

func codexCLIRateLimited(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "usage limit") || strings.Contains(m, "rate limit") ||
		strings.Contains(m, "429") || strings.Contains(m, "quota")
}

// codexCLIOutage covers server-side unavailability beyond transientProviderError's
// connection-level patterns.
func codexCLIOutage(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "503") || strings.Contains(m, "502") ||
		strings.Contains(m, "unavailable") || strings.Contains(m, "overloaded") ||
		strings.Contains(m, "at capacity")
}

// classifyCodexCLIFailure maps a turn.failed / exit message to the leg error
// classes the reroute net understands.
func classifyCodexCLIFailure(msg string) error {
	switch {
	case codexCLIRateLimited(msg):
		return rateLimited(LegCodexCLI, msg)
	case codexCLIAuthError(msg):
		// Provider-down so the task reroutes and the user still gets an
		// answer; harnessFault() keeps it off the model's reliability stats.
		return fmt.Errorf("codex exec: %w: not logged in - run `codex login` (the Codex CLI keeps its own credential store): %s",
			ErrProviderDown, truncateStr(msg, 160))
	case transientProviderError(msg) || codexCLIOutage(msg):
		return fmt.Errorf("codex exec: %w: %s", ErrProviderDown, truncateStr(msg, 160))
	}
	return fmt.Errorf("codex exec error: %s", truncateStr(msg, 300))
}

// runCodexCLIStream runs `codex exec --json` and folds its events into a Result:
// every agent_message streams to onDelta (blank-line separated) and forms the
// answer; started activity items go to onStatus; turn.completed carries usage;
// turn.failed is the only authoritative failure. Time bounds follow the CLI
// legs' progress contract (progressCtx) and the cap stays authoritative -
// the pipe is closed on ctx.Done() and the wait is bounded (a grandchild
// holding stdout/stderr would otherwise block past the deadline).
func runCodexCLIStream(dir, task string, base, ceil time.Duration, onDelta, onStatus func(string), effort Effort, steer *Steer) (Result, error) {
	start := time.Now()
	ctx := context.Background()
	prog := &progress{}
	var capped *atomic.Bool
	if base > 0 {
		// Progress-aware always: past base the run dies only when quiet, and
		// a running command is never quiet (a CI watch, a long test).
		var cancel context.CancelFunc
		ctx, cancel, prog, capped = progressCtx(base, ceil)
		defer cancel()
	}
	ctx, stopped, detachStop := interruptible(ctx, steer, LegCodexCLI) // /interrupt keeps what streamed
	defer detachStop()
	cmd := exec.CommandContext(ctx, "codex", codexCLICmdArgs(dir, task, effort)...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("codex exec pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			// Name the fix and stay reroutable: an uninstalled CLI is not a
			// dead end for the task, the next leg takes it (2026-09-22).
			return Result{}, fmt.Errorf("codex exec: %w: the Codex CLI is not installed - `npm i -g @openai/codex`", ErrProviderDown)
		}
		return Result{}, fmt.Errorf("codex exec start: %w", err)
	}
	scanDone := make(chan struct{})
	defer close(scanDone)
	go func() {
		select {
		case <-ctx.Done():
			stdout.Close()
		case <-scanDone:
		}
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var acc strings.Builder
	tokens := 0
	failed := ""
	haveFailed := false
	for sc.Scan() {
		prog.touch()
		var ev codexEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "item.started", "item.completed", "item.updated":
			var it codexItem
			if json.Unmarshal(ev.Item, &it) != nil {
				continue
			}
			// A tool in flight is the run's reason to be silent.
			if codexItemIsTool(it.Type) {
				switch ev.Type {
				case "item.started":
					prog.toolStart()
				case "item.completed":
					prog.toolEnd()
				}
			}
			if it.Type == "agent_message" {
				if ev.Type != "item.completed" || it.Text == "" {
					continue
				}
				chunk := it.Text
				if acc.Len() > 0 {
					chunk = "\n" + chunk
					if !strings.HasSuffix(acc.String(), "\n") {
						chunk = "\n" + chunk
					}
				}
				acc.WriteString(chunk)
				if onDelta != nil {
					onDelta(chunk)
				} else if onStatus != nil { // nobody streams the text: its gist is the progress
					if s := Narration(it.Text); s != "" {
						onStatus(s)
					}
				}
				continue
			}
			// Codex summarizes its thinking as reasoning items: the worker's
			// own account of where it is, between two commands.
			if it.Type == "reasoning" && ev.Type == "item.completed" && onStatus != nil {
				if s := Narration(it.Text); s != "" {
					onStatus(s)
				}
				continue
			}
			if ev.Type == "item.started" && onStatus != nil {
				if s := codexItemStatus(it.Type, ev.Item); s != "" {
					onStatus(s)
				}
			}
			if ev.Type == "item.completed" && onStatus != nil && it.Type == "command_execution" {
				code := 0
				if it.ExitCode != nil {
					code = *it.ExitCode
				}
				if s := Outcome(it.AggregatedOutput, code, code != 0 || it.Status == "failed"); s != "" {
					onStatus(s)
				}
			}
		case "turn.completed":
			tokens = ev.Usage.InputTokens + ev.Usage.OutputTokens
		case "turn.failed":
			failed, haveFailed = ev.Error.Message, true
		}
	}
	waitErr := waitBounded(cmd, 2*time.Second)
	if stopped.Load() {
		return Result{Text: strings.TrimSpace(acc.String()), Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
			fmt.Errorf("codex exec stopped after %s: %w", time.Since(start).Round(time.Second), ErrInterrupted)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (capped != nil && capped.Load()) {
		if partial := strings.TrimSpace(acc.String()); partial != "" {
			return Result{Text: partial, Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
				fmt.Errorf("codex exec did not finish within %s: %w", time.Since(start).Round(time.Second), ErrWorkerTimeout)
		}
		return Result{}, fmt.Errorf("codex exec did not finish within %s: %w", time.Since(start).Round(time.Second), ErrWorkerTimeout)
	}
	if haveFailed {
		err := classifyCodexCLIFailure(failed)
		if errors.Is(err, ErrRateLimited) {
			return partialResult(acc.String(), failed, start, onDelta), err
		}
		return Result{}, err
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			if transientProviderError(msg) || codexCLIAuthError(msg) || codexCLIRateLimited(msg) {
				return Result{}, classifyCodexCLIFailure(msg)
			}
			return Result{}, fmt.Errorf("codex exec: %w: %s", waitErr, truncateStr(msg, 300))
		}
		return Result{}, fmt.Errorf("codex exec: %w", waitErr)
	}
	return Result{Text: acc.String(), Tokens: tokens, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil}, nil
}
