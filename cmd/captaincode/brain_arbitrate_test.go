package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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
	assert.Contains(t, out, "director's call on parse.go: w2-codex's changes land")
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

// runTeamPlan runs one team turn with the given workers in a git repo.
func runTeamPlan(t *testing.T, b *brain, repo, task string, workers []captaincode.Worker) string {
	t.Helper()
	b.storeTeamPlan(task, captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "split", Workers: workers})
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": task}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions?cwd="+url.QueryEscape(repo), bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	return rec.Body.String()
}

func readRepoFile(t *testing.T, repo, name string) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(repo, name))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(got)
}

var fourWorkers = []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "x"}, {Leg: captaincode.LegCodex, Brief: "x"},
	{Leg: captaincode.LegGrok, Brief: "y"}, {Leg: captaincode.LegCursor, Brief: "y"}}

// fourWayEdits: claude and codex change x.go, grok and cursor change y.go.
var fourWayEdits = map[captaincode.Leg]map[string]string{
	captaincode.LegClaude: {"x.go": "package p // claude\n"},
	captaincode.LegCodex:  {"x.go": "package p // codex\n"},
	captaincode.LegGrok:   {"y.go": "package p // grok\n"},
	captaincode.LegCursor: {"y.go": "package p // cursor\n"},
}

// ROADMAP Q7: two separate overlaps get two rulings. Before this, one ruling
// covered every contested worker: one of the four landed and three were
// dropped, although x.go and y.go never touched each other.
func TestTeamRulesEachConflictGroup(t *testing.T) {
	repo := arbitrationRepo(t)
	b := teamBrain()
	b.runWorkerFn = editingWorker(t, fourWayEdits)
	var mu sync.Mutex
	var asked [][]string
	b.arbitrateFn = func(task string, c map[string]captaincode.Contender) (captaincode.Ruling, error) {
		mu.Lock()
		defer mu.Unlock()
		var ids []string
		for id := range c {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		asked = append(asked, ids)
		if _, ok := c["w2-codex"]; ok {
			return captaincode.Ruling{Winner: "w2-codex", Reason: "smaller x change"}, nil
		}
		return captaincode.Ruling{Winner: "w3-grok", Reason: "keeps the y tests"}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "ANSWER"}, nil
	}

	out := runTeamPlan(t, b, repo, "fix x and y", fourWorkers)

	assert.Equal(t, [][]string{{"w1-claude", "w2-codex"}, {"w3-grok", "w4-cursor"}}, asked,
		"one ruling per group, x.go's group first, each asked only about its own members")
	assert.Equal(t, "package p // codex\n", readRepoFile(t, repo, "x.go"))
	assert.Equal(t, "package p // grok\n", readRepoFile(t, repo, "y.go"), "the second group's winner lands too")
	assert.Contains(t, out, "director's call on x.go: w2-codex's changes land")
	assert.Contains(t, out, "director's call on y.go: w3-grok's changes land")

	stages := b.stageIntegrationsFor(firstTaskID(t, b))
	require.Len(t, stages, 1, "the team's integration is kept for `captain task inspect`")
	require.Len(t, stages[0].Rulings, 2)
	assert.Equal(t, []string{"x.go"}, stages[0].Rulings[0].Files)
	assert.Equal(t, []string{"w1-claude"}, stages[0].Rulings[0].Dropped)
	assert.Equal(t, []string{"y.go"}, stages[0].Rulings[1].Files)
	assert.Equal(t, []string{"w4-cursor"}, stages[0].Rulings[1].Dropped)
	lines := rulingLines(stages[0].Rulings)
	require.Len(t, lines, 2)
	assert.Equal(t, "ruling on x.go: w2-codex lands, set aside: w1-claude - smaller x change", lines[0])
}

// A worker that shares no file with another worker is in no group: it lands
// as it is, and the director rules only on the overlap.
func TestTeamDisjointWorkerLandsWithoutRuling(t *testing.T) {
	repo := arbitrationRepo(t)
	b := teamBrain()
	b.runWorkerFn = editingWorker(t, map[captaincode.Leg]map[string]string{
		captaincode.LegClaude: {"x.go": "package p // claude\n"},
		captaincode.LegCodex:  {"x.go": "package p // codex\n"},
		captaincode.LegGrok:   {"z.go": "package p // grok\n"},
	})
	rulings := 0
	b.arbitrateFn = func(task string, c map[string]captaincode.Contender) (captaincode.Ruling, error) {
		rulings++
		_, inRuling := c["w3-grok"]
		assert.False(t, inRuling, "the disjoint worker is not a contender")
		return captaincode.Ruling{Winner: "w1-claude", Reason: "fewer lines"}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "ANSWER"}, nil
	}
	runTeamPlan(t, b, repo, "fix x, add z", []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "x"},
		{Leg: captaincode.LegCodex, Brief: "x"}, {Leg: captaincode.LegGrok, Brief: "z"}})

	assert.Equal(t, 1, rulings)
	assert.Equal(t, "package p // claude\n", readRepoFile(t, repo, "x.go"))
	assert.Equal(t, "package p // grok\n", readRepoFile(t, repo, "z.go"))
}

// Two workers on one leg are two owners. Owners used to be recorded by leg,
// so the conflict read "claude, claude" and named nobody the director could
// pick.
func TestTeamConflictOwnersNamedByWorker(t *testing.T) {
	repo := arbitrationRepo(t)
	b := teamBrain()
	var mu sync.Mutex
	n := 0
	b.runWorkerFn = func(leg captaincode.Leg, brief string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		m := workerDirRe.FindStringSubmatch(brief)
		require.Len(t, m, 2)
		mu.Lock()
		n++
		body := fmt.Sprintf("package p // take %d\n", n)
		mu.Unlock()
		require.NoError(t, os.WriteFile(filepath.Join(m[1], "parse.go"), []byte(body), 0o644))
		return leg, captaincode.Result{Text: "rewrote parse.go"}, nil
	}
	var asked []string
	b.arbitrateFn = func(task string, c map[string]captaincode.Contender) (captaincode.Ruling, error) {
		for id := range c {
			asked = append(asked, id)
		}
		return captaincode.Ruling{Winner: "w2-claude", Reason: "clearer"}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "ANSWER"}, nil
	}
	out := runTeamPlan(t, b, repo, "two takes on the parser", []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "a"},
		{Leg: captaincode.LegClaude, Brief: "b"}})

	assert.ElementsMatch(t, []string{"w1-claude", "w2-claude"}, asked)
	assert.Contains(t, out, "[conflict] parse.go ← w1-claude, w2-claude")
	ic, ok := b.lastIntegration(firstTaskID(t, b))
	require.True(t, ok)
	require.Len(t, ic.Conflicts, 1)
	assert.Equal(t, []string{"w1-claude", "w2-claude"}, ic.Conflicts[0].Workers)
	assert.Equal(t, captaincode.IntegrationResolved, ic.Status)
}

// Every group's ruling is reserved before the first one runs: two director
// attempts per group (the call and one retry for a malformed reply). A
// budget that cannot cover every group rules none, applies nothing and
// keeps the diffs. A budget that can settles what ran and holds nothing.
func TestTeamRulingsFitAttemptBudget(t *testing.T) {
	for _, tc := range []struct {
		cap   string
		ruled int
	}{{"3", 0}, {"4", 2}} {
		t.Run("cap "+tc.cap, func(t *testing.T) {
			t.Setenv("CAPTAIN_MAX_ATTEMPTS", tc.cap)
			repo := arbitrationRepo(t)
			b := teamBrain()
			b.runWorkerFn = editingWorker(t, fourWayEdits)
			ruled := 0
			b.arbitrateFn = func(task string, c map[string]captaincode.Contender) (captaincode.Ruling, error) {
				ruled++
				for _, id := range []string{"w1-claude", "w3-grok"} {
					if _, ok := c[id]; ok {
						return captaincode.Ruling{Winner: id, Reason: "ok"}, nil
					}
				}
				return captaincode.Ruling{}, assert.AnError
			}
			b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
				return captaincode.MultiAssessment{Synthesis: "ANSWER"}, nil
			}
			out := runTeamPlan(t, b, repo, "fix x and y", fourWorkers)

			assert.Equal(t, tc.ruled, ruled)
			bud := workflowBudget(t, b)
			assert.Zero(t, bud.ReservedAttempts, "no reservation is left behind")
			if tc.ruled == 0 {
				assert.Contains(t, out, "cannot cover 2 conflict ruling(s) (4 director attempts) - nothing applied; the diffs are kept")
				assert.Empty(t, readRepoFile(t, repo, "x.go"))
				assert.Empty(t, readRepoFile(t, repo, "y.go"))
				ic, ok := b.lastIntegration(firstTaskID(t, b))
				require.True(t, ok)
				assert.Equal(t, captaincode.IntegrationConflicted, ic.Status)
				for _, m := range ic.Manifests {
					_, err := os.Stat(m.DiffPath)
					assert.NoError(t, err, "%s's diff is kept as an artifact", m.Worker)
				}
				return
			}
			assert.Equal(t, "package p // claude\n", readRepoFile(t, repo, "x.go"))
			assert.Equal(t, "package p // grok\n", readRepoFile(t, repo, "y.go"))
		})
	}
}

// firstTaskID is the one task a test turn opened.
func firstTaskID(t *testing.T, b *brain) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Len(t, b.ledger.Budgets, 1, "one turn opens one task")
	return b.ledger.Budgets[0].TaskID
}
