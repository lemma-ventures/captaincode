package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenShellFailureNeverReroutesToHost(t *testing.T) {
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegOpenShell: true, captaincode.LegCursor: true}
	var legs []captaincode.Leg
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		legs = append(legs, leg)
		if leg == captaincode.LegOpenShell {
			return leg, captaincode.Result{}, captaincode.NewRateLimitError(leg, "test provider limit", time.Time{})
		}
		return leg, captaincode.Result{Text: "ran on host"}, nil
	}
	leg, _, err := b.runWorkerRerouted(defaultWorkspace(), captaincode.LegOpenShell, "fix the parser", nil, nil, "")
	require.ErrorIs(t, err, errOpenShellEntry)
	assert.Equal(t, captaincode.LegOpenShell, leg)
	assert.Empty(t, legs, "the generic worker path refuses OpenShell before any worker runs")
	_, ok := b.rerouteTarget(captaincode.LegOpenShell, "fix the parser")
	assert.False(t, ok)
}

func TestOpenShellSkipsHostVerificationAndEscalation(t *testing.T) {
	t.Setenv("CAPTAIN_SOLO_VERIFY", "1")
	b := teamBrain()
	var checks, workers int
	b.captureTestFn = func(ctx context.Context, dir string) (*captaincode.CheckEvidence, error) {
		checks++
		return &captaincode.CheckEvidence{Command: []string{"host-test"}, Passed: true}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		workers++
		return leg, captaincode.Result{Text: "unexpected repair"}, nil
	}
	ws := captaincode.Workspace{Dir: gitRepoWithChange(t)}
	result := captaincode.Result{Text: "OpenShell pass: exported verified patch"}
	leg, finalWS, res, outcome := b.verifyAndEscalate(ws, captaincode.LegOpenShell, "fix the parser", result, "sandbox-task", nil, nil)
	assert.Equal(t, captaincode.LegOpenShell, leg)
	assert.Equal(t, ws, finalWS)
	assert.Equal(t, result, res)
	assert.Nil(t, outcome)
	assert.Zero(t, checks)
	assert.Zero(t, workers)
}

func TestOpenShellDoesNotClaimHostWorkspaceEdits(t *testing.T) {
	b := teamBrain()
	dir := gitRepoWithChange(t)
	b.recordRunAt(captaincode.LegOpenShell, "fix the parser", captaincode.Result{Text: "exported patch"}, captaincode.Workspace{Dir: dir}, "", 1, "", "")
	require.Len(t, b.ledger.Events, 1)
	ev := b.ledger.Events[0]
	attempt := b.ledger.AttemptStateFor(ev.AttemptID)
	require.NotNil(t, attempt)
	assert.Empty(t, attempt.ChangedFiles)
	assert.Empty(t, attempt.DiffPath)
	assert.Empty(t, b.ledger.OutcomeFor(ev.TaskID).ChangedFiles)
}

func TestOpenShellArtifactsAndHandoffSurviveRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := gitRepoWithChange(t)
	before, err := os.ReadFile(filepath.Join(dir, "a.go"))
	require.NoError(t, err)
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	require.NoError(t, err)
	b := teamBrain()
	b.ledger, err = captaincode.LoadLedger()
	require.NoError(t, err)
	export := &captaincode.VerifiedExport{
		Repository: dir, Runtime: "vm", RunRecord: filepath.Join(t.TempDir(), "run.json"),
		Manifest: captaincode.PatchManifest{
			Version: captaincode.ArtifactVersion, TaskID: "controller-task", StageID: "controller-stage", AttemptID: "controller-attempt",
			Leg: "openshell-nim", BaseRevision: strings.Repeat("a", 40),
			ChangedFiles: []string{"parser.py"}, DiffDigest: strings.Repeat("b", 64), DiffPath: filepath.Join(t.TempDir(), "integrated.patch"),
			Check: &captaincode.CheckEvidence{Command: []string{"python3", "-m", "unittest"}, Passed: true},
		},
	}
	b.recordRunAt(captaincode.LegOpenShell, "fix the parser", captaincode.Result{Text: "verified export", Export: export}, captaincode.Workspace{Dir: dir}, "", 1, "", "")
	require.Len(t, b.ledger.Events, 1)
	ev := b.ledger.Events[0]
	other := b.openTask("another task")
	require.NoError(t, b.ledger.Save())
	export.Manifest.ChangedFiles[0] = "mutated"
	export.Manifest.Check.Command[0] = "mutated"
	assert.Equal(t, "controller-task", export.Manifest.TaskID)
	restored := teamBrain()
	restored.ledger, err = captaincode.LoadLedger()
	require.NoError(t, err)
	for _, taskID := range []string{ev.TaskID, other} {
		rec := httptest.NewRecorder()
		restored.taskArtifacts(rec, nil, captaincode.TaskRequest{TaskID: taskID})
		require.Equal(t, 200, rec.Code)
		var envelope captaincode.TaskResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		var arts captaincode.ArtifactsResponse
		require.NoError(t, json.Unmarshal(envelope.Body, &arts))
		assert.Nil(t, arts.Integration)
		assert.Empty(t, arts.Manifests)
		if taskID != ev.TaskID {
			assert.Empty(t, arts.Exports)
			continue
		}
		require.Len(t, arts.Exports, 1)
		m := arts.Exports[0].Manifest
		assert.Equal(t, ev.TaskID, m.TaskID)
		assert.Equal(t, ev.AttemptID, m.AttemptID)
		assert.Empty(t, m.StageID)
		assert.Equal(t, string(captaincode.LegOpenShell), m.Leg)
		assert.Equal(t, []string{"parser.py"}, m.ChangedFiles)
		assert.Equal(t, []string{"python3", "-m", "unittest"}, m.Check.Command)
		assert.True(t, m.Check.Passed)
		assert.Equal(t, export.RunRecord, arts.Exports[0].RunRecord)
		assert.Equal(t, dir, arts.Exports[0].Repository)
	}
	stored := restored.ledger.HandoffFor(ev.TaskID)
	require.NotNil(t, stored)
	rebuilt := captaincode.BuildHandoffBrief(restored.ledger, ev.TaskID, "fix the parser", restored.integrationFor(ev.TaskID))
	for _, brief := range []captaincode.HandoffBrief{*stored, rebuilt} {
		assert.Nil(t, brief.Integration)
		require.Len(t, brief.Artifacts, 1)
		a := brief.Artifacts[0]
		assert.Equal(t, "exported", a.Disposition)
		assert.True(t, a.CheckPassed)
		assert.Equal(t, "vm", a.Runtime)
		assert.Equal(t, export.Manifest.BaseRevision, a.BaseRevision)
		assert.Equal(t, []string{"parser.py"}, a.ChangedFiles)
		assert.Contains(t, captaincode.FormatHandoffBrief(brief), "exported (not applied)")
		assert.Contains(t, captaincode.FormatHandoffBrief(brief), "verification: python3 -m unittest")
		assert.Contains(t, strings.Join(brief.RemainingActions, "\n"), "before applying")
	}
	attempt := restored.ledger.AttemptStateFor(ev.AttemptID)
	assert.Empty(t, attempt.ChangedFiles)
	assert.Empty(t, attempt.DiffPath)
	assert.Empty(t, restored.ledger.OutcomeFor(ev.TaskID).ChangedFiles)
	after, err := os.ReadFile(filepath.Join(dir, "a.go"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	indexAfter, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, index, indexAfter)
}

func TestOpenShellRejectsUnsupportedCompositionBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"team", "workflow"} {
		t.Run(mode, func(t *testing.T) {
			b := teamBrain()
			b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
				t.Fatal("unsupported composition dispatched a worker")
				return leg, captaincode.Result{}, nil
			}
			rec := httptest.NewRecorder()
			req := oaiChatReq{ws: captaincode.Workspace{Dir: t.TempDir()}}
			if mode == "team" {
				b.storeTeamPlan("fix it", captaincode.Plan{Workers: []captaincode.Worker{{Leg: captaincode.LegGLM}, {Leg: captaincode.LegOpenShell}}})
				b.teamChat(rec, req, "fix it")
				assert.Contains(t, rec.Body.String(), "captain openshell --team")
			} else {
				wf := captaincode.Workflow{Stages: []captaincode.WorkflowStage{
					{Legs: []captaincode.WorkflowLeg{{Leg: captaincode.LegGLM}}},
					{Legs: []captaincode.WorkflowLeg{{Leg: captaincode.LegOpenShell, Gate: "host-command"}}},
				}}
				b.runWorkflow(rec, req, "fix it", wf, "")
				assert.Equal(t, 400, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "cannot share a workflow")
			}
			assert.Empty(t, b.ledger.Events)
		})
	}
}

func TestOpenShellNeverRunsInADirectorPlannedTeam(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"single worker plan", "fix it"},
		{"named in the team", "/team /openshell fix it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := teamBrain()
			b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
				t.Fatal("a director-planned team dispatched an OpenShell worker")
				return leg, captaincode.Result{}, nil
			}
			planned := false
			b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
				planned = true
				return captaincode.Plan{Workers: []captaincode.Worker{{Leg: captaincode.LegOpenShell, Brief: task}}}, nil
			}
			if tc.name == "single worker plan" {
				b.storeTeamPlan("fix it", captaincode.Plan{Workers: []captaincode.Worker{{Leg: captaincode.LegOpenShell, Brief: "fix it"}}})
			}
			rec := httptest.NewRecorder()
			req := oaiChatReq{ws: captaincode.Workspace{Dir: t.TempDir()}, Messages: []oaiMessage{{Role: "user", Content: jsonString(tc.text)}}}
			b.teamChat(rec, req, "[user]\n"+tc.text+"\n\n")
			assert.Contains(t, rec.Body.String(), "captain openshell --team")
			assert.Empty(t, b.ledger.Events)
			if tc.name == "named in the team" {
				assert.False(t, planned, "a named OpenShell worker is refused before the director is asked")
			}
		})
	}
}

func TestOpenShellPartialReportCannotBecomeSuccessful(t *testing.T) {
	b := teamBrain()
	for _, err := range []error{captaincode.ErrInterrupted, captaincode.ErrWorkerTimeout, captaincode.ErrRateLimited} {
		res := captaincode.Result{Text: strings.Repeat("partial sandbox report\n", 100)}
		_, got, note := b.salvagePartial(captaincode.LegOpenShell, res, err)
		assert.ErrorIs(t, got, err)
		assert.Empty(t, note)
	}
}
