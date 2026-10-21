package presence

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"testing"
)

func cborBytes(b []byte) []byte {
	if len(b) < 256 {
		return append([]byte{0x58, byte(len(b))}, b...)
	}
	return append([]byte{0x59, byte(len(b) >> 8), byte(len(b))}, b...)
}
func registration(t *testing.T, s *Store, code string) Registration {
	t.Helper()
	other, _, _ := fixture(t, "Ed25519")
	c, e := other.Read()
	if e != nil {
		t.Fatal(e)
	}
	k := c.Users[0].Credentials[0]
	key, _ := Decode(k.Key)
	cfg, _ := s.Read()
	r := Registration{Machine: cfg.Machine, User: "operator", Origin: k.Origin, RPID: k.RPID, Label: "new device", Nonce: Encode([]byte("registration-nonce-32-bytes-long!!")), Expires: nowUnix() + 240, Code: code, CredentialID: Encode([]byte("new-credential"))}
	challenge := r.Challenge()
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": Encode(challenge), "origin": k.Origin})
	rp := sha256.Sum256([]byte(k.RPID))
	ad := append(rp[:], 0x45, 0, 0, 0, 0)
	ad = append(ad, make([]byte, 16)...)
	id, _ := Decode(r.CredentialID)
	ad = append(ad, 0, byte(len(id)))
	ad = append(ad, id...)
	ad = append(ad, key...)
	obj := []byte{0xa3, 0x63, 'f', 'm', 't', 0x64, 'n', 'o', 'n', 'e', 0x67, 'a', 't', 't', 'S', 't', 'm', 't', 0xa0, 0x68, 'a', 'u', 't', 'h', 'D', 'a', 't', 'a'}
	obj = append(obj, cborBytes(ad)...)
	r.ClientDataJSON = Encode(cd)
	r.AttestationObject = Encode(obj)
	return r
}
func TestRegistrationProof(t *testing.T) {
	t.Run("relay_without_proof", func(t *testing.T) {
		s, _, _ := fixture(t, "Ed25519")
		r := registration(t, s, "")
		before, _ := os.ReadFile(s.Path)
		if e := s.Register(r); e == nil {
			t.Fatal("untrusted relay registered key")
		}
		after, _ := os.ReadFile(s.Path)
		if string(before) != string(after) {
			t.Fatal("mutation")
		}
	})
	t.Run("terminal_code", func(t *testing.T) {
		s, _, _ := fixture(t, "Ed25519")
		code, e := s.EnrollCode("operator")
		if e != nil {
			t.Fatal(e)
		}
		r := registration(t, s, code)
		if e = s.Register(r); e != nil {
			t.Fatal(e)
		}
		if e = s.Register(r); e == nil {
			t.Fatal("code replay")
		}
	})
	t.Run("trusted_signature", func(t *testing.T) {
		s, _, sign := fixture(t, "ES256")
		r := registration(t, s, "")
		a := sign(r.ApprovalChallenge(), 1)
		r.Approval = &a
		if e := s.Register(r); e != nil {
			t.Fatal(e)
		}
		c, _ := s.Read()
		if len(c.Users[0].Credentials) != 2 {
			t.Fatal("key missing")
		}
	})
	t.Run("expired_code", func(t *testing.T) {
		s, _, _ := fixture(t, "Ed25519")
		code, _ := s.EnrollCode("operator")
		_ = s.Update(func(c *Config) error {
			c.Tickets[HashText(code)] = Ticket{User: "operator", Expires: nowUnix() - 1}
			return nil
		})
		if e := s.Register(registration(t, s, code)); e == nil {
			t.Fatal("expired code accepted")
		}
	})
	t.Run("changed_key", func(t *testing.T) {
		s, _, sign := fixture(t, "ES256")
		r := registration(t, s, "")
		a := sign(r.ApprovalChallenge(), 1)
		r.Approval = &a
		r.Label = "other"
		if e := s.Register(r); e == nil {
			t.Fatal("unbound key metadata")
		}
	})
}
func TestSeparateSignedDecision(t *testing.T) {
	s, p, sign := fixture(t, "ES256")
	g, e := s.VerifyPrompt(p)
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.RequireDecision(g.ID, "git push origin main")
	if e != nil || d.Approved {
		t.Fatalf("prompt approved high tier: %+v %v", d, e)
	}
	if e = s.CheckAction(g.ID, d.Action); e == nil {
		t.Fatal("unsigned yes allowed")
	}
	a := sign(DecisionChallenge(g.Machine, d), 2)
	if e = s.ApproveDecision(d.ID, a); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckAction(g.ID, d.Action); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckAction(g.ID, d.Action); e == nil {
		t.Fatal("decision replay")
	}
}
