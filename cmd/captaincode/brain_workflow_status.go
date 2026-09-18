package main

// "Not sure what it's been doing…" - the workflow step tracker rides the
// reasoning channel, and the fork collapses that block by default (its title is
// fixed at creation, so it cannot show the live step). A ten-minute run then
// looks identical to a wedged one from the outside.
//
// This adds two surfaces that do not depend on the TUI's thinking mode:
//
//   - GET /v1/workflow/status → the live checklist, verbatim
//   - `captain status` / `captain watch` → the same thing in any terminal
//
// plus a run file per workflow so every worker's FULL output survives the
// review's compression (spec W5).

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// wfLive is the snapshot the status surfaces read. One per workspace: two
// terminals can each run a workflow, and each sidebar shows its own.
type wfLive struct {
	dir       string
	id        string
	key       string
	tracker   *workflowTracker
	startedAt time.Time
	endedAt   time.Time
	runs      int
	stages    int
}

func (b *brain) setLiveWorkflow(l *wfLive) {
	b.wmu.Lock()
	if b.liveBy == nil {
		b.liveBy = map[string]*wfLive{}
	}
	b.liveBy[l.dir] = l
	b.live = l
	b.wmu.Unlock()
}

func (b *brain) finishLiveWorkflow(l *wfLive) {
	b.wmu.Lock()
	l.endedAt = time.Now()
	b.wmu.Unlock()
}

// liveWorkflow is the workspace's live (or last) workflow; with no workspace
// named, the most recently started one on the machine.
func (b *brain) liveWorkflow(dir string, mine bool) *wfLive {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	if mine {
		return b.liveBy[dir]
	}
	return b.live
}

// workflowStatus serves the live (or last) workflow checklist.
func (b *brain) workflowStatus(w http.ResponseWriter, r *http.Request) {
	l := b.liveWorkflow(workspaceFilter(r))
	if l == nil {
		writeJSON(w, 200, map[string]any{"active": false})
		return
	}
	active := l.endedAt.IsZero()
	elapsed := time.Since(l.startedAt)
	if !active {
		elapsed = l.endedAt.Sub(l.startedAt)
	}
	writeJSON(w, 200, map[string]any{
		"active": active, "id": l.id, "key": l.key,
		"stages": l.stages, "runs": l.runs,
		"elapsed":   elapsed.Round(time.Second).String(),
		"checklist": l.tracker.checklist(),
	})
}

// ---------------------------------------------------------------- run files

// runFilePath is where a workflow's full transcript is kept. The review
// delivers ONE aggregate, so without this the workers' complete outputs exist
// nowhere the user can read them.
func runFilePath(id, key string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	safe := strings.NewReplacer("/", "-", ">", "-to-", "+", "_", " ", "").Replace(key)
	return filepath.Join(home, ".captaincode", "runs", fmt.Sprintf("%s-%s.md", id, safe))
}

// runFile accumulates a workflow's transcript on disk as it happens, so a live
// run can be tailed and a finished one can be read in full.
type runFile struct{ path string }

func newRunFile(id, key, task string) *runFile {
	p := runFilePath(id, key)
	if p == "" {
		return &runFile{}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return &runFile{}
	}
	f := &runFile{path: p}
	f.write(fmt.Sprintf("# workflow %s · %s\n\n**request**\n\n%s\n", id, key, strings.TrimSpace(task)))
	return f
}

func (f *runFile) write(s string) {
	if f == nil || f.path == "" {
		return
	}
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	fh.WriteString(s)
}

func (f *runFile) stage(idx, total int, leg string, dur time.Duration, text string, err error) {
	if err != nil {
		f.write(fmt.Sprintf("\n## stage %d/%d · %s · %s · FAILED\n\n%v\n", idx, total, leg, dur.Round(time.Second), err))
		return
	}
	f.write(fmt.Sprintf("\n## stage %d/%d · %s · %s · %d chars\n\n%s\n", idx, total, leg, dur.Round(time.Second), len(text), text))
}

func (f *runFile) review(text string, dur time.Duration) {
	f.write(fmt.Sprintf("\n## director review · %s\n\n%s\n", dur.Round(time.Second), text))
}

func (f *runFile) location() string {
	if f == nil || f.path == "" {
		return ""
	}
	return f.path
}
