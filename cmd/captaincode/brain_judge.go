package main

import (
	"sync"
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

// judgeFor decides whether a run is judged and by which legs: a panel of
// two from two vendors, neither the worker's, when two are open (one
// otherwise). Only first attempts with a real answer are judged; a repair is
// graded by the check that gated it. The panel is called where the model's
// estimate is uncertain, and on one run in ten otherwise to keep measuring.
func (b *brain) judgeFor(leg captaincode.Leg, ev captaincode.Event, label string, res captaincode.Result) ([]captaincode.Leg, bool) {
	if label != "" || len(res.Text) < 200 || res.DurationMs < 5000 || !judgingEnabled() {
		return nil, false
	}
	b.mu.Lock()
	est := b.modelEstimator().Estimate(leg, ev.Model, ev.Effort, captaincode.Domain(ev.Domain))
	now := time.Now()
	open := func(l captaincode.Leg) bool { return b.laneOpen(l, now) }
	judges := captaincode.PickJudges(leg, open, judgePanelSize())
	b.mu.Unlock()
	if len(judges) == 0 {
		return nil, false
	}
	if !est.Uncertain() && !b.shouldAssess(leg) {
		return nil, false
	}
	return judges, true
}

// judgePanelSize: CAPTAIN_JUDGES, default 2 (1 halves the judging spend).
func judgePanelSize() int {
	if n := envInt("CAPTAIN_JUDGES", 2); n >= 1 {
		return n
	}
	return 2
}

// gradePanel asks every judge at once and combines their grades: the mean
// quality, the harshest verdict, a pass only when every judge passed. ok is
// false when no judge answered.
func (b *brain) gradePanel(judges []captaincode.Leg, taskID, task, output, objective string, skills []captaincode.SkillRef) (captaincode.Assessment, []captaincode.Leg, bool) {
	type graded struct {
		judge captaincode.Leg
		a     captaincode.Assessment
		err   error
	}
	res := make([]graded, len(judges))
	var wg sync.WaitGroup
	for i, j := range judges {
		wg.Add(1)
		go func(i int, j captaincode.Leg) {
			defer wg.Done()
			a, err := b.doJudge(j, taskID, task, output, objective, skills)
			res[i] = graded{j, a, err}
		}(i, j)
	}
	wg.Wait()
	var out captaincode.Assessment
	var who []captaincode.Leg
	sum := 0.0
	rank := map[string]int{"good": 0, "acceptable": 1, "poor": 2}
	for _, g := range res {
		if g.err != nil {
			continue
		}
		if len(who) == 0 {
			out = g.a
		} else {
			if rank[g.a.Verdict] > rank[out.Verdict] {
				out.Verdict = g.a.Verdict
			}
			if g.a.Notes != "" {
				out.Notes += "\n" + string(g.judge) + ": " + g.a.Notes
			}
			out.Skills = append(out.Skills, g.a.Skills...)
		}
		sum += g.a.Quality
		who = append(who, g.judge)
	}
	if len(who) == 0 {
		return captaincode.Assessment{}, nil, false
	}
	out.Quality = sum / float64(len(who))
	return out, who, true
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
