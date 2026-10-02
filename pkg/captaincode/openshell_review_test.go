package captaincode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reviewOpenShellTask(id, prompt string) OpenShellTask {
	return OpenShellTask{ID: id, Mode: "review", Profile: "nim", Prompt: prompt, Verify: []string{"check", "all"}}
}

func TestOpenShellReviewValidation(t *testing.T) {
	task := reviewOpenShellTask("review", "inspect the snapshot")
	require.NoError(t, task.Validate())
	for _, tc := range []struct {
		name string
		edit func(*OpenShellTask)
	}{
		{"scope", func(v *OpenShellTask) { v.Allowed = []string{"a.txt"} }},
		{"failing baseline", func(v *OpenShellTask) { v.Baseline = "fail" }},
		{"any baseline", func(v *OpenShellTask) { v.Baseline = "any" }},
		{"repair", func(v *OpenShellTask) { v.RepairAttempts = 1 }},
		{"mode", func(v *OpenShellTask) { v.Mode = "verify" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := task
			tc.edit(&v)
			require.Error(t, v.Validate())
		})
	}
	file := filepath.Join(t.TempDir(), "team.json")
	data, err := json.Marshal(OpenShellTeam{Schema: 1, ID: "review", Tasks: []OpenShellTask{task}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, data, 0o600))
	team, err := LoadOpenShellTeam(file)
	require.NoError(t, err)
	assert.Equal(t, "review", team.Tasks[0].Mode)
}

func TestOpenShellReviewExportsVerifiedUnchangedTree(t *testing.T) {
	r := newFakeOpenShell(t)
	r.RequireAll = true
	before, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	team := OpenShellTeam{Schema: 1, ID: "review", Tasks: []OpenShellTask{reviewOpenShellTask("review", `{}`)}}
	res, err := runOpenShellTeam(context.Background(), r, team, nil)
	require.NoError(t, err, res.Text)
	require.NotNil(t, res.Export)
	assert.Empty(t, res.Export.Manifest.ChangedFiles)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(nil)), res.Export.Manifest.DiffDigest)
	assert.Contains(t, res.Text, "verified unchanged snapshot")
	assert.NotContains(t, res.Text, "apply with:")
	data, err := os.ReadFile(res.Export.RunRecord)
	require.NoError(t, err)
	var run OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	assert.True(t, run.reviewOnly())
	assert.Equal(t, OpenShellUnchanged, run.Tasks[0].Outcome)
	require.NotNil(t, run.Integrated.Report)
	assert.True(t, run.Integrated.Passed)
	_, err = r.verifiedExport(context.Background(), &run)
	require.NoError(t, err)
	run.Integrated.Tree = r.Revision
	_, err = r.verifiedExport(context.Background(), &run)
	require.ErrorContains(t, err, "verified tree")
	after, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestOpenShellReviewRejectsEditsAndUnverifiedExports(t *testing.T) {
	for _, tc := range []struct{ name, mode, prompt, want string }{
		{"changed review", "review", `{"write":{"a.txt":"changed\n"}}`, "review changed"},
		{"wrong digest", "review", `{"sha":"wrong"}`, "not the one"},
		{"failed review", "review", `{"fail":true}`, "required worker"},
		{"empty edit", "edit", `{}`, "edit produced no change"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeOpenShell(t)
			r.RequireAll = true
			task := reviewOpenShellTask("worker", tc.prompt)
			task.Mode = tc.mode
			if tc.mode == "edit" {
				task.Allowed = []string{"a.txt"}
			}
			res, err := runOpenShellTeam(context.Background(), r, OpenShellTeam{Schema: 1, ID: "review", Tasks: []OpenShellTask{task}}, nil)
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, res.Export)
			assert.NotContains(t, res.Text, "apply with:")
		})
	}
}

func TestOpenShellReviewWorkflowPreservesVerifiedPredecessor(t *testing.T) {
	r := openShellLegEnv(t)
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{"CAPTAIN_MAX_ATTEMPTS", "CAPTAIN_MAX_WALLTIME", "CAPTAIN_MAX_COST", "CAPTAIN_STRICT"} {
		t.Setenv(key, "")
	}
	wf, err := ParseWorkflow(`/openshell {"write":{"a.txt":"first\n"}} > /openshell --review {"expect":{"a.txt":"first\n"}} > /openshell {"expect":{"a.txt":"first\n"},"write":{"a.txt":"final\n"}}`)
	require.NoError(t, err)
	res, err := (Workspace{Dir: r.Repo}).RunOpenShellWorkflow(context.Background(), wf, "")
	require.NoError(t, err, res.Text)
	require.NotNil(t, res.Export)
	data, err := os.ReadFile(res.Export.RunRecord)
	require.NoError(t, err)
	var run OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	require.Len(t, run.Stages, 3)
	assert.Equal(t, run.Stages[0].Tree, run.Stages[1].Tree)
	assert.Equal(t, run.Stages[1].NextRevision, run.Stages[2].Revision)
	assert.Equal(t, OpenShellUnchanged, run.Tasks[1].Outcome)
	patch, err := os.ReadFile(res.Export.Manifest.DiffPath)
	require.NoError(t, err)
	assert.Contains(t, string(patch), "+final")
	assert.NotContains(t, string(patch), "+first")
}

func TestOpenShellReviewSequenceWithNoEditsAndRejectedModification(t *testing.T) {
	for _, prompt := range []string{`{}`, `{"write":{"a.txt":"changed\n"}}`} {
		t.Run(prompt, func(t *testing.T) {
			r := newFakeOpenShell(t)
			teams := []OpenShellTeam{
				{Schema: 1, ID: "s1", Tasks: []OpenShellTask{reviewOpenShellTask("r1", prompt)}},
				{Schema: 1, ID: "s2", Tasks: []OpenShellTask{reviewOpenShellTask("r2", `{}`)}},
			}
			res, err := runOpenShellSequence(context.Background(), r, teams, nil)
			if prompt == `{}` {
				require.NoError(t, err, res.Text)
				require.NotNil(t, res.Export)
				assert.Empty(t, res.Export.Manifest.ChangedFiles)
			} else {
				require.ErrorContains(t, err, "review changed")
				assert.Nil(t, res.Export)
				assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
			}
			assert.NotContains(t, res.Text, "apply with:")
		})
	}
}

func TestOpenShellReviewMarkerIsExplicitAndScoped(t *testing.T) {
	r := openShellLegEnv(t)
	_, template, err := openShellConfig(context.Background(), r.Repo, "workflow")
	require.NoError(t, err)
	for _, prompt := range []string{"--review inspect snapshot", "--review\ninspect snapshot"} {
		wf, err := ParseOpenShellWorkflow("/openshell " + prompt)
		require.NoError(t, err)
		require.Len(t, wf.Stages, 1)
		team, err := openShellWorkflowTeam(template, wf, "[user]\nearlier instructions")
		require.NoError(t, err)
		require.Len(t, team.Tasks, 1)
		assert.Equal(t, "review", team.Tasks[0].Mode)
		assert.Empty(t, team.Tasks[0].Allowed)
		assert.Zero(t, team.Tasks[0].RepairAttempts)
		assert.Equal(t, "pass", team.Tasks[0].Baseline)
		assert.Contains(t, team.Tasks[0].Prompt, "earlier instructions")
		assert.Contains(t, team.Tasks[0].Prompt, "inspect snapshot")
		assert.NotContains(t, team.Tasks[0].Prompt, "--review")
	}
	wf, err := ParseOpenShellWorkflow("/openshell --review")
	require.NoError(t, err)
	_, err = openShellWorkflowTeam(template, wf, "[user]\nhistory is not an assignment")
	require.ErrorContains(t, err, "assignment")
	wf, err = ParseOpenShellWorkflow("/openshell document --review")
	require.NoError(t, err)
	assert.Empty(t, wf.Stages)
	assert.False(t, isOpenShellReview("--reviewer inspect"))
}
