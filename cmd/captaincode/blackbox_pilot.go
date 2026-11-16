package main

// BB5 rollout gate: the benign-action pilot review. `captain blackbox pilot`
// replays a deterministic corpus of benign tool events through both supported
// hosts (the opencode hook and the Claude composition) in enforce mode,
// against the same rule source the operator configured for real screening.
// The gate passes only when every action runs: zero refusals, zero degraded
// or unavailable screenings. Any refusal fails the run and prints the reason
// class and the corpus index, so a false positive is explained, not counted.
//
// The corpus is deliberately ordinary work: version control, builds, tests,
// file reads and writes inside the workspace. It contains no action a
// security rule should refuse. It is a false-positive gate, not a detection
// test: detection is proven by the enforce and export test suites.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type blackboxPilotEvent struct {
	Host      string
	Workspace int
	Index     int
	Tool      string
}

type blackboxPilotOutcome struct {
	Event       blackboxPilotEvent
	Refused     bool
	Reason      string
	Disposition string
	Duration    time.Duration
}

type blackboxPilotSummary struct {
	Count        int                    `json:"count"`
	Hosts        int                    `json:"hosts"`
	Workspaces   int                    `json:"workspaces"`
	Mode         string                 `json:"mode"`
	Refusals     int                    `json:"refusals"`
	Dispositions map[string]int         `json:"dispositions"`
	PerHost      map[string]int         `json:"per_host"`
	TotalTimeMs  float64                `json:"total_time_ms"`
	MedianMs     float64                `json:"median_ms"`
	P95Ms        float64                `json:"p95_ms"`
	MaxMs        float64                `json:"max_ms"`
	Verdict      string                 `json:"verdict"`
	Failures     []blackboxPilotFailure `json:"failures,omitempty"`
}

type blackboxPilotFailure struct {
	Host        string `json:"host"`
	Workspace   int    `json:"workspace"`
	Index       int    `json:"index"`
	Tool        string `json:"tool"`
	Reason      string `json:"reason"`
	Disposition string `json:"disposition"`
}

// blackboxPilotCorpus is one benign opencode tool event: the tool name and a
// JSON args object, or a post-phase output sample.
type blackboxPilotCorpusEntry struct {
	Tool   string
	Args   string
	Phase  string
	Output string
}

// blackboxPilotClaudeCorpus is one benign Claude Code PreToolUse payload.
type blackboxPilotClaudeEntry struct {
	Tool  string
	Input map[string]any
}

// blackboxPilotCommands are ordinary version-control, build and test
// commands. None names a secret path, a pinned file or a known-bad package.
var blackboxPilotCommands = []string{
	"git status",
	"git diff --stat",
	"git log --oneline -5",
	"git branch --show-current",
	"ls -la",
	"gofmt -l cmd pkg",
	"go vet ./...",
	"go test ./pkg/captaincode -run TestDecide",
	"rg -n func blackbox_pilot.go",
	"cat README.md",
	"mkdir -p build/out",
	"cp notes.txt build/out/notes.txt",
	"npm install left-pad@1.3.0",
	"cargo build --quiet",
	"python3 -m unittest test_serve",
	"echo build finished",
	"wc -l blackbox_pilot.go",
	"diff -u a.txt b.txt",
	"tar -czf build/out.tgz notes.txt",
	"true",
}

// blackboxPilotFiles are benign workspace-relative file paths and contents.
var blackboxPilotFiles = []string{
	"README.md", "notes.txt", "src/main.go", "docs/guide.md",
	"pkg/example/example.go", "web/index.html", "build/out.txt",
	"tests/example_test.go", "config/default.json", "scripts/run.sh",
}

var blackboxPilotFileBody = "one benign line of documentation text\n"

// blackboxPilotReads are post-phase tool outputs a benign run produces.
var blackboxPilotOutputs = []string{
	"3 files changed, 12 insertions",
	"ok  good.test 0.001s",
	"main.go:42: note",
	"",
	"build finished",
}

func runBlackboxPilot(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("blackbox pilot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	count := flags.Int("count", 1000, "benign actions to screen")
	workspaces := flags.Int("workspaces", 2, "disposable workspaces")
	state := flags.String("state", "", "disposable security-record directory (required)")
	mode := flags.String("mode", "enforce", "screening mode for the review (enforce is the rollout gate)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: captain blackbox pilot [--count N] [--workspaces N] [--state dir] [--mode audit|enforce]")
	}
	if *count < 1 || *workspaces < 1 {
		return fmt.Errorf("blackbox pilot: --count and --workspaces must be at least 1")
	}
	switch strings.ToLower(*mode) {
	case "audit", "enforce":
	default:
		return fmt.Errorf("blackbox pilot: unknown mode %q", *mode)
	}
	if *state == "" {
		return fmt.Errorf("blackbox pilot: --state <dir> is required; the review files one record per action there")
	}
	if err := os.MkdirAll(*state, 0o700); err != nil {
		return fmt.Errorf("blackbox pilot: state: %w", err)
	}

	// The review drives the real screening paths, so it inherits the same
	// rule-source resolution as production: an explicit snapshot, else the
	// configured authenticated store, else the default file. A configured
	// but broken store degrades every screening and fails the gate below.
	previousMode := os.Getenv("CAPTAIN_BLACKBOX")
	previousState := os.Getenv("CAPTAIN_BLACKBOX_STATE")
	os.Setenv("CAPTAIN_BLACKBOX", strings.ToLower(*mode))
	os.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(*state, "security.jsonl"))
	defer func() {
		os.Setenv("CAPTAIN_BLACKBOX", previousMode)
		os.Setenv("CAPTAIN_BLACKBOX_STATE", previousState)
	}()

	workspaceDirs := make([]string, *workspaces)
	for i := range workspaceDirs {
		workspaceDirs[i] = filepath.Join(*state, fmt.Sprintf("workspace-%d", i))
		if err := os.MkdirAll(workspaceDirs[i], 0o700); err != nil {
			return fmt.Errorf("blackbox pilot: workspace: %w", err)
		}
	}

	outcomes := make([]blackboxPilotOutcome, 0, *count)
	start := time.Now()
	for i := 0; i < *count; i++ {
		workspace := i % *workspaces
		seq := i / 2 // the per-host sequence number of this action
		if i%2 == 0 {
			outcomes = append(outcomes, blackboxPilotOpenCodeStep(i, seq, workspace, workspaceDirs[workspace]))
			continue
		}
		outcomes = append(outcomes, blackboxPilotClaudeStep(i, seq, workspace, workspaceDirs[workspace]))
	}
	total := time.Since(start)

	summary := blackboxPilotSummarize(outcomes, *mode, *workspaces, total)
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(summary); err != nil {
		return fmt.Errorf("blackbox pilot: write summary: %w", err)
	}
	if summary.Verdict != "pass" {
		return fmt.Errorf("blackbox pilot: %d of %d benign actions were refused or unexplained; see the summary", len(summary.Failures), summary.Count)
	}
	return nil
}

// blackboxPilotOpenCodeStep screens one benign event through the real
// `captain blackbox hook` path, exactly as the opencode plugin calls it.
func blackboxPilotOpenCodeStep(index, seq, workspace int, dir string) blackboxPilotOutcome {
	entry := blackboxPilotOpenCodeCorpus(seq)
	event := blackboxPilotEvent{Host: "opencode", Workspace: workspace, Index: index, Tool: entry.Tool}
	input := map[string]any{
		"tool":    entry.Tool,
		"cwd":     dir,
		"session": fmt.Sprintf("pilot-%d", index),
		"phase":   entry.Phase,
	}
	if entry.Args != "" {
		var args any
		if err := json.Unmarshal([]byte(entry.Args), &args); err == nil {
			input["args"] = args
		}
	} else {
		input["args"] = map[string]any{}
	}
	if entry.Phase == "post" {
		input["output"] = blackboxPilotOutputs[index%len(blackboxPilotOutputs)]
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return blackboxPilotOutcome{Event: event, Refused: true, Reason: "corpus_encode_failed", Disposition: "error"}
	}
	started := time.Now()
	var out bytes.Buffer
	if err := runBlackboxHook([]string{"--mode", os.Getenv("CAPTAIN_BLACKBOX")}, bytes.NewReader(encoded), &out); err != nil {
		return blackboxPilotOutcome{Event: event, Refused: true, Reason: "hook_error", Disposition: "error", Duration: time.Since(started)}
	}
	var hookOut blackboxHookOutput
	if err := json.Unmarshal(out.Bytes(), &hookOut); err != nil {
		return blackboxPilotOutcome{Event: event, Refused: true, Reason: "hook_output_unreadable", Disposition: "error", Duration: time.Since(started)}
	}
	return blackboxPilotOutcome{Event: event, Refused: hookOut.Refuse, Reason: hookOut.Reason, Disposition: hookOut.Disposition, Duration: time.Since(started)}
}

// blackboxPilotClaudeStep screens one benign event through the real Claude
// composition (`captain redact --hook` body), exactly as Claude Code calls it.
func blackboxPilotClaudeStep(index, seq, workspace int, dir string) blackboxPilotOutcome {
	entry := blackboxPilotClaudeCorpus(seq)
	event := blackboxPilotEvent{Host: "claude", Workspace: workspace, Index: index, Tool: entry.Tool}
	input := claudeHookInput{ToolName: entry.Tool, ToolInput: entry.Input, SessionID: fmt.Sprintf("pilot-%d", index), Cwd: dir}
	started := time.Now()
	var out bytes.Buffer
	redactHookRun(input, &out)
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil && out.Len() > 0 {
		return blackboxPilotOutcome{Event: event, Refused: true, Reason: "hook_output_unreadable", Disposition: "error", Duration: time.Since(started)}
	}
	if decoded != nil {
		if hookSpecific, ok := decoded["hookSpecificOutput"].(map[string]any); ok {
			if decision, _ := hookSpecific["permissionDecision"].(string); decision == "deny" {
				reason, _ := hookSpecific["permissionDecisionReason"].(string)
				return blackboxPilotOutcome{Event: event, Refused: true, Reason: reason, Disposition: "evaluated", Duration: time.Since(started)}
			}
		}
	}
	return blackboxPilotOutcome{Event: event, Disposition: "evaluated", Duration: time.Since(started)}
}

// blackboxPilotOpenCodeCorpus picks one deterministic benign opencode event.
// Even sequence numbers spread across bash commands, file edits and reads;
// every third event is a post-phase observation with benign output. Commands
// stay inside the supported shell grammar (no quotes, pipes or redirection):
// syntax the evaluator cannot fully parse is an incomplete screening, and an
// incomplete required check refuses in enforce mode.
func blackboxPilotOpenCodeCorpus(seq int) blackboxPilotCorpusEntry {
	if seq%3 == 2 {
		return blackboxPilotCorpusEntry{Tool: "bash", Phase: "post"}
	}
	switch seq % 6 {
	case 0, 3:
		command := blackboxPilotCommands[(seq/6)%len(blackboxPilotCommands)]
		return blackboxPilotCorpusEntry{Tool: "bash", Phase: "pre", Args: `{"command":` + mustMarshalJSON(command) + `}`}
	default:
		file := blackboxPilotFiles[(seq/6)%len(blackboxPilotFiles)]
		if seq%6 == 1 {
			return blackboxPilotCorpusEntry{Tool: "write", Phase: "pre", Args: `{"file_path":` + mustMarshalJSON(file) + `,"content":` + mustMarshalJSON(blackboxPilotFileBody) + `}`}
		}
		return blackboxPilotCorpusEntry{Tool: "read", Phase: "pre", Args: `{"file_path":` + mustMarshalJSON(file) + `}`}
	}
}

// blackboxPilotClaudeCorpus picks one deterministic benign Claude event.
func blackboxPilotClaudeCorpus(seq int) blackboxPilotClaudeEntry {
	switch seq % 4 {
	case 0:
		command := blackboxPilotCommands[(seq/4)%len(blackboxPilotCommands)]
		return blackboxPilotClaudeEntry{Tool: "Bash", Input: map[string]any{"command": command}}
	case 1:
		file := blackboxPilotFiles[(seq/4)%len(blackboxPilotFiles)]
		return blackboxPilotClaudeEntry{Tool: "Read", Input: map[string]any{"file_path": file}}
	case 2:
		file := blackboxPilotFiles[(seq/4)%len(blackboxPilotFiles)]
		return blackboxPilotClaudeEntry{Tool: "Edit", Input: map[string]any{"file_path": file, "old_string": "old", "new_string": "new"}}
	default:
		return blackboxPilotClaudeEntry{Tool: "Grep", Input: map[string]any{"pattern": "func Test", "path": "."}}
	}
}

func mustMarshalJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// blackboxPilotSummarize turns the outcomes into the gate verdict. Every
// refusal is a failure, and so is every screening that did not complete:
// a degraded or unavailable check in enforce mode is a refusal in production,
// so the review may not pass with one.
func blackboxPilotSummarize(outcomes []blackboxPilotOutcome, mode string, workspaces int, total time.Duration) blackboxPilotSummary {
	summary := blackboxPilotSummary{
		Count: len(outcomes), Hosts: 2, Workspaces: workspaces, Mode: mode,
		Dispositions: map[string]int{}, PerHost: map[string]int{},
		TotalTimeMs: float64(total.Microseconds()) / 1000,
	}
	durations := make([]float64, 0, len(outcomes))
	for _, outcome := range outcomes {
		summary.PerHost[outcome.Event.Host]++
		summary.Dispositions[outcome.Disposition]++
		durations = append(durations, float64(outcome.Duration.Microseconds())/1000)
		unexplained := outcome.Refused || outcome.Disposition == "" || outcome.Disposition == "degraded" || outcome.Disposition == "error"
		if !unexplained {
			continue
		}
		summary.Failures = append(summary.Failures, blackboxPilotFailure{
			Host: outcome.Event.Host, Workspace: outcome.Event.Workspace, Index: outcome.Event.Index,
			Tool: outcome.Event.Tool, Reason: outcome.Reason, Disposition: outcome.Disposition,
		})
	}
	if len(durations) > 0 {
		sort.Float64s(durations)
		summary.MedianMs = durations[len(durations)/2]
		summary.P95Ms = durations[(len(durations)*95)/100]
		summary.MaxMs = durations[len(durations)-1]
	}
	summary.Refusals = len(summary.Failures)
	if summary.Refusals == 0 {
		summary.Verdict = "pass"
	} else {
		summary.Verdict = "fail"
	}
	return summary
}

// blackboxPilotRecordCount reads the disposable record stream, so the review
// can prove one record was filed per screened action.
func blackboxPilotRecordCount(state string) (int, error) {
	data, err := os.ReadFile(filepath.Join(state, "security.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return bytes.Count(data, []byte("\n")), nil
}
