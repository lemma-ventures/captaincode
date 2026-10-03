package captaincode

// Which task an opencode session served, and when. The opencode workers share
// one `opencode serve`, so no task identity reaches the process that screens
// their tools: every screening they made was unsettleable, and the gate could
// never be calibrated (0 of 399 settled, 2026-10-03). The session does reach
// it. The brain files one row per worker run - session, task, start, end - and
// the report joins a screening to the task whose run held its session at that
// moment. Nothing is looked up while a tool waits.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GateSession is one worker run's hold on an opencode session.
type GateSession struct {
	Session string    `json:"session"`
	TaskID  string    `json:"task_id"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

// GateSessionsPath is where the brain files them.
func GateSessionsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "gate-sessions.jsonl")
}

// AppendGateSession files one run. Best effort: a run is never failed for
// its bookkeeping.
func AppendGateSession(s GateSession) {
	if s.Session == "" || s.TaskID == "" {
		return
	}
	p := GateSessionsPath()
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(s)
	f.Write(append(b, '\n'))
}

func readGateSessions() []GateSession {
	raw, err := os.ReadFile(GateSessionsPath())
	if err != nil {
		return nil
	}
	var out []GateSession
	for _, line := range strings.Split(string(raw), "\n") {
		var s GateSession
		if json.Unmarshal([]byte(line), &s) == nil && s.Session != "" {
			out = append(out, s)
		}
	}
	return out
}

// gateSessionSlack covers the clocks of two processes and a tool call that
// lands just after the run's own end is stamped.
const gateSessionSlack = 5 * time.Second

// ResolveGateTasks gives a task identity to every screening that has a
// session but no task: the task whose run held that session when the
// screening was made. It returns how many it resolved.
func ResolveGateTasks(rows []ShadowRecord) int {
	sessions := readGateSessions()
	if len(sessions) == 0 {
		return 0
	}
	n := 0
	for i := range rows {
		r := &rows[i]
		if r.TaskID != "" || r.SessionID == "" {
			continue
		}
		for _, s := range sessions {
			if s.Session == r.SessionID && !r.At.Before(s.Start.Add(-gateSessionSlack)) && !r.At.After(s.End.Add(gateSessionSlack)) {
				r.TaskID = s.TaskID
				n++
				break
			}
		}
	}
	return n
}
