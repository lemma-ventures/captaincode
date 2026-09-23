package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The routing stages in the brain (2026-09-22): the typed director pick,
// the judge's leg on a high-class menu, the tier-0 medium margin keeping
// the free-leg classify out, the attribution every event and decision now
// carries, the expected-cost policy and its data gate, the solo verify →
// repair → effort → leg sequence, and the follow-up signals that settle an
// outcome.

func TestDirectorPickIsATypedChoiceOverTheValueMenu(t *testing.T) {
	t.Setenv("CAPTAIN_DIRECTOR_PICK", "1")
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	noDirector(t, b) // the PLAN call must not run
	var menuSeen []captaincode.Scored
	b.pickFn = func(task string, class captaincode.Class, prefer string, menu []captaincode.Scored, self captaincode.Leg) (captaincode.WorkerPick, error) {
		menuSeen = menu
		assert.Equal(t, captaincode.LegClaude, self)
		return captaincode.WorkerPick{Leg: captaincode.LegGLM, Class: captaincode.ClassHigh}, nil
	}
	resp := routeBody(t, b, "audit the security of the auth proxy across the codebase", nil)
	assert.Equal(t, "glm", resp["modelID"])
	assert.Equal(t, "high", resp["class"], "the director's own reading of the class stands")
	assert.Contains(t, resp["rationale"], "director pick: glm")
	require.NotEmpty(t, menuSeen)
	assert.LessOrEqual(t, len(menuSeen), pickMenuMax, "the menu is the top rows, not the whole roster")
	d := decisionAfterRoute(t, b, "audit the security of the auth proxy across the codebase")
	assert.Equal(t, captaincode.PathPick, d.Path)
	assert.Equal(t, captaincode.TriageByDirector, d.TriageBy)
	assert.NotEmpty(t, d.Effort, "the effort rides on the decision")

	b.pickFn = func(string, captaincode.Class, string, []captaincode.Scored, captaincode.Leg) (captaincode.WorkerPick, error) {
		return captaincode.WorkerPick{}, errors.New("timed out")
	}
	resp = routeBody(t, b, "audit the security of the auth proxy across the codebase", nil)
	assert.Contains(t, resp["rationale"], "director pick failed", "a slow judge does not block the turn: the menu's first row runs")
	assert.NotEqual(t, "", resp["modelID"])
}

func TestTheJudgesOwnLegJoinsAHighClassMenuOnly(t *testing.T) {
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	for name, task := range map[string]string{
		"high":   "audit the security of the auth proxy across the codebase",
		"medium": "polish this sentence",
	} {
		t.Run(name, func(t *testing.T) {
			b := teamBrain()
			var open []captaincode.Leg
			b.planFn = func(task string, class captaincode.Class, prefer string, menu []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
				open = menu
				return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGLM, Brief: "x"}}}, nil
			}
			extra := map[string]any{}
			if name == "medium" {
				extra["prefer"] = "speed" // a preference keeps the director path for a medium task
			}
			routeBody(t, b, task, extra)
			if name == "high" {
				assert.Contains(t, open, captaincode.LegClaude, "the hardest work may reach the judge's own leg")
			} else {
				assert.NotContains(t, open, captaincode.LegClaude, "trivial and medium menus keep the bazooka gate")
			}
		})
	}
	t.Run("off", func(t *testing.T) {
		t.Setenv("CAPTAIN_DIRECTOR_SELF", "0")
		b := teamBrain()
		var open []captaincode.Leg
		b.planFn = func(task string, class captaincode.Class, prefer string, menu []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			open = menu
			return captaincode.Plan{Class: captaincode.ClassHigh, Workers: []captaincode.Worker{{Leg: captaincode.LegGLM, Brief: "x"}}}, nil
		}
		routeBody(t, b, "audit the security of the auth proxy across the codebase", nil)
		assert.NotContains(t, open, captaincode.LegClaude)
	})
}

func TestAnAnchoredMediumTaskSkipsTheFreeLegClassify(t *testing.T) {
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	noDirector(t, b)
	b.classifyLLMFn = func(task string) (captaincode.Class, captaincode.Domain, error) {
		t.Fatalf("the free-leg classify ran for %q - a medium task with an anchor is confident enough", task)
		return "", "", nil
	}
	resp := routeBody(t, b, "implement the retry loop for the webhook client and make it back off when the server answers slowly", nil)
	assert.Equal(t, "medium", resp["class"])
	d := decisionAfterRoute(t, b, "implement the retry loop for the webhook client and make it back off when the server answers slowly")
	assert.Equal(t, captaincode.TriageByHeuristic, d.TriageBy)
	assert.GreaterOrEqual(t, d.Confidence, 0.6)
	assert.Equal(t, 1, d.Attempt)
}

func TestEveryEventCarriesTheRoutingClassEffortAndModel(t *testing.T) {
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 8, Verdict: "good"}, nil
	}
	noDirector(t, b)
	task := "tighten the wording of this paragraph and keep the argument intact"
	resp, _ := routeLeg(t, b, task)
	require.NotEmpty(t, resp.Effort)
	leg := captaincode.Leg(resp.Leg)

	out := make([]byte, 600)
	for i := range out {
		out[i] = 'x'
	}
	b.recordRunAt(leg, "[user]\n"+task+"\n\n", captaincode.Result{Text: string(out), Tokens: 900, DurationMs: 20_000},
		captaincode.Workspace{Effort: captaincode.Effort(resp.Effort)}, "", 1, "", "")
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Len(t, b.ledger.Events, 1)
	ev := b.ledger.Events[0]
	assert.Equal(t, captaincode.Class(resp.Class), ev.Class, "the class that ROUTED, not the keyword classifier's")
	assert.NotEqual(t, captaincode.Classify(task), ev.Class, "…and the two differ on this task, which is the bug this closes")
	assert.Equal(t, captaincode.Effort(resp.Effort), ev.Effort)
	assert.Equal(t, captaincode.ModelIDAt(leg, ev.Effort), ev.Model)
	assert.Equal(t, captaincode.TriageByHeuristic, ev.ClassBy)
	assert.Greater(t, ev.Confidence, 0.0)
	assert.Equal(t, 1, ev.Attempt)
	assert.NotEmpty(t, ev.Path)
	o := b.ledger.OutcomeFor(ev.TaskID)
	require.NotNil(t, o)
	assert.Equal(t, 600, o.DeliveredChars, "what was delivered is on the outcome")
	assert.Equal(t, ev.Effort, o.Effort)
	assert.False(t, o.DeliveredAt.IsZero())
}

func labeledHistory(n int, leg captaincode.Leg, ok bool) []captaincode.RoutingSample {
	var hist []captaincode.RoutingSample
	for i := 0; i < n; i++ {
		id := string(rune('a'+i%26)) + string(rune('a'+i/26))
		st := captaincode.AcceptanceRejected
		if ok {
			st = captaincode.AcceptanceAccepted
		}
		hist = append(hist, captaincode.RoutingSample{
			Decision: captaincode.Decision{TaskID: id, Task: "tighten the wording of this paragraph", Class: captaincode.ClassMedium, Domain: captaincode.DomainEditorial, Chosen: leg, Effort: captaincode.EffortMedium, At: time.Now()},
			Events:   []captaincode.Event{{TaskID: id, Leg: leg, Outcome: "ok", Effort: captaincode.EffortMedium, Model: captaincode.ModelIDAt(leg, captaincode.EffortMedium)}},
			Outcome:  &captaincode.OutcomeEvidence{TaskID: id, Status: st, DecidedBy: captaincode.DecidedByChecks},
		})
	}
	return hist
}

func TestExpectedCostPolicyIsGatedOnLabelledOutcomes(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "")
	t.Setenv("CAPTAIN_EXPLORE", "0")
	t.Setenv("CAPTAIN_VALUE_TAU", "5,7")
	t.Setenv("CAPTAIN_ROUTING_POLICY", "expected")
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	task := "tighten the wording of this paragraph and keep the argument intact"

	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGemini: true}
	b.estimatorFn = func() *captaincode.SuccessEstimator { return captaincode.NewSuccessEstimator(nil) }
	resp, why := routeLeg(t, b, task)
	assert.Equal(t, "gemini", resp.Leg, "under the gate the value order runs")
	assert.Contains(t, why, "expected-cost gate")
	d := decisionAfterRoute(t, b, task)
	assert.Equal(t, captaincode.PathValue, d.Path)
	assert.NotEmpty(t, d.Expected, "…but the arms are on the record")

	// Sixty labelled outcomes: gemini failed every one, glm passed every
	// one. The expected cost of a gemini attempt now carries its repair.
	b = teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGemini: true}
	hist := append(labeledHistory(30, captaincode.LegGemini, false), labeledHistory(30, captaincode.LegGLM, true)...)
	b.estimatorFn = func() *captaincode.SuccessEstimator { return captaincode.NewSuccessEstimator(hist) }
	resp, why = routeLeg(t, b, task)
	assert.Equal(t, "glm", resp.Leg, "the leg that succeeds is cheaper per successful task: %s", why)
	assert.Contains(t, why, "expected $")
	assert.NotEmpty(t, resp.Effort)
	d = decisionAfterRoute(t, b, task)
	assert.Equal(t, captaincode.PathExpected, d.Path)
	assert.Equal(t, captaincode.PathExpected, d.Policy.Name)
	assert.Equal(t, captaincode.Effort(resp.Effort), d.Effort)
}

// gitRepoWithChange builds a repository whose working tree has an edit, so
// the solo check sees changed files.
func gitRepoWithChange(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	run("add", "a.go")
	run("commit", "-q", "-m", "init")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // edited by the worker\n"), 0o644))
	return dir
}

func TestSoloVerifyRepairsThenClimbsEffortThenEscalatesTheLeg(t *testing.T) {
	t.Setenv("CAPTAIN_SOLO_VERIFY", "1")
	dir := gitRepoWithChange(t)
	b := teamBrain()
	b.escalation = captaincode.EscalationPolicy{Version: 1, MaxRepairs: 1, MaxEscalations: 1, MaxEffortEscalations: 1}
	calls := 0
	b.captureTestFn = func(ctx context.Context, d string) (*captaincode.CheckEvidence, error) {
		calls++
		return &captaincode.CheckEvidence{Command: []string{"sh", "-c", "go test ./..."}, ExitCode: 1, Passed: calls >= 4, Output: "FAIL"}, nil
	}
	var runs []string
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs = append(runs, string(leg))
		return leg, captaincode.Result{Text: "fixed it, honest", Tokens: 10, DurationMs: 100}, nil
	}
	ws := captaincode.Workspace{Dir: dir, Effort: captaincode.EffortMedium}
	taskID := b.openTask("fix the parser in a.go")
	leg, ws2, res, rec := b.verifyAndEscalate(ws, captaincode.LegGLM, "[user]\nfix the parser in a.go\n\n", captaincode.Result{Text: "done"}, taskID, nil, nil)
	require.NotNil(t, rec)
	assert.Equal(t, 4, calls, "the first check, the repair's, the effort step's, the escalation's")
	assert.Equal(t, 1, rec.RepairsUsed)
	assert.Equal(t, 1, rec.EffortEscalations)
	assert.Equal(t, captaincode.EffortHigh, rec.EscalatedEffort, "the same model one rung up, before any other leg")
	assert.True(t, rec.Escalated)
	assert.True(t, rec.ObjectiveMet)
	assert.Equal(t, 4, rec.Attempts)
	assert.NotEqual(t, captaincode.LegGLM, leg, "the leg escalation ran on a stronger leg")
	assert.Equal(t, rec.EscalatedTo, leg)
	assert.Equal(t, captaincode.LegGLM, rec.From)
	assert.Equal(t, []string{"glm", "glm", string(leg)}, runs, "repair on glm, effort step on glm, then the escalation")
	assert.NotEmpty(t, ws2.Effort)
	assert.Contains(t, res.Text, "fixed it")
	b.mu.Lock()
	o := b.ledger.OutcomeFor(taskID)
	b.mu.Unlock()
	require.NotNil(t, o)
	require.NotNil(t, o.Escalation)
	assert.Equal(t, 4, len(o.Checks), "every check is on the outcome")
	assert.True(t, o.Checks[3].Passed)
	assert.Equal(t, "tests", o.Checks[0].Source)
}

func TestSoloVerifyStopsWhenTheSupervisorSaysItNeedsTheUser(t *testing.T) {
	t.Setenv("CAPTAIN_SOLO_VERIFY", "1")
	dir := gitRepoWithChange(t)
	b := teamBrain()
	b.captureTestFn = func(ctx context.Context, d string) (*captaincode.CheckEvidence, error) {
		return &captaincode.CheckEvidence{Command: []string{"go", "test"}, ExitCode: 1, Passed: false}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Fatal("no retry when the work needs the user")
		return leg, captaincode.Result{}, nil
	}
	task := "fix the parser in a.go"
	b.lastSupervise = map[string]map[string]float64{superviseKey(captaincode.LegGLM, task): {captaincode.PointNeedsHuman: 0.9}}
	taskID := b.openTask(task)
	_, _, res, rec := b.verifyAndEscalate(captaincode.Workspace{Dir: dir}, captaincode.LegGLM, "[user]\n"+task+"\n\n", captaincode.Result{Text: "done"}, taskID, nil, nil)
	require.NotNil(t, rec)
	assert.False(t, rec.ObjectiveMet)
	assert.Contains(t, res.Text, "needs you")
}

func TestSoloVerifyIsSilentWhenNothingChanged(t *testing.T) {
	t.Setenv("CAPTAIN_SOLO_VERIFY", "1")
	b := teamBrain()
	b.captureTestFn = func(ctx context.Context, d string) (*captaincode.CheckEvidence, error) {
		t.Fatal("no tests when the worker changed nothing")
		return nil, nil
	}
	taskID := b.openTask("explain the parser")
	leg, _, _, rec := b.verifyAndEscalate(captaincode.Workspace{Dir: t.TempDir()}, captaincode.LegGLM, "[user]\nexplain the parser\n\n", captaincode.Result{Text: "it parses"}, taskID, nil, nil)
	assert.Nil(t, rec)
	assert.Equal(t, captaincode.LegGLM, leg)
}

func TestACorrectiveFollowUpSettlesTheLastDeliveryAsRejected(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	task := "tighten the wording of this paragraph"
	b.recordRunAt(captaincode.LegGLM, "[user]\n"+task+"\n\n", captaincode.Result{Text: "tightened", Tokens: 10, DurationMs: 100}, captaincode.Workspace{Dir: dir}, "", 1, "", "")
	b.mu.Lock()
	taskID := b.ledger.Events[0].TaskID
	b.mu.Unlock()
	b.noteFollowUp(dir, "now translate it to French")
	b.mu.Lock()
	assert.Equal(t, captaincode.AcceptancePending, b.ledger.OutcomeFor(taskID).Status, "the next task is not a verdict")
	b.mu.Unlock()
	b.noteFollowUp(dir, "no, that's not what I asked - keep my wording")
	b.mu.Lock()
	defer b.mu.Unlock()
	o := b.ledger.OutcomeFor(taskID)
	assert.Equal(t, captaincode.AcceptanceRejected, o.Status)
	assert.Equal(t, captaincode.DecidedByReprompt, o.DecidedBy)
	require.NotNil(t, o.Reprompt)
}

func TestAFollowUpCommitSettlesTheDeliveryAsAccepted(t *testing.T) {
	prev := captaincode.CommitsTouching
	t.Cleanup(func() { captaincode.CommitsTouching = prev })
	captaincode.CommitsTouching = func(ctx context.Context, dir string, since time.Time, files []string) (captaincode.CommitRecord, bool) {
		return captaincode.CommitRecord{SHA: "abc123", Subject: "keep it", Files: files}, true
	}
	dir := gitRepoWithChange(t)
	b := teamBrain()
	task := "fix the parser in a.go"
	b.recordRunAt(captaincode.LegGLM, "[user]\n"+task+"\n\n", captaincode.Result{Text: "fixed", Tokens: 10, DurationMs: 100}, captaincode.Workspace{Dir: dir}, "", 1, "", "")
	b.mu.Lock()
	first := b.ledger.OutcomeFor(b.ledger.Events[0].TaskID)
	b.mu.Unlock()
	require.NotNil(t, first)
	assert.NotEmpty(t, first.ChangedFiles, "the artifact capture named the files")
	// The sweep runs on the next recorded turn: a second run in the same repo.
	b.recordRunAt(captaincode.LegGLM, "[user]\nanother task\n\n", captaincode.Result{Text: "ok", Tokens: 10, DurationMs: 100}, captaincode.Workspace{Dir: dir}, "", 1, "", "")
	b.mu.Lock()
	defer b.mu.Unlock()
	o := b.ledger.OutcomeFor(first.TaskID)
	require.NotNil(t, o.Commit)
	assert.Equal(t, captaincode.AcceptanceAccepted, o.Status)
	assert.Equal(t, captaincode.DecidedByCommit, o.DecidedBy)
}

func TestSoloVerifyStopsWhenARetryHasNoVerdict(t *testing.T) {
	for _, stage := range []string{"repair", "effort", "leg"} {
		for _, result := range []string{"missing", "error", "timeout"} {
			t.Run(stage+"/"+result, func(t *testing.T) {
				t.Setenv("CAPTAIN_SOLO_VERIFY", "1")
				b := teamBrain()
				b.escalation = captaincode.EscalationPolicy{Version: 1, MaxRepairs: 1, MaxEffortEscalations: 1, MaxEscalations: 1}
				stopAt := map[string]int{"repair": 2, "effort": 3, "leg": 4}[stage]
				checks, runs := 0, 0
				b.captureTestFn = func(context.Context, string) (*captaincode.CheckEvidence, error) {
					checks++
					if checks >= stopAt {
						switch result {
						case "missing":
							return nil, nil
						case "error":
							return nil, errors.New("test runner unavailable")
						case "timeout":
							return &captaincode.CheckEvidence{Command: []string{"go", "test"}, TimedOut: true}, nil
						}
					}
					return &captaincode.CheckEvidence{Command: []string{"go", "test"}, ExitCode: 1, Output: "FAIL"}, nil
				}
				var lastLeg captaincode.Leg
				b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
					runs++
					lastLeg = leg
					assert.NotContains(t, brief, "tests `` FAILED")
					return leg, captaincode.Result{Text: "latest repair", Tokens: 10, DurationMs: 100}, nil
				}
				taskID := b.openTask("fix the parser in a.go")
				ws := captaincode.Workspace{Dir: gitRepoWithChange(t), Effort: captaincode.EffortMedium}
				leg, finalWS, res, rec := b.verifyAndEscalate(ws, captaincode.LegGLM, "fix the parser in a.go", captaincode.Result{Text: "done"}, taskID, nil, nil)
				require.NotNil(t, rec)
				assert.Equal(t, stopAt, checks)
				assert.Equal(t, stopAt-1, runs)
				assert.Equal(t, stopAt, rec.Attempts)
				assert.Equal(t, lastLeg, leg)
				assert.Equal(t, leg, rec.FinalLeg)
				assert.Equal(t, finalWS.Effort, rec.FinalEffort)
				assert.False(t, rec.ObjectiveMet)
				assert.Contains(t, res.Text, "latest repair")
				assert.Contains(t, res.Text, "verification inconclusive")
				assert.NotContains(t, res.Text, "still fails")
				b.mu.Lock()
				defer b.mu.Unlock()
				o := b.ledger.OutcomeFor(taskID)
				require.NotNil(t, o)
				assert.Len(t, o.Checks, stopAt-1)
				require.NotNil(t, o.Escalation)
				assert.False(t, o.Escalation.ObjectiveMet)
			})
		}
	}
}
