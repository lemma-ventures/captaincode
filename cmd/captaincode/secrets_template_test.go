package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsTemplateDoesNotExportMachineValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("CAPTAIN_AUTO_UPGRADE", "private-value")
	t.Setenv("CAPTAIN_REDACT", "off")
	t.Setenv("OPENROUTER_API_KEY", "fixture-secret")
	config := filepath.Join(home, "captain")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config, "env")
	original := "CAPTAIN_PROXY_ADDR=private-host\nCUSTOM_SETTING=fixture-secret\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := secretsTemplate(&output); err != nil {
		t.Fatal(err)
	}
	want := "CAPTAIN_AUTO_UPGRADE=all\nCAPTAIN_REDACT=on\nCAPTAIN_PROXY_ADDR=127.0.0.1:14098\nCAPTAIN_PROXY_CLAUDE=1\nCAPTAIN_PROXY_CODEX=1\n"
	if output.String() != want {
		t.Fatalf("unexpected template: %q", output.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != original {
		t.Fatal("template changed the source file", err)
	}
	entries, err := os.ReadDir(config)
	if err != nil || len(entries) != 1 {
		t.Fatal("template wrote configuration files", err)
	}
	if os.Getenv("CAPTAIN_REDACT") != "off" {
		t.Fatal("template changed the process environment")
	}
}

func TestSecretsTemplateCommand(t *testing.T) {
	output := captureStdout(t, func() {
		if err := runSecrets([]string{"template"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.HasPrefix(output, "CAPTAIN_AUTO_UPGRADE=all\n") {
		t.Fatalf("unexpected command output: %q", output)
	}
	if err := runSecrets([]string{"template", "--from-env"}); err == nil {
		t.Fatal("template accepted an unsupported export option")
	}
}

type templateFailWriter struct{}

func (templateFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("fixture write failure")
}

func TestSecretsTemplateWriteFailure(t *testing.T) {
	if err := secretsTemplate(templateFailWriter{}); err == nil {
		t.Fatal("template hid an output failure")
	}
}

func TestSecretsTemplateEntryPoint(t *testing.T) {
	if os.Getenv("CAPTAIN_TEMPLATE_TEST_HELPER") == "1" {
		os.Args = []string{"captain", "secrets", "template"}
		main()
		return
	}
	t.Setenv("CAPTAIN_DIRECTOR", "invalid-template-fixture")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecretsTemplateEntryPoint$")
	cmd.Env = append(os.Environ(), "CAPTAIN_TEMPLATE_TEST_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("template failed before machine setup: %v: %s", err, output)
	}
	if !strings.HasPrefix(string(output), "CAPTAIN_AUTO_UPGRADE=all\n") {
		t.Fatalf("unexpected startup output: %s", output)
	}
	entries, err := os.ReadDir(os.Getenv("XDG_CONFIG_HOME"))
	if err != nil || len(entries) != 0 {
		t.Fatal("template initialized machine configuration", err)
	}
}

func BenchmarkSecretsTemplate(b *testing.B) {
	var output bytes.Buffer
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		output.Reset()
		if err := secretsTemplate(&output); err != nil {
			b.Fatal(err)
		}
	}
}
