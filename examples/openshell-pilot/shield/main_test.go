package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMask(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPTAIN_REDACT", "off")
	secret := "nvapi-" + strings.Repeat("a1", 24)
	in, _ := json.Marshal(map[string]any{"messages": []any{map[string]string{"content": secret}}, "seed": json.Number("9007199254740993")})
	out, err := mask(in)
	if err != nil || strings.Contains(string(out), secret) || !strings.Contains(string(out), "[[secret:nvidia:") || !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("masking failed: %v", err)
	}
	var result struct {
		Secrets int `json:"secrets"`
	}
	if json.Unmarshal(out, &result) != nil || result.Secrets != 1 {
		t.Fatal("missing masking evidence")
	}
	info, err := os.Stat(os.Getenv("HOME") + "/.captaincode/vault.jsonl")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("vault must be private")
	}
}

func TestMaskRejectsMalformedOrMultipleDocuments(t *testing.T) {
	for _, in := range []string{"{", "{} {}", "{} trailing", ""} {
		if _, err := mask([]byte(in)); err == nil {
			t.Fatal("accepted invalid document")
		}
	}
}
