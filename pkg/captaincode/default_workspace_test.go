package captaincode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A brain pinned to a folder this machine does not have (a migrated
// CAPTAIN_CWD=~/.buzz) falls back to captain's own folder instead of failing
// every call that names no project.
func TestRepairDefaultWorkspaceReplacesAMissingFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_CWD", filepath.Join(home, ".buzz"))
	assert.Contains(t, RepairDefaultWorkspace(), "does not exist")
	want := filepath.Join(home, ".captaincode", "workers")
	assert.Equal(t, want, os.Getenv("CAPTAIN_CWD"))
	assert.DirExists(t, want)
}

func TestRepairDefaultWorkspaceKeepsAFolderThatExists(t *testing.T) {
	nest := t.TempDir()
	t.Setenv("CAPTAIN_CWD", nest)
	assert.Equal(t, "", RepairDefaultWorkspace())
	assert.Equal(t, nest, os.Getenv("CAPTAIN_CWD"), "pinning the brain to Buzz stays an option")

	t.Setenv("CAPTAIN_CWD", "")
	assert.Equal(t, "", RepairDefaultWorkspace())
}
