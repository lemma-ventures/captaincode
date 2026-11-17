package presence

import "testing"

func TestVerifyPromptReadOnlyPersistsScopeAndReplayRefusal(t *testing.T) {
	s, p, _ := fixture(t, "Ed25519")
	g, err := s.VerifyPromptReadOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	if !g.ReadOnly {
		t.Fatal("read-only scope was not granted")
	}
	stored, err := s.Grant(g.ID)
	if err != nil || !stored.ReadOnly {
		t.Fatalf("stored grant: %+v, %v", stored, err)
	}
	if _, err := s.VerifyPromptReadOnly(p); Code(err) != "replayed_nonce" {
		t.Fatalf("replay error = %v", err)
	}
}
