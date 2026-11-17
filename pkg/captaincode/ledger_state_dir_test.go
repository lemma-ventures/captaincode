package captaincode

import (
	"os"
	"path/filepath"
	"testing"
)

// The ledger's home state directory is private to the account that runs the
// captain: the record holds run history, cooldowns and thread ids. A new
// account gets it at 0700, and a directory an older build left at 0755 is
// tightened on the next command instead of staying listable by other users.
func TestLoadLedgerStateDirPrivacy(t *testing.T) {
	t.Run("new directory is 0700", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if _, err := LoadLedger(); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(home, ".captaincode")
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("new state directory is %v, want 0700", info.Mode().Perm())
		}
	})

	t.Run("older 0755 directory is tightened", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".captaincode")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil { // umask can mask 0755
			t.Fatal(err)
		}
		if _, err := LoadLedger(); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("state directory is %v after load, want 0700", info.Mode().Perm())
		}
	})

	t.Run("pre-rename state still carries over", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		old := filepath.Join(home, ".superagent")
		if err := os.MkdirAll(old, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(old, "state.json"), []byte(`{"sessions":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadLedger(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(home, ".captaincode", "state.json")); err != nil {
			t.Errorf("carried-over state: %v", err)
		}
	})
}
