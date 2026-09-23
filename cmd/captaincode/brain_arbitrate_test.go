package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// arbitrationRepo is a committed git repo with one file both workers edit.
func arbitrationRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "--quiet", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "parse.go"), []byte("package p\n"), 0o644))
	run("add", ".")
	run("commit", "--quiet", "-m", "base")
	return dir
}

var workerDirRe = regexp.MustCompile(`repository at (\S+?)\. `)

// editingWorker is a team worker stub that writes files in the directory
// its brief names - its own worktree when the team is isolated.
func editingWorker(t *testing.T, edits map[captaincode.Leg]map[string]string) func(captaincode.Leg, string, func(string), func(string)) (captaincode.Leg, captaincode.Result, error) {
	return func(leg captaincode.Leg, brief string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		m := workerDirRe.FindStringSubmatch(brief)
		if !assert.Len(t, m, 2, "brief names no working directory") {
			return leg, captaincode.Result{}, nil
		}
		for name, body := range edits[leg] {
			require.NoError(t, os.WriteFile(filepath.Join(m[1], name), []byte(body), 0o644))
		}
		return leg, captaincode.Result{Text: string(leg) + " rewrote parse.go"}, nil
	}
}

func runTeamTurn(t *testing.T, b *brain, repo, task string) string {
	t.Helper()
	b.storeTeamPlan(task, captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "two takes",
		Workers: []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "fix it"}, {Leg: captaincode.LegCodex, Brief: "fix it"}}})
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": task}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions?cwd="+url.QueryEscape(repo), bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	return rec.Body.String()
}

// Claude and Codex both rewrite parse.go. The director picks Codex: Codex's
// file lands whole, Claude's does not, and the synthesis is told which one
// was applied so it does not describe both as done.
func TestTeamConflictIsTheDirectorsCall(t *testing.T) {
	repo := arbitrationRepo(t)
	b := teamBrain()
	b.runWorkerFn = editingWorker(t, map[captaincode.Leg]map[string]string{
		captaincode.LegClaude: {"parse.go": "package p // claude\n", "extra.go": "package p\n"},
		captaincode.LegCodex:  {"parse.go": "package p // codex\n"},
	})
	var mu sync.Mutex
	var asked map[string]captaincode.Contender
	b.arbitrateFn = func(task string, c map[string]captaincode.Contender) (captaincode.Ruling, error) {
		mu.Lock()
		asked = c
		mu.Unlock()
		return captaincode.Ruling{Winner: "w2-codex", Reason: "smaller change"}, nil
	}
	var objective string
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, obj string) (captaincode.MultiAssessment, error) {
		objective = obj
		return captaincode.MultiAssessment{Synthesis: "ANSWER"}, nil
	}

	out := runTeamTurn(t, b, repo, "fix the parser")

	require.Len(t, asked, 2, "both workers that touched parse.go go before the director")
	assert.ElementsMatch(t, []string{"parse.go", "extra.go"}, asked["w1-claude"].Files)
	got, err := os.ReadFile(filepath.Join(repo, "parse.go"))
	require.NoError(t, err)
	assert.Equal(t, "package p // codex\n", string(got), "the winner's version lands, not a splice")
	_, err = os.Stat(filepath.Join(repo, "extra.go"))
	assert.True(t, os.IsNotExist(err), "the losing worker's changes are set aside whole")
	assert.Contains(t, out, "director's call: w2-codex's changes land")
	assert.Contains(t, objective, "only w2-codex's changes were applied")
	assert.Contains(t, objective, "w1-claude's changes were NOT applied")

	require.Len(t, b.lastIntegrations, 1, "the ruling is kept for `captain task artifacts`")
	for _, ic := range b.lastIntegrations {
		assert.Equal(t, captaincode.IntegrationResolved, ic.Status)
		assert.Equal(t, []string{"w1-claude"}, ic.Dropped)
	}
}

// No ruling (director down, or it names someone who was not in the running)
// means nothing lands: the harness does not guess a winner.
func TestTeamConflictWithoutARulingAppliesNothing(t *testing.T) {
	for name, ruling := range map[string]func(string, map[string]captaincode.Contender) (captaincode.Ruling, error){
		"director down": func(string, map[string]captaincode.Contender) (captaincode.Ruling, error) {
			return captaincode.Ruling{}, assert.AnError
		},
		"not a worker": func(string, map[string]captaincode.Contender) (captaincode.Ruling, error) {
			return captaincode.Ruling{Winner: "grok"}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := arbitrationRepo(t)
			b := teamBrain()
			b.runWorkerFn = editingWorker(t, map[captaincode.Leg]map[string]string{
				captaincode.LegClaude: {"parse.go": "package p // claude\n"},
				captaincode.LegCodex:  {"parse.go": "package p // codex\n"},
			})
			b.arbitrateFn = ruling
			var objective string
			b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, obj string) (captaincode.MultiAssessment, error) {
				objective = obj
				return captaincode.MultiAssessment{Synthesis: "ANSWER"}, nil
			}
			out := runTeamTurn(t, b, repo, "fix the parser")
			got, err := os.ReadFile(filepath.Join(repo, "parse.go"))
			require.NoError(t, err)
			assert.Equal(t, "package p\n", string(got))
			assert.Contains(t, out, "nothing applied")
			assert.Contains(t, objective, "NONE of their file changes were applied")
		})
	}
}

// Workers that changed different files are not a disagreement: both land,
// and the director is not asked. Before this, an isolated team's file
// changes were discarded with its worktrees.
func TestTeamDisjointChangesBothLand(t *testing.T) {
	repo := arbitrationRepo(t)
	b := teamBrain()
	b.runWorkerFn = editingWorker(t, map[captaincode.Leg]map[string]string{
		captaincode.LegClaude: {"a.go": "package p // a\n"},
		captaincode.LegCodex:  {"b.go": "package p // b\n"},
	})
	b.arbitrateFn = func(string, map[string]captaincode.Contender) (captaincode.Ruling, error) {
		t.Error("no conflict, no ruling")
		return captaincode.Ruling{}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, obj string) (captaincode.MultiAssessment, error) {
		assert.Equal(t, "none", obj)
		return captaincode.MultiAssessment{Synthesis: "ANSWER"}, nil
	}
	out := runTeamTurn(t, b, repo, "do a and b")
	for name, want := range map[string]string{"a.go": "package p // a\n", "b.go": "package p // b\n"} {
		got, err := os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err, name)
		assert.Equal(t, want, string(got))
	}
	assert.True(t, strings.Contains(out, "applied to workspace"), out)
}

// A parallel workflow stage takes the same road: overlapping changes go to
// the director, and the winner's land when the review is done. Before this a
// conflicted stage applied nothing and nobody was asked.
func TestWorkflowStageConflictIsTheDirectorsCall(t *testing.T) {
	repo := arbitrationRepo(t)
	b := teamBrain()
	b.runWorkerFn = editingWorker(t, map[captaincode.Leg]map[string]string{
		captaincode.LegClaude: {"parse.go": "package p // claude\n"},
		captaincode.LegCodex:  {"parse.go": "package p // codex\n"},
	})
	var asked []string
	b.arbitrateFn = func(task string, c map[string]captaincode.Contender) (captaincode.Ruling, error) {
		for id := range c {
			asked = append(asked, id)
		}
		return captaincode.Ruling{Winner: "s1w2-claude", Reason: "handles the edge case"}, nil
	}
	var objective string
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, obj string) (captaincode.MultiAssessment, error) {
		objective = obj
		return captaincode.MultiAssessment{Synthesis: "REVIEWED"}, nil
	}
	req := wfReq(false, "/codex fix the parser + /claude fix the parser")
	req.URL.RawQuery = "cwd=" + url.QueryEscape(repo)
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	assert.ElementsMatch(t, []string{"s1w1-codex", "s1w2-claude"}, asked)
	got, err := os.ReadFile(filepath.Join(repo, "parse.go"))
	require.NoError(t, err)
	assert.Equal(t, "package p // claude\n", string(got))
	assert.Contains(t, objective, "only s1w2-claude's changes were applied")
}
