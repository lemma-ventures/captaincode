package captaincode

// The routing journal (stage 1: close the loop before anything learns).
//
// state.json keeps ring buffers - 500 events, 200 decisions, 200 outcomes -
// which is what a sidebar needs and less than what learning needs: a kNN
// over past tasks, a bandit's counts and a distilled router all want every
// labelled decision ever made, and a buffer that forgets one row in two
// hundred cannot hold a few thousand. So every decision that reached a task,
// every run event and every outcome that settled is ALSO appended, one line
// each, to routing.jsonl beside state.json. Append-only, never rewritten,
// read by the estimator (estimate.go) and the distill export (bandit.go).
//
// An in-memory ledger (tests, a one-off CLI read) never writes it;
// CAPTAIN_ROUTING_LOG=0 turns it off for a persistent one.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	RoutingKindDecision = "decision"
	RoutingKindEvent    = "event"
	RoutingKindOutcome  = "outcome"
)

// RoutingRecord is one journal line: exactly one of the three payloads.
type RoutingRecord struct {
	Kind     string           `json:"kind"`
	At       time.Time        `json:"at"`
	TaskID   string           `json:"task_id,omitempty"`
	Decision *Decision        `json:"decision,omitempty"`
	Event    *Event           `json:"event,omitempty"`
	Outcome  *OutcomeEvidence `json:"outcome,omitempty"`
}

// RoutingLogEnv turns the journal off (0) or names another file.
const RoutingLogEnv = "CAPTAIN_ROUTING_LOG"

// RoutingLogPath is the default journal location for the state directory.
func RoutingLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "routing.jsonl")
}

// routingLogMaxBytes bounds the file: past it the newest half is kept. Sixty
// four megabytes is on the order of a hundred thousand lines - years of use.
func routingLogMaxBytes() int64 {
	if v, err := strconv.Atoi(os.Getenv("CAPTAIN_ROUTING_LOG_MAX_MB")); err == nil && v > 0 {
		return int64(v) << 20
	}
	return 64 << 20
}

// journalPath is where this ledger's journal lives: beside its state file,
// so a test ledger with a temp path journals into the temp dir, and an
// in-memory ledger nowhere.
func (l *Ledger) journalPath() string {
	if l == nil || l.path == "" {
		return ""
	}
	switch v := strings.TrimSpace(os.Getenv(RoutingLogEnv)); v {
	case "0", "off", "false":
		return ""
	case "":
		return filepath.Join(filepath.Dir(l.path), "routing.jsonl")
	default:
		return v
	}
}

// journal appends one record. Best effort and silent: the journal is a copy
// of what state.json already holds, and a write error must never fail a
// turn that has already run.
func (l *Ledger) journal(r RoutingRecord) {
	p := l.journalPath()
	if p == "" {
		return
	}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	if st, err := os.Stat(p); err == nil && st.Size() > routingLogMaxBytes() {
		trimRoutingLog(p)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// trimRoutingLog keeps the newest half of an oversized journal.
func trimRoutingLog(p string) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return
	}
	cut := len(raw) / 2
	if i := strings.IndexByte(string(raw[cut:]), '\n'); i >= 0 {
		cut += i + 1
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw[cut:], 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// ReadRoutingLog reads a journal, newest last. max > 0 keeps only the newest
// max lines; malformed lines are skipped.
func ReadRoutingLog(path string, max int) []RoutingRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []RoutingRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r RoutingRecord
		if json.Unmarshal([]byte(line), &r) == nil && r.Kind != "" {
			out = append(out, r)
		}
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// RoutingSample is one task's routing decision joined to what came of it:
// the last decision recorded for the task, the outcome's last state, and
// the run events in order. A sample is Labeled when its outcome settled.
type RoutingSample struct {
	Decision Decision
	Outcome  *OutcomeEvidence
	Events   []Event
}

// Labeled says the outcome settled one way or the other.
func (s RoutingSample) Labeled() bool {
	return s.Outcome != nil && s.Outcome.Settled()
}

// Success is the label: accepted is a success, rejected or regressed a
// failure. Meaningless unless Labeled.
func (s RoutingSample) Success() bool {
	return s.Outcome != nil && s.Outcome.Status == AcceptanceAccepted
}

// JoinRouting folds journal records into per-task samples, oldest task
// first. The ledger's own ring buffers are folded in by JoinLedger so a
// fresh install with no journal still learns from what state.json holds.
func JoinRouting(records []RoutingRecord) []RoutingSample {
	byTask := map[string]*RoutingSample{}
	var order []string
	get := func(id string) *RoutingSample {
		s, ok := byTask[id]
		if !ok {
			s = &RoutingSample{}
			byTask[id] = s
			order = append(order, id)
		}
		return s
	}
	for _, r := range records {
		if r.TaskID == "" {
			continue
		}
		switch r.Kind {
		case RoutingKindDecision:
			if r.Decision != nil {
				get(r.TaskID).Decision = *r.Decision
			}
		case RoutingKindEvent:
			if r.Event != nil {
				get(r.TaskID).Events = append(get(r.TaskID).Events, *r.Event)
			}
		case RoutingKindOutcome:
			if r.Outcome != nil {
				o := *r.Outcome
				get(r.TaskID).Outcome = &o
			}
		}
	}
	out := make([]RoutingSample, 0, len(order))
	for _, id := range order {
		s := byTask[id]
		if s.Decision.TaskID == "" && len(s.Events) == 0 {
			continue
		}
		out = append(out, *s)
	}
	return out
}

// JoinLedger builds samples from a ledger's ring buffers.
func JoinLedger(l *Ledger) []RoutingSample {
	if l == nil {
		return nil
	}
	var recs []RoutingRecord
	for i := range l.Decisions {
		d := l.Decisions[i]
		recs = append(recs, RoutingRecord{Kind: RoutingKindDecision, TaskID: d.TaskID, At: d.At, Decision: &d})
	}
	for i := range l.Events {
		e := l.Events[i]
		recs = append(recs, RoutingRecord{Kind: RoutingKindEvent, TaskID: e.TaskID, At: e.At, Event: &e})
	}
	for i := range l.Outcomes {
		o := l.Outcomes[i]
		recs = append(recs, RoutingRecord{Kind: RoutingKindOutcome, TaskID: o.TaskID, At: o.UpdatedAt, Outcome: &o})
	}
	return JoinRouting(recs)
}

// RoutingHistory is the journal and the ledger merged, one sample per task,
// the journal's fuller record winning where both hold a task. This is what
// the estimator and the bandit learn from.
func RoutingHistory(l *Ledger) []RoutingSample {
	path := ""
	if l != nil {
		path = l.journalPath()
	}
	if path == "" && l != nil && l.path != "" {
		path = filepath.Join(filepath.Dir(l.path), "routing.jsonl")
	}
	seen := map[string]bool{}
	var out []RoutingSample
	if path != "" {
		for _, s := range JoinRouting(ReadRoutingLog(path, 0)) {
			if s.Decision.TaskID != "" {
				seen[s.Decision.TaskID] = true
			}
			out = append(out, s)
		}
	}
	for _, s := range JoinLedger(l) {
		if s.Decision.TaskID != "" && seen[s.Decision.TaskID] {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Decision.At.Before(out[j].Decision.At) })
	return out
}

// LabeledCount is how many samples carry a settled outcome - the number
// every learning gate reads (bandit.go) before it is allowed to act.
func LabeledCount(samples []RoutingSample) int {
	n := 0
	for _, s := range samples {
		if s.Labeled() {
			n++
		}
	}
	return n
}
