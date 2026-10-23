package presence

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func enc(b []byte) string  { return base64.RawURLEncoding.EncodeToString(b) }
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func fixture(t *testing.T, alg string) (*Store, Signed, func([]byte, uint32) Assertion) {
	t.Helper()
	s := &Store{Path: filepath.Join(t.TempDir(), "presence.json")}
	var key any
	var cose []byte
	if alg == "ES256" {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		key = k
		cose = append([]byte{0xa5, 1, 2, 3, 0x26, 0x20, 1, 0x21, 0x58, 0x20}, k.X.FillBytes(make([]byte, 32))...)
		cose = append(cose, 0x22, 0x58, 0x20)
		cose = append(cose, k.Y.FillBytes(make([]byte, 32))...)
	} else {
		pub, k, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		key = k
		cose = append([]byte{0xa4, 1, 1, 3, 0x27, 0x20, 6, 0x21, 0x58, 0x20}, pub...)
	}
	id := enc([]byte("test-credential"))
	err := s.Update(func(c *Config) error {
		c.Machine = "test-machine"
		c.Origins = []string{"http://localhost:8781", "https://example.org"}
		c.Folders = map[string]string{"repo": t.TempDir()}
		c.Users = []User{{Name: "operator", Credentials: []Credential{{ID: id, Key: enc(cose), Algorithm: alg, RPID: "localhost", Origin: "http://localhost:8781", Label: "test device", Created: time.Now().UTC().Format(time.RFC3339)}}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sign := func(challenge []byte, count uint32) Assertion {
		cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": enc(challenge), "origin": "http://localhost:8781", "crossOrigin": false})
		rp := sha256.Sum256([]byte("localhost"))
		ad := append(rp[:], 5, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(ad[33:], count)
		h := sha256.Sum256(cd)
		msg := append(append([]byte{}, ad...), h[:]...)
		var sig []byte
		switch k := key.(type) {
		case *ecdsa.PrivateKey:
			digest := sha256.Sum256(msg)
			sig, _ = ecdsa.SignASN1(rand.Reader, k, digest[:])
		case ed25519.PrivateKey:
			sig = ed25519.Sign(k, msg)
		}
		return Assertion{CredentialID: id, AuthenticatorData: enc(ad), ClientDataJSON: enc(cd), Signature: enc(sig)}
	}
	now := time.Now().Unix()
	e := Envelope{V: 1, Machine: "test-machine", Folder: "repo", TextSHA256: hash("change a file"), IssuedAt: now - 1, ExpiresAt: now + 100, Nonce: enc(make([]byte, 32))}
	digest, _ := e.Hash()
	signed := Signed{Envelope: e, Text: "change a file", Assertion: sign(digest, 1)}
	if err := s.Prepare(e, signed.Text); err != nil {
		t.Fatal(err)
	}
	return s, signed, sign
}

func TestPromptVerification(t *testing.T) {
	for _, alg := range []string{"ES256", "Ed25519"} {
		t.Run(alg, func(t *testing.T) {
			s, p, _ := fixture(t, alg)
			g, e := s.VerifyPrompt(p)
			if e != nil {
				t.Fatal(e)
			}
			if g.User != "operator" || g.Folder != "repo" {
				t.Fatalf("bad grant: %+v", g)
			}
			if _, e = s.VerifyPrompt(p); e == nil {
				t.Fatal("replay accepted")
			}
			s2 := &Store{Path: s.Path}
			if _, e = s2.VerifyPrompt(p); e == nil {
				t.Fatal("replay after reopen accepted")
			}
			st, _ := os.Stat(s.Path)
			if st.Mode().Perm() != 0600 {
				t.Fatal(st.Mode())
			}
		})
	}
}

func TestRefusalsChangeNothing(t *testing.T) {
	cases := map[string]func(*Signed){
		"expired":   func(p *Signed) { p.Envelope.IssuedAt -= 200; p.Envelope.ExpiresAt -= 200 },
		"future":    func(p *Signed) { p.Envelope.IssuedAt += 100 },
		"window":    func(p *Signed) { p.Envelope.ExpiresAt += 10000 },
		"machine":   func(p *Signed) { p.Envelope.Machine = "elsewhere" },
		"folder":    func(p *Signed) { p.Envelope.Folder = "unlinked" },
		"edited":    func(p *Signed) { p.Text += "!" },
		"unknown":   func(p *Signed) { p.CredentialID = enc([]byte("unknown")) },
		"signature": func(p *Signed) { p.Signature = enc([]byte("invalid")) },
		"wrong_rp": func(p *Signed) {
			b, _ := base64.RawURLEncoding.DecodeString(p.AuthenticatorData)
			b[0] ^= 1
			p.AuthenticatorData = enc(b)
		},
		"missing_uv": func(p *Signed) {
			b, _ := base64.RawURLEncoding.DecodeString(p.AuthenticatorData)
			b[32] = 1
			p.AuthenticatorData = enc(b)
		},
		"missing_up": func(p *Signed) {
			b, _ := base64.RawURLEncoding.DecodeString(p.AuthenticatorData)
			b[32] = 4
			p.AuthenticatorData = enc(b)
		},
		"wrong_origin": func(p *Signed) {
			b, _ := base64.RawURLEncoding.DecodeString(p.ClientDataJSON)
			var c map[string]any
			_ = json.Unmarshal(b, &c)
			c["origin"] = "https://evil.example"
			b, _ = json.Marshal(c)
			p.ClientDataJSON = enc(b)
		},
		"credential_origin": func(p *Signed) {
			b, _ := base64.RawURLEncoding.DecodeString(p.ClientDataJSON)
			var c map[string]any
			_ = json.Unmarshal(b, &c)
			c["origin"] = "https://example.org"
			b, _ = json.Marshal(c)
			p.ClientDataJSON = enc(b)
		},
		"wrong_type": func(p *Signed) {
			b, _ := base64.RawURLEncoding.DecodeString(p.ClientDataJSON)
			var c map[string]any
			_ = json.Unmarshal(b, &c)
			c["type"] = "webauthn.create"
			b, _ = json.Marshal(c)
			p.ClientDataJSON = enc(b)
		},
		"cross_origin": func(p *Signed) {
			b, _ := base64.RawURLEncoding.DecodeString(p.ClientDataJSON)
			var c map[string]any
			_ = json.Unmarshal(b, &c)
			c["crossOrigin"] = true
			b, _ = json.Marshal(c)
			p.ClientDataJSON = enc(b)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, p, _ := fixture(t, "ES256")
			before, _ := os.ReadFile(s.Path)
			mutate(&p)
			if _, e := s.VerifyPrompt(p); e == nil {
				t.Fatal("accepted")
			}
			after, _ := os.ReadFile(s.Path)
			if string(before) != string(after) {
				t.Fatal("failed request changed state")
			}
		})
	}
}

func TestCounterAndConcurrentReplay(t *testing.T) {
	s, p, sign := fixture(t, "Ed25519")
	if _, e := s.VerifyPrompt(p); e != nil {
		t.Fatal(e)
	}
	p.Envelope.Nonce = enc([]byte("another-unique-nonce-32-bytes-long"))
	d, _ := p.Envelope.Hash()
	p.Assertion = sign(d, 0)
	if e := s.Prepare(p.Envelope, p.Text); e != nil {
		t.Fatal(e)
	}
	if _, e := s.VerifyPrompt(p); e == nil {
		t.Fatal("lower counter accepted")
	}
	p.Assertion = sign(d, 1)
	if _, e := s.VerifyPrompt(p); e == nil {
		t.Fatal("equal counter accepted")
	}
	p.Assertion = sign(d, 2)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other := &Store{Path: s.Path}
			if _, e := other.VerifyPrompt(p); e == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("accepted %d copies", accepted)
	}
}

func TestNoScreenNoTrust(t *testing.T) {
	s, p, _ := fixture(t, "Ed25519")
	_ = s.Update(func(c *Config) error { c.Screened = nil; return nil })
	if _, e := s.VerifyPrompt(p); e == nil {
		t.Fatal("unscreened prompt accepted")
	}
}

func TestTerminalOnly(t *testing.T) {
	for _, via := range []string{"http", "sent", "worker"} {
		if e := LocalAuthority(via, false); e == nil {
			t.Fatal(via)
		}
	}
	if e := LocalAuthority("terminal", true); e != nil {
		t.Fatal(e)
	}
}

func TestCBOR(t *testing.T) {
	for _, bad := range [][]byte{{}, {0x9f, 0xff}, {0xa2, 1, 1, 1, 2}, {0x58, 32, 1}, {0x1b, 255, 255, 255, 255, 255, 255, 255, 255}, {0x61, 255}, {0xc0, 0}, {0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0}} {
		if _, _, e := decodeCBOR(bad); e == nil {
			t.Fatalf("accepted %x", bad)
		}
	}
	v, n, e := decodeCBOR([]byte{0xa2, 1, 0x26, 0x21, 0x42, 1, 2})
	if e != nil || n != 7 || v == nil {
		t.Fatalf("valid CBOR: %v %d %v", v, n, e)
	}
}

func TestStrictJSONAndStoreMode(t *testing.T) {
	for _, raw := range []string{`{"v":1,"v":2}`, `{"unknown":1}`, `{} {}`, `{"v":1,"machine":"x","folder":"repo","text_sha256":"x","issued_at":1.5,"expires_at":2,"nonce":"x"}`} {
		var e Envelope
		if err := ReadJSON(bytes.NewBufferString(raw), &e); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	s, p, _ := fixture(t, "Ed25519")
	if e := os.Chmod(s.Path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := s.VerifyPrompt(p); e == nil {
		t.Fatal("world-readable store accepted")
	}
}
func TestDecisionSignatureCannotApproveAnotherRequest(t *testing.T) {
	s, p, sign := fixture(t, "Ed25519")
	g, e := s.VerifyPrompt(p)
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.RequireDecision(g.ID, "delete item A")
	if e != nil {
		t.Fatal(e)
	}
	a := sign(DecisionChallenge(g.Machine, d), 0)
	if e = s.ApproveDecision(d.ID, a); e == nil {
		t.Fatal("counter reset accepted")
	}
	a = sign(DecisionChallenge(g.Machine, d), 2)
	if e = s.ApproveDecision(d.ID, a); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckAction(g.ID, d.Action); e != nil {
		t.Fatal(e)
	}
	next, e := s.RequireDecision(g.ID, d.Action)
	if e != nil {
		t.Fatal(e)
	}
	if next.ID == d.ID {
		t.Fatal("decision identity reused")
	}
	if e = s.ApproveDecision(next.ID, a); e == nil {
		t.Fatal("signature replay accepted")
	}
}
func TestExportContainsOnlyPublicCredentialData(t *testing.T) {
	s, p, _ := fixture(t, "Ed25519")
	if _, e := s.VerifyPrompt(p); e != nil {
		t.Fatal(e)
	}
	if _, e := s.EnrollCode("operator"); e != nil {
		t.Fatal(e)
	}
	raw, e := s.Export()
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"used_nonces", "grants", "enrollment_tickets", "machine", "folders", "audit", "screened"} {
		if bytes.Contains(raw, []byte(`"`+secret+`"`)) {
			t.Fatal(secret)
		}
	}
	other := &Store{Path: filepath.Join(t.TempDir(), "presence.json")}
	if e = other.Import(raw); e != nil {
		t.Fatal(e)
	}
	c, e := other.Read()
	if e != nil || c.Machine == "test-machine" || len(c.Users) != 1 || len(c.Origins) != 0 {
		t.Fatal(c, e)
	}
	before, _ := os.ReadFile(other.Path)
	if e = other.Import(raw); e == nil {
		t.Fatal("duplicate import accepted")
	}
	after, _ := os.ReadFile(other.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed import changed keys")
	}
}
func FuzzCBOR(f *testing.F) {
	f.Add([]byte{0xa1, 1, 2})
	f.Add([]byte{0x9f, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		_, n, _ := decodeCBOR(b)
		if n < 0 || n > len(b) {
			t.Fatal("bad consumed length")
		}
	})
}

func TestMachineIDAcceptsWholeBase64URLAlphabet(t *testing.T) {
	_, p, _ := fixture(t, "Ed25519")
	for _, id := range []string{"_machine", "-machine"} {
		p.Envelope.Machine = id
		if _, e := p.Envelope.Hash(); e != nil {
			t.Fatal(e)
		}
	}
}
func TestApprovalPreviewDoesNotSpendDecision(t *testing.T) {
	s, p, sign := fixture(t, "Ed25519")
	g, e := s.VerifyPrompt(p)
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.RequireDecision(g.ID, "action")
	if e != nil {
		t.Fatal(e)
	}
	a := sign(DecisionChallenge(g.Machine, d), 2)
	if e = s.ApproveDecision(d.ID, a); e != nil {
		t.Fatal(e)
	}
	if e = s.PreviewAction(g.ID, "action"); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckAction(g.ID, "action"); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckAction(g.ID, "action"); e == nil {
		t.Fatal("approval consumed twice")
	}
}

// One passkey approval covers every listed decision, and only those; purge
// removes pending decisions and approves nothing.
func TestApproveAllAndPurgeDecisions(t *testing.T) {
	s, p, sign := fixture(t, "Ed25519")
	g, e := s.VerifyPrompt(p)
	if e != nil {
		t.Fatal(e)
	}
	var ds []Decision
	for _, act := range []string{"push main", "merge feature", "delete item A"} {
		d, e := s.RequireDecision(g.ID, act)
		if e != nil {
			t.Fatal(e)
		}
		ds = append(ds, d)
	}
	pair := []Decision{ds[0], ds[1]}
	slices.SortFunc(pair, func(a, b Decision) int { return strings.Compare(a.ID, b.ID) })
	a := sign(DecisionsChallenge(g.Machine, pair), 2)
	if e := s.ApproveDecisions([]string{ds[0].ID, ds[2].ID}, a); e == nil {
		t.Fatal("an approval signed for two decisions approved another set")
	}
	if e := s.ApproveDecisions([]string{ds[1].ID, ds[0].ID}, a); e != nil {
		t.Fatal(e)
	}
	for _, d := range pair {
		if e := s.CheckAction(g.ID, d.Action); e != nil {
			t.Fatalf("%s approved: %v", d.Action, e)
		}
	}
	if e := s.ApproveDecisions([]string{ds[0].ID, ds[1].ID}, a); e == nil {
		t.Fatal("a used approval approved again")
	}
	n, e := s.PurgeDecisions([]string{ds[2].ID, "no-such-id"})
	if e != nil || n != 1 {
		t.Fatal(n, e)
	}
	left, _ := s.Decisions()
	for _, d := range left {
		if d.ID == ds[2].ID {
			t.Fatal("a purged decision is still pending")
		}
	}
	if e := s.CheckAction(g.ID, "delete item A"); e == nil {
		t.Fatal("a purged action is still refused until approved")
	}
}
