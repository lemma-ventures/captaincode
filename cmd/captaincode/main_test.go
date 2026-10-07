package main

import (
	"os"
	"os/exec"
	"testing"
)

// Tests exercise the wrapper end to end, and the wrapper writes run history and
// transcripts under $HOME. Without an isolated HOME the suite scribbles test
// data into the user's real ~/.captaincode (observed 2026-07-31: "a b" and
// "draft it audit it" runs in `captain runs`).
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "captain-test-home")
	if err != nil {
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	// A request that names no workspace falls back to CAPTAIN_CWD, else the
	// process cwd: this checkout. Test turns then diffed the developer's
	// uncommitted work and reset their index, and team turns cut worktrees
	// from it (2026-09-30). The fallback is a throwaway repository instead.
	ws, err := os.MkdirTemp("", "captain-test-workspace")
	if err != nil {
		os.Exit(1)
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "seed"},
	} {
		if err := exec.Command("git", append([]string{"-C", ws}, args...)...).Run(); err != nil {
			os.Exit(1)
		}
	}
	os.Setenv("CAPTAIN_CWD", ws)
	// Never start an opencode serve from a test: one spawned on the live
	// port with this throwaway HOME was adopted by the running brain and
	// broke every opencode leg (2026-09-18).
	os.Setenv("CAPTAIN_OPENCODE_SPAWN", "0")
	os.Setenv("CAPTAIN_EUCLID_AUTOINDEX", "0")          // no background engine runs against temp brains
	os.Setenv("CAPTAIN_INBOX_JUDGE", "0")               // no model call per sent prompt; the judge's own tests stub it
	os.Setenv("CAPTAIN_OPENSHELL_ADVISORY_REVIEW", "0") // tests opt in with a fake reviewer
	// Same isolation as pkg/captaincode: the session's MCP config is bound
	// to the live brain, not to a temp brain these tests build.
	os.Unsetenv("CAPTAIN_EUCLID_MCP_CONFIG")
	os.Unsetenv("CAPTAIN_EUCLID_MEMORY_CONFIG")
	// The director-path tests stub the PLAN call; the typed pick (the
	// default since 2026-09-22) has its own tests, which opt in. Solo
	// verification runs real test commands in real repositories; off here.
	os.Setenv("CAPTAIN_DIRECTOR_PICK", "0")
	os.Setenv("CAPTAIN_SOLO_VERIFY", "0")
	os.Setenv("CAPTAIN_TRIAGE_SHADOW_RATE", "0")
	code := m.Run()
	os.RemoveAll(home)
	os.RemoveAll(ws)
	os.Exit(code)
}
