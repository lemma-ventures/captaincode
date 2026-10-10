package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReviewCLIRejectsUnsafeInput(t *testing.T) {
	d := t.TempDir()
	file := filepath.Join(d, "prompt")
	if err := os.WriteFile(file, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(d, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{link, fifo, d} {
		if _, err := readReviewInput(name, 1024); err == nil {
			t.Fatalf("unsafe input accepted: %s", name)
		}
	}
	if _, err := readReviewInput(file, 3); err == nil {
		t.Fatal("size limit ignored")
	}
	if err := executeReview([]string{"run", "raw task text"}); err == nil {
		t.Fatal("positional prompt accepted")
	}
}

func TestReviewCLIExclusiveOutput(t *testing.T) {
	d := t.TempDir()
	profile := filepath.Join(d, "profile.json")
	prompt := filepath.Join(d, "prompt.txt")
	out := filepath.Join(d, "run.json")
	data := `{"version":1,"legs":[{"id":"primary","tier":"frontier","vendor":"fixture","model":"fixture","endpoint":"https://example.invalid/v1/chat/completions","transport":"chat-completions","auth_env":"REVIEW_ABSENT_KEY","terms":"fixture","retention":"fixture"}]}`
	t.Setenv("REVIEW_ABSENT_KEY", "")
	if err := os.WriteFile(profile, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prompt, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--profile", profile, "--prompt", prompt, "--tier", "frontier", "--legs", "primary", "--out", out}
	if err := executeReview(args); err == nil {
		t.Fatal("missing credential accepted")
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if json.Unmarshal(b, &record) != nil {
		t.Fatal("no failure record")
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatal("output is not private")
	}
	if err := executeReview(args); err == nil {
		t.Fatal("existing record overwritten")
	}
	after, _ := os.ReadFile(out)
	if string(after) != string(b) {
		t.Fatal("existing record changed")
	}
}
