package main

// The sent-turn policy at the opencode tool boundary (pkg sentpolicy.go).
// opencode workers share one serve, so no per-run environment can say a turn
// was sent; the session can. The plugin asks before each tool call that can
// change the machine or reach off it; the brain reads the session's worker
// prompt once, from the serve, and applies the policy when it carries the
// sent-turn provenance line.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type sentSessions struct {
	mu   sync.Mutex
	seen map[string]sessionFacts
}

// sessionFacts is what a worker session's prompt says about its turn: sent
// by another agent, a release asked for, a checkout already dirty.
type sessionFacts struct {
	sent, mayPublish, dirty bool
}

var sentSessionCache = &sentSessions{}

// facts reads an opencode session's worker prompt once, from the serve.
// fetch is a test seam.
func (s *sentSessions) facts(session string, fetch func(string) (string, error)) sessionFacts {
	s.mu.Lock()
	v, ok := s.seen[session]
	s.mu.Unlock()
	if ok {
		return v
	}
	prompt, err := fetch(session)
	if err != nil {
		// The serve did not answer: no evidence the turn was typed by the
		// user either. Fail closed for this call, and ask again next time.
		fmt.Printf("captain brain: sent-turn gate - could not read session %s (%v); applying the sent-turn policy to this call\n", session, err)
		return sessionFacts{sent: true, dirty: true}
	}
	v = sessionFacts{sent: captaincode.IsSentTurn(prompt),
		mayPublish: strings.Contains(prompt, captaincode.PublishGrantMarker),
		dirty:      strings.Contains(prompt, captaincode.DirtyCheckoutMarker)}
	s.mu.Lock()
	if s.seen == nil || len(s.seen) > 5000 {
		s.seen = map[string]sessionFacts{}
	}
	s.seen[session] = v
	s.mu.Unlock()
	return v
}

// isSent reports whether an opencode session answers a sent turn.
func (s *sentSessions) isSent(session string, fetch func(string) (string, error)) bool {
	return s.facts(session, fetch).sent
}

// firstUserPrompt reads the first user message of an opencode session.
func firstUserPrompt(session string) (string, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/session/%s/message", opencodePort, session))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var msgs []struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return "", err
	}
	for _, m := range msgs {
		if m.Info.Role != "user" {
			continue
		}
		var sb strings.Builder
		for _, p := range m.Parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String(), nil
	}
	return "", nil
}

// sessionDirectory reads the folder an opencode session works in.
func sessionDirectory(session string) (string, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/session/%s", opencodePort, session))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var s struct {
		Directory string `json:"directory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", err
	}
	return s.Directory, nil
}

// gateSentHTTP: POST /v1/gate/sent {"session":"…","tool":"bash","args":{…},"cwd":"…"}
// → {"allow":true}, {"allow":true,"jail":[…prefix]} (run the shell command
// as prefix + one quoted argument, pkg jail.go), or {"allow":false,"reason":"…"}.
// A refusal also closes the folder's inbox (tripwire).
func (b *brain) gateSentHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST")
		return
	}
	var req struct {
		Session string         `json:"session"`
		Tool    string         `json:"tool"`
		Args    map[string]any `json:"args"`
		Cwd     string         `json:"cwd"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	fetch := firstUserPrompt
	if b.sessionPromptFn != nil {
		fetch = b.sessionPromptFn
	}
	if req.Session == "" {
		writeJSON(w, 200, map[string]any{"allow": true})
		return
	}
	f := sentSessionCache.facts(req.Session, fetch)
	a := captaincode.GateAction{Tool: strings.ToLower(req.Tool),
		Command: firstString(req.Args, "command", "cmd", "url", "script"),
		Path:    firstString(req.Args, "filePath", "file_path", "path"), Cwd: req.Cwd, SessionID: req.Session}
	if !f.sent {
		// Every worker: no unrequested release, no bulk staging over other
		// sessions' changes (pkg workerguard.go).
		if a.Tool == "bash" || a.Tool == "shell" {
			if why := captaincode.WorkerGuardRefusal(a.Command, f.mayPublish, f.dirty); why != "" {
				captaincode.AppendConduct(captaincode.ConductEvent{Rule: why, Command: a.Command, Dir: a.Cwd, Session: req.Session})
				fmt.Printf("captain brain: worker guard - session %s refused: %s\n", req.Session, why)
				writeJSON(w, 200, map[string]any{"allow": false, "reason": why})
				return
			}
		}
		writeJSON(w, 200, map[string]any{"allow": true})
		return
	}
	dirFn := sessionDirectory
	if b.sessionDirFn != nil {
		dirFn = b.sessionDirFn
	}
	if d, err := dirFn(req.Session); err == nil && d != "" {
		a.Cwd = d
	}
	why, jail := captaincode.SentTurnDecision(a)
	if why != "" {
		captaincode.AppendInjectionLog(captaincode.InjectionEvent{Channel: "tool", Action: "refused", Detail: a.Tool + ": " + truncate(a.Command+a.Path, 160), Why: why})
		fmt.Printf("captain brain: sent turn %s - %s refused: %s\n", req.Session, a.Tool, why)
		b.tripInbox(a.Cwd, a.Tool, why)
		writeJSON(w, 200, map[string]any{"allow": false, "reason": why})
		return
	}
	if jail {
		writeJSON(w, 200, map[string]any{"allow": true, "jail": captaincode.JailPrefix(a.Cwd)})
		return
	}
	writeJSON(w, 200, map[string]any{"allow": true})
}
