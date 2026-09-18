package main

import (
	"os"
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
	// Never start an opencode serve from a test: one spawned on the live
	// port with this throwaway HOME was adopted by the running brain and
	// broke every opencode leg (2026-09-18).
	os.Setenv("CAPTAIN_OPENCODE_SPAWN", "0")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
