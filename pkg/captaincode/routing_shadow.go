package captaincode

// The offline shadow router, P7 step 3 (ROADMAP build queue, owner decision
// dq-261117-f0191b). `captain why --shadow <file>` trains a routing policy on
// one slice of an exported dataset and reports what its picks did on the
// other slice, so a candidate route can be compared with what the director
// actually did.
//
// The split is the date split `--score` uses: the scoring slice trains, the
// held-out slice is evaluated. Nothing the held-out slice carries reaches the
// model: a leg that appears only there cannot be picked, which is what keeps
// the evaluation honest.
//
// Three policies run on both slices:
//
//   - director: the leg the director recorded. It agrees with itself, so it
//     measures the harness rather than a router.
//   - prior: the hand priors ranked for the task's domain (QualityPriorFor),
//     over the legs the training slice ran.
//   - learned: the training slice's own record per domain, the leg with the
//     better settled acceptance rate among legs carrying at least
//     ShadowMinLabeled settled tasks. A domain that cannot fill that bar
//     falls back to the priors, and the run counts those fallbacks.
//
// Four measures, each with what it can honestly mean:
//
//   - Agreement, coverage and position: which picks the director recorded,
//     which picks ran on that task, and whether they ran first or only after
//     another attempt. A pick that never ran has no outcome and is
//     uncovered, never a failure.
//   - Verdicts: one task carries one verdict for the whole task, so only
//     picks that ran first are scored on it. A pick that ran later shares
//     that verdict with a repair chain and is counted for coverage alone.
//   - Preference pairs: inside one task, a leg whose attempt finished ok is
//     preferred to a leg that failed and never finished. A ranking gets
//     credit for putting the preferred leg first. These pairs are observed
//     work, so they need no counterfactual.
//   - Head to head: tasks where the policy and the director chose different
//     legs and both ran. Each side's own attempt outcome counts.
//
// A claim needs ShadowClaimMin head-to-head tasks; below that the report
// says so and claims nothing. The shadow writes no file, calls no model,
// spends nothing and changes no route.

import (
	"sort"
	"strconv"
	"time"
)

const (
	// The three policies, as the report prints them.
	ShadowPolicyDirector = "director"
	ShadowPolicyPrior    = "prior"
	ShadowPolicyLearned  = "learned"

	// ShadowMinLabeled is the settled task count a leg needs in a domain
	// before the learned policy will rank it there. Under it the rate is
	// one run's luck.
	ShadowMinLabeled = 3

	// ShadowClaimMin is the head-to-head task count below which the report
	// makes no claim.
	ShadowClaimMin = 10
)

// Attempt outcomes, as the routing journal writes them.
const (
	shadowOutcomeOK   = "ok"
	shadowOutcomeFail = "fail"
)

// shadowTask is one task's rows, keyed the way the score joins them.
type shadowTask struct {
	key  string
	rows []DatasetRow
}

func shadowTasks(rows []DatasetRow) []shadowTask {
	byKey := map[string][]DatasetRow{}
	var order []string
	for _, row := range rows {
		k := taskKey(row)
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], row)
	}
	out := make([]shadowTask, 0, len(order))
	for _, k := range order {
		out = append(out, shadowTask{key: k, rows: byKey[k]})
	}
	return out
}

type shadowLegStat struct {
	first    int
	labeled  int
	accepted int
	cost     float64
	hasCost  bool
}

// ShadowRouter is a policy trained on one slice. It holds only that slice's
// legs and only that slice's records.
type ShadowRouter struct {
	legs        []Leg
	domains     map[string]map[Leg]*shadowLegStat
	trainedTask int
	domainSet   map[string]bool
}

// NewShadowRouter trains on one slice. A task with no leg teaches nothing
// and is counted only in the task total.
func NewShadowRouter(train []DatasetRow) *ShadowRouter {
	r := &ShadowRouter{
		domains:   map[string]map[Leg]*shadowLegStat{},
		domainSet: map[string]bool{},
	}
	seen := map[Leg]bool{}
	for _, task := range shadowTasks(train) {
		r.trainedTask++
		first := shadowFirstLeg(task.rows)
		if first == "" {
			continue
		}
		if !seen[first] {
			seen[first] = true
			r.legs = append(r.legs, first)
		}
		d := shadowDomain(task.rows)
		r.domainSet[d] = true
		stats := r.domains[d]
		if stats == nil {
			stats = map[Leg]*shadowLegStat{}
			r.domains[d] = stats
		}
		st := stats[first]
		if st == nil {
			st = &shadowLegStat{}
			stats[first] = st
		}
		st.first++
		label, labeled := taskVerdict(task.rows)
		if !labeled {
			continue
		}
		st.labeled++
		if label == AcceptanceAccepted {
			st.accepted++
			if cost, ok := shadowLegCost(task.rows, first); ok {
				st.cost += cost
				st.hasCost = true
			}
		}
	}
	sort.Slice(r.legs, func(i, j int) bool { return r.legs[i] < r.legs[j] })
	return r
}

// Legs is the set the router may pick from, sorted.
func (r *ShadowRouter) Legs() []Leg { return append([]Leg(nil), r.legs...) }

// Trained reports the tasks the router saw, the distinct legs it may pick,
// and the domains it met. A task that carried no leg taught it a domain and
// nothing else.
func (r *ShadowRouter) Trained() (tasks, legs, domains int) {
	for d := range r.domainSet {
		if d != "" {
			domains++
		}
	}
	return r.trainedTask, len(r.legs), domains
}

// Predict answers with a policy's pick for one task. The bool says the
// learned policy fell back to the priors on this task. An empty leg means
// the router had nothing to pick: no leg in training, or no recorded choice.
func (r *ShadowRouter) Predict(policy string, rows []DatasetRow) (Leg, bool) {
	d := shadowDomain(rows)
	switch policy {
	case ShadowPolicyDirector:
		if pick := shadowChosen(rows); pick != "" {
			return pick, false
		}
		if len(r.legs) == 0 {
			return "", false
		}
		return r.priorLeg(d), false
	case ShadowPolicyLearned:
		if len(r.legs) == 0 {
			return "", false
		}
		if leg := r.learnedLeg(d); leg != "" {
			return leg, false
		}
		return r.priorLeg(d), true
	default:
		if len(r.legs) == 0 {
			return "", false
		}
		return r.priorLeg(d), false
	}
}

// Rank is the policy's order over the trained legs for a domain, best first.
// The director records one leg, not an order, so it has no ranking.
func (r *ShadowRouter) Rank(policy, domain string) []Leg {
	switch policy {
	case ShadowPolicyPrior:
		return r.priorRank(domain)
	case ShadowPolicyLearned:
		return r.learnedRank(domain)
	default:
		return nil
	}
}

// learnedLeg is the domain's best-settled leg, empty when no leg in that
// domain reached ShadowMinLabeled.
func (r *ShadowRouter) learnedLeg(domain string) Leg {
	rank := r.learnedRank(domain)
	if len(rank) == 0 {
		return ""
	}
	best := rank[0]
	st := r.domains[domain][best]
	if st == nil || st.labeled < ShadowMinLabeled {
		return ""
	}
	return best
}

// learnedRank orders the trained legs: an eligible leg's settled acceptance
// rate first, then its cost per accepted task, then the hand prior, then the
// name. A leg the training slice did not settle often enough ranks below
// every eligible one.
func (r *ShadowRouter) learnedRank(domain string) []Leg {
	stats := r.domains[domain]
	out := r.Legs()
	sort.SliceStable(out, func(i, j int) bool {
		return shadowLearnBetter(shadowStat(stats, out[i]), out[i], shadowStat(stats, out[j]), out[j], domain)
	})
	return out
}

func shadowStat(stats map[Leg]*shadowLegStat, leg Leg) *shadowLegStat {
	if st := stats[leg]; st != nil {
		return st
	}
	return &shadowLegStat{}
}

func shadowLearnBetter(a *shadowLegStat, al Leg, b *shadowLegStat, bl Leg, domain string) bool {
	ar, br := shadowRankRate(a), shadowRankRate(b)
	if ar != br {
		return ar > br
	}
	ac, aok := shadowRateCost(a)
	bc, bok := shadowRateCost(b)
	if aok != bok {
		return aok
	}
	if aok && ac != bc {
		return ac < bc
	}
	ap, bp := QualityPriorFor(al, Domain(domain)), QualityPriorFor(bl, Domain(domain))
	if ap != bp {
		return ap > bp
	}
	return al < bl
}

// shadowRankRate is what the learned ranking compares: an eligible leg's
// acceptance rate, and -1 for a leg the training slice settled too rarely
// to rank.
func shadowRankRate(s *shadowLegStat) float64 {
	if s.labeled < ShadowMinLabeled {
		return -1
	}
	return float64(s.accepted) / float64(s.labeled)
}

// shadowRateCost is a leg's cost per task it got accepted. A leg with no
// measured cost reports false and ranks below one that has it.
func shadowRateCost(s *shadowLegStat) (float64, bool) {
	if !s.hasCost || s.accepted == 0 {
		return 0, false
	}
	return s.cost / float64(s.accepted), true
}

// priorRank orders the trained legs by their hand prior for the domain,
// best first, ties broken by the name, so the answer never moves.
func (r *ShadowRouter) priorRank(domain string) []Leg {
	out := r.Legs()
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := QualityPriorFor(out[i], Domain(domain)), QualityPriorFor(out[j], Domain(domain))
		if pi != pj {
			return pi > pj
		}
		return out[i] < out[j]
	})
	return out
}

// priorLeg is the head of the prior ranking.
func (r *ShadowRouter) priorLeg(domain string) Leg {
	rank := r.priorRank(domain)
	if len(rank) == 0 {
		return ""
	}
	return rank[0]
}

// shadowFirstLeg is the leg a task first ran on: the lowest attempt number,
// the earlier row winning a row with the same number. It falls back to a
// recorded choice when nothing ran yet.
func shadowFirstLeg(rows []DatasetRow) Leg {
	var best DatasetRow
	found := false
	for _, row := range rows {
		if row.Leg == "" || row.RunOutcome == "" {
			continue
		}
		if !found || row.Attempt < best.Attempt || (row.Attempt == best.Attempt && row.At.Before(best.At)) {
			best, found = row, true
		}
	}
	if found {
		return best.Leg
	}
	return shadowChosen(rows)
}

// shadowChosen is the director's answer for a task: its first recorded
// choice, else the leg it first ran.
func shadowChosen(rows []DatasetRow) Leg {
	for _, row := range rows {
		if row.Chosen != "" {
			return row.Chosen
		}
	}
	for _, row := range rows {
		if row.Leg != "" {
			return row.Leg
		}
	}
	return ""
}

// shadowDomain is the task's domain, the first non-empty one. The empty
// domain is its own bucket: a dataset that recorded no domain still learns.
func shadowDomain(rows []DatasetRow) string {
	for _, row := range rows {
		if row.Domain != "" {
			return string(row.Domain)
		}
	}
	return ""
}

// shadowLegCost sums one leg's own attempt costs on a task. It reports
// false when that leg carries no measured or estimated cost, so an unknown
// cost never reads as zero.
func shadowLegCost(rows []DatasetRow, leg Leg) (float64, bool) {
	var sum float64
	var seen bool
	for _, row := range rows {
		if row.Leg != leg {
			continue
		}
		if row.CostStatus == UsageMeasured || row.CostStatus == UsageEstimated {
			sum += row.Cost
			seen = true
		}
	}
	return sum, seen
}

// legRan reports where a leg ran on a task. First means its lowest attempt
// number is also the task's lowest, so a leg that only ran after another
// one counts as later. Any means it completed at least one run there.
func legRan(rows []DatasetRow, leg Leg) (first, any bool) {
	if leg == "" {
		return false, false
	}
	ownLowest, lowest := 0, 0
	for _, row := range rows {
		if row.RunOutcome == "" {
			continue
		}
		if lowest == 0 || row.Attempt < lowest {
			lowest = row.Attempt
		}
		if row.Leg != leg {
			continue
		}
		if ownLowest == 0 || row.Attempt < ownLowest {
			ownLowest = row.Attempt
		}
	}
	if ownLowest == 0 {
		return false, false
	}
	return ownLowest == lowest, true
}

// shadowTaskOutcomes is one task's attempt verdict per leg: ok when any of
// that leg's attempts finished, fail when it ran and never did. A leg whose
// attempts were all rate-limited gets no entry: a stop is not an opinion.
func shadowTaskOutcomes(rows []DatasetRow) map[Leg]string {
	out := map[Leg]string{}
	for _, row := range rows {
		if row.Leg == "" || row.RunOutcome == "" {
			continue
		}
		switch row.RunOutcome {
		case shadowOutcomeOK:
			out[row.Leg] = shadowOutcomeOK
		case shadowOutcomeFail:
			if out[row.Leg] != shadowOutcomeOK {
				out[row.Leg] = shadowOutcomeFail
			}
		}
	}
	return out
}

type shadowPair struct {
	better Leg
	worse  Leg
}

// shadowPairs are the observed preferences in one task: every leg that
// finished against every leg that failed and never held.
func shadowPairs(rows []DatasetRow) []shadowPair {
	outcomes := shadowTaskOutcomes(rows)
	var legs []Leg
	for leg := range outcomes {
		legs = append(legs, leg)
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i] < legs[j] })
	var out []shadowPair
	for i := range legs {
		for j := i + 1; j < len(legs); j++ {
			a, b := legs[i], legs[j]
			switch {
			case outcomes[a] == shadowOutcomeOK && outcomes[b] == shadowOutcomeFail:
				out = append(out, shadowPair{better: a, worse: b})
			case outcomes[b] == shadowOutcomeOK && outcomes[a] == shadowOutcomeFail:
				out = append(out, shadowPair{better: b, worse: a})
			}
		}
	}
	return out
}

// ShadowPairRank is a ranking scored against a slice's observed
// preferences: how many pairs its order explains, and how many it gets
// backwards. Pairs naming a leg the policy does not know are unrated.
type ShadowPairRank struct {
	Judged     int
	Concordant int
	Discordant int
	Unrated    int
	Accuracy   float64
}

// String is the accuracy cell the report prints: a dash when no pair could
// be judged, so a thin slice never looks like a bad score.
func (r ShadowPairRank) String() string {
	if r.Judged == 0 {
		return "-"
	}
	return strconv.FormatFloat(r.Accuracy*100, 'f', 1, 64) + "%"
}

// ShadowPolicyRun is one policy over one slice.
type ShadowPolicyRun struct {
	Policy     string
	Tasks      int
	Agreement  int // picks the director recorded the same way
	Covered    int // picks that ran on that task
	First      int // ... and ran as a first attempt
	Later      int // ... and ran only after another attempt
	Uncovered  int // picks that never ran on that task
	NoPick     int // tasks the policy had no leg for
	Labeled    int // first-attempt picks with a verdict on the task
	Accepted   int
	Acceptance float64 // accepted / labeled, 0 when nothing is labeled
	Cost       float64 // the pick's own attempt costs on accepted tasks
	HasCost    bool
	CostPer    float64
	Fallbacks  int // learned picks that fell back to the priors
	// Attempts and AttemptsOk count the pick's own runs over every task it
	// covered, first attempt or later: what the model liked in fact, before
	// any verdict is attached.
	Attempts   int
	AttemptsOk int
	Rank       ShadowPairRank
}

// ShadowCompare is one policy against the director on the tasks where the
// two chose different legs and both ran: same task, two attempts, each side
// scored on its own outcome.
type ShadowCompare struct {
	Policy     string
	Tasks      int
	PolicyOk   int
	DirectorOk int
}

func (c ShadowCompare) Ahead() bool { return c.PolicyOk > c.DirectorOk }

// ShadowSliceEval is every policy over one slice of the dataset.
type ShadowSliceEval struct {
	Name     string
	Tasks    int
	Runs     []ShadowPolicyRun
	Compares []ShadowCompare
}

// ShadowReport is a full shadow run: what the scoring slice trained, what
// both slices measured, and the claim the held-out slice supports.
type ShadowReport struct {
	Cutoff time.Time
	// What the scoring slice gave the model: its tasks, the legged part of
	// them and the domains it recorded. The held-out slice never adds here.
	TrainedTasks   int
	TrainedLegs    int
	TrainedDomains int
	Train          ShadowSliceEval
	HeldOut        ShadowSliceEval
	Claim          string
}

// shadowPolicies is the fixed order the report prints.
func shadowPolicies() []string {
	return []string{ShadowPolicyDirector, ShadowPolicyPrior, ShadowPolicyLearned}
}

// EvaluateShadow trains on the scoring slice and reports both slices. The
// held-out slice never reaches the model.
func EvaluateShadow(train, heldOut []DatasetRow, cutoff time.Time) ShadowReport {
	router := NewShadowRouter(train)
	trained, legged, domains := router.Trained()
	report := ShadowReport{
		Cutoff:         cutoff,
		TrainedTasks:   trained,
		TrainedLegs:    legged,
		TrainedDomains: domains,
		Train:          shadowSlice(router, "training", train),
		HeldOut:        shadowSlice(router, "held-out", heldOut),
	}
	report.Claim = shadowClaim(report.HeldOut)
	return report
}

// shadowChoice is one policy's answer for one task, with the reason it
// settled there.
type shadowChoice struct {
	leg      Leg
	fellBack bool
}

func shadowSlice(router *ShadowRouter, name string, rows []DatasetRow) ShadowSliceEval {
	tasks := shadowTasks(rows)
	eval := ShadowSliceEval{Name: name, Tasks: len(tasks)}
	comparePolicies := []string{ShadowPolicyPrior, ShadowPolicyLearned}

	choices := make([]map[string]shadowChoice, len(tasks))
	for i, task := range tasks {
		choice := map[string]shadowChoice{}
		for _, policy := range shadowPolicies() {
			leg, fellBack := router.Predict(policy, task.rows)
			choice[policy] = shadowChoice{leg: leg, fellBack: fellBack}
		}
		choices[i] = choice
	}

	for _, policy := range shadowPolicies() {
		run := ShadowPolicyRun{Policy: policy, Tasks: len(tasks)}
		for i, task := range tasks {
			pick := choices[i][policy]
			if pick.leg == "" {
				run.NoPick++
				continue
			}
			if pick.fellBack {
				run.Fallbacks++
			}
			director := choices[i][ShadowPolicyDirector].leg
			if director != "" && pick.leg == director {
				run.Agreement++
			}
			first, any := legRan(task.rows, pick.leg)
			if !any {
				run.Uncovered++
				continue
			}
			run.Covered++
			for _, row := range task.rows {
				if row.Leg != pick.leg || row.RunOutcome == "" {
					continue
				}
				run.Attempts++
				if row.RunOutcome == shadowOutcomeOK {
					run.AttemptsOk++
				}
			}
			if !first {
				run.Later++
				continue
			}
			run.First++
			label, labeled := taskVerdict(task.rows)
			if !labeled {
				continue
			}
			run.Labeled++
			if label != AcceptanceAccepted {
				continue
			}
			run.Accepted++
			if cost, ok := shadowLegCost(task.rows, pick.leg); ok {
				run.Cost += cost
				run.HasCost = true
			}
		}
		for _, task := range tasks {
			// Each task scores the policy's ranking for its own domain.
			run.Rank.add(router.Rank(policy, shadowDomain(task.rows)), shadowPairs(task.rows))
		}
		run.Rank.done()
		if run.Labeled > 0 {
			run.Acceptance = float64(run.Accepted) / float64(run.Labeled)
		}
		if run.Accepted > 0 && run.HasCost {
			run.CostPer = run.Cost / float64(run.Accepted)
		}
		eval.Runs = append(eval.Runs, run)
	}

	for _, policy := range comparePolicies {
		c := ShadowCompare{Policy: policy}
		for i, task := range tasks {
			pick := choices[i][policy].leg
			director := choices[i][ShadowPolicyDirector].leg
			if pick == "" || director == "" || pick == director {
				continue
			}
			outcomes := shadowTaskOutcomes(task.rows)
			if outcomes[pick] == "" || outcomes[director] == "" {
				continue
			}
			c.Tasks++
			if outcomes[pick] == shadowOutcomeOK {
				c.PolicyOk++
			}
			if outcomes[director] == shadowOutcomeOK {
				c.DirectorOk++
			}
		}
		eval.Compares = append(eval.Compares, c)
	}
	return eval
}

// add scores one task's observed preferences against one policy's ranking
// for that task's domain, and accumulates them until done sets the rate.
func (r *ShadowPairRank) add(rank []Leg, pairs []shadowPair) {
	pos := map[Leg]int{}
	for i, leg := range rank {
		pos[leg] = i
	}
	for _, p := range pairs {
		ai, aok := pos[p.better]
		bi, bok := pos[p.worse]
		if !aok || !bok {
			r.Unrated++
			continue
		}
		r.Judged++
		if ai < bi {
			r.Concordant++
		} else {
			r.Discordant++
		}
	}
}

func (r *ShadowPairRank) done() {
	if r.Judged > 0 {
		r.Accuracy = float64(r.Concordant) / float64(r.Judged)
	} else {
		r.Accuracy = 0
	}
}

// shadowClaim is what the held-out slice supports, in one sentence. It says
// no more than the counts behind it: raw totals, no test of significance.
func shadowClaim(heldOut ShadowSliceEval) string {
	var said int
	out := ""
	for _, c := range heldOut.Compares {
		if c.Tasks < ShadowClaimMin {
			continue
		}
		verdict := "level"
		switch {
		case c.PolicyOk > c.DirectorOk:
			verdict = "ahead"
		case c.PolicyOk < c.DirectorOk:
			verdict = "behind"
		}
		if said > 0 {
			out += "; "
		}
		out += c.Policy + ": " + strconv.Itoa(c.PolicyOk) + " ok against the director's " +
			strconv.Itoa(c.DirectorOk) + " on " + strconv.Itoa(c.Tasks) + " tasks - " + verdict
		said++
	}
	if said == 0 {
		best := 0
		for _, c := range heldOut.Compares {
			if c.Tasks > best {
				best = c.Tasks
			}
		}
		return "no claim: " + strconv.Itoa(best) + " held-out task(s) where the shadow and the director chose different legs and both ran, " +
			strconv.Itoa(ShadowClaimMin) + " needed"
	}
	return out + " (raw counts, no significance test)"
}
