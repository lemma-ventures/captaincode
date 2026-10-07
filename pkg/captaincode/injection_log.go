package captaincode

// The injection audit log: every finding, hold, release and refusal of the
// prompt-injection defences, one JSON line each, in
// ~/.captaincode/injection.jsonl. `captain inbox --log` prints it. What was
// caught is the evidence for tuning the screen; what was let through after a
// finding is the evidence for its false alarms.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// InjectionEvent is one decision of the defences.
type InjectionEvent struct {
	At       time.Time `json:"at"`
	Channel  string    `json:"channel"`        // inbox, handoff, context, tool
	Action   string    `json:"action"`         // held, flagged, sanitized, released, dropped, refused
	From     string    `json:"from,omitempty"` // the sender, when known
	Dir      string    `json:"dir,omitempty"`  // the folder it was for
	Findings []Finding `json:"findings,omitempty"`
	Detail   string    `json:"detail,omitempty"` // a short excerpt
	Why      string    `json:"why,omitempty"`    // a refusal's reason
}

// InjectionLogPath is CAPTAIN_INJECTION_LOG, else ~/.captaincode/injection.jsonl.
func InjectionLogPath() string {
	if p := os.Getenv("CAPTAIN_INJECTION_LOG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".captaincode", "injection.jsonl")
}

// AppendInjectionLog records one event; a write failure is not the caller's
// problem.
func AppendInjectionLog(e InjectionEvent) {
	path := InjectionLogPath()
	if path == "" {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(e)
	_, _ = f.Write(append(line, '\n'))
}

// ReadInjectionLog returns the last n events (n <= 0: all).
func ReadInjectionLog(n int) []InjectionEvent {
	f, err := os.Open(InjectionLogPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []InjectionEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e InjectionEvent
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
