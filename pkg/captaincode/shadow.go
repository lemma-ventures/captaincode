package captaincode

// Shadow decisions on the decision leg (ROADMAP M5.4, the jev experiment).
//
// Several of captain's own choices are closed-set questions a System One
// model answers in a few hundred milliseconds with a calibrated probability:
// how big the task is, what kind of work it is, whether one worker or a team
// should take it, which worker, and which running worker a mid-turn note
// concerns. Today a frontier director answers the last three in prose, on
// the critical path. Before any of them is handed to jev, captain has to
// know how well jev agrees with what captain actually did, at what
// confidence, and how those turns ended - so the questions are asked in
// the shadow: answered beside the real decision, recorded next to it with
// who made it, joined to the task's outcome, and never acted on.
//
// jev is not a chat model, so nothing here sends it a prompt. Each decision
// point is translated into the API's own shape - one typed question with
// instructions and criteria - by the code below, deterministically: the leg
// question's options are the director's menu described from the registry,
// the note question's options are the running workers' briefs. The state is
// the same redacted task head (or note) that already leaves the machine for
// triage; worker output, gate logs and memory never do.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ShadowVersion is stamped on standalone shadow rows (ShadowRecord).
const ShadowVersion = 1

const maxShadows = 400

// Decision points a shadow answer is recorded for. The route-time points
// ride on the turn's Decision record; a note route has no decision record
// and is a ShadowRecord of its own.
const (
	PointClass     = "class"
	PointDomain    = "domain"
	PointShape     = "shape"
	PointLeg       = "leg"
	PointNoteRoute = "note-route"
	// The two questions added beside class and domain at the triage point
	// (stage 2): whether the work has a cheap undo, and whether a mid-tier
	// worker would get it right first time.
	PointIrreversible = "irreversible"
	PointMidTier      = "mid-tier"
)

// Shapes a turn can take. The director's plan is one worker or a fan-out;
// a workflow is the user's own control word and is not a routing choice.
const (
	ShapeSolo = "solo"
	ShapeTeam = "team"
)

// NoteToAll is the note-route answer meaning every running worker.
const NoteToAll = "all"

// DecidedByJev marks a point where jev's own answer was the decision (a sure
// triage answer taken by tier 1): it trivially agrees with itself, so the
// calibration counts it as acted on, not as a comparison.
const DecidedByJev = "jev"

// ShadowAnswer is jev's answer at one point beside what captain did there.
type ShadowAnswer struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Actual        string             `json:"actual,omitempty"` // what captain decided; legs of a team joined by "+"
	By            string             `json:"by,omitempty"`     // who decided: director, value, ladder, forced, heuristic, classify, jev
	Agree         bool               `json:"agree,omitempty"`  // Choice is Actual, or one of Actual's "+"-joined parts
	// NotOffered: captain's own pick was not among the options jev was given -
	// a leg forced off the ladder, a director naming a leg the menu did not
	// carry. jev could not have answered it, so it is not a disagreement.
	NotOffered bool `json:"not_offered,omitempty"`
}

// Shadow is one decision-leg call made beside a captain decision: the
// versioned model that served it, its cost, the menu the leg question was
// asked over, and the answers with their comparison.
type Shadow struct {
	Leg   Leg    `json:"leg"`
	Model string `json:"model,omitempty"`
	// Backend is WHICH System One implementation answered - "typesafe" for the
	// vendor, the host for anything else (systemone.go Backend). A bar
	// calibrated against one backend says nothing about another, and `jev-latest`
	// is an alias that moves, so a report that pooled two of them would be
	// quoting a number no single configuration ever produced.
	Backend string                  `json:"backend,omitempty"`
	At      time.Time               `json:"at"`
	Ms      int64                   `json:"ms,omitempty"`
	Tokens  int                     `json:"tokens,omitempty"`
	Err     string                  `json:"err,omitempty"` // the call failed: recorded as such, never invented
	Menu    []Leg                   `json:"menu,omitempty"`
	Answers map[string]ShadowAnswer `json:"answers,omitempty"`
}

// ShadowRecord is a Shadow at a point that has no decision record of its own
// (note routing), joined to the turn's task when the turn had one.
type ShadowRecord struct {
	Version int    `json:"version"`
	Point   string `json:"point"`
	// Points: the decision points this one call answered, when it answered
	// more than the row's own (the action gate's three nouls, the supervisor's
	// four). Empty means the row carries Point alone.
	Points []string `json:"points,omitempty"`
	TaskID string   `json:"task_id,omitempty"`
	Task   string   `json:"task,omitempty"` // the note's head
	Shadow
}

// Stamp records what captain actually decided at a point and who decided it.
// Agreement is containment: a leg among a team's legs, a worker among the
// workers a note was routed to, count as agreement with a single pick.
//
// A menu point captain settled with something the menu never carried - `/grok`
// forcing the director leg, which serves tasks but is no rung - is marked
// NotOffered rather than counted as a miss: the calibration exists to set a
// bar, and a question jev was never allowed to answer must not move it.
func (sh *Shadow) Stamp(point, actual, by string) {
	if sh == nil {
		return
	}
	a, ok := sh.Answers[point]
	if !ok {
		return
	}
	a.Actual, a.By, a.Agree, a.NotOffered = actual, by, false, false
	for _, part := range strings.Split(actual, "+") {
		if part != "" && part == a.Choice {
			a.Agree = true
		}
	}
	a.NotOffered = !a.Agree && sh.offMenu(point, actual)
	sh.Answers[point] = a
}

// menuPoint says a point is answered over a menu of options - the leg and the
// note route. The menu is recorded once per call, so class, domain and shape
// ride beside one without being asked over it.
func menuPoint(point string) bool { return point == PointLeg || point == PointNoteRoute }

// offMenu says none of what captain decided was on the menu jev was asked
// over. Only the menu points have one; "all" is always open on a note route;
// one offered part is enough, since jev could have named that part and agreed.
func (sh *Shadow) offMenu(point, actual string) bool {
	if len(sh.Menu) == 0 || !menuPoint(point) {
		return false
	}
	for _, part := range strings.Split(actual, "+") {
		if part == "" || (point == PointNoteRoute && part == NoteToAll) || legIn(Leg(part), sh.Menu) {
			return false
		}
	}
	return true
}

// shadowFrom turns one call into its record. A failed call keeps its model,
// latency and error so a report can count it, and carries no answers.
func shadowFrom(c *SystemOneClient, resp S1Response, res Result, err error, menu []Leg) *Shadow {
	sh := &Shadow{Leg: LegJev, Model: resp.Model, At: time.Now(), Ms: res.DurationMs, Tokens: res.Tokens, Menu: menu}
	if c != nil {
		if sh.Model == "" {
			sh.Model = c.model()
		}
		sh.Backend = c.Backend()
	}
	if err != nil {
		sh.Err = err.Error()
		return sh
	}
	// A noul answer (irreversible, mid-tier) is a probability, recorded the
	// way the gate and the supervisor record theirs: as a true/false choice
	// at the probability's confidence, so the calibration reads one shape.
	sh.Answers = nulAnswers(resp.Answers)
	return sh
}

// ---- translation: a decision point → one typed question ---------------------

// JevShapeQuestion asks whether the task is one thread of work or splits into
// parts that can run at the same time - the director's solo-or-team call.
func JevShapeQuestion() S1Question {
	return S1Question{Type: "choice",
		Instructions: "How should a coding-agent router staff this task? Judge whether the work splits into parts that can be done at the same time by different workers, not how big it is.",
		Criteria: map[string]string{
			ShapeSolo: "one worker does the whole task start to finish: one thread of work, or parts that depend on each other",
			ShapeTeam: "two or more workers take separable parts at the same time and the results are merged: the task names or clearly contains independent parts (several files, several questions, an audit from several angles)",
		}}
}

// jevNoteMax bounds each option's description: a leg's registry note or a
// worker's brief. Enough to carry what the option is for, small enough that
// a ten-leg menu is a few hundred input tokens.
const jevNoteMax = 160

// JevLegQuestion translates the director's menu into a choice: each open
// worker leg is an option described from the registry (its briefing line,
// its quality prior for this kind of work, what it costs). Fewer than two
// options is no question, and the legs actually offered are returned so
// the record can carry the menu it was asked over.
func JevLegQuestion(menu []Leg, d Domain) (S1Question, []Leg) {
	if d == "" {
		d = DomainGeneral
	}
	crit := map[string]string{}
	var offered []Leg
	for _, l := range menu {
		if !KnownLeg(l) || !ServesTasks(l) || legIn(l, offered) {
			continue
		}
		offered = append(offered, l)
		crit[string(l)] = describeLegForJev(l, d)
	}
	if len(offered) < 2 {
		return S1Question{}, nil
	}
	return S1Question{Type: "choice",
		Instructions: fmt.Sprintf("Which worker should take this %s task? Each option is one coding agent: what it is good at, its quality prior for %s work (0-10) and what it costs. Pick the cheapest one that would still do this task well; pick a stronger one only when the task needs it.", d, d),
		Criteria:     crit}, offered
}

// describeLegForJev is one option's criteria: the registry's briefing line,
// the domain prior, and the cost - the same facts the director's menu carries.
func describeLegForJev(l Leg, d Domain) string {
	s, _ := Spec(l)
	note := strings.TrimSpace(s.Note)
	if note == "" {
		note = s.Display
	}
	parts := []string{truncateStr(strings.Join(strings.Fields(note), " "), jevNoteMax)}
	if q := QualityPriorFor(l, d); q > 0 {
		parts = append(parts, fmt.Sprintf("quality prior %.1f/10 for %s work", q, d))
	}
	switch {
	case s.Subscription:
		parts = append(parts, "billed by a subscription window, no cost per token")
	case s.PriceIn > 0:
		parts = append(parts, fmt.Sprintf("$%.2f per 1M input tokens", s.PriceIn))
	default:
		parts = append(parts, "free")
	}
	return strings.Join(parts, " · ")
}

// JevRouteQuestions is the whole route-time call: the triage questions the
// gate acts on, plus the shadow ones (shape, and leg over the menu).
func JevRouteQuestions(menu []Leg, d Domain) (map[string]S1Question, []Leg) {
	qs := jevClassQuestions()
	qs[PointShape] = JevShapeQuestion()
	q, offered := JevLegQuestion(menu, d)
	if len(offered) > 0 {
		qs[PointLeg] = q
	}
	return qs, offered
}

// JevNoteQuestion translates a note-routing situation into one choice: each
// running worker's brief (redacted, cut to its head) is an option, and
// "all" is the option for a note about the whole task. The workers are
// returned in the order the record lists them.
func JevNoteQuestion(briefs map[Leg]string) (S1Question, []Leg) {
	legs := make([]Leg, 0, len(briefs))
	for l := range briefs {
		legs = append(legs, l)
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i] < legs[j] })
	crit := map[string]string{NoteToAll: "the note changes every worker's assignment: it is about the whole task, not one worker's part"}
	for _, l := range legs {
		brief, _ := Redact(strings.Join(strings.Fields(briefs[l]), " "))
		crit[string(l)] = "this worker's assignment: " + truncateStr(brief, jevNoteMax*2)
	}
	return S1Question{Type: "choice",
		Instructions: "The user sent this note while several workers were running on their assignments. Which worker's assignment does the note change? Answer \"all\" when it applies to the whole task.",
		Criteria:     crit}, legs
}

// ---- the calls ----------------------------------------------------------------

// JevTriageOptions says what rides along with the triage questions.
type JevTriageOptions struct {
	Shadow bool   // also ask shape and leg, for the record
	Menu   []Leg  // the legs the leg question is asked over (the director's menu)
	Domain Domain // the heuristic's domain, for the leg question's framing
}

// TriageWithJev is the route-time call: the triage answer the gate reads
// (class, domain, the weaker confidence) and, when asked for, the shadow
// with every answer the call produced - returned on a failed call too, so a
// failure is on the record. The task is redacted and cut to its head the
// way the egress proxy treats a worker's traffic before it leaves the machine.
func TriageWithJev(ctx context.Context, c *SystemOneClient, task string, o JevTriageOptions) (TriageResult, *Shadow, Result, error) {
	state, _ := Redact(truncateStr(task, systemOneStateMax))
	qs, menu := jevClassQuestions(), []Leg(nil)
	if o.Shadow {
		qs, menu = JevRouteQuestions(o.Menu, o.Domain)
	}
	resp, res, err := c.Ask(ctx, state, qs)
	var sh *Shadow
	if o.Shadow {
		sh = shadowFrom(c, resp, res, err, menu)
	}
	if err != nil {
		return TriageResult{}, sh, res, err
	}
	tr, err := triageFromAnswers(resp, res)
	return tr, sh, res, err
}

// triageFromAnswers maps the class and domain answers onto the triage
// verdict; the confidence is the weaker of the two, the figure the gate reads.
func triageFromAnswers(resp S1Response, res Result) (TriageResult, error) {
	class, ok := ParseClass(resp.Answers[PointClass].Choice)
	if !ok {
		return TriageResult{}, fmt.Errorf("jev returned class %q", resp.Answers[PointClass].Choice)
	}
	domain := DomainGeneral
	switch d := Domain(resp.Answers[PointDomain].Choice); d {
	case DomainCode, DomainEditorial, DomainResearch, DomainGeneral:
		domain = d
	}
	conf := resp.Answers[PointClass].Confidence
	if dc := resp.Answers[PointDomain].Confidence; dc < conf {
		conf = dc
	}
	tr := TriageResult{Class: class, Domain: domain, Confidence: conf, By: TriageByJev,
		Why: fmt.Sprintf("jev %s/%s (conf %.2f, %s, %dms)", class, domain, conf, resp.Model, res.DurationMs)}
	// The noul answers: a probability each, read only when the model
	// answered them (an older backend, or a conformance fake, may not).
	if a, ok := resp.Answers[PointIrreversible]; ok {
		tr.Irreversible = a.Noul >= 0.5
	}
	if a, ok := resp.Answers[PointMidTier]; ok {
		tr.MidTierP = a.Noul
	}
	return tr, nil
}

// RouteNoteWithJev asks which running worker a mid-turn note concerns, from
// the same briefs the director reads. The answer is a shadow: recorded
// beside the director's, never delivered.
func RouteNoteWithJev(ctx context.Context, c *SystemOneClient, note string, briefs map[Leg]string) (*Shadow, Result, error) {
	state, _ := Redact(truncateStr(strings.TrimSpace(note), 1200))
	q, legs := JevNoteQuestion(briefs)
	resp, res, err := c.Ask(ctx, state, map[string]S1Question{PointNoteRoute: q})
	return shadowFrom(c, resp, res, err, legs), res, err
}

// ---- the ledger -----------------------------------------------------------------

// RecordShadow appends a standalone shadow row (note routing).
func (l *Ledger) RecordShadow(r ShadowRecord) {
	r.Version = ShadowVersion
	if r.At.IsZero() {
		r.At = time.Now()
	}
	l.Shadows = append(l.Shadows, r)
	if len(l.Shadows) > maxShadows {
		l.Shadows = l.Shadows[len(l.Shadows)-maxShadows:]
	}
}

// ---- the reading ----------------------------------------------------------------

// CalBin counts the comparisons at or above a confidence floor - cumulative,
// so each bin reads "if the bar were here".
type CalBin struct {
	Floor float64 `json:"floor"`
	N     int     `json:"n"`
	Agree int     `json:"agree"`
}

// calFloors are the bars a report tries, top down.
var calFloors = []float64{0.95, 0.9, 0.8, 0.7, 0.6, 0.5, 0}

// PointCalibration is one decision point's shadow record read as a
// calibration: how often jev agreed with captain, at what confidence, and
// how the turns it was compared on ended.
type PointCalibration struct {
	Point    string `json:"point"`
	Compared int    `json:"compared"` // rows with an actual decision to compare against
	Agree    int    `json:"agree"`
	Acted    int    `json:"acted"`  // rows where jev's own answer was the decision (no comparison)
	Failed   int    `json:"failed"` // calls that produced no answer
	// NotOffered: rows where captain's pick was not on the menu jev was given.
	// Kept out of Compared so a forced leg cannot drag the agreement rate down.
	NotOffered int `json:"not_offered"`
	// MenuMin/MenuMax: how many options jev was actually given at a menu point
	// (leg, note route), across the compared rows. A two-option menu and a
	// fourteen-option one are not the same question, and pooling them silently
	// makes a low rate read as "jev is bad at picking legs" when it may only
	// mean jev was shown two legs on a turn the director always settles the
	// same way. Zero at a point that has no menu.
	MenuMin int      `json:"menu_min,omitempty"`
	MenuMax int      `json:"menu_max,omitempty"`
	Bins    []CalBin `json:"bins"`

	// Backends/Models: which System One implementation and which versioned
	// model answered the compared rows, counted. A bar is a property of ONE
	// backend at ONE version: `jev-latest` is an alias that moves under the
	// record, and an open re-implementation is a different model entirely. When
	// more than one appears here the rows are not one sample, and SuggestedBar
	// declines rather than average them.
	Backends map[string]int `json:"backends,omitempty"`
	Models   map[string]int `json:"models,omitempty"`

	Accepted      int `json:"accepted"` // compared rows whose task was accepted
	AcceptedAgree int `json:"accepted_agree"`
	Rejected      int `json:"rejected"` // rejected or regressed
	RejectedAgree int `json:"rejected_agree"`
	Pending       int `json:"pending"`
}

// Rate is the agreement rate over the compared rows.
func (p PointCalibration) Rate() float64 {
	if p.Compared == 0 {
		return 0
	}
	return float64(p.Agree) / float64(p.Compared)
}

// Mixed says the compared rows came from more than one backend or more than
// one versioned model - so they are not one calibration sample.
func (p PointCalibration) Mixed() bool { return len(p.Backends) > 1 || len(p.Models) > 1 }

// sole names the only key of a counted set, or "" when there is not exactly one.
func sole(m map[string]int) string {
	if len(m) != 1 {
		return ""
	}
	for k := range m {
		return k
	}
	return ""
}

// Served names the backend and model the compared rows came from, or says
// they were mixed.
func (p PointCalibration) Served() string {
	b, m := sole(p.Backends), sole(p.Models)
	switch {
	case b != "" && m != "":
		return b + " " + m
	case p.Mixed():
		return fmt.Sprintf("%d backend(s), %d model(s) - mixed", len(p.Backends), len(p.Models))
	}
	return b + m
}

// SuggestedBar is the lowest confidence floor at which jev agreed with
// captain at least target of the time, over at least minN comparisons, with
// every higher floor meeting the target too - the bar a gate for this point
// could be set at. False when no floor qualifies: the honest answer while the
// sample is small.
func (p PointCalibration) SuggestedBar(target float64, minN int) (float64, bool) {
	if p.Mixed() {
		return 0, false // rows from two backends or two model versions are two samples
	}
	bar, ok := 0.0, false
	for _, b := range p.Bins {
		if b.N < minN || float64(b.Agree)/float64(b.N) < target {
			break
		}
		bar, ok = b.Floor, true
	}
	return bar, ok
}

// ShadowCalibration reads every shadow on the ledger - the route-time ones on
// the decision records, the standalone ones - into one calibration per
// decision point, labelled with the outcome of the task each was recorded on.
func ShadowCalibration(decisions []Decision, shadows []ShadowRecord, outcomes []OutcomeEvidence) []PointCalibration {
	status := make(map[string]AcceptanceStatus, len(outcomes))
	for _, o := range outcomes {
		status[o.TaskID] = o.Status
	}
	points := map[string]*PointCalibration{}
	get := func(name string) *PointCalibration {
		p, ok := points[name]
		if !ok {
			p = &PointCalibration{Point: name, Bins: make([]CalBin, len(calFloors))}
			for i, f := range calFloors {
				p.Bins[i].Floor = f
			}
			points[name] = p
		}
		return p
	}
	add := func(point string, a ShadowAnswer, taskID string, menu int, sh Shadow) {
		p := get(point)
		if a.By == DecidedByJev {
			p.Acted++
			return
		}
		if a.Actual == "" {
			return // never compared: the turn did not reach a decision at this point
		}
		if a.NotOffered {
			p.NotOffered++
			return // jev was never offered what captain picked: not a miss
		}
		p.Compared++
		if a.Agree {
			p.Agree++
		}
		if sh.Backend != "" {
			if p.Backends == nil {
				p.Backends = map[string]int{}
			}
			p.Backends[sh.Backend]++
		}
		if sh.Model != "" {
			if p.Models == nil {
				p.Models = map[string]int{}
			}
			p.Models[sh.Model]++
		}
		if menu > 0 && menuPoint(point) {
			if p.MenuMin == 0 || menu < p.MenuMin {
				p.MenuMin = menu
			}
			if menu > p.MenuMax {
				p.MenuMax = menu
			}
		}
		for i := range p.Bins {
			if a.Confidence >= p.Bins[i].Floor {
				p.Bins[i].N++
				if a.Agree {
					p.Bins[i].Agree++
				}
			}
		}
		switch status[taskID] {
		case AcceptanceAccepted:
			p.Accepted++
			if a.Agree {
				p.AcceptedAgree++
			}
		case AcceptanceRejected, AcceptanceRegressed:
			p.Rejected++
			if a.Agree {
				p.RejectedAgree++
			}
		case AcceptancePending:
			p.Pending++
		}
	}
	for _, d := range decisions {
		if d.Shadow == nil {
			continue
		}
		if d.Shadow.Err != "" {
			for _, point := range []string{PointClass, PointDomain, PointShape, PointLeg} {
				get(point).Failed++
			}
			continue
		}
		for point, a := range d.Shadow.Answers {
			add(point, a, d.TaskID, len(d.Shadow.Menu), *d.Shadow)
		}
	}
	for _, r := range shadows {
		if r.Err != "" {
			for _, point := range r.points() {
				get(point).Failed++
			}
			continue
		}
		for _, point := range r.points() {
			if a, ok := r.Answers[point]; ok {
				add(point, a, r.TaskID, len(r.Menu), r.Shadow)
			}
		}
	}
	order := map[string]int{PointClass: 0, PointDomain: 1, PointShape: 2, PointLeg: 3, PointNoteRoute: 4,
		PointGateDestructive: 5, PointGateOutOfScope: 6, PointGateExfil: 7,
		PointWorkerStuck: 8, PointWorkOffTrack: 9, PointNeedsHuman: 10, PointAgentsDrift: 11}
	out := make([]PointCalibration, 0, len(points))
	for _, p := range points {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, oki := order[out[i].Point]
		oj, okj := order[out[j].Point]
		if oki != okj {
			return oki
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].Point < out[j].Point
	})
	return out
}

// formatPointOrder is the order a shadow's answers read on one line.
var formatPointOrder = append(append([]string{PointClass, PointDomain, PointShape, PointLeg, PointNoteRoute},
	GatePoints...), SupervisePoints...)

// points is the decision points a row carries: Points when a single call
// answered several, the row's own Point otherwise.
func (r ShadowRecord) points() []string {
	if len(r.Points) > 0 {
		return r.Points
	}
	return []string{r.Point}
}

// FormatShadow renders one shadow on a line: the model and latency, then
// each answer with its confidence and how it compared.
func FormatShadow(sh Shadow) string {
	head := string(sh.Leg)
	if sh.Model != "" {
		head = sh.Model
	}
	if sh.Ms > 0 {
		head += fmt.Sprintf(" %dms", sh.Ms)
	}
	if sh.Err != "" {
		return head + " · no answer: " + sh.Err
	}
	parts := []string{head}
	for _, point := range formatPointOrder {
		a, ok := sh.Answers[point]
		if !ok {
			continue
		}
		s := fmt.Sprintf("%s %s %.2f", point, a.Choice, a.Confidence)
		switch {
		case a.By == DecidedByJev:
			s += " (acted)"
		case a.Actual == "":
			s += " (not compared)"
		case a.NotOffered:
			s += " (not offered: " + a.Actual + " by " + a.By + ")"
		case a.Agree:
			s += " ✓"
		default:
			s += " ✗ (" + a.Actual + " by " + a.By + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

// FormatShadowCalibration renders the calibration the way it should be read:
// agreement by confidence floor, how wide a menu point's menu was, the
// outcome labels, and the bar each point could be gated at - or that the
// sample is still too small to say.
func FormatShadowCalibration(cal []PointCalibration, target float64, minN int) string {
	if len(cal) == 0 {
		return "no shadow decisions yet - they are recorded once a decision leg is configured (TYPESAFE_API_KEY, or a keyless CAPTAIN_SYSTEMONE_URL) and turns route through triage or the director\n"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "shadow decisions: jev beside captain's own choices, recorded and never acted on (bar target %.2f, at least %d comparisons)\n", target, minN)
	for _, p := range cal {
		if p.Compared == 0 {
			// An agreement rate over nothing reads as total disagreement,
			// which is the opposite of what an uncompared point means.
			fmt.Fprintf(&sb, "\n%-10s 0 compared - nothing has settled these yet, so there is no agreement rate", p.Point)
		} else {
			fmt.Fprintf(&sb, "\n%-10s %d compared, agreement %.2f", p.Point, p.Compared, p.Rate())
		}
		switch {
		case p.MenuMin == p.MenuMax && p.MenuMax > 0:
			fmt.Fprintf(&sb, " over menus of %d", p.MenuMin)
		case p.MenuMax > 0:
			fmt.Fprintf(&sb, " over menus of %d-%d", p.MenuMin, p.MenuMax)
		}
		if p.Acted > 0 {
			fmt.Fprintf(&sb, " (+%d where jev's own answer was taken)", p.Acted)
		}
		if p.Failed > 0 {
			fmt.Fprintf(&sb, " (%d calls unanswered)", p.Failed)
		}
		if p.NotOffered > 0 {
			fmt.Fprintf(&sb, " (%d not on the menu jev was given)", p.NotOffered)
		}
		if served := p.Served(); served != "" {
			fmt.Fprintf(&sb, "\n  served by: %s", served)
		}
		if p.Compared > 0 {
			sb.WriteString("\n  by confidence:")
			for _, b := range p.Bins {
				if b.N == 0 {
					continue
				}
				fmt.Fprintf(&sb, "  ≥%.2f %d/%d (%.2f)", b.Floor, b.Agree, b.N, float64(b.Agree)/float64(b.N))
			}
		}
		fmt.Fprintf(&sb, "\n  outcomes: accepted %d (agree %d) · rejected %d (agree %d) · pending %d\n", p.Accepted, p.AcceptedAgree, p.Rejected, p.RejectedAgree, p.Pending)
		switch bar, ok := p.SuggestedBar(target, minN); {
		case ok && bar == 0:
			// The lowest floor qualifying means the sample has no confidence
			// band where jev disagrees - which is a fact about the sample, not
			// a number to copy into a gate. Enforcing at zero gates on nothing.
			fmt.Fprintf(&sb, "  bar: 0.00 - jev agreed ≥%.0f%% of the time at EVERY floor here, down to the lowest.\n", target*100)
			fmt.Fprintf(&sb, "       Read that as \"this sample has no band where it disagrees\", not as a setting: a\n")
			fmt.Fprintf(&sb, "       bar of zero draws no line. Widen the sample before gating on it.\n")
		case ok:
			fmt.Fprintf(&sb, "  bar: %.2f - jev agreed ≥%.0f%% of the time at or above it\n", bar, target*100)
		case p.Mixed():
			fmt.Fprintf(&sb, "  bar: none - these rows came from more than one backend or model version, which is more than one sample. Re-run the shadow against one backend (CAPTAIN_SYSTEMONE_URL) and pin the model (CAPTAIN_JEV_MODEL) before reading a bar off them.\n")
		default:
			fmt.Fprintf(&sb, "  bar: none yet - no floor reaches %.0f%% agreement over %d comparisons\n", target*100, minN)
		}
	}
	return sb.String()
}

// ---- reading one backend at a time ----------------------------------------------

// ShadowUnstamped is what a row written before a backend was recorded is
// counted and filtered as. It is a name rather than a blank because the
// report offers every name it prints to --backend, and a listing that cannot
// be read back is a listing that sends an operator to an empty report.
const ShadowUnstamped = "unstamped"

// ShadowBackends counts which backend answered, over every row a calibration
// would read. It is what tells a report that there is more than one to read.
func ShadowBackends(decisions []Decision, shadows []ShadowRecord) map[string]int {
	out := map[string]int{}
	count := func(sh *Shadow) {
		if sh == nil {
			return
		}
		out[shadowBackendName(sh.Backend)]++
	}
	for i := range decisions {
		count(decisions[i].Shadow)
	}
	for i := range shadows {
		count(&shadows[i].Shadow)
	}
	return out
}

// DecisionsFromBackend and ShadowsFromBackend keep the rows one backend
// answered and drop the rest.
//
// Mixed() already stops a report quoting a bar no single configuration ever
// produced, and while a backend was something you swapped by editing an
// environment variable that was the whole job: the pooling was a mistake and
// refusing to read it was the fix. Once a sidecar runs BESIDE the primary on
// purpose (systemone_open.go), every point is mixed for good, and a report
// that can only say so is a report that never suggests a bar again - which is
// also the number an operator needs before promoting anything. So the rows
// have to be separable, not only detectable: read one backend, get that
// backend's bar, promote on it.
func DecisionsFromBackend(ds []Decision, backend string) []Decision {
	out := make([]Decision, 0, len(ds))
	for _, d := range ds {
		if d.Shadow != nil && shadowBackendName(d.Shadow.Backend) == backend {
			out = append(out, d)
		}
	}
	return out
}

// ShadowsFromBackend is DecisionsFromBackend for the standalone rows.
func ShadowsFromBackend(rows []ShadowRecord, backend string) []ShadowRecord {
	out := make([]ShadowRecord, 0, len(rows))
	for _, r := range rows {
		if shadowBackendName(r.Backend) == backend {
			out = append(out, r)
		}
	}
	return out
}

// shadowBackendName is the one name a row is both counted and filtered under.
func shadowBackendName(backend string) string {
	if backend == "" {
		return ShadowUnstamped
	}
	return backend
}
