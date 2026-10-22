package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The brain adds a private repository's name on its own, says so on the
// folder's next turn, and answers /private without a worker.
func TestTheBrainCuratesPrivateNamesAndAnswersPrivate(t *testing.T) {
	t.Setenv("CAPTAIN_PRIVATE_NAMES", filepath.Join(t.TempDir(), "names"))
	prev := captaincode.RepoVisibility
	captaincode.RepoVisibility = func(string) string { return "private" }
	defer func() { captaincode.RepoVisibility = prev }()
	repo := filepath.Join(t.TempDir(), "zephyrine")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	require.NoError(t, os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{"name":"@acme/quillcore"}`), 0o644))

	b := teamBrain()
	b.curatePrivateNames(repo)
	assert.Contains(t, captaincode.PrivateNames(), "zephyrine")
	note := b.privateNotice(repo)
	assert.Contains(t, note, "added zephyrine")
	assert.Contains(t, note, "suggested quillcore (package name)")
	assert.Empty(t, b.privateNotice(repo), "told once")
	b.curatePrivateNames(repo)
	assert.Empty(t, b.privateNotice(repo), "a folder is looked at once a day")

	ran := false
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = true
		return leg, captaincode.Result{Text: "x"}, nil
	}
	ask := func(text string) string {
		body, _ := json.Marshal(map[string]any{"model": "auto", "stream": false, "messages": []map[string]string{{"role": "user", "content": text}}})
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		r.Header.Set(workspaceHeader, repo)
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, r)
		return rec.Body.String()
	}
	assert.Contains(t, ask("/private"), "quillcore")
	assert.Contains(t, ask("/private add quillcore lumenvale"), "added quillcore, lumenvale")
	assert.Contains(t, captaincode.PrivateNames(), "lumenvale", "any word the user names")
	assert.Equal(t, "zephyrine", captaincode.NameRecords()["quillcore"].Repo, "an accepted suggestion keeps its project")
	assert.False(t, ran, "/private never reaches a worker")
}

// A worker in a private repository is told it may name that work; one in a
// public repository keeps the names out and runs the check.
func TestTheWorkerPrivacyRuleFollowsTheRepositorysVisibility(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	prev := captaincode.RepoVisibility
	defer func() { captaincode.RepoVisibility = prev }()
	ws := captaincode.Workspace{Dir: repo}

	captaincode.RepoVisibility = func(string) string { return "private" }
	assert.Contains(t, privacyRules(ws), "this repository is private, so it may name itself")
	captaincode.RepoVisibility = func(string) string { return "public" }
	assert.Contains(t, privacyRules(ws), "in a public repository")
	assert.Contains(t, privacyRules(ws), "captain leakcheck --staged")
	captaincode.RepoVisibility = func(string) string { return "unknown" }
	assert.Contains(t, privacyRules(ws), "in a public repository", "a visibility GitHub cannot confirm keeps the strict rule")
}
