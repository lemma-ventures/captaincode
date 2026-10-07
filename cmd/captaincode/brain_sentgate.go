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
	seen map[string]bool
}

var sentSessionCache = &sentSessions{}

// sessionIsSent reports whether an opencode session answers a sent turn,
// reading its first user message from the serve once. fetch is a test seam.
func (s *sentSessions) isSent(session string, fetch func(string) (string, error)) bool {
	s.mu.Lock()
	v, ok := s.seen[session]
	s.mu.Unlock()
	if ok {
		return v
	}
	prompt, err := fetch(session)
	if err != nil {
		return false // the serve did not answer: no evidence the turn was sent
	}
	v = captaincode.IsSentTurn(prompt)
	s.mu.Lock()
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if len(s.seen) > 5000 {
		s.seen = map[string]bool{}
	}
	s.seen[session] = v
	s.mu.Unlock()
	return v
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

// gateSentHTTP: POST /v1/gate/sent {"session":"…","tool":"bash","args":{…},"cwd":"…"}
// → {"allow":true} or {"allow":false,"reason":"…"}.
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
	if req.Session == "" || !sentSessionCache.isSent(req.Session, fetch) {
		writeJSON(w, 200, map[string]any{"allow": true})
		return
	}
	a := captaincode.GateAction{Tool: strings.ToLower(req.Tool),
		Command: firstString(req.Args, "command", "cmd", "url", "script"),
		Path:    firstString(req.Args, "filePath", "file_path", "path"), Cwd: req.Cwd, SessionID: req.Session}
	if why := captaincode.SentTurnRefusal(a); why != "" {
		captaincode.AppendInjectionLog(captaincode.InjectionEvent{Channel: "tool", Action: "refused", Detail: a.Tool + ": " + truncate(a.Command+a.Path, 160), Why: why})
		fmt.Printf("captain brain: sent turn %s - %s refused: %s\n", req.Session, a.Tool, why)
		writeJSON(w, 200, map[string]any{"allow": false, "reason": why})
		return
	}
	writeJSON(w, 200, map[string]any{"allow": true})
}
