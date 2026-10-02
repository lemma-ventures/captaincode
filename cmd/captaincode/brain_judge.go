package main

import (
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// modelEstimator returns the Phase 2 estimator, rebuilt from the routing
// history when stale. Caller holds b.mu.
func (b *brain) modelEstimator() *captaincode.ModelEstimator {
	if b.modelEstimatorFn != nil {
		return b.modelEstimatorFn()
	}
	if b.modelEst != nil && time.Since(b.modelEstAt) < estimatorTTL {
		return b.modelEst
	}
	now := time.Now()
	b.modelEst = captaincode.NewModelEstimator(captaincode.RoutingHistory(b.ledger), b.ledger.Events, now)
	b.modelEstAt = now
	return b.modelEst
}

// judgeFor decides whether a run is judged and by which leg. Only first
// attempts with a real answer are; a repair is graded by the check that
// gated it. The judge is called where the model's estimate is uncertain,
// and on one run in ten otherwise to keep measuring.
func (b *brain) judgeFor(leg captaincode.Leg, ev captaincode.Event, label string, res captaincode.Result) (captaincode.Leg, bool) {
	if label != "" || len(res.Text) < 200 || res.DurationMs < 5000 || !judgingEnabled() {
		return "", false
	}
	b.mu.Lock()
	est := b.modelEstimator().Estimate(leg, ev.Model, ev.Effort, captaincode.Domain(ev.Domain))
	now := time.Now()
	open := func(l captaincode.Leg) bool { return b.laneOpen(l, now) }
	judge, ok := captaincode.PickJudge(leg, open)
	b.mu.Unlock()
	if !ok {
		return "", false
	}
	if !est.Uncertain() && !b.shouldAssess(leg) {
		return "", false
	}
	return judge, true
}

// doJudge asks judge to grade output on the rubric. The judge is never told
// which leg or model wrote it (Manager.Assess shows only task and output).
func (b *brain) doJudge(judge captaincode.Leg, taskID, task, output, objective string, skills []captaincode.SkillRef) (captaincode.Assessment, error) {
	if b.assessFn != nil {
		return b.assessFn(task, output, objective)
	}
	mgr := captaincode.Manager{Director: judge, Port: b.mgr.Port, Skills: skills}
	mgr.CallLabel, mgr.OnCall = "judge", b.chargeAux(taskID)
	return mgr.Assess(task, output, objective)
}

// judgeObjective is the rubric with the repository's test result beside it
// when a labelling test check ran for the task.
func (b *brain) judgeObjective(taskID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	obj := captaincode.JudgeRubric
	if o := b.ledger.OutcomeFor(taskID); o != nil {
		for _, c := range o.Checks {
			if c.Source != "tests" || !c.Labels() {
				continue
			}
			if c.Passed {
				obj += " The repository's tests (`" + c.Command + "`) passed after this run."
			} else {
				obj += " The repository's tests (`" + c.Command + "`) FAILED after this run, and passed before it."
			}
		}
	}
	return obj
}

// judgingEnabled: CAPTAIN_ASSESS_MIN_SCORED=0 with CAPTAIN_ASSESS_REFRESH_EVERY=0
// turned scoring off before the judge existed, and still does.
func judgingEnabled() bool {
	return envInt("CAPTAIN_ASSESS_MIN_SCORED", 10) != 0 || envInt("CAPTAIN_ASSESS_REFRESH_EVERY", 10) != 0
}
