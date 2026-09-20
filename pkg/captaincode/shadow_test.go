package captaincode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shadow decisions: the routing questions are translated into jev's typed
// shape, asked beside captain's own decision, recorded with what captain did
// and who did it, and read back as a calibration per decision point.

// s1Answering answers every choice question in the request with the wanted
// option (or the first criteria key when none is wanted, "solo" for the
// shape), all at one confidence, and records what was asked.
func s1Answering(t *testing.T, want map[string]string, conf float64, got *map[string]any) *SystemOneClient {
	t.Helper()
	return s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		*got = req
		answers := map[string]any{}
		for name, raw := range req["questions"].(map[string]any) {
			crit, _ := raw.(map[string]any)["criteria"].(map[string]any)
			keys := make([]string, 0, len(crit))
			for k := range crit {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			choice, ok := want[name]
			if _, in := crit[choice]; !ok || !in {
				choice = keys[0]
				if name == PointShape {
					choice = ShapeSolo
				}
			}
			answers[name] = map[string]any{"type": "choice", "choice": choice, "probabilities": map[string]float64{choice: conf}, "confidence": conf}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 400, "output_tokens": 8}})
	})
}

func questionNames(got map[string]any) []string {
	var names []string
	for k := range got["questions"].(map[string]any) {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func criteriaOf(got map[string]any, q string) map[string]any {
	crit, _ := got["questions"].(map[string]any)[q].(map[string]any)["criteria"].(map[string]any)
	return crit
}

func TestTriageWithJevAsksShapeAndLegBesideClassAndDomain(t *testing.T) {
	var got map[string]any
	c := s1Answering(t, map[string]string{PointClass: "medium", PointDomain: "code", PointLeg: "cursor"}, 0.8, &got)
	tr, sh, res, err := TriageWithJev(context.Background(), c, "refactor the queue worker and its tests", JevTriageOptions{
		Shadow: true, Menu: []Leg{LegCursor, LegCodex, LegJev, "nope", LegGrok, LegCursor}, Domain: DomainCode})
	require.NoError(t, err)
	assert.Equal(t, []string{PointClass, PointDomain, PointLeg, PointShape}, questionNames(got), "one request carries all four questions")
	assert.Equal(t, ClassMedium, tr.Class)
	assert.Equal(t, DomainCode, tr.Domain)
	assert.InDelta(t, 0.8, tr.Confidence, 1e-9, "the gate reads the weaker of class and domain - never a shadow answer")

	crit := criteriaOf(got, PointLeg)
	assert.Len(t, crit, 3, "the menu translated: each worker once, the decision leg and an unknown leg left out")
	for _, l := range []string{"cursor", "codex", "grok"} {
		assert.Contains(t, crit, l)
	}
	assert.Contains(t, crit["cursor"], "quality prior", "each option carries the registry's facts, not a prompt")
	assert.Contains(t, crit["cursor"], "code work")
	assert.Contains(t, got["questions"].(map[string]any)[PointLeg].(map[string]any)["instructions"], "code task")

	require.NotNil(t, sh)
	assert.Equal(t, []Leg{LegCursor, LegCodex, LegGrok}, sh.Menu, "the record carries the menu the leg question was asked over")
	assert.Equal(t, "jev-1.13.0", sh.Model, "the versioned id that served the call")
	assert.Equal(t, LegJev, sh.Leg)
	assert.Equal(t, 408, sh.Tokens)
	assert.Equal(t, res.Tokens, sh.Tokens)
	assert.Equal(t, ShapeSolo, sh.Answers[PointShape].Choice)
	assert.Equal(t, "cursor", sh.Answers[PointLeg].Choice)
	assert.InDelta(t, 0.8, sh.Answers[PointLeg].Confidence, 1e-9)
	assert.Empty(t, sh.Answers[PointLeg].Actual, "nothing is compared until captain has decided")
}

func TestClassifyWithJevStillAsksOnlyTheTriageQuestions(t *testing.T) {
	var got map[string]any
	c := s1Answering(t, map[string]string{PointClass: "trivial", PointDomain: "editorial"}, 0.9, &got)
	tr, _, err := ClassifyWithJev(context.Background(), c, "fix the typo")
	require.NoError(t, err)
	assert.Equal(t, []string{PointClass, PointDomain}, questionNames(got))
	assert.Equal(t, ClassTrivial, tr.Class)

	c = s1Answering(t, nil, 0.9, &got)
	_, sh, _, err := TriageWithJev(context.Background(), c, "fix the typo", JevTriageOptions{Menu: []Leg{LegCursor, LegGrok}})
	require.NoError(t, err)
	assert.Nil(t, sh, "no shadow asked for, none returned")
	assert.Equal(t, []string{PointClass, PointDomain}, questionNames(got))
}

func TestJevLegQuestionNeedsTwoWorkersAndSkipsDecisionLegs(t *testing.T) {
	_, offered := JevLegQuestion([]Leg{LegCursor}, DomainCode)
	assert.Nil(t, offered, "one worker is no choice")
	_, offered = JevLegQuestion([]Leg{LegJev, LegCursor}, "")
	assert.Nil(t, offered, "the decision leg is not a worker and does not make it a choice")

	q, offered := JevLegQuestion([]Leg{LegCursor, LegCursor, LegGrok}, "")
	assert.Equal(t, []Leg{LegCursor, LegGrok}, offered)
	crit := q.Criteria.(map[string]string)
	assert.Len(t, crit, 2)
	assert.Contains(t, q.Instructions, "general task", "no domain given reads as general work")
	assert.Contains(t, crit["grok"], "subscription", "a subscription leg says so instead of a price")

	var got map[string]any
	c := s1Answering(t, nil, 0.9, &got)
	_, sh, _, err := TriageWithJev(context.Background(), c, "x", JevTriageOptions{Shadow: true, Menu: []Leg{LegCursor}})
	require.NoError(t, err)
	assert.Equal(t, []string{PointClass, PointDomain, PointShape}, questionNames(got), "the shape is still asked when the menu has no choice")
	assert.Nil(t, sh.Menu)
}

func TestJevNoteQuestionOffersEveryWorkerAndAll(t *testing.T) {
	q, legs := JevNoteQuestion(map[Leg]string{LegGrok: "run the GPU  benchmark\n on the SKU", LegGLM: "write the docs"})
	assert.Equal(t, []Leg{LegGLM, LegGrok}, legs, "in a stable order")
	crit := q.Criteria.(map[string]string)
	assert.Len(t, crit, 3)
	assert.Contains(t, crit, NoteToAll)
	assert.Contains(t, crit["grok"], "run the GPU benchmark on the SKU", "the brief, whitespace folded")
	assert.Contains(t, q.Instructions, `"all"`)
}

func TestRouteNoteWithJevIsAShadowOverTheWorkers(t *testing.T) {
	var got map[string]any
	c := s1Answering(t, map[string]string{PointNoteRoute: "grok"}, 0.75, &got)
	sh, res, err := RouteNoteWithJev(context.Background(), c, "  Cerebras, not Cerberus  ", map[Leg]string{LegGrok: "benchmark", LegGLM: "docs"})
	require.NoError(t, err)
	assert.Equal(t, "Cerebras, not Cerberus", got["state"], "the note is the state; the briefs are the options")
	assert.Equal(t, []string{PointNoteRoute}, questionNames(got))
	assert.Contains(t, criteriaOf(got, PointNoteRoute), NoteToAll)
	assert.Equal(t, []Leg{LegGLM, LegGrok}, sh.Menu)
	assert.Equal(t, "grok", sh.Answers[PointNoteRoute].Choice)
	assert.Equal(t, res.Tokens, sh.Tokens)
}

func TestShadowStampComparesByContainment(t *testing.T) {
	sh := &Shadow{Answers: map[string]ShadowAnswer{PointLeg: {Choice: "grok"}, PointShape: {Choice: ShapeSolo}}}
	sh.Stamp(PointLeg, "glm+grok", PathDirector)
	assert.True(t, sh.Answers[PointLeg].Agree, "one of a team's legs is agreement with a single pick")
	assert.Equal(t, "glm+grok", sh.Answers[PointLeg].Actual)
	assert.Equal(t, PathDirector, sh.Answers[PointLeg].By)
	sh.Stamp(PointShape, ShapeTeam, PathDirector)
	assert.False(t, sh.Answers[PointShape].Agree)
	sh.Stamp(PointClass, "medium", "heuristic")
	assert.NotContains(t, sh.Answers, PointClass, "a point that was not asked gains no answer")
	sh.Stamp(PointLeg, "", PathForced)
	assert.False(t, sh.Answers[PointLeg].Agree, "no actual decision is no agreement")
	var none *Shadow
	none.Stamp(PointLeg, "grok", PathValue) // nil-safe
}

func TestShadowFromAFailedCallKeepsTheErrorAndNoAnswers(t *testing.T) {
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	_, sh, _, err := TriageWithJev(context.Background(), c, "x", JevTriageOptions{Shadow: true, Menu: []Leg{LegCursor, LegGrok}})
	require.Error(t, err)
	require.NotNil(t, sh, "a failed call is on the record too")
	assert.Contains(t, sh.Err, "401")
	assert.Empty(t, sh.Answers)
	assert.Equal(t, "jev-latest", sh.Model, "the model asked for, since none answered")
	assert.Equal(t, []Leg{LegCursor, LegGrok}, sh.Menu)
}

func shadowLeg(choice string, conf float64, actual string, agree bool) *Shadow {
	return &Shadow{Leg: LegJev, Model: "jev-1.13.0", Menu: []Leg{LegCursor, LegCodex, LegGrok},
		Answers: map[string]ShadowAnswer{
			PointLeg: {Choice: choice, Confidence: conf, Actual: actual, By: PathDirector, Agree: agree}}}
}

func TestShadowCalibrationSkipsActedRowsBinsByConfidenceAndLabelsOutcomes(t *testing.T) {
	decisions := []Decision{
		{TaskID: "t1", Shadow: shadowLeg("cursor", 0.95, "cursor", true)},
		{TaskID: "t2", Shadow: shadowLeg("cursor", 0.85, "codex", false)},
		{TaskID: "t3", Shadow: shadowLeg("grok", 0.92, "grok", true)},
		{TaskID: "t4", Shadow: &Shadow{Answers: map[string]ShadowAnswer{PointClass: {Choice: "medium", Confidence: 0.9, Actual: "medium", By: DecidedByJev, Agree: true}}}},
		{TaskID: "t5", Shadow: &Shadow{Err: "boom"}},
		{TaskID: "t6", Shadow: &Shadow{Answers: map[string]ShadowAnswer{PointLeg: {Choice: "grok", Confidence: 0.9}}}},
		{TaskID: "t7"},
	}
	shadows := []ShadowRecord{
		{Point: PointNoteRoute, TaskID: "t1", Shadow: Shadow{Answers: map[string]ShadowAnswer{PointNoteRoute: {Choice: "grok", Confidence: 0.6, Actual: "grok", By: PathDirector, Agree: true}}}},
		{Point: PointNoteRoute, Shadow: Shadow{Err: "late"}},
	}
	outcomes := []OutcomeEvidence{{TaskID: "t1", Status: AcceptanceAccepted}, {TaskID: "t2", Status: AcceptanceRejected}, {TaskID: "t3", Status: AcceptancePending}}

	cal := ShadowCalibration(decisions, shadows, outcomes)
	names := make([]string, 0, len(cal))
	byPoint := map[string]PointCalibration{}
	for _, p := range cal {
		names = append(names, p.Point)
		byPoint[p.Point] = p
	}
	assert.Equal(t, []string{PointClass, PointDomain, PointShape, PointLeg, PointNoteRoute}, names, "the route-time points first, in decision order")

	leg := byPoint[PointLeg]
	assert.Equal(t, 3, leg.Compared, "t6 was never compared: no actual decision at that point")
	assert.Equal(t, 2, leg.Agree)
	assert.Equal(t, 1, leg.Failed, "t5's failed call counts against every route-time point")
	assert.Zero(t, leg.Acted)
	assert.InDelta(t, 2.0/3, leg.Rate(), 1e-9)
	bins := map[float64]CalBin{}
	for _, b := range leg.Bins {
		bins[b.Floor] = b
	}
	assert.Equal(t, CalBin{Floor: 0.95, N: 1, Agree: 1}, bins[0.95])
	assert.Equal(t, CalBin{Floor: 0.9, N: 2, Agree: 2}, bins[0.9])
	assert.Equal(t, CalBin{Floor: 0.8, N: 3, Agree: 2}, bins[0.8], "cumulative: the bins read 'if the bar were here'")
	assert.Equal(t, 1, leg.Accepted)
	assert.Equal(t, 1, leg.AcceptedAgree)
	assert.Equal(t, 1, leg.Rejected)
	assert.Zero(t, leg.RejectedAgree)
	assert.Equal(t, 1, leg.Pending)
	bar, ok := leg.SuggestedBar(0.9, 1)
	assert.True(t, ok)
	assert.Equal(t, 0.9, bar, "the lowest floor meeting the target with every floor above it")
	_, ok = leg.SuggestedBar(0.9, 5)
	assert.False(t, ok, "a small sample suggests nothing")

	class := byPoint[PointClass]
	assert.Equal(t, 1, class.Acted, "jev's own answer taken is not a comparison: it would agree with itself")
	assert.Zero(t, class.Compared)
	assert.Equal(t, 1, class.Failed)

	note := byPoint[PointNoteRoute]
	assert.Equal(t, 1, note.Compared)
	assert.Equal(t, 1, note.Accepted, "a note's shadow joins the task it was recorded on")
	assert.Equal(t, 1, note.Failed)

	out := FormatShadowCalibration(cal, 0.9, 1)
	assert.Contains(t, out, "leg        3 compared, agreement 0.67 over menus of 3")
	assert.Contains(t, out, "≥0.90 2/2 (1.00)")
	assert.Contains(t, out, "bar: 0.90")
	assert.Contains(t, out, "class      0 compared")
	assert.Contains(t, out, "+1 where jev's own answer was taken")
	assert.Contains(t, out, "bar: none yet")
	assert.Contains(t, FormatShadowCalibration(nil, 0.9, 20), "no shadow decisions yet")
}

func TestFormatShadowNamesEachComparison(t *testing.T) {
	sh := Shadow{Model: "jev-1.13.0", Ms: 312, Answers: map[string]ShadowAnswer{
		PointClass:  {Choice: "medium", Confidence: 0.91, Actual: "medium", By: DecidedByJev, Agree: true},
		PointDomain: {Choice: "code", Confidence: 0.88, Actual: "code", By: "heuristic", Agree: true},
		PointShape:  {Choice: ShapeSolo, Confidence: 0.95},
		PointLeg:    {Choice: "codex", Confidence: 0.62, Actual: "cursor", By: PathDirector},
	}}
	out := FormatShadow(sh)
	assert.True(t, strings.HasPrefix(out, "jev-1.13.0 312ms · "), out)
	for _, want := range []string{"class medium 0.91 (acted)", "domain code 0.88 ✓", "shape solo 0.95 (not compared)", "leg codex 0.62 ✗ (cursor by director)"} {
		assert.Contains(t, out, want)
	}
	assert.Less(t, strings.Index(out, "class "), strings.Index(out, "leg "), "points in decision order")
	assert.Equal(t, "jev · no answer: boom", FormatShadow(Shadow{Leg: LegJev, Err: "boom"}))
}

func TestLedgerShadowsAreCapped(t *testing.T) {
	l := &Ledger{}
	for i := 0; i < maxShadows+5; i++ {
		l.RecordShadow(ShadowRecord{Point: PointNoteRoute, Task: "n"})
	}
	assert.Len(t, l.Shadows, maxShadows)
	assert.Equal(t, ShadowVersion, l.Shadows[0].Version)
	assert.WithinDuration(t, time.Now(), l.Shadows[0].At, time.Minute)
}

// A menu point's agreement rate means nothing without the width of the menu:
// two options and fourteen are not the same question, and the report has to
// say so on the line that carries the rate.
func TestShadowCalibrationCarriesTheWidthOfTheMenuJevWasAsked(t *testing.T) {
	narrow := shadowLeg("ds-flash", 0.8, "claude", false)
	narrow.Menu = []Leg{LegClaude, Leg("ds-flash")}
	wide := shadowLeg("cursor", 0.7, "cursor", true)
	wide.Menu = Rungs

	cal := ShadowCalibration([]Decision{{TaskID: "t1", Shadow: narrow}, {TaskID: "t2", Shadow: wide}}, nil, nil)
	var leg PointCalibration
	for _, p := range cal {
		if p.Point == PointLeg {
			leg = p
		}
	}
	assert.Equal(t, 2, leg.Compared)
	assert.Equal(t, 2, leg.MenuMin)
	assert.Equal(t, len(Rungs), leg.MenuMax)
	out := FormatShadowCalibration(cal, 0.9, 20)
	assert.Contains(t, out, fmt.Sprintf("agreement 0.50 over menus of 2-%d", len(Rungs)))

	// The menu is recorded once per call, beside every answer on it. Shape is
	// not asked over it, so its line must not claim to have been.
	shape := shadowLeg("cursor", 0.7, "cursor", true)
	shape.Answers[PointShape] = ShadowAnswer{Choice: ShapeSolo, Confidence: 0.8, Actual: ShapeSolo, By: PathDirector, Agree: true}
	out = FormatShadowCalibration(ShadowCalibration([]Decision{{TaskID: "t3", Shadow: shape}}, nil, nil), 0.9, 20)
	assert.NotContains(t, strings.Split(out, "leg")[0], "over menus of", "only a menu point is asked over a menu")
}
