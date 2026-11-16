package captaincode

// BB5 worker containment: a worker's shell runs inside the jail, and it must
// reach neither the enforcement state nor the enforcement settings. The two
// profile builders a sent turn uses are the whole claim, so these checks pin
// the overridden file, the record stream and the mode to what they deny.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlackboxStateIsOutsideTheJail(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_BLACKBOX_OVERRIDES", "")

	secret := filepath.Join(home, ".config", "captain")
	state := filepath.Join(home, ".captaincode")
	for _, dir := range []string{secret, state} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// Seatbelt: the last write allow must precede the read/write deny block,
	// and every enforcement directory must sit inside that block.
	profile := SeatbeltProfile(workspace, home)
	allow := strings.LastIndex(profile, "(allow file-write*")
	denyRead := strings.LastIndex(profile, "(deny file-read* file-write*")
	if allow < 0 || denyRead < allow {
		t.Fatalf("the read/write deny block must follow every write allow:\n%s", profile)
	}
	for _, dir := range []string{secret, state} {
		if at := strings.LastIndex(profile, `(subpath "`+dir+`")`); at < denyRead {
			t.Fatalf("%s is not inside the final deny block:\n%s", dir, profile)
		}
	}

	// bubblewrap: both directories are a tmpfs, never a writable bind.
	args := BwrapArgs(workspace, home)
	tmpfs := func(dir string) bool {
		for i, arg := range args {
			if arg == "--tmpfs" && i+1 < len(args) && args[i+1] == dir {
				return true
			}
		}
		return false
	}
	for _, dir := range []string{secret, state} {
		if !tmpfs(dir) {
			t.Fatalf("%s is not a tmpfs: %q", dir, args)
		}
	}

	// The default override file lives inside the denied directory, where a
	// worker can neither read it nor write a rule of its own.
	if overrides := BlackboxOverridesPath(); !strings.HasPrefix(overrides, secret+string(os.PathSeparator)) {
		t.Fatalf("override file %q is not inside %s", overrides, secret)
	}
}

func TestJailEnvDropsBlackboxSettings(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"LANG=C",
		"CAPTAIN_LEGS=claude",
		"CAPTAIN_BLACKBOX=enforce",
		"CAPTAIN_BLACKBOX_OVERRIDES=/tmp/overrides.json",
		"CAPTAIN_BLACKBOX_STATE=/tmp/security.jsonl",
		"CAPTAIN_BLACKBOX_SNAPSHOT=/tmp/snapshot.json",
		"CAPTAIN_BLACKBOX_STORE=/tmp/store",
	}
	names := map[string]bool{}
	for _, kv := range JailEnv(in) {
		name, _, _ := strings.Cut(kv, "=")
		names[name] = true
		if strings.HasPrefix(name, "CAPTAIN_BLACKBOX") {
			t.Fatalf("a worker shell received the enforcement setting %s", name)
		}
	}
	for _, keep := range []string{"PATH", "LANG", "CAPTAIN_LEGS", "GOPROXY", "CAPTAIN_JAILED"} {
		if !names[keep] {
			t.Fatalf("JailEnv dropped %s: %v", keep, names)
		}
	}
}
