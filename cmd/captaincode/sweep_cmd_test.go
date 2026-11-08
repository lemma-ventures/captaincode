package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReposRootSetting(t *testing.T) {
	home, _ := os.UserHomeDir()
	t.Setenv("CAPTAIN_REPOS_ROOT", "")
	if got := reposRoot(); got != filepath.Join(home, "Gits") {
		t.Fatalf("default = %q", got)
	}
	t.Setenv("CAPTAIN_REPOS_ROOT", "/srv/repos")
	if got := reposRoot(); got != "/srv/repos" {
		t.Fatalf("absolute = %q", got)
	}
	t.Setenv("CAPTAIN_REPOS_ROOT", "~/work")
	if got := reposRoot(); got != filepath.Join(home, "work") {
		t.Fatalf("home-relative = %q", got)
	}
}
