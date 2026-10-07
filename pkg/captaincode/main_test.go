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
	os.Setenv("CAPTAIN_EUCLID_AUTOINDEX", "0")          // no background engine runs against temp brains
	os.Setenv("CAPTAIN_OPENSHELL_ADVISORY_REVIEW", "0") // tests opt in with a fake reviewer
	// A captain session sets these. That connection is bound to the live
	// brain. A temp brain is a different root, so journal, learn and
	// orientation fail closed ("bound to a different brain") instead of
	// using the files the test built. A test that wants MCP sets the path
	// itself.
	os.Unsetenv("CAPTAIN_EUCLID_MCP_CONFIG")
	os.Unsetenv("CAPTAIN_EUCLID_MEMORY_CONFIG")
	os.Exit(m.Run())
}
