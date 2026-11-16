package captaincode

// Offline scoring of the routing dataset, step 2 of P7 (ROADMAP build
// queue, owner decision dq-261116-a61645): split the exported dataset by
// date, then score the director's actual routing and the hand priors on
// acceptance rate and cost per accepted task. A read over rows that
// already exist: no model call, no routing change, no spend.

import (
	"sort"
	"time"
)

// DatasetCutoffFraction is the default split: the first 70% of the
// observed time span is the scoring split, the rest is held out. A held-out
// window needs enough days to be meaningful, so the default is a fraction
// of the span, not a fixed day count.
const DatasetCutoffFraction = 0.7

// DatasetSplit is one side of a date split.
type DatasetSplit struct {
	Name  string
	Rows  []DatasetRow
	From  time.Time
	Until time.Time
}

// DatasetLegScore is one leg's slice of a split: what the director sent it
// and what came of it.
type DatasetLegScore struct {
	Leg            Leg
	Attempts       int
	Tasks          int
	Labeled        int
	Accepted       int
	Rejected       int
	AcceptanceRate float64 // accepted / labeled; 0 when nothing is labeled
	CostUSD        float64 // attempt cost on tasks that ended accepted
	CostPerUSD     float64 // CostUSD / accepted; 0 when nothing is accepted
	HasCost        bool
}

// DatasetSplitScore is the scored summary of one split: the director's
// actual routing overall, per leg, and the hand priors' agreement with the
// observed acceptance order.
type DatasetSplitScore struct {
	Split          DatasetSplit
	Tasks          int
	Labeled        int
	Accepted       int
	Rejected       int
	AcceptanceRate float64
	CostUSD        float64
	CostPerUSD     float64
	HasCost        bool
	Legs           []DatasetLegScore
}

// taskKey groups rows of one task. TaskID is the key when the ledger held
// it; the prompt digest stands in when it did not, so a task never splits
// in two over a missing id.
func taskKey(r DatasetRow) string {
	if r.TaskID != "" {
		return r.TaskID
	}
	return r.PromptSHA
}

// taskVerdict reads a task's final label from its rows: the last settled
// row with a label wins, because repairs and re-asks restate the base.
func taskVerdict(rows []DatasetRow) (AcceptanceStatus, bool) {
	var found AcceptanceStatus
	for _, r := range rows {
		if r.Label != "" {
			found = r.Label
		}
	}
	return found, found != ""
}

// taskCost sums a task's attempt costs. Measured and estimated attempt
// costs are summed; a task with no attempt cost falls back to the ledger's
// billed task total, so the two are never mixed (that would double count).
func taskCost(rows []DatasetRow) (float64, bool) {
	var sum float64
	var seen bool
	for _, r := range rows {
		if r.CostStatus == UsageMeasured || r.CostStatus == UsageEstimated {
			sum += r.Cost
			seen = true
		}
	}
	if seen {
		return sum, true
	}
	for _, r := range rows {
		if r.TaskBilled {
			return r.TaskCost, true
		}
	}
	return 0, false
}

// DefaultDatasetCutoff picks the split date that puts the first
// DatasetCutoffFraction of the observed time span before it. A span of one
// day or less cannot be split and returns the zero time.
func DefaultDatasetCutoff(rows []DatasetRow) time.Time {
	if len(rows) == 0 {
		return time.Time{}
	}
	min, max := rows[0].At, rows[0].At
	for _, r := range rows[1:] {
		if r.At.Before(min) {
			min = r.At
		}
		if r.At.After(max) {
			max = r.At
		}
	}
	span := max.Sub(min)
	if span <= 24*time.Hour {
		return time.Time{}
	}
	return min.Add(time.Duration(float64(span) * DatasetCutoffFraction))
}

// SplitDatasetByDate splits rows around a cutoff: rows at or after the
// cutoff are held out. Rows with no timestamp (none in a real export) sit
// before it, with the decisions they came from.
func SplitDatasetByDate(rows []DatasetRow, cutoff time.Time) (DatasetSplit, DatasetSplit) {
	before := DatasetSplit{Name: "scoring"}
	after := DatasetSplit{Name: "held-out"}
	for _, r := range rows {
		if !cutoff.IsZero() && !r.At.Before(cutoff) {
			after.Rows = append(after.Rows, r)
		} else {
			before.Rows = append(before.Rows, r)
		}
	}
	nameSpan(&before)
	nameSpan(&after)
	return before, after
}

func nameSpan(s *DatasetSplit) {
	if len(s.Rows) == 0 {
		return
	}
	s.From, s.Until = s.Rows[0].At, s.Rows[0].At
	for _, r := range s.Rows[1:] {
		if r.At.Before(s.From) {
			s.From = r.At
		}
		if r.At.After(s.Until) {
			s.Until = r.At
		}
	}
}

// ScoreDatasetSplit scores one split. Per leg: attempts, tasks, the
// labeled verdicts and the acceptance rate, plus the attempt cost spent on
// tasks that ended accepted and its per-accepted-task figure. Overall: the
// director's actual routing across every leg in the split.
func ScoreDatasetSplit(s DatasetSplit) DatasetSplitScore {
	score := DatasetSplitScore{Split: s}
	byTask := map[string][]DatasetRow{}
	var order []string
	for _, r := range s.Rows {
		k := taskKey(r)
		if _, ok := byTask[k]; !ok {
			order = append(order, k)
		}
		byTask[k] = append(byTask[k], r)
	}
	legTasks := map[Leg]map[string]bool{}
	legAttempts := map[Leg]int{}
	for _, r := range s.Rows {
		if r.Leg != "" {
			legAttempts[r.Leg]++
			if legTasks[r.Leg] == nil {
				legTasks[r.Leg] = map[string]bool{}
			}
			legTasks[r.Leg][taskKey(r)] = true
		}
	}
	legAccepted := map[Leg]int{}
	legRejected := map[Leg]int{}
	legCost := map[Leg]float64{}
	legHasCost := map[Leg]bool{}
	acceptedTask := map[string]bool{}
	for _, k := range order {
		rows := byTask[k]
		label, labeled := taskVerdict(rows)
		if labeled {
			score.Labeled++
			switch label {
			case AcceptanceAccepted:
				score.Accepted++
				legAccepted[lastLeg(rows)]++
				acceptedTask[k] = true
			case AcceptanceRejected:
				score.Rejected++
				legRejected[lastLeg(rows)]++
			}
		}
		cost, hasCost := taskCost(rows)
		if hasCost {
			score.HasCost = true
			if acceptedTask[k] {
				score.CostUSD += cost
			}
		}
	}
	// A leg's own cost is the sum of its attempt costs on tasks that ended
	// accepted - a repair chain that switched legs charges each leg only
	// its own attempts.
	for _, r := range s.Rows {
		if r.Leg == "" || !acceptedTask[taskKey(r)] {
			continue
		}
		if r.CostStatus == UsageMeasured || r.CostStatus == UsageEstimated {
			legCost[r.Leg] += r.Cost
			legHasCost[r.Leg] = true
		}
	}
	score.Tasks = len(order)
	if score.Labeled > 0 {
		score.AcceptanceRate = float64(score.Accepted) / float64(score.Labeled)
	}
	if score.Accepted > 0 && score.HasCost {
		score.CostPerUSD = score.CostUSD / float64(score.Accepted)
	}
	for leg := range legTasks {
		ls := DatasetLegScore{
			Leg:      leg,
			Attempts: legAttempts[leg],
			Tasks:    len(legTasks[leg]),
			Accepted: legAccepted[leg],
			Rejected: legRejected[leg],
			CostUSD:  legCost[leg],
			HasCost:  legHasCost[leg],
		}
		ls.Labeled = ls.Accepted + ls.Rejected
		if ls.Labeled > 0 {
			ls.AcceptanceRate = float64(ls.Accepted) / float64(ls.Labeled)
		}
		if ls.Accepted > 0 && ls.HasCost {
			ls.CostPerUSD = ls.CostUSD / float64(ls.Accepted)
		}
		score.Legs = append(score.Legs, ls)
	}
	sort.Slice(score.Legs, func(i, j int) bool {
		if score.Legs[i].AcceptanceRate != score.Legs[j].AcceptanceRate {
			return score.Legs[i].AcceptanceRate > score.Legs[j].AcceptanceRate
		}
		return score.Legs[i].Leg < score.Legs[j].Leg
	})
	return score
}

// lastLeg is the leg of a task's final attempt: the row with the highest
// attempt number, ties by time.
func lastLeg(rows []DatasetRow) Leg {
	var best DatasetRow
	var found bool
	for _, r := range rows {
		if r.Leg == "" {
			continue
		}
		if !found || r.Attempt > best.Attempt || (r.Attempt == best.Attempt && r.At.After(best.At)) {
			best = r
			found = true
		}
	}
	if !found {
		return best.Chosen
	}
	return best.Leg
}

// PriorConcordance scores the hand priors against the observed acceptance
// order: over every pair of legs that both carry verdicts and both have a
// distinct prior, a pair is concordant when the higher prior also has the
// higher acceptance rate. Ties in acceptance count neither way.
func PriorConcordance(legs []DatasetLegScore, prior func(Leg) float64) (concordant, discordant, judgedPairs int) {
	var scored []DatasetLegScore
	for _, l := range legs {
		if l.Labeled > 0 && prior(l.Leg) > 0 {
			scored = append(scored, l)
		}
	}
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			a, b := scored[i], scored[j]
			pa, pb := prior(a.Leg), prior(b.Leg)
			if pa == pb {
				continue
			}
			judgedPairs++
			if a.AcceptanceRate == b.AcceptanceRate {
				judgedPairs--
				continue
			}
			higherPrior, lowerPrior := a, b
			if pb > pa {
				higherPrior, lowerPrior = b, a
			}
			if higherPrior.AcceptanceRate > lowerPrior.AcceptanceRate {
				concordant++
			} else {
				discordant++
			}
		}
	}
	return concordant, discordant, judgedPairs
}
