package presence

import (
	"path/filepath"
	"testing"
)

func TestLoginRepairChallengeBindsCLIAndMachine(t *testing.T) {
	a := LoginRepairChallenge("m1", "codex")
	if string(a) == string(LoginRepairChallenge("m1", "grok")) {
		t.Fatal("challenge does not bind the cli")
	}
	if string(a) == string(LoginRepairChallenge("m2", "codex")) {
		t.Fatal("challenge does not bind the machine")
	}
}

func TestLoginChallengeRefusesUnknownCLI(t *testing.T) {
	s, _, _ := fixture(t, "Ed25519")
	if _, e := s.LoginChallenge("curl"); e == nil || Code(e) != "unknown_cli" {
		t.Fatalf("unknown cli accepted: %v", e)
	}
	if _, e := s.LoginChallenge(""); e == nil || Code(e) != "unknown_cli" {
		t.Fatalf("empty cli accepted: %v", e)
	}
	if _, e := s.LoginChallenge("codex"); e != nil {
		t.Fatal(e)
	}
}

func TestVerifyLoginRepair(t *testing.T) {
	s, _, sign := fixture(t, "Ed25519")
	ch, e := s.LoginChallenge("codex")
	if e != nil {
		t.Fatal(e)
	}
	if e := s.VerifyLoginRepair("codex", sign(ch, 1)); e != nil {
		t.Fatal(e)
	}
	c, _ := s.Read()
	found := false
	for _, ev := range c.Audit {
		if ev.Kind == "login:codex" && ev.User == "operator" && ev.Machine == "test-machine" {
			found = true
		}
	}
	if !found {
		t.Fatal("no audit event for the repair")
	}
	grokCh, _ := s.LoginChallenge("grok")
	if e := s.VerifyLoginRepair("codex", sign(grokCh, 2)); e == nil {
		t.Fatal("an approval signed for another cli started this one")
	}
	if e := s.VerifyLoginRepair("rm", sign(ch, 3)); e == nil {
		t.Fatal("unknown cli accepted")
	}
	empty := &Store{Path: filepath.Join(t.TempDir(), "presence.json")}
	if e := empty.Update(func(c *Config) error { c.Machine = "m"; return nil }); e != nil {
		t.Fatal(e)
	}
	if e := empty.VerifyLoginRepair("codex", sign(ch, 4)); e == nil {
		t.Fatal("a store without enrolled owners accepted a repair")
	}
}

func TestVerifyLoginRepairCountersAdvance(t *testing.T) {
	s, _, sign := fixture(t, "Ed25519")
	ch, _ := s.LoginChallenge("grok")
	if e := s.VerifyLoginRepair("grok", sign(ch, 5)); e != nil {
		t.Fatal(e)
	}
	if e := s.VerifyLoginRepair("grok", sign(ch, 5)); e == nil {
		t.Fatal("replayed assertion accepted")
	}
}
