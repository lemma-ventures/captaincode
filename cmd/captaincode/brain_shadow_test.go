package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shadow decisions in the brain: the decision leg's routing answers are
// recorded beside what captain did - on the tier-1 call, beside the
// director's plan, beside a note's routing - stamped with who decided, and
// never acted on (brain_shadow.go).

// jevFake is a System One stand-in that answers every choice question in a
// request: the wanted option when it is on the menu, else the first option
// ("solo" for the shape), all at one confidence. It keeps what was asked.
type jevFake struct {
	client *captaincode.SystemOneClient
	mu     sync.Mutex
	asked  [][]string // each request's question names
}

func (f *jevFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

func (f *jevFake) questions(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[i]
}

func jevFakeServer(t *testing.T, status int, want map[string]string, conf float64) *jevFake {
	t.Helper()
	f := &jevFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Questions map[string]struct {
				Type     string            `json:"type"`
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		names := make([]string, 0, len(req.Questions))
		for n := range req.Questions {
			names = append(names, n)
		}
		sort.Strings(names)
		f.mu.Lock()
		f.asked = append(f.asked, names)
		f.mu.Unlock()
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		answers := map[string]any{}
		for _, name := range names {
			if req.Questions[name].Type == "noul" {
				// The stage-2 triage answers (irreversible, mid-tier) and
				// the supervisor's points are probabilities: low unless
				// the test wants "true".
				p := 0.2
				if want[name] == "true" {
					p = 0.9
				}
				answers[name] = map[string]any{"type": "noul", "noul": p, "confidence": conf}
				continue
			}
			crit := req.Questions[name].Criteria
			keys := make([]string, 0, len(crit))
			for k := range crit {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			choice, ok := want[name]
			if _, in := crit[choice]; !ok || !in {
				choice = keys[0]
				if name == captaincode.PointShape {
					choice = captaincode.ShapeSolo
				}
			}
			answers[name] = map[string]any{"type": "choice", "choice": choice, "probabilities": map[string]float64{choice: conf}, "confidence": conf}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 300, "output_tokens": 6}})
	}))
	t.Cleanup(srv.Close)
	f.client = &captaincode.SystemOneClient{BaseURL: srv.URL, APIKey: "k"}
	return f
}

// jevCallsLabelled counts the decision leg's call rows carrying label.
func jevCallsLabelled(b *brain, label string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.ledger.Charges {
		if c.Leg == captaincode.LegJev && c.Label == label && c.Kind == captaincode.KindCall {
			n++
		}
	}
	return n
}

func decisionAfterRoute(t *testing.T, b *brain, task string) captaincode.Decision {
	t.Helper()
	taskID := b.openTask(task)
	d, ok := b.ledger.DecisionFor(taskID)
	require.True(t, ok, "no decision reached the task identity")
	return d
}

// In the band, jev's triage answer is taken AND its shape and leg answers
// ride along in the same request, stamped with what the fast path chose.
func TestJevShadowRidesAlongWithTier1AndIsStampedOnTheDecision(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	f := jevFakeServer(t, 200, map[string]string{"class": "medium", "domain": "code", "leg": "cursor"}, 0.9)
	b.jev = f.client
	resp := routeBody(t, b, bandTask, nil)
	assert.Equal(t, "medium", resp["class"])
	require.Equal(t, 1, f.calls(), "one request: the shadow questions add no call")
	assert.Equal(t, []string{"class", "domain", "irreversible", "leg", "mid-tier", "shape"}, f.questions(0))
	assert.Equal(t, 1, jevCharges(b), "charged once, as the classify it is")

	d := decisionAfterRoute(t, b, bandTask)
	require.NotNil(t, d.Shadow, "the shadow is on the decision record")
	assert.Equal(t, "jev-1.13.0", d.Shadow.Model)
	assert.Contains(t, d.Shadow.Menu, captaincode.LegCursor, "asked over the director's menu")
	assert.Len(t, d.Shadow.Answers, 6, "class, domain, shape, leg, and the two stage-2 answers")
	class := d.Shadow.Answers[captaincode.PointClass]
	assert.Equal(t, captaincode.DecidedByJev, class.By, "jev's own answer was taken: recorded as acted on, not as a comparison")
	assert.True(t, class.Agree)
	shape := d.Shadow.Answers[captaincode.PointShape]
	assert.Equal(t, captaincode.ShapeSolo, shape.Actual)
	assert.Equal(t, d.Path, shape.By)
	assert.True(t, shape.Agree)
	leg := d.Shadow.Answers[captaincode.PointLeg]
	assert.Equal(t, string(d.Chosen), leg.Actual, "compared against the leg that actually ran")
	assert.Equal(t, d.Path, leg.By)
	assert.Equal(t, "cursor", leg.Choice)
	assert.Equal(t, leg.Choice == leg.Actual, leg.Agree)
	assert.Equal(t, captaincode.ShapeSolo, d.Shape)
}

// An unsure jev keeps the heuristic - and its answers stay on the record,
// compared against the heuristic that stood.
func TestAnUnsureJevStillLeavesItsShadow(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevFakeServer(t, 200, map[string]string{"class": "medium", "domain": "code"}, 0.3).client
	resp := routeBody(t, b, bandTask, nil)
	assert.Equal(t, "trivial", resp["class"], "the heuristic stands")
	d := decisionAfterRoute(t, b, bandTask)
	require.NotNil(t, d.Shadow)
	class := d.Shadow.Answers[captaincode.PointClass]
	assert.Equal(t, byHeuristic, class.By)
	assert.Equal(t, "trivial", class.Actual)
	assert.Equal(t, "medium", class.Choice)
	assert.False(t, class.Agree)
}

// A failed call is on the record as a failure - with nothing invented.
func TestAFailedJevCallIsOnTheRecordAsAFailure(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	b.jev = jevFakeServer(t, 500, nil, 0).client
	routeBody(t, b, bandTask, nil)
	d := decisionAfterRoute(t, b, bandTask)
	require.NotNil(t, d.Shadow)
	assert.Contains(t, d.Shadow.Err, "500")
	assert.Empty(t, d.Shadow.Answers)
	assert.Equal(t, 1, jevCharges(b), "a failed call is still charged")
}

// On the director path jev is asked beside the plan - the same questions,
// over the director's own menu - and compared with what the director chose.
func TestJevShadowRunsBesideTheDirectorPlan(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	var menu []captaincode.Leg
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		menu = open
		return captaincode.Plan{Class: captaincode.ClassMedium, Rationale: "glm fits", Workers: []captaincode.Worker{{Leg: captaincode.LegGLM, Brief: "do it"}}}, nil
	}
	f := jevFakeServer(t, 200, map[string]string{"class": "high", "domain": "code", "leg": "glm"}, 0.8)
	b.jev = f.client
	task := "design the retry policy for the queue and implement it"
	resp := routeBody(t, b, task, nil)
	assert.Equal(t, "glm", resp["modelID"])
	require.Equal(t, 1, f.calls())
	assert.Equal(t, []string{"class", "domain", "irreversible", "leg", "mid-tier", "shape"}, f.questions(0))
	assert.Equal(t, 1, jevCallsLabelled(b, "shadow"), "charged to the turn, as a shadow")
	assert.Zero(t, jevCharges(b), "no classify: triage did not run")

	d := decisionAfterRoute(t, b, task)
	assert.Equal(t, captaincode.PathDirector, d.Path)
	require.NotNil(t, d.Shadow)
	assert.Equal(t, menu, d.Shadow.Menu, "asked over exactly the menu the director was handed")
	class := d.Shadow.Answers[captaincode.PointClass]
	assert.Equal(t, captaincode.PathDirector, class.By, "the director settled the class")
	assert.Equal(t, "medium", class.Actual)
	assert.False(t, class.Agree, "jev said high")
	leg := d.Shadow.Answers[captaincode.PointLeg]
	assert.Equal(t, "glm", leg.Actual)
	assert.Equal(t, captaincode.PathDirector, leg.By)
	assert.True(t, leg.Agree)
}

// A team plan is a decision too: its shape and its legs are on the record,
// and a leg among the team's counts as agreement with jev's single pick.
func TestATeamPlanRecordsItsShapeAndTheShadowComparesByContainment(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	t.Setenv("CAPTAIN_TEAM", "1")
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "two fronts", Workers: []captaincode.Worker{
			{Leg: captaincode.LegGrokMax, Brief: "benchmark"}, {Leg: captaincode.LegGLM, Brief: "docs"}}}, nil
	}
	b.jev = jevFakeServer(t, 200, map[string]string{"class": "high", "domain": "code", "leg": "grok-max"}, 0.7).client
	task := "benchmark the GPU and document the deployment"
	resp := routeBody(t, b, task, nil)
	assert.Equal(t, "team", resp["modelID"])

	d := decisionAfterRoute(t, b, task)
	assert.Equal(t, captaincode.ShapeTeam, d.Shape)
	assert.Equal(t, []captaincode.Leg{captaincode.LegGrokMax, captaincode.LegGLM}, d.Workers)
	assert.Empty(t, d.Chosen, "no single leg was chosen")
	require.NotNil(t, d.Shadow)
	shape := d.Shadow.Answers[captaincode.PointShape]
	assert.Equal(t, captaincode.ShapeTeam, shape.Actual)
	assert.Equal(t, captaincode.ShapeSolo, shape.Choice)
	assert.False(t, shape.Agree)
	leg := d.Shadow.Answers[captaincode.PointLeg]
	assert.Equal(t, "glm+grok-max", leg.Actual)
	assert.True(t, leg.Agree, "grok-max is one of the team's legs")
	assert.False(t, leg.NotOffered, "both of the team's legs were on the menu")

	l := &captaincode.Ledger{}
	l.RecordDecision(d)
	out := captureStdout(t, func() { printDecision(l, d.TaskID) })
	assert.Contains(t, out, "director → team glm+grok-max (high/")
	assert.Contains(t, out, "shadow: jev-1.13.0")
	assert.Contains(t, out, "shape solo 0.70 ✗ (team by director)")
	assert.Contains(t, out, "leg grok-max 0.70 ✓")
}

// A leg the menu never carried - the director leg is a worker leg too, but no
// rung - is not a disagreement: jev was never allowed to name it. The row is
// kept, marked, and left out of the agreement rate the bar is read from.
func TestAPickThatWasNeverOnTheMenuIsNotCountedAgainstJev(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	t.Setenv("CAPTAIN_DIRECTOR_SELF", "0") // the premise: the judge's leg is off the menu (its own test opts the join in)
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		require.NotContains(t, open, captaincode.LegGrok, "the director leg is not a rung")
		return captaincode.Plan{Class: captaincode.ClassMedium, Rationale: "grok takes it", Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: "do it"}}}, nil
	}
	b.jev = jevFakeServer(t, 200, map[string]string{"class": "medium", "domain": "code", "leg": "glm"}, 0.9).client
	task := "design the retry policy for the queue and implement it"
	routeBody(t, b, task, nil)

	d := decisionAfterRoute(t, b, task)
	leg := d.Shadow.Answers[captaincode.PointLeg]
	assert.Equal(t, "grok", leg.Actual)
	assert.False(t, leg.Agree)
	assert.True(t, leg.NotOffered, "grok was not among the options jev was given")

	out := captureStdout(t, func() {
		l := &captaincode.Ledger{}
		l.RecordDecision(d)
		printDecision(l, d.TaskID)
	})
	assert.Contains(t, out, "leg glm 0.90 (not offered: grok by director)")

	cal := captaincode.ShadowCalibration([]captaincode.Decision{d}, nil, nil)
	var legCal captaincode.PointCalibration
	for _, p := range cal {
		if p.Point == captaincode.PointLeg {
			legCal = p
		}
	}
	assert.Equal(t, 1, legCal.NotOffered)
	assert.Zero(t, legCal.Compared, "it does not move the agreement rate")
}

// CAPTAIN_JEV_SHADOW=0: the tier-1 call asks only the triage questions and
// nothing is asked beside the director.
func TestTheShadowCanBeTurnedOff(t *testing.T) {
	t.Setenv("CAPTAIN_JEV_SHADOW", "0")
	b := teamBrain()
	noDirector(t, b)
	f := jevFakeServer(t, 200, map[string]string{"class": "medium", "domain": "code"}, 0.9)
	b.jev = f.client
	resp := routeBody(t, b, bandTask, nil)
	assert.Equal(t, "medium", resp["class"], "the triage answer is unaffected")
	require.Equal(t, 1, f.calls())
	assert.Equal(t, []string{"class", "domain", "irreversible", "mid-tier"}, f.questions(0))
	assert.Nil(t, decisionAfterRoute(t, b, bandTask).Shadow)

	t.Setenv("CAPTAIN_TRIAGE", "0")
	b = teamBrain()
	b.jev = f.client
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGLM}}}, nil
	}
	routeBody(t, b, "design the retry policy for the queue and implement it", nil)
	assert.Equal(t, 1, f.calls(), "nothing asked beside the director")
}

// A note routed by the director during a team turn leaves jev's answer to
// the same question beside it, joined to the turn's task.
func TestANoteRouteLeavesAShadowRowJoinedToTheTask(t *testing.T) {
	b := teamBrain()
	f := jevFakeServer(t, 200, map[string]string{captaincode.PointNoteRoute: "grok"}, 0.8)
	b.jev = f.client
	s := captaincode.NewSteer(t.TempDir())
	s.Describe(captaincode.LegGrok, "run the GPU benchmark")
	s.Describe(captaincode.LegGLM, "write the docs")
	s.SetTask("task_9")
	attached := []captaincode.Leg{captaincode.LegGLM, captaincode.LegGrok}

	b.routeNoteFn = func(string, map[captaincode.Leg]string) ([]captaincode.Leg, string, error) {
		return []captaincode.Leg{captaincode.LegGrok}, "the benchmark", nil
	}
	only, _, why := b.routeNote(s, "Cerebras, not Cerberus", attached)
	assert.Equal(t, []captaincode.Leg{captaincode.LegGrok}, only, "the note goes where the director says")
	assert.Contains(t, why, "director")
	require.Equal(t, 1, f.calls())
	assert.Equal(t, []string{captaincode.PointNoteRoute}, f.questions(0))
	b.mu.Lock()
	require.Len(t, b.ledger.Shadows, 1)
	rec := b.ledger.Shadows[0]
	b.mu.Unlock()
	assert.Equal(t, captaincode.PointNoteRoute, rec.Point)
	assert.Equal(t, "task_9", rec.TaskID, "joined to the turn's task, so an outcome can label it")
	assert.Equal(t, "Cerebras, not Cerberus", rec.Task)
	assert.Equal(t, []captaincode.Leg{captaincode.LegGLM, captaincode.LegGrok}, rec.Menu)
	a := rec.Answers[captaincode.PointNoteRoute]
	assert.Equal(t, "grok", a.Choice)
	assert.Equal(t, "grok", a.Actual)
	assert.Equal(t, captaincode.PathDirector, a.By)
	assert.True(t, a.Agree)
	assert.Equal(t, 1, jevCallsLabelled(b, captaincode.PointNoteRoute), "charged under its own row")

	// The director sends it to everyone: the actual is "all".
	b.routeNoteFn = func(string, map[captaincode.Leg]string) ([]captaincode.Leg, string, error) {
		return nil, "whole task", nil
	}
	only, _, _ = b.routeNote(s, "use the new SKU name everywhere", attached)
	assert.Nil(t, only)
	b.mu.Lock()
	require.Len(t, b.ledger.Shadows, 2)
	a = b.ledger.Shadows[1].Answers[captaincode.PointNoteRoute]
	b.mu.Unlock()
	assert.Equal(t, captaincode.NoteToAll, a.Actual)
	assert.False(t, a.Agree, "jev named one worker")

	// One worker, or a user-addressed note: no director, no shadow.
	only, _, _ = b.routeNote(s, "@glm mention the SKU", attached)
	assert.Equal(t, []captaincode.Leg{captaincode.LegGLM}, only)
	assert.Equal(t, 2, f.calls(), "a note the user addressed asks nobody")
}

// A shadow answer that misses the grace is recorded as unanswered and still
// charged when it lands: no jev call goes unbilled.
func TestALateShadowIsRecordedAsSuchAndChargedWhenItLands(t *testing.T) {
	prev := shadowGrace
	shadowGrace = 10 * time.Millisecond
	t.Cleanup(func() { shadowGrace = prev })
	b := teamBrain()
	ch := make(chan shadowReply, 1)
	r := b.awaitShadow(ch, "shadow")
	require.ErrorIs(t, r.err, errShadowLate)
	require.NotNil(t, r.sh)
	assert.Contains(t, r.sh.Err, "grace")
	assert.Empty(t, r.sh.Answers)
	ch <- shadowReply{res: captaincode.Result{Tokens: 300}}
	require.Eventually(t, func() bool { return jevCallsLabelled(b, "shadow") == 1 }, 2*time.Second, 5*time.Millisecond, "the late answer was never charged")
}

// `captain why` shows the shadow on the line under the policy.
func TestWhyPrintsTheShadow(t *testing.T) {
	l := &captaincode.Ledger{}
	l.RecordDecision(captaincode.Decision{
		TaskID: "task_1", Task: "tidy the README", Class: captaincode.ClassTrivial, Domain: captaincode.DomainEditorial,
		Path: captaincode.PathValue, Chosen: captaincode.LegGLM, Shape: captaincode.ShapeSolo,
		Policy: captaincode.PolicyFor(captaincode.ClassTrivial, 16000, 0),
		Shadow: &captaincode.Shadow{Leg: captaincode.LegJev, Model: "jev-1.13.0", Ms: 240, Answers: map[string]captaincode.ShadowAnswer{
			captaincode.PointClass: {Choice: "trivial", Confidence: 0.93, Actual: "trivial", By: byHeuristic, Agree: true},
			captaincode.PointLeg:   {Choice: "free", Confidence: 0.55, Actual: "glm", By: captaincode.PathValue},
		}},
	})
	out := captureStdout(t, func() { printDecision(l, "task_1") })
	assert.Contains(t, out, "value → glm (trivial/editorial)")
	assert.Contains(t, out, "shadow: jev-1.13.0 240ms · class trivial 0.93 ✓ · leg free 0.55 ✗ (glm by value)")
}
