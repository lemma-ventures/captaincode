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

// workerLogsDir is where worker logs live (next to workflow transcripts).
func workerLogsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".captaincode", "runs")
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
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	l := &workerLog{path: f.Name(), f: f, started: now}
	fmt.Fprintf(f, "# captain worker log\nleg: %s\nstarted: %s\ncwd: %s\ntask: %s\n---\n",
		leg, now.Format(time.RFC3339), ws.Dir, truncate(strings.Join(strings.Fields(lastUserTurn(task)), " "), 600))
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

func (l *workerLog) status(s string) {
	if l == nil || strings.TrimSpace(s) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.f, "\n[%s] %s\n", time.Since(l.started).Round(time.Second), strings.TrimSpace(s))
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
