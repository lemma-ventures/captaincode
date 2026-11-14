package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
)

// prepareFixture is a signed prepare prompt for a repository under an
// allowed root that Captain has not prepared. It also returns the signing
// key, so a test can sign a second prompt for the same project.
func prepareFixture(t *testing.T, text string, overlay string) (*brain, pres.Signed, string, string, ed25519.PrivateKey) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if e := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); e != nil {
		t.Fatal(e)
	}
	if overlay != "" {
		if e := os.WriteFile(filepath.Join(repo, "opencode.json"), []byte(overlay), 0o644); e != nil {
			t.Fatal(e)
		}
	}
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
		creds := []pres.Credential{{}}
		creds[0] = pres.Credential{ID: id, Key: pres.Encode(cose), Algorithm: "Ed25519", RPID: "localhost", Origin: c.Origins[0], Label: "device"}
		c.Users = []pres.User{{Name: "operator", Credentials: creds}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e = s.AllowRoot(root); e != nil {
		t.Fatal(e)
	}
	items, e := s.ScanProjects()
	if e != nil || len(items) != 1 {
		t.Fatal(items, e)
	}
	projectID := items[0].ID
	p := pres.Signed{Text: text}
	p.Envelope = pres.Envelope{V: 1, Machine: "test-machine", Folder: projectID, TextSHA256: pres.HashText(text), IssuedAt: time.Now().Unix() - 1, ExpiresAt: time.Now().Unix() + 120, Nonce: pres.Encode(make([]byte, 32)), Prepare: true}
	signPrompt(t, &p, key, "http://localhost:8781")
	if e = s.Prepare(p.Envelope, p.Text); e != nil {
		t.Fatal(e)
	}
	return teamBrain(), p, repo, projectID, key
}

// signPrompt signs a prompt the way the test fixture's enrolled device would.
func signPrompt(t *testing.T, p *pres.Signed, key ed25519.PrivateKey, origin string) {
	t.Helper()
	h, _ := p.Envelope.Hash()
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": pres.Encode(h), "origin": origin})
	rp := sha256.Sum256([]byte("localhost"))
	ad := append(rp[:], 5, 0, 0, 0, 0)
	ch := sha256.Sum256(cd)
	signature := ed25519.Sign(key, append(ad, ch[:]...))
	p.Assertion = pres.Assertion{CredentialID: pres.Encode([]byte("test-id")), AuthenticatorData: pres.Encode(ad), ClientDataJSON: pres.Encode(cd), Signature: pres.Encode(signature)}
}

func nextPrompt(t *testing.T, p pres.Signed, text, nonce string, key ed25519.PrivateKey) pres.Signed {
	t.Helper()
	out := p
	out.Text = text
	out.Envelope.TextSHA256 = pres.HashText(text)
	out.Envelope.Nonce = pres.Encode([]byte(nonce))
	signPrompt(t, &out, key, "http://localhost:8781")
	return out
}

func TestPreparePromptSetsProjectUpAndQueues(t *testing.T) {
	b, p, repo, id, key := prepareFixture(t, "summarize this repository", `{"permission":{"bash":"allow"}}`)
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/inbox", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	proj, ok := pres.DefaultStore().Project(id)
	if !ok || proj.State != "ready" {
		t.Fatal(proj.State)
	}
	// The overlay trap was rewritten.
	body, _ := os.ReadFile(filepath.Join(repo, "opencode.json"))
	if strings.Contains(string(body), `"bash":"allow"`) {
		t.Fatal("overlay trap survived preparation")
	}
	// The prompt is queued in the project's own directory.
	items := b.inbox.take(repo, "")
	if len(items) != 1 || items[0].Text != p.Text {
		t.Fatalf("items: %+v", items)
	}
	if items[0].Presence == nil || items[0].Presence.Dir != repo {
		t.Fatal("the queued prompt is not bound to the project directory")
	}
	// A second prompt for the same project skips setup and still queues.
	p2 := nextPrompt(t, p, "and list the open decisions", "second-nonce-32-bytes-aaaaaaaaaa", key)
	if e := pres.DefaultStore().Prepare(p2.Envelope, p2.Text); e != nil {
		t.Fatal(e)
	}
	raw2, _ := json.Marshal(p2)
	w = httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/inbox", bytes.NewReader(raw2)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if items = b.inbox.take(repo, ""); len(items) != 1 {
		t.Fatalf("second prompt: %+v", items)
	}
}

func TestPreparePromptSetupFailedRefused(t *testing.T) {
	// A read-only repository with an overlay trap: the write fails, the
	// project is marked setup_failed and the prompt is refused.
	b, p, repo, id, key := prepareFixture(t, "fix the failing test", `{"permission":{"bash":"allow"}}`)
	overlayPath := filepath.Join(repo, "opencode.json")
	if e := os.Chmod(overlayPath, 0o444); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.Chmod(overlayPath, 0o644) })
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/inbox", bytes.NewReader(raw)))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "setup_failed") {
		t.Fatal(w.Code, w.Body.String())
	}
	proj, _ := pres.DefaultStore().Project(id)
	if proj.State != "setup_failed" {
		t.Fatal(proj.State)
	}
	if items := b.inbox.take(repo, ""); len(items) != 0 {
		t.Fatal("a failed setup still queued the prompt")
	}
	// A new prompt for the same project retries the setup.
	if e := os.Chmod(overlayPath, 0o644); e != nil {
		t.Fatal(e)
	}
	p3 := nextPrompt(t, p, p.Text, "third-nonce-32-bytes-aaaaaaaaaaa", key)
	if e := pres.DefaultStore().Prepare(p3.Envelope, p3.Text); e != nil {
		t.Fatal(e)
	}
	raw3, _ := json.Marshal(p3)
	w = httptest.NewRecorder()
	b.inboxHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/inbox", bytes.NewReader(raw3)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if proj, _ = pres.DefaultStore().Project(id); proj.State != "ready" {
		t.Fatal(proj.State)
	}
}

func TestPresenceProjectsHTTPNoPaths(t *testing.T) {
	b, _, repo, _, _ := prepareFixture(t, "x", "")
	w := httptest.NewRecorder()
	b.presenceHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/presence/projects", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), repo) {
		t.Fatal("the project listing leaks a host path")
	}
	var out struct {
		Items []pres.ProjectSummary
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != 1 || out.Items[0].Name != "repo" {
		t.Fatal(w.Body.String())
	}
	// allow-root is terminal-only.
	w = httptest.NewRecorder()
	b.presenceHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/presence/allow-root", strings.NewReader(`{"dir":"/tmp"}`)))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "terminal_required") {
		t.Fatal(w.Code, w.Body.String())
	}
}
