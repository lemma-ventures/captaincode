package captaincode

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSecretsFileSafety(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "fifo", "executable", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("CAPTAIN_TEST_UNSAFE", "shell")
			writeEnv(t, home, "env", "CAPTAIN_TEST_UNSAFE=plain\n", 0o600)
			path := CaptainSecretsPath()
			payload := []byte("CAPTAIN_TEST_UNSAFE=unsafe\n")
			var err error
			switch kind {
			case "symlink", "hardlink":
				target := filepath.Join(home, "target")
				if err = os.WriteFile(target, payload, 0o600); err == nil {
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
			case "executable":
				err = os.WriteFile(path, payload, 0o700)
			case "oversized":
				err = os.WriteFile(path, append(payload, []byte(strings.Repeat("#", 1<<20))...), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{"CAPTAIN_TEST_UNSAFE": true}
			if got := loadSecretsFile(seen); len(got) != 0 {
				t.Fatalf("unsafe file loaded names: %v", got)
			}
			if os.Getenv("CAPTAIN_TEST_UNSAFE") != "shell" {
				t.Fatal("unsafe file changed the environment")
			}
		})
	}
}

func TestSecretsFileSizeBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("CAPTAIN_TEST_BOUNDARY", "shell")
	payload := "CAPTAIN_TEST_BOUNDARY=loaded\n#"
	payload += strings.Repeat("x", (1<<20)-len(payload))
	writeEnv(t, home, "secrets.env", payload, 0o400)
	if got := loadSecretsFile(map[string]bool{"CAPTAIN_TEST_BOUNDARY": true}); len(got) != 1 || os.Getenv("CAPTAIN_TEST_BOUNDARY") != "loaded" {
		t.Fatal("a private regular file at the size limit was refused")
	}
}

func BenchmarkLoadSecretsFile(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	b.Setenv("CAPTAIN_BENCH_SETTING", "shell")
	if err := os.MkdirAll(filepath.Dir(CaptainSecretsPath()), 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(CaptainSecretsPath(), []byte("CAPTAIN_BENCH_SETTING=fixture\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		loadSecretsFile(map[string]bool{})
	}
}
