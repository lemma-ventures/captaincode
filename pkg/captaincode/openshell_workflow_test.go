package captaincode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOpenShellWorkflowBoundary(t *testing.T) {
	for _, tc := range []struct{ prompt, want string }{
		{"/openshell edit a > /cursor edit b", "host workers"},
		{"/openshell edit a + /cursor edit b", "host workers"},
		{"/openshell edit a + /openshell edit b gate: touch marker", "host gates"},
		{"/openshell edit a gate: touch marker", "host gates"},
		{"/openshell + /openshell", "assignment"},
		{"/openshell a + /openshell b + /openshell c + /openshell d + /openshell e", "at most"},
		{"/openshell a + /unknown b", "unknown leg"},
	} {
		t.Run(tc.prompt, func(t *testing.T) {
			_, err := ParseOpenShellWorkflow(tc.prompt)
			require.ErrorContains(t, err, tc.want)
		})
	}
	for _, task := range []string{"fix the parser", "/openshell fix the parser", "document a > b and a + b"} {
		wf, err := ParseOpenShellWorkflow(task)
		require.NoError(t, err)
		assert.Empty(t, wf.Stages)
	}
	wf, err := ParseOpenShellWorkflow("/openshell edit a + /openshell edit b")
	require.NoError(t, err)
	require.Len(t, wf.Stages, 1)
	assert.Len(t, wf.Stages[0].Legs, 2)
}

func TestOpenShellParallelWorkflowCancellationStopsEveryController(t *testing.T) {
	r := newFakeOpenShell(t)
	r.RequireAll = true
	team := OpenShellTeam{Schema: 1, ID: "cancel-parallel", Tasks: []OpenShellTask{
		fakeOpenShellTask("w1-openshell", "nim", `{"hang":true}`, "all", "a.txt"),
		fakeOpenShellTask("w2-openshell", "nim", `{"hang":true}`, "all", "b.txt"),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var res Result
	var runErr error
	go func() {
		defer close(done)
		res, runErr = runOpenShellTeam(ctx, r, team, nil)
	}()
	deadline := time.Now().Add(10 * time.Second)
	var started []string
	for time.Now().Before(deadline) {
		started, _ = filepath.Glob(filepath.Join(r.StateRoot, "*", "started"))
		if len(started) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("parallel controllers did not finish cancellation")
	}
	require.Len(t, started, 2)
	require.Error(t, runErr)
	assert.Nil(t, res.Export)
	assert.NotContains(t, res.Text, "apply with:")
	for _, path := range started {
		assert.FileExists(t, filepath.Join(filepath.Dir(path), "cleaned"))
	}
}

func TestOpenShellWorkflowScopesAssignmentAndPreservesConfiguration(t *testing.T) {
	r := openShellLegEnv(t)
	t.Setenv("CAPTAIN_OPENSHELL_ALLOWED", "a.txt,b.txt")
	t.Setenv("CAPTAIN_OPENSHELL_PROTECTED", "c.txt,d.txt")
	_, template, err := openShellConfig(context.Background(), r.Repo, "parallel")
	require.NoError(t, err)
	wf, err := ParseOpenShellWorkflow("/openshell edit a + /openshell edit b")
	require.NoError(t, err)
	team, err := openShellWorkflowTeam(template, wf, "[user]\nkeep the public API\n")
	require.NoError(t, err)
	require.Len(t, team.Tasks, 2)
	for i, task := range team.Tasks {
		assert.Equal(t, template.Tasks[0].Allowed, task.Allowed)
		assert.Equal(t, template.Tasks[0].Protected, task.Protected)
		assert.Equal(t, template.Tasks[0].Verify, task.Verify)
		assert.Equal(t, template.Tasks[0].Profile, task.Profile)
		assert.Equal(t, 1, task.RepairAttempts)
		assert.Contains(t, task.Prompt, "keep the public API")
		assert.Contains(t, task.Prompt, wf.Stages[0].Legs[i].Prompt)
		assert.NotContains(t, task.Prompt, wf.Stages[0].Legs[1-i].Prompt)
	}
	assert.Equal(t, "w1-openshell", team.Tasks[0].ID)
	assert.Equal(t, "w2-openshell", team.Tasks[1].ID)
	_, err = openShellWorkflowTeam(template, wf, strings.Repeat("x", 16384))
	require.ErrorContains(t, err, "prompt must be")
	wf.Stages[0].Legs[0].Prompt = "changed assignment"
	changed, err := openShellWorkflowTeam(template, wf, "[user]\nkeep the public API\n")
	require.NoError(t, err)
	assert.NotEqual(t, team.ID, changed.ID)
}

func TestOpenShellParallelWorkflowExportsBothChanges(t *testing.T) {
	r := openShellLegEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPTAIN_OPENSHELL_ALLOWED", "a.txt,b.txt")
	t.Setenv("CAPTAIN_OPENSHELL_CONCURRENCY", "2")
	for _, key := range []string{"CAPTAIN_MAX_ATTEMPTS", "CAPTAIN_MAX_WALLTIME", "CAPTAIN_MAX_COST", "CAPTAIN_STRICT"} {
		t.Setenv(key, "")
	}
	index, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(r.Repo, "a.txt"), []byte("operator's uncommitted edit\n"), 0o644))
	wf, err := ParseOpenShellWorkflow(`/openshell {"write":{"a.txt":"first\n"}} + /openshell {"write":{"b.txt":"second\n"}}`)
	require.NoError(t, err)
	res, err := (Workspace{Dir: r.Repo}).RunOpenShellWorkflow(context.Background(), wf, "")
	require.NoError(t, err, res.Text)
	require.NotNil(t, res.Export)
	assert.Equal(t, []string{"a.txt", "b.txt"}, res.Export.Manifest.ChangedFiles)
	assert.Equal(t, r.Revision, res.Export.Manifest.BaseRevision)
	assert.Contains(t, res.Text, "2/2 task(s) passed")
	assert.Contains(t, res.Text, "exported (not applied)")
	data, err := os.ReadFile(res.Export.RunRecord)
	require.NoError(t, err)
	var run OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	assert.True(t, run.RequireAll)
	assert.Equal(t, 2, run.Concurrency)
	require.Len(t, run.Tasks, 2)
	for _, worker := range run.Tasks {
		require.NotNil(t, worker.Manifest)
		assert.Equal(t, r.Revision, worker.Manifest.BaseRevision)
	}
	log, err := os.ReadFile(filepath.Join(run.Integrated.Evidence, "baseline-verify.log"))
	require.NoError(t, err)
	assert.Contains(t, string(log), "a.txt=first\n")
	assert.Contains(t, string(log), "b.txt=second\n")
	afterIndex, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, index, afterIndex)
	after, err := os.ReadFile(filepath.Join(r.Repo, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "operator's uncommitted edit\n", string(after))
}

func TestOpenShellParallelWorkflowWithholdsIncompleteExport(t *testing.T) {
	for _, tc := range []struct{ name, prompt, want string }{
		{"worker failure", `{"fail":true}`, "required worker"},
		{"bad digest", `{"write":{"b.txt":"second\n"},"sha":"wrong"}`, "required worker"},
		{"outside scope", `{"write":{"d.txt":"outside\n"}}`, "required worker"},
		{"integration failure", `{"write":{"b.txt":"BREAK\n"}}`, "integrated verification"},
		{"unresolved overlap", `{"write":{"a.txt":"conflict\n"}}`, "required conflict ruling"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeOpenShell(t)
			r.RequireAll = true
			team := OpenShellTeam{Schema: 1, ID: "parallel", Tasks: []OpenShellTask{
				fakeOpenShellTask("w1-openshell", "nim", `{"write":{"a.txt":"first\n"}}`, "all", "a.txt", "b.txt"),
				fakeOpenShellTask("w2-openshell", "nim", tc.prompt, "all", "a.txt", "b.txt"),
				fakeOpenShellTask("w3-openshell", "nim", `{"write":{"c.txt":"independent\n"}}`, "all", "c.txt"),
			}}
			res, err := runOpenShellTeam(context.Background(), r, team, nil)
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, res.Export)
			assert.NotContains(t, res.Text, "apply with:")
			data, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
			require.NoError(t, err)
			var run OpenShellRun
			require.NoError(t, json.Unmarshal(data, &run))
			assert.Equal(t, "fail", run.Verdict)
			require.Len(t, run.Tasks, 3)
			assert.FileExists(t, run.Tasks[0].Manifest.DiffPath)
		})
	}
}

func TestOpenShellSequentialWorkflowHandsOffVerifiedSnapshot(t *testing.T) {
	r := openShellLegEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPTAIN_OPENSHELL_ALLOWED", "a.txt,b.txt,c.txt")
	for _, key := range []string{"CAPTAIN_MAX_ATTEMPTS", "CAPTAIN_MAX_WALLTIME", "CAPTAIN_MAX_COST", "CAPTAIN_STRICT"} {
		t.Setenv(key, "")
	}
	index, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(r.Repo, "a.txt"), []byte("operator edit\n"), 0o644))
	wf, err := ParseWorkflow(`/openshell {"write":{"a.txt":"first\n"}} + /openshell {"write":{"b.txt":"parallel\n"}} > /openshell {"expect":{"a.txt":"first\n","b.txt":"parallel\n"},"write":{"a.txt":"second\n","c.txt":"derived\n"}}`)
	require.NoError(t, err)
	res, err := (Workspace{Dir: r.Repo}).RunOpenShellWorkflow(context.Background(), wf, "")
	require.NoError(t, err, res.Text)
	require.NotNil(t, res.Export)
	assert.Equal(t, r.Repo, res.Export.Repository)
	assert.Equal(t, r.Revision, res.Export.Manifest.BaseRevision)
	assert.Equal(t, []string{"a.txt", "b.txt", "c.txt"}, res.Export.Manifest.ChangedFiles)
	patch, err := os.ReadFile(res.Export.Manifest.DiffPath)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(patch)), res.Export.Manifest.DiffDigest)
	assert.Contains(t, string(patch), "+second")
	assert.Contains(t, string(patch), "+parallel")
	assert.Contains(t, string(patch), "+derived")
	assert.NotContains(t, string(patch), "+first")
	assert.NotContains(t, string(patch), "operator edit")
	data, err := os.ReadFile(res.Export.RunRecord)
	require.NoError(t, err)
	var run OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	assert.Equal(t, "pass", run.Verdict)
	require.Len(t, run.Stages, 2)
	assert.Equal(t, r.Revision, run.Stages[0].Revision)
	assert.Equal(t, run.Stages[0].NextRevision, run.Stages[1].Revision)
	assert.Equal(t, run.Stages[1].Tree, run.Integrated.Tree)
	for _, stage := range run.Stages {
		assert.FileExists(t, stage.RunRecord)
		assert.Equal(t, "pass", stage.Verdict)
	}
	replayed, err := r.treeWith(context.Background(), patch)
	require.NoError(t, err)
	assert.Equal(t, run.Integrated.Tree, replayed)
	require.Len(t, run.Tasks, 3)
	assert.NotEqual(t, r.Revision, run.Tasks[2].Manifest.BaseRevision)
	log, err := os.ReadFile(filepath.Join(run.Integrated.Evidence, "baseline-verify.log"))
	require.NoError(t, err)
	assert.Contains(t, string(log), "a.txt=second\n")
	assert.Contains(t, string(log), "b.txt=parallel\n")
	assert.Contains(t, string(log), "c.txt=derived\n")
	after, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, index, after)
	after, err = os.ReadFile(filepath.Join(r.Repo, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "operator edit\n", string(after))
}

func TestOpenShellSequenceStopsBeforeNextStage(t *testing.T) {
	for _, tc := range []struct{ name, prompt string }{
		{"worker failure", `{"fail":true}`},
		{"integration failure", `{"write":{"a.txt":"BREAK\n"}}`},
		{"untrusted export", `{"write":{"a.txt":"first\n"},"sha":"wrong"}`},
		{"scope violation", `{"write":{"d.txt":"outside\n"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeOpenShell(t)
			teams := []OpenShellTeam{
				{Schema: 1, ID: "s1", Tasks: []OpenShellTask{fakeOpenShellTask("s1-w1", "nim", tc.prompt, "all", "a.txt")}},
				{Schema: 1, ID: "s2", Tasks: []OpenShellTask{fakeOpenShellTask("s2-w1", "nim", `{"write":{"b.txt":"second\n"}}`, "all", "b.txt")}},
			}
			res, err := runOpenShellSequence(context.Background(), r, teams, nil)
			require.Error(t, err)
			assert.Nil(t, res.Export)
			assert.NotContains(t, res.Text, "apply with:")
			assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
			data, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
			require.NoError(t, err)
			var run OpenShellRun
			require.NoError(t, json.Unmarshal(data, &run))
			assert.Equal(t, "fail", run.Verdict)
			require.Len(t, run.Stages, 1)
			assert.Equal(t, "fail", run.Stages[0].Verdict)
			assert.Empty(t, run.Stages[0].NextRevision)
		})
	}
}

func TestOpenShellSequenceCancellationKeepsEarlierEvidenceWithoutExport(t *testing.T) {
	r := newFakeOpenShell(t)
	teams := []OpenShellTeam{
		{Schema: 1, ID: "s1", Tasks: []OpenShellTask{fakeOpenShellTask("s1-w1", "nim", `{"write":{"a.txt":"first\n"}}`, "all", "a.txt")}},
		{Schema: 1, ID: "s2", Tasks: []OpenShellTask{fakeOpenShellTask("s2-w1", "nim", `{"expect":{"a.txt":"first\n"},"hang":true}`, "all", "a.txt")}},
		{Schema: 1, ID: "s3", Tasks: []OpenShellTask{fakeOpenShellTask("s3-w1", "nim", `{"write":{"a.txt":"third\n"}}`, "all", "a.txt")}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var res Result
	var runErr error
	go func() {
		defer close(done)
		res, runErr = runOpenShellSequence(ctx, r, teams, nil)
	}()
	deadline := time.Now().Add(10 * time.Second)
	var started []string
	for time.Now().Before(deadline) {
		started, _ = filepath.Glob(filepath.Join(r.StateRoot, "*", "started"))
		if len(started) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("controller did not finish cancellation")
	}
	require.Len(t, started, 1)
	require.ErrorIs(t, runErr, ErrInterrupted)
	assert.Nil(t, res.Export)
	assert.NotContains(t, res.Text, "apply with:")
	assert.FileExists(t, filepath.Join(filepath.Dir(started[0]), "cleaned"))
	assert.FileExists(t, filepath.Join(r.RunDir, "stage-1", "integrated.patch"))
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-3"))
	data, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
	require.NoError(t, err)
	var run OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	require.Len(t, run.Stages, 2)
	assert.Equal(t, "pass", run.Stages[0].Verdict)
	assert.Equal(t, run.Stages[0].NextRevision, run.Stages[1].Revision)
	assert.Equal(t, "fail", run.Verdict)
}

func TestOpenShellSequenceRejectsInvalidLaterStageBeforeDispatch(t *testing.T) {
	r := openShellLegEnv(t)
	t.Setenv("HOME", t.TempDir())
	wf, err := ParseWorkflow("/openshell edit a > /openshell " + strings.Repeat("x", 16385))
	require.NoError(t, err)
	_, err = (Workspace{Dir: r.Repo}).RunOpenShellWorkflow(context.Background(), wf, "")
	require.ErrorContains(t, err, "prompt must be")
	paths, err := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".captaincode", "openshell", "*"))
	require.NoError(t, err)
	assert.Empty(t, paths)
}

func TestOpenShellSequenceNeverRunsSnapshotHooksOrFilters(t *testing.T) {
	r := newFakeOpenShell(t)
	marker := filepath.Join(t.TempDir(), "host-executed")
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", r.Repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	require.NoError(t, os.WriteFile(filepath.Join(r.Repo, ".gitattributes"), []byte("*.txt filter=host\n"), 0o600))
	git("add", ".gitattributes")
	git("-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "attribute fixture")
	r.Revision = git("rev-parse", "HEAD")
	config := filepath.Join(t.TempDir(), "config")
	hooks := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\ntouch "+shellJoin([]string{marker})+"\n"), 0o700))
	for _, args := range [][]string{
		{"core.hooksPath", hooks},
		{"filter.host.smudge", "touch " + shellJoin([]string{marker}) + "; cat"},
		{"filter.host.clean", "cat"},
	} {
		git(append([]string{"config", "--file", config}, args...)...)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	teams := []OpenShellTeam{
		{Schema: 1, ID: "s1", Tasks: []OpenShellTask{fakeOpenShellTask("s1-w1", "nim", `{"write":{"a.txt":"first\n"}}`, "all", "a.txt")}},
		{Schema: 1, ID: "s2", Tasks: []OpenShellTask{fakeOpenShellTask("s2-w1", "nim", `{"expect":{"a.txt":"first\n"},"write":{"a.txt":"second\n"}}`, "all", "a.txt")}},
	}
	res, err := runOpenShellSequence(context.Background(), r, teams, nil)
	require.NoError(t, err, res.Text)
	require.NotNil(t, res.Export)
	assert.NoFileExists(t, marker)
}
