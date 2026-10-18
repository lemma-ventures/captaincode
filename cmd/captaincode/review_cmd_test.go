package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestReviewCLICheck(t *testing.T) {
	d := t.TempDir()
	commit := strings.Repeat("a", 40)
	evidence := filepath.Join(d, "evidence.json")
	claims := filepath.Join(d, "claims.json")
	out := filepath.Join(d, "check.json")
	ev := `{"version":1,"declared_commit":"` + commit + `","archive_sha256":"` + strings.Repeat("b", 64) + `","scope":[],"files":[{"path":"root/README.md","sha256":"` + strings.Repeat("c", 64) + `","text":"alpha\nbeta"}],"excluded":[]}`
	cs := `{"version":1,"declared_commit":"` + commit + `","claims":[{"id":"C01","type":"fact","statement":"alpha is first","path":"README.md","start":1,"end":1,"quote":"alpha"},{"id":"C02","type":"fact","statement":"missing file","path":"gone.md","start":1,"end":1,"quote":"alpha"}]}`
	if err := os.WriteFile(evidence, []byte(ev), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claims, []byte(cs), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"check", "--evidence", evidence, "--claims", claims, "--out", out}
	if err := executeReview(args); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("check log is not private")
	}
	var log struct {
		ArchiveSHA256 string `json:"archive_sha256"`
		ClaimsSHA256  string `json:"claims_sha256"`
		Counts        struct {
			Present int `json:"present"`
			Dropped int `json:"dropped"`
		} `json:"counts"`
		Results []struct {
			ID     string `json:"id"`
			Result string `json:"result"`
			Reason string `json:"reason"`
		} `json:"results"`
	}
	if json.Unmarshal(b, &log) != nil || log.Counts.Present != 1 || log.Counts.Dropped != 1 {
		t.Fatalf("bad check log: %s", b)
	}
	if log.ArchiveSHA256 == "" || log.ClaimsSHA256 == "" || log.Results[1].Reason != "unknown_path" {
		t.Fatalf("log not bound or wrong reason: %s", b)
	}
	if err := executeReview(args); err == nil {
		t.Fatal("existing check log overwritten")
	}
	if err := executeReview([]string{"check", "--claims", claims, "--out", out + "2"}); err == nil {
		t.Fatal("missing evidence accepted")
	}
	hostile := filepath.Join(d, "hostile.json")
	if err := os.WriteFile(hostile, []byte(`{"version":1,"declared_commit":"`+strings.Repeat("b", 40)+`","claims":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	out2 := filepath.Join(d, "check2.json")
	err = executeReview([]string{"check", "--evidence", evidence, "--claims", hostile, "--out", out2})
	if err == nil {
		t.Fatal("commit mismatch accepted")
	}
	if _, statErr := os.Stat(out2); statErr == nil {
		t.Fatal("failed check left an output record")
	}
	badEvidence := filepath.Join(d, "bad.json")
	if err := os.WriteFile(badEvidence, []byte(`{"version":1,"extra":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := executeReview([]string{"check", "--evidence", badEvidence, "--claims", claims, "--out", out + "3"}); err == nil {
		t.Fatal("malformed evidence accepted")
	}
}
