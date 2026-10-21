package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// What a watched round sends to the TUI. A /repeat that is watched streams
// every round into one assistant message, and the TUI redraws the growing
// reasoning block on every line. Live 2026-10-19: a /repeat ran five
// rounds over 7h40m; round 5 alone was 1,545 lines (863 tool calls) and the
// message held 540 KB of reasoning when the TUI stopped answering keys.
// The worker log keeps every line; the watcher gets the readable part.

// watchRoundBytes caps what one round sends to a watcher.
const watchRoundBytes = 30 << 10

var heartbeatTools = regexp.MustCompile(`· (\d+) tool calls`)

// watchFilter decides, line by line, what of a round's live feed a watcher
// sees. One filter serves one round; reset starts the next.
type watchFilter struct {
	bytes    int
	held     int  // lines kept back by the cap
	narrated bool // the round's narration ("≡ …") has started
	beat     string
}

func (f *watchFilter) reset() { *f = watchFilter{} }

// keep reports whether line s goes to the watcher.
//   - Tool calls and their results ("⚙", "↳") stay in the worker log once
//     the round's narration runs, since the narration covers them.
//   - A heartbeat ("… still working") goes out only when the tool count
//     has changed since the last one shown.
//   - Past watchRoundBytes, nothing goes out; held counts what was kept back.
func (f *watchFilter) keep(s string) bool {
	switch {
	case strings.HasPrefix(s, "≡"):
		f.narrated = true
	case strings.HasPrefix(s, "⚙"), strings.HasPrefix(s, "↳"):
		if f.narrated {
			return false
		}
	case strings.HasPrefix(s, "…") && strings.Contains(s, "still working"):
		tools := "0"
		if m := heartbeatTools.FindStringSubmatch(s); m != nil {
			tools = m[1]
		}
		if tools == f.beat {
			return false
		}
		f.beat = tools
	}
	if f.bytes+len(s) > watchRoundBytes {
		f.held++
		return false
	}
	f.bytes += len(s)
	return true
}

// heldNote is the line that replaces what the cap kept back, or "".
func (f *watchFilter) heldNote(since time.Time) string {
	if f.held == 0 {
		return ""
	}
	where := "the round's worker log (`captain runs`)"
	if name := roundLogName(since); name != "" {
		where = "`runs/" + name + "`"
	}
	return fmt.Sprintf("… %d more lines in %s", f.held, where)
}

// roundLogName is the first worker log started at or after since: the log of
// the round that started then. "" when there is none.
func roundLogName(since time.Time) string {
	dir := workerLogsDir()
	if dir == "" || since.IsZero() {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	floor := since.Truncate(time.Second)
	best, bestAt := "", time.Time{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".log") {
			continue
		}
		if info, err := e.Info(); err != nil || info.ModTime().Before(floor) {
			continue
		}
		at := logStarted(filepath.Join(dir, name))
		if at.IsZero() || at.Before(floor) {
			continue
		}
		if best == "" || at.Before(bestAt) {
			best, bestAt = name, at
		}
	}
	return best
}

// logStarted reads the "started:" line of a worker log's header.
func logStarted(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for i := 0; i < 6 && sc.Scan(); i++ {
		if v, ok := strings.CutPrefix(sc.Text(), "started: "); ok {
			at, _ := time.Parse(time.RFC3339, strings.TrimSpace(v))
			return at
		}
	}
	return time.Time{}
}
