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

// loginFixture enrolls an owner and points PATH at a fake codex that prints
// a device URL and code, then waits like a real device login would.
func loginFixture(t *testing.T, script string) (*brain, ed25519.PrivateKey) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	if e := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cose := append([]byte{0xa4, 1, 1, 3, 0x27, 0x20, 6, 0x21, 0x58, 32}, pub...)
	id := pres.Encode([]byte("login-test-id"))
	s := pres.DefaultStore()
	if e = s.Update(func(c *pres.Config) error {
		c.Machine = "test-machine"
		c.Origins = []string{"http://localhost:8781"}
		cred := pres.Credential{Algorithm: "Ed25519", RPID: "localhost", Origin: "http://localhost:8781", Label: "device"}
		cred.ID = id
		cred.Key = pres.Encode(cose)
		user := pres.User{Name: "operator"}
		user.Credentials = append(user.Credentials, cred)
		c.Users = append(c.Users, user)
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return teamBrain(), key
}

// signLogin signs a repair assertion the way the enrolled device would.
func signLogin(t *testing.T, key ed25519.PrivateKey, challenge []byte, count uint32) pres.Assertion {
	t.Helper()
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": pres.Encode(challenge), "origin": "http://localhost:8781"})
	rp := sha256.Sum256([]byte("localhost"))
	ad := append(rp[:], 5, 0, 0, 0, 0)
	ad[33], ad[34], ad[35], ad[36] = byte(count>>24), byte(count>>16), byte(count>>8), byte(count)
	ch := sha256.Sum256(cd)
	sig := ed25519.Sign(key, append(ad, ch[:]...))
	return pres.Assertion{CredentialID: pres.Encode([]byte("login-test-id")), AuthenticatorData: pres.Encode(ad), ClientDataJSON: pres.Encode(cd), Signature: pres.Encode(sig)}
}

func loginPost(t *testing.T, b *brain, route string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	b.presenceHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/presence/"+route, bytes.NewReader(raw)))
	return w
}

const fakeCodexLogin = `#!/bin/sh
echo "Open https://auth.example.org/activate to sign in."
echo "Code: WDJB-MJHT"
sleep 30
`

func TestLoginRepairRoundTrip(t *testing.T) {
	old := loginRepairTimeout
	loginRepairTimeout = 5 * time.Second
	defer func() { loginRepairTimeout = old }()
	b, key := loginFixture(t, fakeCodexLogin)

	w := loginPost(t, b, "login/challenge", map[string]string{"cli": "codex"})
	if w.Code != 200 {
		t.Fatalf("challenge: %d %s", w.Code, w.Body)
	}
	var ch struct{ Challenge string }
	_ = json.Unmarshal(w.Body.Bytes(), &ch)
	raw, _ := pres.Decode(ch.Challenge)
	assertion := signLogin(t, key, raw, 1)

	w = loginPost(t, b, "login/repair", map[string]any{"cli": "codex", "assertion": assertion})
	if w.Code != 200 {
		t.Fatalf("repair: %d %s", w.Code, w.Body)
	}
	var r struct {
		URL  string `json:"url"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if r.URL != "https://auth.example.org/activate" || r.Code != "WDJB-MJHT" {
		t.Fatalf("bad repair answer: %+v", r)
	}

	st := httptest.NewRecorder()
	b.presenceHTTP(st, httptest.NewRequest(http.MethodGet, "/v1/presence/login/status?cli=codex", nil))
	if st.Code != 200 {
		t.Fatalf("status: %d %s", st.Code, st.Body)
	}
	if strings.Contains(st.Body.String(), "WDJB-MJHT") || strings.Contains(st.Body.String(), "auth.example.org") {
		t.Fatalf("status repeated the code or url: %s", st.Body)
	}
	if !strings.Contains(st.Body.String(), `"state":"waiting"`) {
		t.Fatalf("status not waiting: %s", st.Body)
	}

	w = loginPost(t, b, "login/cancel", map[string]any{"cli": "codex", "assertion": signLogin(t, key, pres.LoginRepairChallenge("test-machine", "codex"), 2)})
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	st = httptest.NewRecorder()
	b.presenceHTTP(st, httptest.NewRequest(http.MethodGet, "/v1/presence/login/status?cli=codex", nil))
	if !strings.Contains(st.Body.String(), `"state":"none"`) {
		t.Fatalf("cancel left state: %s", st.Body)
	}
}

func TestLoginRepairRefusesBadAssertion(t *testing.T) {
	b, key := loginFixture(t, fakeCodexLogin)
	w := loginPost(t, b, "login/repair", map[string]any{"cli": "codex", "assertion": signLogin(t, key, pres.LoginRepairChallenge("other-machine", "codex"), 1)})
	if w.Code != 403 {
		t.Fatalf("wrong machine accepted: %d %s", w.Code, w.Body)
	}
	w = loginPost(t, b, "login/repair", map[string]any{"cli": "curl", "assertion": signLogin(t, key, pres.LoginRepairChallenge("test-machine", "curl"), 2)})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "unknown_cli") {
		t.Fatalf("unknown cli accepted: %d %s", w.Code, w.Body)
	}
}

func TestLoginRepairBusy(t *testing.T) {
	old := loginRepairTimeout
	loginRepairTimeout = 5 * time.Second
	defer func() { loginRepairTimeout = old }()
	b, key := loginFixture(t, fakeCodexLogin)
	w := loginPost(t, b, "login/repair", map[string]any{"cli": "codex", "assertion": signLogin(t, key, pres.LoginRepairChallenge("test-machine", "codex"), 1)})
	if w.Code != 200 {
		t.Fatalf("first repair: %d %s", w.Code, w.Body)
	}
	w = loginPost(t, b, "login/repair", map[string]any{"cli": "codex", "assertion": signLogin(t, key, pres.LoginRepairChallenge("test-machine", "codex"), 2)})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "repair_busy") {
		t.Fatalf("second repair accepted: %d %s", w.Code, w.Body)
	}
	loginPost(t, b, "login/cancel", map[string]any{"cli": "codex", "assertion": signLogin(t, key, pres.LoginRepairChallenge("test-machine", "codex"), 3)})
}

func TestLoginRepairNoCode(t *testing.T) {
	old := loginRepairTimeout
	loginRepairTimeout = 300 * time.Millisecond
	defer func() { loginRepairTimeout = old }()
	b, key := loginFixture(t, "#!/bin/sh\necho nothing useful\nsleep 30\n")
	w := loginPost(t, b, "login/repair", map[string]any{"cli": "codex", "assertion": signLogin(t, key, pres.LoginRepairChallenge("test-machine", "codex"), 1)})
	if w.Code != 502 || !strings.Contains(w.Body.String(), "no_code") {
		t.Fatalf("silent cli not refused: %d %s", w.Code, w.Body)
	}
}

func TestParseLoginLine(t *testing.T) {
	for _, row := range []struct {
		line string
		url  string
		code string
	}{
		{"Visit https://example.org/device and enter AB12-CD34", "https://example.org/device", "AB12-CD34"},
		{"  code:  XY9Z-8W7V-Q1R2  ", "", "XY9Z-8W7V-Q1R2"},
		{"error: something failed", "", ""},
		{"https://example.org/a/b?x=1 only a url", "https://example.org/a/b?x=1", ""},
	} {
		url, code := parseLoginLine(row.line)
		if url != row.url || code != row.code {
			t.Fatalf("parse(%q) = %q,%q want %q,%q", row.line, url, code, row.url, row.code)
		}
	}
}

func BenchmarkParseLoginLine(b *testing.B) {
	line := "Visit https://auth.example.org/activate and enter code WDJB-MJHT"
	for i := 0; i < b.N; i++ {
		parseLoginLine(line)
	}
}

func BenchmarkLoginRepairChallenge(b *testing.B) {
	for i := 0; i < b.N; i++ {
		pres.LoginRepairChallenge("test-machine", "codex")
	}
}

func TestLoginRepairCommandNotFound(t *testing.T) {
	old := loginRepairTimeout
	loginRepairTimeout = 500 * time.Millisecond
	defer func() { loginRepairTimeout = old }()
	t.Setenv("HOME", t.TempDir())
	b, key := loginFixture(t, "")
	// Overwrite the fake codex with nothing on PATH by pointing HOME only:
	// loginFixture put a fake codex first; remove it so LookPath fails.
	if e := os.Remove(filepath.Join(filepath.SplitList(os.Getenv("PATH"))[0], "codex")); e != nil {
		t.Fatal(e)
	}
	w := loginPost(t, b, "login/repair", map[string]any{"cli": "codex", "assertion": signLogin(t, key, pres.LoginRepairChallenge("test-machine", "codex"), 1)})
	if w.Code != 502 || !strings.Contains(w.Body.String(), "no_code") {
		t.Fatalf("missing cli not refused: %d %s", w.Code, w.Body)
	}
}

func TestLoginRepairOneRunPerCLI(t *testing.T) {
	old := loginRepairTimeout
	loginRepairTimeout = 3 * time.Second
	defer func() { loginRepairTimeout = old }()
	b, _ := loginFixture(t, fakeCodexLogin)
	if _, ok := b.logins.run("codex"); !ok {
		t.Fatal("first run refused")
	}
	if _, ok := b.logins.run("codex"); ok {
		t.Fatal("second run accepted while one was active")
	}
	if _, ok := b.logins.run("grok"); !ok {
		t.Fatal("another cli was blocked by an active repair")
	}
	if !b.logins.stop("codex") || !b.logins.stop("grok") {
		t.Fatal("stop lost a run")
	}
	if b.logins.stop("codex") {
		t.Fatal("stop accepted twice")
	}
	if _, ok := b.logins.run("codex"); !ok {
		t.Fatal("a new run was refused after a stop")
	}
	b.logins.stop("codex")
}
