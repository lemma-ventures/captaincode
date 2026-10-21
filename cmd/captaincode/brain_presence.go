package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func presenceError(w http.ResponseWriter, err error) {
	code := pres.Code(err)
	fmt.Printf("captain presence: refused %s\n", code)
	writeJSON(w, 403, map[string]any{"error": code})
}
func presenceCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	c, e := pres.DefaultStore().Read()
	if e != nil || !pres.Allowed(c.Origins, origin) {
		writeJSON(w, 403, map[string]string{"error": "origin_not_allowed"})
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
	}
	return true
}
func (b *brain) presenceHTTP(w http.ResponseWriter, r *http.Request) {
	if !presenceCORS(w, r) {
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(204)
		return
	}
	s := pres.DefaultStore()
	route := strings.TrimPrefix(r.URL.Path, "/v1/presence/")
	if route == "enroll" || route == "import" || route == "remove" {
		writeJSON(w, 403, map[string]string{"error": "terminal_required"})
		return
	}
	switch route {
	case "info":
		if r.Method != http.MethodGet {
			writeErr(w, 405, "GET")
			return
		}
		info, e := s.Info()
		if e != nil {
			presenceError(w, e)
			return
		}
		writeJSON(w, 200, info)
	case "prepare":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "POST")
			return
		}
		var p struct {
			Envelope pres.Envelope `json:"envelope"`
			Text     string        `json:"text"`
		}
		if e := pres.ReadJSON(r.Body, &p); e != nil {
			presenceError(w, e)
			return
		}
		findings := captaincode.ScanInjection(p.Text)
		if e := s.ValidatePrompt(p.Envelope, p.Text); e != nil {
			presenceError(w, e)
			return
		}
		screen := "patterns; model screen disabled"
		if judgeEnabled() {
			verdict := b.judgePanel(p.Text)
			screen = verdict.Why
			if verdict.Hold {
				findings = append(findings, captaincode.Finding{Kind: "model judge", Severity: captaincode.SevHigh, Level: "high", Excerpt: verdict.Why})
			}
		}
		if e := s.Prepare(p.Envelope, p.Text); e != nil {
			presenceError(w, e)
			return
		}
		h, _ := p.Envelope.Hash()
		writeJSON(w, 200, map[string]any{"challenge": pres.Encode(h), "text": p.Text, "findings": findings, "screen": screen})
	case "register":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "POST")
			return
		}
		var reg pres.Registration
		if e := pres.ReadJSON(r.Body, &reg); e != nil {
			presenceError(w, e)
			return
		}
		if e := s.Register(reg); e != nil {
			presenceError(w, e)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "decisions":
		if r.Method != http.MethodGet {
			writeErr(w, 405, "GET")
			return
		}
		ds, e := s.Decisions()
		if e != nil {
			presenceError(w, e)
			return
		}
		c, e := s.Read()
		if e != nil {
			presenceError(w, e)
			return
		}
		items := []any{}
		for _, d := range ds {
			items = append(items, map[string]any{"decision": d, "challenge": pres.Encode(pres.DecisionChallenge(c.Machine, d))})
		}
		writeJSON(w, 200, map[string]any{"items": items})
	case "decision":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "POST")
			return
		}
		var d struct {
			ID        string         `json:"id"`
			Assertion pres.Assertion `json:"assertion"`
		}
		if e := pres.ReadJSON(r.Body, &d); e != nil {
			presenceError(w, e)
			return
		}
		if e := s.ApproveDecision(d.ID, d.Assertion); e != nil {
			presenceError(w, e)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		writeErr(w, 404, "unknown presence route")
	}
}
func (b *brain) signedInbox(w http.ResponseWriter, p pres.Signed) {
	b.inbox.mu.Lock()
	defer b.inbox.mu.Unlock()
	held := -1
	if p.HeldID != "" {
		c, e := pres.DefaultStore().Read()
		if e != nil {
			presenceError(w, e)
			return
		}
		for i, it := range b.inbox.held {
			if it.ID == p.HeldID && it.Text == p.Text && it.Dir == c.Folders[p.Envelope.Folder] {
				held = i
				break
			}
		}
		if held < 0 {
			writeJSON(w, 403, map[string]string{"error": "held_text_mismatch"})
			return
		}
	}
	g, e := pres.DefaultStore().VerifyPrompt(p)
	if e != nil {
		presenceError(w, e)
		return
	}
	if held >= 0 {
		b.inbox.held = append(b.inbox.held[:held:held], b.inbox.held[held+1:]...)
	}
	if b.inbox.items == nil {
		b.inbox.items = map[string][]inboxItem{}
	}
	b.inbox.seq++
	it := inboxItem{ID: fmt.Sprintf("in_%d_%d", time.Now().Unix(), b.inbox.seq), Text: p.Text, At: time.Now(), Dir: g.Dir, Presence: &g}
	b.inbox.items[g.Dir] = append(b.inbox.items[g.Dir], it)
	fmt.Printf("captain presence: verified user=%q device=%q machine=%q folder=%q text_sha256=%s\n", g.User, g.Label, g.Machine, g.Folder, g.TextSHA256)
	writeJSON(w, 200, map[string]any{"ok": true, "id": it.ID, "pending": len(b.inbox.items[g.Dir]), "trust": "typed"})
}
func (b *brain) detectSignedInbox(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodOptions {
		if presenceCORS(w, r) {
			w.WriteHeader(204)
		}
		return true
	}
	if r.Method != http.MethodPost {
		return false
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, pres.MaxBody+1))
	if e != nil || len(raw) > pres.MaxBody {
		writeErr(w, 413, "request too large")
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		writeErr(w, 400, "invalid JSON")
		return true
	}
	_, hasEnvelope := fields["envelope"]
	_, hasCredential := fields["credential_id"]
	_, hasSignature := fields["signature"]
	if hasEnvelope || hasCredential || hasSignature {
		if !presenceCORS(w, r) {
			return true
		}
		var p pres.Signed
		if e := pres.ReadJSON(bytes.NewReader(raw), &p); e != nil {
			presenceError(w, e)
			return true
		}
		b.signedInbox(w, p)
		return true
	}
	if r.Header.Get("Origin") != "" && !localOrigin(r.Header.Get("Origin")) {
		writeErr(w, 403, "signature required")
		return true
	}
	return false
}

type presenceRoundKey struct{}

func presenceHookRun() {
	var in struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
		Cwd       string         `json:"cwd"`
	}
	decoder := json.NewDecoder(os.Stdin)
	decoder.UseNumber()
	err := decoder.Decode(&in)
	if err == nil {
		err = captaincode.PresenceActionForHook(os.Getenv(captaincode.PresenceEnv), presenceToolAction(in.ToolName, in.ToolInput, gateCwd(in.Cwd)))
	}
	if err != nil {
		writeStdoutJSON(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": err.Error()}})
	}
}

func (b *brain) presenceBudget(ws captaincode.Workspace, taskID string) error {
	if ws.PresenceID == "" || taskID == "" {
		return nil
	}
	var snapshot captaincode.Budget
	b.withBudget(taskID, func(budget *captaincode.Budget) { snapshot = *budget })
	if snapshot.MaxCostUSD <= 0 || snapshot.SettledCostUSD+snapshot.ReservedCostUSD < snapshot.MaxCostUSD {
		return nil
	}
	next := snapshot.MaxCostUSD + max(snapshot.MaxCostUSD, snapshot.SettledCostUSD+snapshot.ReservedCostUSD)
	if math.IsNaN(next) || math.IsInf(next, 0) {
		return fmt.Errorf("invalid task cost cap")
	}
	action, _ := json.Marshal(struct {
		Kind     string  `json:"kind"`
		Task     string  `json:"task"`
		From     float64 `json:"from_usd"`
		To       float64 `json:"to_usd"`
		Spent    float64 `json:"settled_usd"`
		Reserved float64 `json:"reserved_usd"`
	}{"increase_task_cap", taskID, snapshot.MaxCostUSD, next, snapshot.SettledCostUSD, snapshot.ReservedCostUSD})
	if e := pres.DefaultStore().CheckAction(ws.PresenceID, string(action)); e != nil {
		return e
	}
	var err error
	b.withBudget(taskID, func(budget *captaincode.Budget) {
		if budget.MaxCostUSD != snapshot.MaxCostUSD || budget.SettledCostUSD != snapshot.SettledCostUSD || budget.ReservedCostUSD != snapshot.ReservedCostUSD {
			err = fmt.Errorf("budget changed; a new signed decision is required")
			return
		}
		budget.MaxCostUSD = next
		if budget.StopReason == captaincode.StopCostExhausted {
			budget.StoppedAt = time.Time{}
			budget.StopReason = ""
		}
		b.ledger.RecordBudget(budget)
		err = b.ledger.Save()
	})
	return err
}

func presenceToolAction(tool string, args map[string]any, cwd string) captaincode.GateAction {
	a := captaincode.GateAction{Tool: strings.ToLower(tool), Command: firstString(args, "command", "cmd", "url", "script"), Path: firstString(args, "file_path", "filePath", "path", "notebook_path"), Cwd: cwd}
	switch a.Tool {
	case "bash", "shell", "run", "execute", "terminal":
	default:
		raw, _ := json.Marshal(args)
		a.Command = string(raw)
	}
	return a
}
