package captaincode

// The journal as memory. state.json keeps the last 500 run events, about a
// week of work, and the scorecards learned from those alone, so a leg's speed
// and reliability forgot everything older than a week. The routing journal
// keeps every event, and from here on it rotates by month (routing-2026-10
// .jsonl beside routing.jsonl) instead of dropping its oldest half when it
// grows: four of the first twelve weeks were lost that way or never written
// ("Twelve Weeks of Routing", 2026-10-10). The scorecards read the last
// CAPTAIN_STATS_DAYS (90) of it.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// statsWindow is how far back the scorecards read the journal.
func statsWindow() time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CAPTAIN_STATS_DAYS"))); err == nil && n > 0 {
		return time.Duration(n) * 24 * time.Hour
	}
	return 90 * 24 * time.Hour
}

// journalFiles are the journal's files, oldest first: the monthly archives,
// then the live file.
func journalFiles(live string) []string {
	if live == "" {
		return nil
	}
	archives, _ := filepath.Glob(filepath.Join(filepath.Dir(live), "routing-*.jsonl"))
	sort.Strings(archives)
	return append(archives, live)
}

// rotateJournal moves the live journal to its month's archive when the
// month it was last written in has ended, or when it outgrew the size cap
// (then a numbered archive for the same month). Nothing is dropped.
func rotateJournal(p string, now time.Time) {
	st, err := os.Stat(p)
	if err != nil {
		return
	}
	month := st.ModTime().Format("2006-01")
	if month == now.Format("2006-01") && st.Size() <= routingLogMaxBytes() {
		return
	}
	dir := filepath.Dir(p)
	dest := filepath.Join(dir, "routing-"+month+".jsonl")
	for i := 2; ; i++ {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			break
		}
		dest = filepath.Join(dir, fmt.Sprintf("routing-%s-%d.jsonl", month, i))
	}
	_ = os.Rename(p, dest)
}

// journalEvents reads the run events of every journal file written since
// cutoff.
func journalEvents(live string, since time.Time) []Event {
	ev, _ := journalRecords(live, since)
	return ev
}

// journalRecords reads the run events and the outcomes of every journal
// file written since cutoff.
func journalRecords(live string, since time.Time) ([]Event, []OutcomeEvidence) {
	var out []Event
	var outs []OutcomeEvidence
	for _, f := range journalFiles(live) {
		if st, err := os.Stat(f); err != nil || st.ModTime().Before(since) {
			continue // an archive last written before the window holds nothing in it
		}
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			line := sc.Bytes()
			// Skip decisions before decoding them: they are most of the bytes.
			head := string(line[:min(len(line), 40)])
			if !strings.Contains(head, `"kind":"event"`) && !strings.Contains(head, `"kind":"outcome"`) {
				continue
			}
			var r RoutingRecord
			if json.Unmarshal(line, &r) != nil || r.At.Before(since) {
				continue
			}
			if r.Event != nil {
				out = append(out, *r.Event)
			}
			if r.Outcome != nil {
				outs = append(outs, *r.Outcome)
			}
		}
		fh.Close()
	}
	return out, outs
}

// historyCache holds the journal's events between reads: Stats runs on every
// route, and the journal changes only when a run ends.
type historyCache struct {
	key      string
	events   []Event
	outcomes []OutcomeEvidence
}

// statsEvents is what the scorecards learn from: the journal's last
// statsWindow of events, with the ring buffer's own on top (an in-memory or
// fresh ledger has only those). One event appears in both; it counts once.
func (l *Ledger) statsEvents() []Event {
	if !l.loadHistory() {
		return l.Events
	}
	type id struct {
		at       int64
		leg      Leg
		task, ty string
	}
	seen := map[id]bool{}
	out := make([]Event, 0, len(l.history.events)+len(l.Events))
	for _, set := range [][]Event{l.history.events, l.Events} {
		for _, e := range set {
			k := id{e.At.UnixNano(), e.Leg, e.TaskID, e.Task}
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// loadHistory refreshes the journal cache when a journal file changed.
// false when this ledger has no journal.
func (l *Ledger) loadHistory() bool {
	live := l.journalPath()
	if live == "" {
		return false
	}
	window := statsWindow()
	key := window.String() + ";"
	for _, f := range journalFiles(live) {
		if st, err := os.Stat(f); err == nil {
			key += fmt.Sprintf("%s:%d:%d;", f, st.Size(), st.ModTime().UnixNano())
		}
	}
	if l.history == nil || l.history.key != key {
		ev, outs := journalRecords(live, time.Now().Add(-window))
		l.history = &historyCache{key: key, events: ev, outcomes: outs}
	}
	return true
}

// statsOutcomes is the outcomes the scorecards read: the journal's, then
// the ring buffer's (the later state of a task wins in foldOutcomes).
func (l *Ledger) statsOutcomes() []OutcomeEvidence {
	if !l.loadHistory() {
		return l.Outcomes
	}
	return append(append([]OutcomeEvidence(nil), l.history.outcomes...), l.Outcomes...)
}

// ImportEvents appends to the journal the run events of older state files
// (state.json backups) that it does not already hold, into an archive of
// their own (routing-import.jsonl), so the scorecards and the reports see
// them. Returns how many it added.
func (l *Ledger) ImportEvents(states ...string) (int, error) {
	live := l.journalPath()
	if live == "" {
		return 0, fmt.Errorf("no routing journal for this ledger")
	}
	have := map[string]bool{}
	key := func(e Event) string { return fmt.Sprintf("%d|%s|%s", e.At.UnixNano(), e.Leg, e.Task) }
	for _, e := range journalEvents(live, time.Time{}) {
		have[key(e)] = true
	}
	var add []Event
	for _, p := range states {
		raw, err := os.ReadFile(p)
		if err != nil {
			return 0, err
		}
		var st struct {
			Events []Event `json:"events"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			return 0, fmt.Errorf("%s: %w", p, err)
		}
		for _, e := range st.Events {
			if e.At.IsZero() || have[key(e)] {
				continue
			}
			have[key(e)] = true
			add = append(add, e)
		}
	}
	if len(add) == 0 {
		return 0, nil
	}
	sort.SliceStable(add, func(i, j int) bool { return add[i].At.Before(add[j].At) })
	f, err := os.OpenFile(filepath.Join(filepath.Dir(live), "routing-import.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	for i := range add {
		line, err := json.Marshal(RoutingRecord{Kind: RoutingKindEvent, At: add[i].At, TaskID: add[i].TaskID, Event: &add[i]})
		if err != nil {
			continue
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return 0, err
		}
	}
	l.history = nil
	return len(add), nil
}
