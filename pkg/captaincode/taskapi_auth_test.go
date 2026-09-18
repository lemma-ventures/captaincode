package captaincode

import (
	"os"
	"testing"
)

func TestCheckAuthLoopbackOnly(t *testing.T) {
	policy := TaskAuthPolicy{LoopbackOnly: true}
	if e := CheckAuth(policy, "127.0.0.1:14097", ""); e != nil {
		t.Fatalf("loopback should be allowed: %v", e)
	}
	if e := CheckAuth(policy, "[::1]:14097", ""); e != nil {
		t.Fatalf("ipv6 loopback should be allowed: %v", e)
	}
	if e := CheckAuth(policy, "10.0.0.5:14097", ""); e == nil {
		t.Fatal("non-loopback should be rejected")
	}
	if e := CheckAuth(policy, "10.0.0.5:14097", ""); e.Code != ErrUnauthorized {
		t.Fatalf("expected unauthorized, got %s", e.Code)
	}
}

func TestCheckAuthTokenValid(t *testing.T) {
	policy := TaskAuthPolicy{Token: "secret-token"}
	if e := CheckAuth(policy, "10.0.0.5:14097", "Bearer secret-token"); e != nil {
		t.Fatalf("valid token from non-loopback should pass: %v", e)
	}
	if e := CheckAuth(policy, "127.0.0.1:14097", "Bearer secret-token"); e != nil {
		t.Fatalf("valid token from loopback should pass: %v", e)
	}
}

func TestCheckAuthTokenMissing(t *testing.T) {
	policy := TaskAuthPolicy{Token: "secret-token"}
	if e := CheckAuth(policy, "10.0.0.5:14097", ""); e == nil {
		t.Fatal("missing header should be rejected")
	}
	if e := CheckAuth(policy, "10.0.0.5:14097", ""); e.Code != ErrUnauthorized {
		t.Fatalf("expected unauthorized, got %s", e.Code)
	}
}

func TestCheckAuthTokenWrong(t *testing.T) {
	policy := TaskAuthPolicy{Token: "secret-token"}
	if e := CheckAuth(policy, "10.0.0.5:14097", "Bearer wrong-token"); e == nil {
		t.Fatal("wrong token should be rejected")
	}
	if e := CheckAuth(policy, "10.0.0.5:14097", "Bearer wrong-token"); e.Code != ErrUnauthorized {
		t.Fatalf("expected unauthorized, got %s", e.Code)
	}
}

func TestCheckAuthMalformedHeader(t *testing.T) {
	policy := TaskAuthPolicy{Token: "secret-token"}
	if e := CheckAuth(policy, "10.0.0.5:14097", "secret-token"); e == nil {
		t.Fatal("bare token without Bearer prefix should be rejected")
	}
	if e := CheckAuth(policy, "10.0.0.5:14097", "Basic secret-token"); e == nil {
		t.Fatal("non-Bearer scheme should be rejected")
	}
}

func TestCheckAuthNoTokenNonLoopback(t *testing.T) {
	policy := TaskAuthPolicy{LoopbackOnly: true}
	if e := CheckAuth(policy, "192.168.1.1:14097", ""); e == nil {
		t.Fatal("non-loopback without token should be rejected")
	}
}

func TestDefaultTaskAuthPolicyNoToken(t *testing.T) {
	os.Unsetenv("CAPTAIN_TASK_TOKEN")
	policy := DefaultTaskAuthPolicy()
	if !policy.LoopbackOnly {
		t.Fatal("without token, policy should be loopback-only")
	}
	if policy.Token != "" {
		t.Fatal("token should be empty")
	}
}

func TestDefaultTaskAuthPolicyWithToken(t *testing.T) {
	os.Setenv("CAPTAIN_TASK_TOKEN", "test-secret")
	defer os.Unsetenv("CAPTAIN_TASK_TOKEN")
	policy := DefaultTaskAuthPolicy()
	if policy.LoopbackOnly {
		t.Fatal("with token, policy should not be loopback-only")
	}
	if policy.Token != "test-secret" {
		t.Fatalf("token mismatch: %s", policy.Token)
	}
}

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:14097", true},
		{"[::1]:14097", true},
		{"localhost:14097", true},
		{"10.0.0.5:14097", false},
		{"192.168.1.1:14097", false},
		{"0.0.0.0:14097", false},
	}
	for _, c := range cases {
		if got := isLoopback(c.addr); got != c.want {
			t.Fatalf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
