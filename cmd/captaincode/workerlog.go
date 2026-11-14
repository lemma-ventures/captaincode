package main

// Worker logs: every worker run streams what it says and does into a file
// under ~/.captaincode/runs as it happens - solo, team and workflow alike.
// Live 2026-09-10: a 45-minute frontier run was discarded by its runner and a
// 15-minute cursor run vanished behind a timeout; the only trace of either
// was the provider's own session store. The log is written line by line, so
// a killed brain, a dead TUI turn or a discarded result never loses the
// agent's narrative, and the history record points at it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type workerLog struct {
	path    string
	f       *os.File
	mu      sync.Mutex
	wrote   bool // any delta written (else res.Text is dumped at close)
	started time.Time
}

// promptFileMax bounds one saved run prompt at 256 KiB.
const promptFileMax = 256 << 10

// workerLogsDir is where worker logs live (next to workflow transcripts).
func workerLogsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".captaincode", "runs")
}

// writePromptFile keeps the exact prompt a worker received beside its log:
// masked with MaskSecrets, mode 0600, cut at 256 KiB with a "[cut N bytes]"
// marker. It reports whether the file was written.
func writePromptFile(path, task string) bool {
	if strings.TrimSpace(task) == "" {
		return false
	}
	text, _ := captaincode.MaskSecrets(task, "[secret]")
	if len(text) > promptFileMax {
		cut := promptFileMax
		for cut > 0 && (text[cut]&0xC0) == 0x80 {
			cut--
		}
		text = text[:cut] + fmt.Sprintf("\n[cut %d bytes]", len(text)-cut)
	}
	return os.WriteFile(path, []byte(text), 0o600) == nil
}

// openWorkerLog creates the log for one run; nil when logging is impossible
// (no home, unwritable dir) - never a reason to fail the run.
func openWorkerLog(ws captaincode.Workspace, leg captaincode.Leg, task string) *workerLog {
	dir := workerLogsDir()
	if dir == "" || os.Getenv("CAPTAIN_WORKER_LOGS") == "0" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	now := time.Now()
	name := fmt.Sprintf("%s-%s.log", newRunID(now), strings.ReplaceAll(string(leg), "/", "_"))
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	l := &workerLog{path: path, f: f, started: now}
	from := ""
	if ws.Origin != "" && filepath.Clean(ws.Origin) != filepath.Clean(ws.Dir) {
		from = "origin: " + ws.Origin + "\n" // the TUI that started it, when the task moved the run elsewhere
	}
	// The full prompt beside the log: a reroute, a gate repair and an
	// escalation each send a prompt the header's task line cannot hold.
	promptPath := strings.TrimSuffix(path, ".log") + ".prompt.md"
	promptLine := ""
	if writePromptFile(promptPath, task) {
		promptLine = "prompt: " + promptPath + "\n"
	}
	// The model the leg dispatches at this effort: "frontier" or "quality"
	// alone does not say which model ran.
	model := captaincode.ModelIDAt(leg, ws.Effort)
	fmt.Fprintf(f, "# captain worker log\nleg: %s\nmodel: %s\nstarted: %s\ncwd: %s\n%stask: %s\n%s---\n",
		leg, model, now.Format(time.RFC3339), ws.Dir, from, truncate(strings.Join(strings.Fields(lastUserTurn(task)), " "), 600), promptLine)
	return l
}

func (l *workerLog) delta(d string) {
	if l == nil || d == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.wrote = true
	_, _ = l.f.WriteString(d)
}

// status writes one status line. A status carrying a full tool input gets a
// second `input:` line under the short line, so the log keeps what the TUI's
// one-line view leaves out.
func (l *workerLog) status(s string) {
	if l == nil || strings.TrimSpace(s) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	show, input := captaincode.StatusParts(s)
	_, _ = fmt.Fprintf(l.f, "\n[%s] %s\n", time.Since(l.started).Round(time.Second), strings.TrimSpace(show))
	if input != "" {
		_, _ = fmt.Fprintf(l.f, "input: %s\n", strings.TrimRight(input, "\n"))
	}
}

// close writes the outcome (and the full text when nothing streamed) and
// returns the path for the Result/history record.
func (l *workerLog) close(res captaincode.Result, err error) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.wrote && strings.TrimSpace(res.Text) != "" {
		_, _ = l.f.WriteString(res.Text)
	}
	outcome := "done"
	if err != nil {
		outcome = "failed"
	}
	_, _ = fmt.Fprintf(l.f, "\n---\n%s in %s (%d chars)\n", outcome, time.Since(l.started).Round(time.Second), len(res.Text))
	if err != nil {
		_, _ = fmt.Fprintf(l.f, "error: %s\n", err.Error())
	}
	if res.Partial {
		_, _ = l.f.WriteString("partial: true\n")
	}
	_ = l.f.Close()
	return l.path
}
