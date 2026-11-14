package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSecretsInventoryRefusesUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "fifo", "loose", "executable", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "env")
			payload := []byte("FIXTURE_TOKEN=private-fixture-value\n")
			var err error
			switch kind {
			case "symlink", "hardlink":
				target := filepath.Join(dir, "target")
				err = os.WriteFile(target, payload, 0o600)
				if err == nil {
					if kind == "symlink" {
						err = os.Symlink(target, path)
					} else {
						err = os.Link(target, path)
					}
				}
			case "directory":
				err = os.Mkdir(path, 0o700)
			case "fifo":
				err = syscall.Mkfifo(path, 0o600)
			case "loose":
				err = os.WriteFile(path, payload, 0o644)
			case "executable":
				err = os.WriteFile(path, payload, 0o700)
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)+1)), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			names, _, err := envNames(path)
			if err == nil || len(names) != 0 {
				t.Fatalf("unsafe file accepted: %v, %v", names, err)
			}
			if strings.Contains(err.Error(), "private-fixture-value") {
				t.Fatal("error contains a credential value")
			}
		})
	}
}

func TestSecretsInventoryOutput(t *testing.T) {
	envPath, secretsPath := secretsFixture(t)
	t.Setenv("FIXTURE_TOKEN", "shell-fixture-value")
	payload := "export FIXTURE_TOKEN=private-fixture-value\nFIXTURE_TOKEN=second-value\nZ_SETTING=setting-value\nBAD\x1b[31m=hidden\n9INVALID=hidden\n# COMMENT=hidden\n"
	if err := os.WriteFile(envPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsPath, []byte("OTHER_TOKEN=other-private-value\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if err := runSecrets([]string{"list"}); err != nil {
			t.Error(err)
		}
	})
	for _, value := range []string{"[[secret:", "private-fixture-value", "second-value", "setting-value", "other-private-value", "hidden", "\x1b", "9INVALID", "COMMENT"} {
		if strings.Contains(output, value) {
			t.Fatalf("inventory exposed invalid content %q", value)
		}
	}
	if strings.Count(output, "FIXTURE_TOKEN") != 1 || !strings.Contains(output, "OTHER_TOKEN  [secret]") || !strings.Contains(output, "Z_SETTING") {
		t.Fatalf("unexpected inventory: %q", output)
	}
	if os.Getenv("FIXTURE_TOKEN") != "shell-fixture-value" {
		t.Fatal("inventory changed the environment")
	}
	raw, err := os.ReadFile(envPath)
	if err != nil || string(raw) != payload {
		t.Fatal("inventory changed the source", err)
	}
}

func TestSecretsInventoryMissingFile(t *testing.T) {
	_, _, err := envNames(filepath.Join(t.TempDir(), "missing"))
	if !os.IsNotExist(err) {
		t.Fatalf("missing file lost its status: %v", err)
	}
}

func BenchmarkSecretsInventory(b *testing.B) {
	path := filepath.Join(b.TempDir(), "env")
	if err := os.WriteFile(path, []byte("FIXTURE_TOKEN=private-fixture-value\nCAPTAIN_REDACT=on\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := envNames(path); err != nil {
			b.Fatal(err)
		}
	}
}
