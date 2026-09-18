package main

// Run history: every turn's OUTPUT, on disk, browsable after the fact.
//
// Until now only workflows left a transcript. A solo or team turn existed only
// in the TUI, so when a request was abandoned, the brain crashed, or a worker
// answered into the void, the work was simply gone - and the user had no way to
// ask "what did it actually say?" (live 2026-07-31). Every turn now appends one
// JSONL record under ~/.captaincode/history/<date>.jsonl:
//
//	captain runs           # what ran, when, how long, ok or not
//	captain show <id>      # the full output of that turn
//
// Append-only, one file per day, plain text - greppable without any tooling.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type workerRecord struct {
	Leg        string `json:"leg"`
	Stage      int    `json:"stage,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Text       string `json:"text,omitempty"`
	Error      string `json:"error,omitempty"`
	Log        string `json:"log,omitempty"`
}

type runRecord struct {
	ID         string         `json:"id"`
	At         time.Time      `json:"at"`
	Kind       string         `json:"kind"` // solo | team | workflow | frontier
	Model      string         `json:"model,omitempty"`
	Legs       []string       `json:"legs,omitempty"`
	Task       string         `json:"task"`
	Output     string         `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	DurationMs int64          `json:"duration_ms"`
	Abandoned  bool           `json:"abandoned,omitempty"` // produced for a client that had gone away - a resend may recover it
	Transcript string         `json:"transcript,omitempty"`
	Workers    []workerRecord `json:"workers,omitempty"`
	Logs       []string       `json:"logs,omitempty"` // per-worker stream logs under ~/.captaincode/runs
}

// logPaths lists the worker log a result carries (nil when none).
func logPaths(res captaincode.Result) []string {
	if res.Log == "" {
		return nil
	}
	return []string{res.Log}
}

func historyDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".captaincode", "history")
}

// newRunID is time-ordered so ids sort the way runs happened.
func newRunID(at time.Time) string {
	return fmt.Sprintf("r%s%s", at.Format("0102-1504"), randSuffix())
}

func randSuffix() string {
	return fmt.Sprintf("%02x", time.Now().UnixNano()%251)
}

// recordRunHistory appends one turn. Failures are recorded too: a turn that
// produced nothing is exactly what the user needs to look up afterwards.
func recordRunHistory(r runRecord) string {
	dir := historyDir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	if r.ID == "" {
		r.ID = newRunID(r.At)
	}
	line, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, r.At.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ""
	}
	defer f.Close()
	f.Write(append(line, '\n'))
	return r.ID
}

// readHistory returns the most recent records, newest first.
func readHistory(limit int) ([]runRecord, error) {
	dir := historyDir()
	if dir == "" {
		return nil, fmt.Errorf("no home directory")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files))) // newest day first
	var out []runRecord
	for _, path := range files {
		recs, err := readHistoryFile(path)
		if err != nil {
			continue
		}
		for i := len(recs) - 1; i >= 0; i-- { // newest line first
			out = append(out, recs[i])
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func readHistoryFile(path string) ([]runRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []runRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 32*1024*1024) // outputs can be large
	for sc.Scan() {
		var r runRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID != "" {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

// findRun resolves an id, a prefix, or "last".
func findRun(idOrLast string) (runRecord, bool) {
	recs, err := readHistory(0)
	if err != nil || len(recs) == 0 {
		return runRecord{}, false
	}
	if idOrLast == "" || idOrLast == "last" {
		return recs[0], true
	}
	for _, r := range recs {
		if r.ID == idOrLast || strings.HasPrefix(r.ID, idOrLast) {
			return r, true
		}
	}
	return runRecord{}, false
}

// searchRuns filters by a substring of the task or the output.
func searchRuns(needle string, limit int) []runRecord {
	recs, _ := readHistory(0)
	needle = strings.ToLower(needle)
	var out []runRecord
	for _, r := range recs {
		if strings.Contains(strings.ToLower(r.Task), needle) || strings.Contains(strings.ToLower(r.Output), needle) {
			out = append(out, r)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// abandonedAnswerTTL bounds how long an undelivered answer is served to a
// resend of the same prompt. Long enough to survive a brain restart and a
// confused minute; short enough that tomorrow's identical prompt runs fresh.
const abandonedAnswerTTL = 15 * time.Minute

// findAbandonedAnswer returns a recent orphaned answer for exactly this task.
func findAbandonedAnswer(task string) (runRecord, bool) {
	task = strings.TrimSpace(task)
	if task == "" {
		return runRecord{}, false
	}
	recs, err := readHistory(50)
	if err != nil {
		return runRecord{}, false
	}
	for _, r := range recs {
		if r.Abandoned && strings.TrimSpace(r.Output) != "" &&
			strings.TrimSpace(r.Task) == task && time.Since(r.At) <= abandonedAnswerTTL {
			return r, true
		}
	}
	return runRecord{}, false
}

// markAnswerDelivered clears the Abandoned flag once an orphaned answer has
// been served, so a THIRD identical prompt runs fresh (asking again after you
// received the answer means: run it again). Best-effort file rewrite.
func markAnswerDelivered(id string) {
	files, _ := filepath.Glob(filepath.Join(historyDir(), "*.jsonl"))
	for _, path := range files {
		recs, err := readHistoryFile(path)
		if err != nil {
			continue
		}
		changed := false
		for i := range recs {
			if recs[i].ID == id && recs[i].Abandoned {
				recs[i].Abandoned = false
				changed = true
			}
		}
		if !changed {
			continue
		}
		var b strings.Builder
		for _, r := range recs {
			line, err := json.Marshal(r)
			if err != nil {
				continue
			}
			b.Write(line)
			b.WriteByte('\n')
		}
		_ = os.WriteFile(path, []byte(b.String()), 0o644)
		return
	}
}
