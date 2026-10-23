package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func signedInboxFixture(t *testing.T) (*brain, pres.Signed, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cose := append([]byte{0xa4, 1, 1, 3, 0x27, 0x20, 6, 0x21, 0x58, 32}, pub...)
	id := pres.Encode([]byte("test-id"))
	s := pres.DefaultStore()
	if e = s.Update(func(c *pres.Config) error {
		c.Machine = "test-machine"
		c.Origins = []string{"http://localhost:8781"}
		c.Folders = map[string]string{"repo": dir}
		c.Users = []pres.User{{Name: "operator", Credentials: []pres.Credential{{ID: id, Key: pres.Encode(cose), Algorithm: "Ed25519", RPID: "localhost", Origin: c.Origins[0], Label: "device"}}}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	p := pres.Signed{Text: "implement the change"}
	p.Envelope = pres.Envelope{V: 1, Machine: "test-machine", Folder: "repo", TextSHA256: pres.HashText(p.Text), IssuedAt: time.Now().Unix() - 1, ExpiresAt: time.Now().Unix() + 120, Nonce: pres.Encode(make([]byte, 32))}
	h, _ := p.Envelope.Hash()
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": pres.Encode(h), "origin": "http://localhost:8781"})
	rp := sha256.Sum256([]byte("localhost"))
	ad := append(rp[:], 5, 0, 0, 0, 0)
	ch := sha256.Sum256(cd)
	signature := ed25519.Sign(key, append(ad, ch[:]...))
	p.Assertion = pres.Assertion{CredentialID: id, AuthenticatorData: pres.Encode(ad), ClientDataJSON: pres.Encode(cd), Signature: pres.Encode(signature)}
	if e = s.Prepare(p.Envelope, p.Text); e != nil {
		t.Fatal(e)
	}
	return teamBrain(), p, dir
}
func TestPresenceSignedInboxTyped(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	b.inbox.admit(dir)
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest("POST", "/v1/inbox", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	items := b.inbox.take(dir, "")
	if len(items) != 1 || items[0].Text != p.Text {
		t.Fatalf("items: %+v", items)
	}
	by, sent, g := b.inbox.notePresenceTurn(dir, p.Text)
	if sent || by != "" || g == nil {
		t.Fatalf("not typed: %q %v %+v", by, sent, g)
	}
	if b.inbox.sent[dir] != 0 {
		t.Fatal("quota not reset")
	}
	w = httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest("POST", "/v1/inbox", bytes.NewReader(raw)))
	if w.Code == 200 {
		t.Fatal("replay")
	}
}
func TestPresenceBadInboxNoMutation(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	p.Text += " edited"
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest("POST", "/v1/inbox", bytes.NewReader(raw)))
	if w.Code == 200 || b.inbox.pending(dir) != 0 {
		t.Fatal(w.Body.String())
	}
}
func TestPresenceUnsignedStillSent(t *testing.T) {
	b, _, dir := signedInboxFixture(t)
	w := sendTo(b, dir, "read a file")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	items := b.inbox.take(dir, "")
	_, sent, g := b.inbox.notePresenceTurn(dir, items[0].Text)
	if !sent || g != nil {
		t.Fatal("unsigned promoted")
	}
	if captaincode.SentTurnRefusal(captaincode.GateAction{Tool: "bash", Command: "git push origin feature"}) == "" {
		t.Fatal("push allowed")
	}
}
func TestPresenceTerminalAndHTTPRefused(t *testing.T) {
	b, _, _ := signedInboxFixture(t)
	for _, route := range []string{"enroll", "import", "remove"} {
		w := httptest.NewRecorder()
		b.presenceHTTP(w, httptest.NewRequest("POST", "/v1/presence/"+route, strings.NewReader(`{}`)))
		if w.Code != 403 {
			t.Fatal(route, w.Code)
		}
	}
	t.Setenv(captaincode.SentTurnEnv, "1")
	for _, args := range [][]string{{"enroll", "operator"}, {"import", "missing.json"}} {
		if e := runPresence(args); e == nil {
			t.Fatal(args)
		}
	}
}
func TestPresenceCredentialFileDenied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(os.Getenv("HOME"), ".config", "captain", "presence.json")
	for _, tool := range []string{"read", "grep", "glob", "write"} {
		if captaincode.SentTurnRefusal(captaincode.GateAction{Tool: tool, Path: p, Cwd: os.Getenv("HOME")}) == "" {
			t.Fatal(tool)
		}
	}
	if !strings.Contains(captaincode.SeatbeltProfile(t.TempDir(), os.Getenv("HOME")), p) {
		t.Fatal("presence file absent from jail")
	}
}
func TestPresenceHTTPInfoAndCORS(t *testing.T) {
	b, _, _ := signedInboxFixture(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/presence/info", nil)
	r.Header.Set("Origin", "http://localhost:8781")
	b.presenceHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "public_key") || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:8781" {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestPresenceHighTierNeedsDecision(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	g, e := pres.DefaultStore().VerifyPrompt(p)
	if e != nil {
		t.Fatal(e)
	}
	sentSessionCache = &sentSessions{}
	b.sessionPromptFn = func(string) (string, error) { return captaincode.PresenceContract(g.ID), nil }
	body, _ := json.Marshal(map[string]any{"session": "signed", "tool": "bash", "args": map[string]string{"command": "git push origin main"}, "cwd": dir})
	w := httptest.NewRecorder()
	b.gateSentHTTP(w, httptest.NewRequest("POST", "/v1/gate/sent", bytes.NewReader(body)))
	if !strings.Contains(w.Body.String(), "signed_decision_required") {
		t.Fatal(w.Body.String())
	}
	ds, e := pres.DefaultStore().Decisions()
	if e != nil || len(ds) != 1 {
		t.Fatal(ds, e)
	}
}

func TestPresenceHeldApprovalExactText(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	b.inbox.hold(inboxItem{ID: "held", Text: p.Text, Dir: dir})
	p.HeldID = "held"
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest("POST", "/v1/inbox", bytes.NewReader(raw)))
	if w.Code != 200 || len(b.inbox.heldFor(dir)) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestPresenceHeldApprovalMismatch(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	b.inbox.hold(inboxItem{ID: "held", Text: p.Text + " edited", Dir: dir})
	p.HeldID = "held"
	before, _ := os.ReadFile(pres.DefaultStore().Path)
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest("POST", "/v1/inbox", bytes.NewReader(raw)))
	after, _ := os.ReadFile(pres.DefaultStore().Path)
	if w.Code == 200 || len(b.inbox.heldFor(dir)) != 1 || !bytes.Equal(before, after) {
		t.Fatal(w.Body.String())
	}
}
func TestPresenceWorkerReceivesTypedTrust(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	t.Setenv("CAPTAIN_COMPACT_AT", "99999999")
	t.Setenv("CAPTAIN_ASSESS_MIN_SCORED", "0")
	t.Setenv("CAPTAIN_ASSESS_REFRESH_EVERY", "0")
	called := false
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		called = true
		if captaincode.IsSentTurn(prompt) || captaincode.PresenceGrantID(prompt) == "" || strings.Contains(prompt, captaincode.PublishGrantMarker) {
			t.Errorf("worker trust was lost")
		}
		return leg, captaincode.Result{Text: "done"}, nil
	}
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest("POST", "/v1/inbox", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	items := b.inbox.take(dir, "")
	body, _ := json.Marshal(oaiChatReq{Model: "grok", Messages: []oaiMessage{{Role: "user", Content: jsonString(items[0].Text)}}})
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set(workspaceHeader, dir)
	w = httptest.NewRecorder()
	b.chatCompletions(w, r)
	if !called {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestPresenceRepeatDoesNotInheritAuthority(t *testing.T) {
	_, p, dir := signedInboxFixture(t)
	g, e := pres.DefaultStore().VerifyPrompt(p)
	if e != nil {
		t.Fatal(e)
	}
	req := oaiChatReq{ws: captaincode.Workspace{Dir: dir, PresenceID: g.ID}}
	r := chatRequestFrom(context.Background(), req)
	if r.Context().Value(presenceRoundKey{}) != g.ID {
		t.Fatal("round lost provenance")
	}
}

func TestPresenceExpiredAtHandoffRefusesExecution(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest("POST", "/v1/inbox", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	b.inbox.take(dir, "")
	if e := pres.DefaultStore().Update(func(c *pres.Config) error {
		for id, g := range c.Grants {
			g.Expires = 0
			c.Grants[id] = g
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	by, sent, g := b.inbox.notePresenceTurn(dir, p.Text)
	if !sent || g != nil || !strings.HasPrefix(by, "presence_refused:") {
		t.Fatalf("expired approval was not refused: %s", by)
	}
}

func TestPresenceBudgetCapNeedsSignedDecision(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	g, e := pres.DefaultStore().VerifyPrompt(p)
	if e != nil {
		t.Fatal(e)
	}
	budget := captaincode.NewBudget("task", "task", captaincode.BudgetOpts{MaxCostUSD: 1})
	budget.Reconcile(0, 1)
	b.ledger.RecordBudget(budget)
	err := b.presenceBudget(captaincode.Workspace{Dir: dir, PresenceID: g.ID}, "task")
	if err == nil || !strings.Contains(err.Error(), "signed_decision_required") {
		t.Fatal(err)
	}
	if b.ledger.BudgetFor("task").MaxCostUSD != 1 {
		t.Fatal("cap changed without signature")
	}
	ds, e := pres.DefaultStore().Decisions()
	if e != nil || len(ds) != 1 || !strings.Contains(ds[0].Action, "increase_task_cap") {
		t.Fatal(ds, e)
	}
}

func TestPresenceDecisionBindsAllToolArguments(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	g, e := pres.DefaultStore().VerifyPrompt(p)
	if e != nil {
		t.Fatal(e)
	}
	sentSessionCache = &sentSessions{}
	b.sessionPromptFn = func(string) (string, error) { return captaincode.PresenceContract(g.ID), nil }
	for _, recipient := range []string{"recipient-a", "recipient-b"} {
		body, _ := json.Marshal(map[string]any{"session": "signed-arguments", "tool": "payment", "args": map[string]any{"recipient": recipient, "amount": 10}, "cwd": dir})
		w := httptest.NewRecorder()
		b.gateSentHTTP(w, httptest.NewRequest("POST", "/v1/gate/sent", bytes.NewReader(body)))
		if !strings.Contains(w.Body.String(), "signed_decision_required") {
			t.Fatal(w.Body.String())
		}
	}
	ds, e := pres.DefaultStore().Decisions()
	if e != nil || len(ds) != 2 {
		t.Fatalf("different actions shared a decision: %d %v", len(ds), e)
	}
}

func TestPresenceHandoffRejectsChangedWhitespace(t *testing.T) {
	b, p, dir := signedInboxFixture(t)
	t.Setenv("CAPTAIN_COMPACT_AT", "99999999")
	t.Setenv("CAPTAIN_ASSESS_MIN_SCORED", "0")
	t.Setenv("CAPTAIN_ASSESS_REFRESH_EVERY", "0")
	b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "unexpected execution"}, nil
	}
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest("POST", "/v1/inbox", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	b.inbox.take(dir, "")
	body, _ := json.Marshal(oaiChatReq{Model: "grok", Messages: []oaiMessage{{Role: "user", Content: jsonString(p.Text + "\n")}}})
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set(workspaceHeader, dir)
	w = httptest.NewRecorder()
	b.chatCompletions(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "presence_refused:") {
		t.Fatalf("changed text was not refused: %d %s", w.Code, w.Body.String())
	}
}

func TestPresenceCredentialFileDeniedThroughParents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, path := range []string{home, filepath.Join(home, ".config"), filepath.Join(home, ".config", "captain")} {
		for _, tool := range []string{"grep", "glob", "read"} {
			if captaincode.SentTurnRefusal(captaincode.GateAction{Tool: tool, Path: path, Cwd: home}) == "" {
				t.Errorf("%s can read presence through %s", tool, path)
			}
		}
	}
}

// runPresenceHook feeds one tool call to the hook and returns what it wrote.
func runPresenceHook(t *testing.T, input string) string {
	t.Helper()
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	_, _ = inW.WriteString(input)
	inW.Close()
	presenceHookRun()
	outW.Close()
	raw, _ := io.ReadAll(outR)
	return string(raw)
}

// A step that needs a signed decision waits for it: the approval lets the
// same command go on in the same turn, and no approval refuses it in time.
func TestPresenceHookWaitsForTheSignedDecision(t *testing.T) {
	_, p, dir := signedInboxFixture(t)
	g, e := pres.DefaultStore().VerifyPrompt(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv(captaincode.PresenceEnv, g.ID)
	oldWait, oldPoll := presenceHookWait, presenceHookPoll
	presenceHookWait, presenceHookPoll = 3*time.Second, 50*time.Millisecond
	defer func() { presenceHookWait, presenceHookPoll = oldWait, oldPoll }()
	call, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": "git push origin main"}, "cwd": dir})

	approved := make(chan struct{})
	found := false
	go func() {
		defer close(approved)
		for i := 0; i < 100; i++ {
			time.Sleep(20 * time.Millisecond)
			ds, _ := pres.DefaultStore().Decisions()
			if len(ds) == 1 {
				_ = pres.DefaultStore().Update(func(c *pres.Config) error {
					d := c.Decisions[ds[0].ID]
					d.Approved = true
					c.Decisions[ds[0].ID] = d
					return nil
				})
				found = true
				return
			}
		}
	}()
	out := runPresenceHook(t, string(call))
	<-approved
	if !found {
		t.Fatal("the push to main created no decision")
	}
	if strings.Contains(out, "deny") {
		t.Fatalf("an approved step goes on: %s", out)
	}

	presenceHookWait = 300 * time.Millisecond
	other, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": "git push origin main --tags"}, "cwd": dir})
	out = runPresenceHook(t, string(other))
	if !strings.Contains(out, "signed_decision_required") {
		t.Fatalf("a step nobody approves is refused: %s", out)
	}
}
