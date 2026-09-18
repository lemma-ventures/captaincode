package captaincode

// Progress-aware worker caps + the --notimeout flag (2026-08-24).
//
// Two user requirements, one mechanism:
//  1. "--notimeout" typed anywhere in the CURRENT prompt (chained after
//     /claude, /team, /wf, or bare) multiplies the run's time bounds ×10.
//  2. Team/workflow/solo workers that are visibly PROGRESSING must not be
//     killed at the base cap - only stuck runs die (the stall watchdog's
//     job). The hard cap therefore extends while activity flows and stops
//     absolutely at a ceiling (default 10× base, CAPTAIN_WORKER_CEILING).
//
// The flag is parsed from the composed worker prompt at the single dispatch
// choke point - no signature churn, concurrency-safe - but only honored in
// the LAST [user] turn: a replayed conversation containing an old
// "--notimeout" must not re-trigger it. It is stripped before the worker
// sees the text.

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var noTimeoutRe = regexp.MustCompile(`(?i)(^|\s)--no-?timeout\b`)

// NoTimeoutMul returns 10 when the current (last) user turn carries
// --notimeout, else 1.
func NoTimeoutMul(prompt string) int {
	seg := prompt
	if i := strings.LastIndex(prompt, "[user]\n"); i >= 0 {
		seg = prompt[i:]
		if j := strings.Index(seg, "[assistant]\n"); j >= 0 {
			seg = seg[:j]
		}
	}
	if noTimeoutRe.MatchString(seg) {
		return 10
	}
	return 1
}

// StripNoTimeout removes every occurrence (and its leading separator) so no
// worker ever sees the flag.
func StripNoTimeout(s string) string {
	return strings.TrimSpace(noTimeoutRe.ReplaceAllString(s, ""))
}

// workerCeiling is the absolute wall-clock stop for a PROGRESSING run. There
// is none by default: a worker that is still working - tools running, output
// flowing - is never cut for having taken long. A 31-minute codex-cli run that
// had just pushed a PR fix and was monitoring CI, as asked, was cut mid-watch
// with "partial output" (live 2026-09-12). Loops are caught elsewhere (the
// doom_loop permission denies a repeated tool call; a quiet run still dies
// past its base cap). CAPTAIN_WORKER_CEILING sets one when an operator wants a
// hard stop regardless of progress.
func workerCeiling() time.Duration {
	if v := os.Getenv("CAPTAIN_WORKER_CEILING"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 0
}

// toolRunTimeout is how long a run may stay silent WHILE A TOOL IS RUNNING
// before it counts as quiet. A build or a test suite prints nothing for
// minutes; that is work, not a stall - `cargo test --release` was aborted at
// the 4m stall window with "User aborted the command" (live 2026-09-12).
// opencode bounds a bash tool at 10 minutes (MAX_TIMEOUT_MS), so a tool
// "running" for longer than that is wedged, not slow. Default 12m;
// CAPTAIN_WORKER_TOOL_TIMEOUT overrides.
func toolRunTimeout() time.Duration {
	if v := os.Getenv("CAPTAIN_WORKER_TOOL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 12 * time.Minute
}

// cliIdleTimeout and cliToolTimeout are the quiet windows of the CLI legs
// (claude -p, codex exec, cursor-agent), which are a different animal from an
// opencode worker: their reasoning is invisible - gpt-6-astra at xhigh thinks
// for minutes between two tool calls and the JSONL carries nothing meanwhile
// - and their tools are bounded by the CLI itself, not by opencode's 10m bash
// cap. A 61-minute codex-cli run was cut 91 seconds after its last tool call,
// mid-thought, on the 90s idle window (live 2026-09-12). The process is ours:
// when it dies, the stream ends and the run ends with it, so silence is only
// suspect after a long time. CAPTAIN_WORKER_CLI_IDLE_TIMEOUT (default 30m)
// and CAPTAIN_WORKER_CLI_TOOL_TIMEOUT (default 2h) override.
func cliIdleTimeout() time.Duration {
	if v := os.Getenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Minute
}

func cliToolTimeout() time.Duration {
	if v := os.Getenv("CAPTAIN_WORKER_CLI_TOOL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 2 * time.Hour
}

// progress is what a running worker reports about itself: when it last did
// anything, and how many tools it has in flight. A run is QUIET only when it
// has been silent longer than the window its state allows - idle between
// tool calls, tool while one runs (the windows are the CLI legs': see
// cliIdleTimeout).
type progress struct {
	last  atomic.Int64
	tools atomic.Int32
	idle  time.Duration // 0 = cliIdleTimeout()
	tool  time.Duration // 0 = cliToolTimeout()
}

func (p *progress) touch() { p.last.Store(time.Now().UnixNano()) }

func (p *progress) toolStart() {
	p.tools.Add(1)
	p.touch()
}

func (p *progress) toolEnd() {
	if p.tools.Add(-1) < 0 {
		p.tools.Store(0)
	}
	p.touch()
}

// quiet reports whether the run has been silent past the window its state
// allows.
func (p *progress) quiet() bool {
	window := p.idle
	if window <= 0 {
		window = cliIdleTimeout()
	}
	if p.tools.Load() > 0 {
		window = p.tool
		if window <= 0 {
			window = cliToolTimeout()
		}
	}
	return time.Since(time.Unix(0, p.last.Load())) >= window
}

// progressCtx bounds a CLI worker by progress, not wall-clock: past base the
// run is cancelled only once it is quiet (see progress.quiet); ceil > 0 adds
// an absolute stop. Our cancellation surfaces as context.Canceled, not
// DeadlineExceeded - callers check capped for timeout classification.
func progressCtx(base, ceil time.Duration) (context.Context, context.CancelFunc, *progress, *atomic.Bool) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &progress{}
	p.touch()
	capped := &atomic.Bool{}
	poll := base / 4
	if poll > 10*time.Second {
		poll = 10 * time.Second
	}
	if poll < 50*time.Millisecond {
		poll = 50 * time.Millisecond
	}
	start := time.Now()
	go func() {
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				el := time.Since(start)
				if (ceil > 0 && el >= ceil) || (el >= base && p.quiet()) {
					capped.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel, p, capped
}

// TitleMarker is what opencode's title agent says in its system prompt;
// TitleRewritePrefix opens the brain's own six-word rewrite of that request.
const (
	TitleMarker        = "You are a title generator"
	TitleRewritePrefix = "Reply with ONLY a short title"
)

// IsTitlePrompt reports whether a task is a session-title request. Detection is
// on the generator's own preamble, not on the word "title": a user asking for a
// title is ordinary work. Only the request's OWN framing counts - the brain's
// rewrite, or the marker inside a leading [system] block. The conversation
// body is never scanned: a session that had inherited a worker transcript
// carried the marker in an old user turn, and every prompt typed in it was
// then run on the title budget and answered with a title (live 2026-09-12).
func IsTitlePrompt(task string) bool {
	if strings.HasPrefix(task, TitleRewritePrefix) {
		return true
	}
	head := task
	if len(head) > 4000 {
		head = head[:4000]
	}
	if j := strings.Index(head, "\n\n["); j >= 0 {
		head = head[:j]
	}
	// A bare title prompt (no conversation markers at all), or the request's
	// own [system] block.
	if strings.HasPrefix(head, "[") && !strings.HasPrefix(head, "[system]\n") {
		return false
	}
	return strings.Contains(head, TitleMarker)
}

// titleBudget bounds a title run. Naming a session is a one-line job on the
// cheapest leg; letting it inherit the worker budget (and the reroute that
// follows a stall) spends real capacity on housekeeping.
func titleBudget() (base, ceil time.Duration) {
	return 90 * time.Second, 3 * time.Minute
}
