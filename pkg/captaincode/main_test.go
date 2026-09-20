package captaincode

import (
	"os"
	"testing"
)

// A test must never start an opencode serve: one spawned on the live port
// while the real serve was down, under a test's HOME, was adopted by the
// running brain and broke every opencode leg (2026-09-18). A test that needs
// a serve builds an httptest one.
func TestMain(m *testing.M) {
	os.Setenv("CAPTAIN_OPENCODE_SPAWN", "0")
	os.Setenv("CAPTAIN_EUCLID_AUTOINDEX", "0") // no background engine runs against temp brains
	os.Exit(m.Run())
}
