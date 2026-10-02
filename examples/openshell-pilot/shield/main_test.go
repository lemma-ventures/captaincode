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
	if err != nil || !strings.Contains(string(out), "[[secret:nvidia:") || !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("masking failed: %v", err)
	}
	var result struct {
		Secrets int             `json:"secrets"`
		Body    json.RawMessage `json:"body"`
	}
	if json.Unmarshal(out, &result) != nil || result.Secrets != 1 || strings.Contains(string(result.Body), secret) {
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

func TestMaskReturnsOnlySecretsObservedInThisRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	canary := "nvapi-" + strings.Repeat("b2", 24)
	in, _ := json.Marshal(map[string]string{"content": canary})
	out, err := mask(in)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Body        map[string]string `json:"body"`
		ToolSecrets map[string]string `json:"tool_secrets"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	handle := result.Body["content"]
	if len(result.ToolSecrets) != 1 || result.ToolSecrets[handle] != canary {
		t.Fatal("missing request-scoped secret")
	}
	forged, _ := json.Marshal(map[string]string{"content": handle})
	out, err = mask(forged)
	if err != nil {
		t.Fatal(err)
	}
	result.ToolSecrets = nil
	if err := json.Unmarshal(out, &result); err != nil || len(result.ToolSecrets) != 0 {
		t.Fatal("a placeholder must not grant access to an earlier secret")
	}
}
