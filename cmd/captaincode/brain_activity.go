package main

// Last Runs on one repository's TUI on the new Mac showed decision records only, then
// nothing (2026-11-10). Three causes: the feed was one ring of 60 entries for
// every folder, so 34 failed decision-record runs (three entries each) pushed
// every prompt of that folder out; the sidebar shows the newest 7; and the ring lived
// in memory, so each brain restart emptied it. Now each folder keeps its own
// entries, a burst of decision records reads as one line, and the brain
// rebuilds the feed from the worker logs when it starts.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const (
	actsPerFolder = 40  // entries one folder keeps; a busy folder cannot evict another's
	recordsPerDir = 150 // decision-record lines one folder keeps (about 50 runs), apart from its prompts
	actsTotal     = 600 // the ring's hard cap
	restoreRuns   = 300 // worker logs read at startup, newest first
)

// recordPrefix opens every decision-record prompt (DecisionRecordPrompt,
// DecisionBundlePrompt).
const recordPrefix = "Record the user's"

func isRecordTask(text string) bool { return strings.HasPrefix(strings.TrimSpace(text), recordPrefix) }

// capActs keeps the newest actsPerFolder entries of each folder - decision
// records on a budget of their own, so a burst of them cannot push the
// folder's prompts out - and the newest actsTotal overall, oldest first.
func capActs(acts []activity) []activity {
	type slot struct {
		dir    string
		record bool
	}
	per := map[slot]int{}
	keep := make([]bool, len(acts))
	n := 0
	for i := len(acts) - 1; i >= 0; i-- {
		d := slot{acts[i].Dir, acts[i].Record}
		limit := actsPerFolder
		if d.record {
			limit = recordsPerDir
		}
		if per[d] >= limit || n >= actsTotal {
			continue
		}
		per[d]++
		n++
		keep[i] = true
	}
	if n == len(acts) {
		return acts
	}
	out := make([]activity, 0, n)
	for i, a := range acts {
		if keep[i] {
			out = append(out, a)
		}
	}
	return out
}

// markRecord tags a decision-record run's lines: the run line by its task,
// the route line just before it, and the done line that closes it.
// Called with amu held.
func (b *brain) markRecord(a *activity) {
	key := a.Dir + "\x00" + a.Leg
	switch a.Kind {
	case "run":
		if !isRecordTask(a.Text) {
			return
		}
		a.Record = true
		if b.recordRuns == nil {
			b.recordRuns = map[string]int{}
		}
		b.recordRuns[key]++
		for i := len(b.acts) - 1; i >= 0 && i >= len(b.acts)-4; i-- {
			if p := &b.acts[i]; p.Kind == "route" && p.Dir == a.Dir && !p.Record {
				p.Record = true
				break
			}
		}
	case "done":
		if b.recordRuns[key] > 0 {
			a.Record = true
			b.recordRuns[key]--
		}
	}
}

// actInFolder: an entry belongs to a TUI's folder when it happened there or
// in one of its .worktrees (a turn moved into a worktree is still its turn).
func actInFolder(actDir, dir string) bool {
	if actDir == "" || actDir == dir {
		return true
	}
	return strings.HasPrefix(actDir, dir+string(filepath.Separator)+".worktrees"+string(filepath.Separator))
}

// foldRecords turns each run of two or more consecutive decision-record
// runs (newest first) into one line: how many, how many failed, when.
func foldRecords(acts []activity) []activity {
	out := make([]activity, 0, len(acts))
	for i := 0; i < len(acts); {
		if !acts[i].Record {
			out = append(out, acts[i])
			i++
			continue
		}
		j := i
		runs, failed := 0, 0
		for j < len(acts) && acts[j].Record {
			switch acts[j].Kind {
			case "run":
				runs++
			case "done":
				if strings.HasPrefix(acts[j].Text, "error") {
					failed++
				}
			}
			j++
		}
		if runs < 2 {
			out = append(out, acts[i:j]...)
			i = j
			continue
		}
		text := fmt.Sprintf("%d decision records", runs)
		if failed > 0 {
			text += fmt.Sprintf(", %d failed", failed)
		}
		if first, last := acts[j-1].At, acts[i].At; first != last {
			text += fmt.Sprintf(" (%s to %s)", first, last)
		}
		out = append(out, activity{At: acts[i].At, Kind: "done", Leg: "decision", Model: "decision",
			Label: "decision records", Text: text, Dir: acts[i].Dir, Record: true})
		i = j
	}
	return out
}

// runLog is the header and outcome of one worker log.
type runLog struct {
	leg, model, cwd, origin, task string
	started                       time.Time
	outcome, errText              string // outcome: "done", "failed", or "" (no footer: the run was cut off)
	dur                           time.Duration
}

var outcomeRe = regexp.MustCompile(`^(done|failed) in (\S+) \(`)

func readRunLog(path string) (runLog, bool) {
	f, err := os.Open(path)
	if err != nil {
		return runLog{}, false
	}
	defer f.Close()
	var r runLog
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	header := true
	for sc.Scan() {
		line := sc.Text()
		if header {
			switch {
			case line == "---":
				header = false
			case strings.HasPrefix(line, "leg: "):
				r.leg = strings.TrimPrefix(line, "leg: ")
			case strings.HasPrefix(line, "model: "):
				r.model = strings.TrimPrefix(line, "model: ")
			case strings.HasPrefix(line, "started: "):
				r.started, _ = time.Parse(time.RFC3339, strings.TrimPrefix(line, "started: "))
			case strings.HasPrefix(line, "cwd: "):
				r.cwd = strings.TrimPrefix(line, "cwd: ")
			case strings.HasPrefix(line, "origin: "):
				r.origin = strings.TrimPrefix(line, "origin: ")
			case strings.HasPrefix(line, "task: "):
				r.task = strings.TrimPrefix(line, "task: ")
			}
			continue
		}
		if m := outcomeRe.FindStringSubmatch(line); m != nil {
			r.outcome = m[1]
			r.dur, _ = time.ParseDuration(m[2])
			r.errText = ""
		} else if r.outcome != "" && strings.HasPrefix(line, "error: ") && r.errText == "" {
			r.errText = strings.TrimPrefix(line, "error: ")
		}
	}
	return r, r.leg != "" && !r.started.IsZero()
}

// restoreActivity rebuilds the feed from the newest worker logs, so Last
// Runs shows the folder's recent prompts after a restart. Each run gives a
// run line (its task) and, when it finished, a done line (its outcome). A
// run filed under the TUI that started it (origin) shows in that TUI.
func (b *brain) restoreActivity(dir string) {
	if dir == "" || os.Getenv("CAPTAIN_RESTORE_ACTIVITY") == "0" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") || !strings.HasPrefix(e.Name(), "r") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, file{filepath.Join(dir, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) > restoreRuns {
		files = files[:restoreRuns]
	}
	var runs []runLog
	for _, f := range files {
		if r, ok := readRunLog(f.path); ok {
			runs = append(runs, r)
		}
	}
	today := time.Now().Format("2006-01-02")
	at := func(t time.Time) string {
		if t.Format("2006-01-02") == today {
			return t.Format("15:04:05")
		}
		return t.Format("01-02 15:04")
	}
	type event struct {
		t time.Time
		a activity
	}
	var evs []event
	for _, r := range runs {
		folder := r.cwd
		if r.origin != "" {
			folder = r.origin
		}
		folder = filepath.Clean(folder)
		leg := captaincode.Leg(r.leg)
		if leg == captaincode.LegFrontier {
			leg = captaincode.LegClaude // the frontier pseudo-leg is claude at max effort
		}
		label := ""
		if captaincode.KnownLeg(leg) {
			label = captaincode.Harness(leg) + ":" + captaincode.ModelShort(r.model)
		}
		rec := isRecordTask(r.task)
		evs = append(evs, event{r.started, activity{At: at(r.started), Kind: "run", Leg: r.leg, Model: r.model, Label: label,
			Text: promptPeek(r.task), Dir: folder, Record: rec}})
		if r.outcome == "" {
			continue
		}
		text := ""
		if r.outcome == "failed" {
			text = "error: " + r.errText
		}
		end := r.started.Add(r.dur)
		evs = append(evs, event{end, activity{At: at(end), Kind: "done", Leg: r.leg, Model: r.model, Label: label,
			Text: promptPeek(text), Ms: r.dur.Milliseconds(), Dir: folder, Record: rec}})
	}
	// The feed is in the order things happened: a long run's done line
	// sorts by its finish, not by its start.
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].t.Before(evs[j].t) })
	acts := make([]activity, 0, len(evs))
	for _, e := range evs {
		acts = append(acts, e.a)
	}
	b.amu.Lock()
	b.acts = capActs(append(acts, b.acts...))
	b.amu.Unlock()
	if len(runs) > 0 {
		fmt.Printf("captain brain: Last Runs restored from %d worker logs\n", len(runs))
	}
}
