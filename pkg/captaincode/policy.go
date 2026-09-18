package captaincode

// Policy evaluation and promotion/rollback (ROADMAP M5.4 + M5.5).
//
// The routing policy — value weights, quality thresholds, priors, the
// director ladder — is environment-overridable, so two installs with
// different CAPTAIN_VALUE_* settings write indistinguishable decisions
// for different rankings. M5.4 makes the policy itself a versioned,
// comparable artifact:
//
//   - Snapshot: a full capture of routing state (weights, tau, refs,
//     priors, director, explore rate) with a fingerprint and timestamp.
//   - Holdout: a set of task families or domains held out from policy
//     tuning, used to validate that a candidate does not regress on them.
//   - ShadowDecision: what a candidate policy WOULD have chosen, recorded
//     alongside the active decision without executing it.
//   - Canary: a bounded opt-in experiment: a candidate policy, a sample
//     rate, max tasks, max cost, start/stop, and quality comparison.
//
// M5.5 adds promotion and rollback:
//
//   - PromotionReport: compares active vs candidate across cohorts
//     (class, domain, leg), with per-cohort regression checks.
//   - Rollback: immediate return to the last accepted policy snapshot.
//
// All state is in-memory (the ledger is the persistence layer; policy
// snapshots are stored there). The controller is conservative by default:
// no canary is active until an operator starts one, and a canary that
// regresses on any holdout cohort is automatically stopped.

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PolicySnapshotVersion is the schema version for policy snapshots.
const PolicySnapshotVersion = 1

const maxSnapshots = 50

// PolicySnapshot is a full capture of routing state at a point in time.
// The Fingerprint uniquely identifies the policy; the human-readable
// Name is what `captain policy` displays.
type PolicySnapshot struct {
	Version      int                    `json:"version"`
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Fingerprint  string                 `json:"fingerprint"`
	CreatedAt    time.Time              `json:"created_at"`
	Active       bool                   `json:"active,omitempty"`
	Accepted     bool                   `json:"accepted,omitempty"`
	AcceptedAt   time.Time              `json:"accepted_at,omitempty"`
	Director     Leg                    `json:"director"`
	Weights      map[Class]ValueWeights `json:"weights"`
	Tau          map[Class]float64      `json:"tau"`
	CostRefUSD   float64                `json:"cost_ref_usd"`
	LatencyRefMs float64                `json:"latency_ref_ms"`
	ExploreRate  float64                `json:"explore_rate"`
	Priors       map[Leg]float64        `json:"priors,omitempty"`
	Notes        string                 `json:"notes,omitempty"`
}

// SnapshotPolicy captures the current routing policy into a versioned snapshot.
func SnapshotPolicy(name string) PolicySnapshot {
	priors := make(map[Leg]float64, len(AllLegs))
	for _, l := range AllLegs {
		priors[l] = QualityPrior(l)
	}
	weights := make(map[Class]ValueWeights, 3)
	tau := make(map[Class]float64, 3)
	for _, c := range []Class{ClassTrivial, ClassMedium, ClassHigh} {
		weights[c] = valueWeights(c)
		tau[c] = GoodEnough(c)
	}
	s := PolicySnapshot{
		Version:      PolicySnapshotVersion,
		Name:         name,
		Director:     Director,
		Weights:      weights,
		Tau:          tau,
		CostRefUSD:   costRef(),
		LatencyRefMs: latRef(),
		ExploreRate:  defaultExploreRate(),
		Priors:       priors,
		CreatedAt:    time.Now(),
	}
	s.Fingerprint = fingerprintSnapshot(s)
	s.ID = shortID(s.Fingerprint, 12)
	return s
}

func fingerprintSnapshot(s PolicySnapshot) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "v%d|%s|dir=%s|cr=%.4f|lr=%.0f|er=%.3f",
		s.Version, s.Name, s.Director, s.CostRefUSD, s.LatencyRefMs, s.ExploreRate)
	classes := []Class{ClassTrivial, ClassMedium, ClassHigh}
	for _, c := range classes {
		w := s.Weights[c]
		fmt.Fprintf(&sb, "|%s:q%.2f c%.2f l%.2f t%.2f", c, w.Q, w.C, w.L, s.Tau[c])
	}
	legs := make([]Leg, 0, len(s.Priors))
	for l := range s.Priors {
		legs = append(legs, l)
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i] < legs[j] })
	for _, l := range legs {
		fmt.Fprintf(&sb, "|%s=%.2f", l, s.Priors[l])
	}
	return sb.String()
}

func shortID(s string, n int) string {
	if len(s) < n {
		return s
	}
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	// %08x: a hash with leading zeros printed shorter than n and the slice
	// panicked (2026-09-17, on the fingerprint of a 15-leg registry).
	return fmt.Sprintf("pol_%08x", h)[:n]
}

func defaultExploreRate() float64 {
	if v := os.Getenv("CAPTAIN_EXPLORE_RATE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return 0.05
}

// ---- holdouts ----

// Holdout is a set of task families or domains held out from policy tuning.
// A candidate policy is NOT tuned on holdout members; they validate that
// the candidate does not regress on them.
type Holdout struct {
	Domains    []Domain `json:"domains,omitempty"`
	Classes    []Class  `json:"classes,omitempty"`
	TaskSubstr []string `json:"task_substr,omitempty"`
}

// InHoldout reports whether a decision's task matches the holdout criteria.
func (h Holdout) Matches(d Decision) bool {
	for _, dom := range h.Domains {
		if d.Domain == dom {
			return true
		}
	}
	for _, c := range h.Classes {
		if d.Class == c {
			return true
		}
	}
	for _, s := range h.TaskSubstr {
		if strings.Contains(strings.ToLower(d.Task), strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// ---- shadow decisions ----

// ShadowDecision records what a candidate policy WOULD have chosen for a task,
// alongside the active policy's actual decision. The shadow is not executed;
// it is compared post-hoc to measure divergence and quality difference.
type ShadowDecision struct {
	TaskID         string    `json:"task_id"`
	At             time.Time `json:"at"`
	ActivePolicyID string    `json:"active_policy_id"`
	ActiveLeg      Leg       `json:"active_leg"`
	ShadowPolicyID string    `json:"shadow_policy_id"`
	ShadowLeg      Leg       `json:"shadow_leg"`
	Diverged       bool      `json:"diverged"`
}

// ---- canary ----

// Canary is a bounded opt-in experiment: a fraction of tasks are routed using
// a candidate policy instead of the active one, with bounds on how many tasks
// and how much cost, and automatic stop on quality regression.
type Canary struct {
	ID                string           `json:"id"`
	CandidatePolicyID string           `json:"candidate_policy_id"`
	ActivePolicyID    string           `json:"active_policy_id"`
	SampleRate        float64          `json:"sample_rate"`
	MaxTasks          int              `json:"max_tasks"`
	MaxCostUSD        float64          `json:"max_cost_usd"`
	StartedAt         time.Time        `json:"started_at"`
	StoppedAt         time.Time        `json:"stopped_at,omitempty"`
	StopReason        string           `json:"stop_reason,omitempty"`
	TasksRouted       int              `json:"tasks_routed"`
	CostUSD           float64          `json:"cost_usd"`
	ShadowDecisions   []ShadowDecision `json:"shadow_decisions,omitempty"`
	Holdout           *Holdout         `json:"holdout,omitempty"`
}

// ShouldRoute reports whether this canary should route the next task through
// the candidate policy. Returns false when the canary is stopped, the sample
// rate rejects the task, or a bound is exceeded.
func (c *Canary) ShouldRoute(taskID string, rng *rand.Rand) bool {
	if !c.StoppedAt.IsZero() {
		return false
	}
	if c.MaxTasks > 0 && c.TasksRouted >= c.MaxTasks {
		return false
	}
	if c.SampleRate <= 0 {
		return false
	}
	return rng.Float64() < c.SampleRate
}

// Stop records a stopping reason and timestamp.
func (c *Canary) Stop(reason string) {
	if !c.StoppedAt.IsZero() {
		return
	}
	c.StoppedAt = time.Now()
	c.StopReason = reason
}

// ---- promotion report (M5.5) ----

// CohortResult is one slice of the comparison between active and candidate.
type CohortResult struct {
	Label        string  `json:"label"`
	ActiveN      int     `json:"active_n"`
	CandN        int     `json:"cand_n"`
	ActiveQ      float64 `json:"active_q"` // mean quality
	CandQ        float64 `json:"cand_q"`
	DeltaQ       float64 `json:"delta_q"`
	ActiveOk     int     `json:"active_ok"`
	CandOk       int     `json:"cand_ok"`
	ActiveOkRate float64 `json:"active_ok_rate"`
	CandOkRate   float64 `json:"cand_ok_rate"`
	Regressed    bool    `json:"regressed"`
}

// PromotionReport compares the active policy against a candidate across
// cohorts (class, domain, leg), with per-cohort regression checks.
type PromotionReport struct {
	GeneratedAt       time.Time      `json:"generated_at"`
	ActivePolicyID    string         `json:"active_policy_id"`
	CandidatePolicyID string         `json:"candidate_policy_id"`
	Cohorts           []CohortResult `json:"cohorts"`
	OverallRegressed  bool           `json:"overall_regressed"`
	Recommendation    string         `json:"recommendation"`
}

// Promote produces the comparison report. decisionsActive and decisionsCand
// are the decisions made under each policy; eventsActive and eventsCand are
// the outcomes (Events) under each. A cohort regresses when the candidate's
// mean quality or acceptance rate is worse than the active's by any margin.
func Promote(active, candidate PolicySnapshot, decisionsActive, decisionsCand []Decision, eventsActive, eventsCand []Event) PromotionReport {
	report := PromotionReport{
		GeneratedAt:       time.Now(),
		ActivePolicyID:    active.ID,
		CandidatePolicyID: candidate.ID,
	}

	qualityActive := meanQuality(decisionsActive)
	qualityCand := meanQuality(decisionsCand)
	okRateActive, okNActive, totalActive := acceptanceRate(eventsActive)
	okRateCand, okNCand, totalCand := acceptanceRate(eventsCand)

	overall := CohortResult{
		Label:        "overall",
		ActiveN:      len(decisionsActive),
		CandN:        len(decisionsCand),
		ActiveQ:      qualityActive,
		CandQ:        qualityCand,
		DeltaQ:       qualityCand - qualityActive,
		ActiveOk:     okNActive,
		CandOk:       okNCand,
		ActiveOkRate: okRateActive,
		CandOkRate:   okRateCand,
	}
	overall.Regressed = overall.DeltaQ < 0 || (totalCand > 0 && totalActive > 0 && okRateCand < okRateActive)
	report.Cohorts = append(report.Cohorts, overall)

	for _, c := range cohortByClass(decisionsActive, decisionsCand, eventsActive, eventsCand) {
		report.Cohorts = append(report.Cohorts, c)
	}
	for _, c := range cohortByDomain(decisionsActive, decisionsCand, eventsActive, eventsCand) {
		report.Cohorts = append(report.Cohorts, c)
	}

	for _, c := range report.Cohorts {
		if c.Regressed {
			report.OverallRegressed = true
			break
		}
	}
	if report.OverallRegressed {
		report.Recommendation = "reject: at least one cohort regressed"
	} else if len(decisionsCand) < 5 {
		report.Recommendation = "insufficient: fewer than 5 candidate decisions"
	} else {
		report.Recommendation = "promote: no cohort regressed"
	}
	return report
}

func meanQuality(decisions []Decision) float64 {
	var sum float64
	var n int
	for _, d := range decisions {
		for _, c := range d.Candidates {
			if c.Eligible() && c.Leg == d.Chosen {
				sum += c.Quality
				n++
				break
			}
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func acceptanceRate(events []Event) (rate float64, okN, total int) {
	for _, e := range events {
		if e.Outcome == "ok" || e.Outcome == "fail" {
			total++
			if e.Outcome == "ok" {
				okN++
			}
		}
	}
	if total == 0 {
		return 0, 0, 0
	}
	return float64(okN) / float64(total), okN, total
}

func cohortByClass(da, dc []Decision, ea, ec []Event) []CohortResult {
	classes := []Class{ClassTrivial, ClassMedium, ClassHigh}
	var out []CohortResult
	for _, c := range classes {
		daC := filterDecisionsByClass(da, c)
		dcC := filterDecisionsByClass(dc, c)
		eaC := filterEventsByClass(ea, c)
		ecC := filterEventsByClass(ec, c)
		if len(daC) == 0 && len(dcC) == 0 {
			continue
		}
		qA := meanQuality(daC)
		qC := meanQuality(dcC)
		rA, okA, totA := acceptanceRate(eaC)
		rC, okC, totC := acceptanceRate(ecC)
		cr := CohortResult{
			Label:        "class=" + string(c),
			ActiveN:      len(daC),
			CandN:        len(dcC),
			ActiveQ:      qA,
			CandQ:        qC,
			DeltaQ:       qC - qA,
			ActiveOk:     okA,
			CandOk:       okC,
			ActiveOkRate: rA,
			CandOkRate:   rC,
		}
		cr.Regressed = cr.DeltaQ < 0 || (totC > 0 && totA > 0 && rC < rA)
		out = append(out, cr)
	}
	return out
}

func cohortByDomain(da, dc []Decision, ea, ec []Event) []CohortResult {
	doms := map[Domain]bool{}
	for _, d := range da {
		doms[d.Domain] = true
	}
	for _, d := range dc {
		doms[d.Domain] = true
	}
	keys := make([]Domain, 0, len(doms))
	for d := range doms {
		keys = append(keys, d)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var out []CohortResult
	for _, dom := range keys {
		daD := filterDecisionsByDomain(da, dom)
		dcD := filterDecisionsByDomain(dc, dom)
		eaD := filterEventsByDomain(ea, dom)
		ecD := filterEventsByDomain(ec, dom)
		if len(daD) == 0 && len(dcD) == 0 {
			continue
		}
		qA := meanQuality(daD)
		qC := meanQuality(dcD)
		rA, okA, totA := acceptanceRate(eaD)
		rC, okC, totC := acceptanceRate(ecD)
		cr := CohortResult{
			Label:        "domain=" + string(dom),
			ActiveN:      len(daD),
			CandN:        len(dcD),
			ActiveQ:      qA,
			CandQ:        qC,
			DeltaQ:       qC - qA,
			ActiveOk:     okA,
			CandOk:       okC,
			ActiveOkRate: rA,
			CandOkRate:   rC,
		}
		cr.Regressed = cr.DeltaQ < 0 || (totC > 0 && totA > 0 && rC < rA)
		out = append(out, cr)
	}
	return out
}

func filterDecisionsByClass(ds []Decision, c Class) []Decision {
	var out []Decision
	for _, d := range ds {
		if d.Class == c {
			out = append(out, d)
		}
	}
	return out
}

func filterDecisionsByDomain(ds []Decision, d Domain) []Decision {
	var out []Decision
	for _, x := range ds {
		if x.Domain == d {
			out = append(out, x)
		}
	}
	return out
}

func filterEventsByClass(es []Event, c Class) []Event {
	var out []Event
	for _, e := range es {
		if e.Class == c {
			out = append(out, e)
		}
	}
	return out
}

func filterEventsByDomain(es []Event, d Domain) []Event {
	var out []Event
	for _, e := range es {
		if Domain(e.Domain) == d {
			out = append(out, e)
		}
	}
	return out
}

// ---- ledger integration ----

// RecordSnapshot stores or updates a policy snapshot in the ledger.
func (l *Ledger) RecordSnapshot(s PolicySnapshot) {
	s.Version = PolicySnapshotVersion
	for i, old := range l.Snapshots {
		if old.ID == s.ID {
			l.Snapshots[i] = s
			return
		}
	}
	l.Snapshots = append(l.Snapshots, s)
	if len(l.Snapshots) > maxSnapshots {
		l.Snapshots = l.Snapshots[len(l.Snapshots)-maxSnapshots:]
	}
}

// SnapshotFor returns the snapshot with the given ID, or nil.
func (l *Ledger) SnapshotFor(id string) *PolicySnapshot {
	for i, s := range l.Snapshots {
		if s.ID == id {
			return &l.Snapshots[i]
		}
	}
	return nil
}

// ActiveSnapshot returns the currently active policy snapshot, or nil.
func (l *Ledger) ActiveSnapshot() *PolicySnapshot {
	for i, s := range l.Snapshots {
		if s.Active {
			return &l.Snapshots[i]
		}
	}
	return nil
}

// LastAcceptedSnapshot returns the most recently accepted snapshot, or nil.
func (l *Ledger) LastAcceptedSnapshot() *PolicySnapshot {
	var best *PolicySnapshot
	for i := range l.Snapshots {
		s := &l.Snapshots[i]
		if s.Accepted {
			if best == nil || s.AcceptedAt.After(best.AcceptedAt) {
				best = s
			}
		}
	}
	return best
}

// RecentSnapshots returns snapshots newest-first.
func (l *Ledger) RecentSnapshots() []PolicySnapshot {
	out := make([]PolicySnapshot, len(l.Snapshots))
	copy(out, l.Snapshots)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ActivateSnapshot marks the given snapshot as active and all others inactive.
func (l *Ledger) ActivateSnapshot(id string) bool {
	found := false
	for i := range l.Snapshots {
		l.Snapshots[i].Active = l.Snapshots[i].ID == id
		if l.Snapshots[i].Active {
			found = true
		}
	}
	return found
}

// AcceptSnapshot marks a snapshot as accepted at the given time.
func (l *Ledger) AcceptSnapshot(id string, at time.Time) bool {
	for i := range l.Snapshots {
		if l.Snapshots[i].ID == id {
			l.Snapshots[i].Accepted = true
			l.Snapshots[i].AcceptedAt = at
			return true
		}
	}
	return false
}

// RecordCanary stores or updates a canary in the ledger.
func (l *Ledger) RecordCanary(c Canary) {
	for i, old := range l.Canaries {
		if old.ID == c.ID {
			l.Canaries[i] = c
			return
		}
	}
	l.Canaries = append(l.Canaries, c)
}

// ActiveCanary returns the currently running canary, or nil.
func (l *Ledger) ActiveCanary() *Canary {
	for i := range l.Canaries {
		if l.Canaries[i].StoppedAt.IsZero() {
			return &l.Canaries[i]
		}
	}
	return nil
}

// RecentCanaries returns canaries newest-first.
func (l *Ledger) RecentCanaries() []Canary {
	out := make([]Canary, len(l.Canaries))
	copy(out, l.Canaries)
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// ---- formatting ----

// FormatSnapshot renders one policy snapshot for `captain policy`.
func FormatSnapshot(s PolicySnapshot) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  %s %s", s.ID, s.Name)
	status := "draft"
	if s.Active {
		status = "active"
	} else if s.Accepted {
		status = "accepted"
	}
	fmt.Fprintf(&sb, " [%s]", status)
	if !s.AcceptedAt.IsZero() {
		fmt.Fprintf(&sb, " accepted %s", s.AcceptedAt.Format("2006-01-02"))
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "    director:  %s\n", s.Director)
	fmt.Fprintf(&sb, "    cost ref:  $%.4f  latency ref: %.0fms  explore: %.1f%%\n",
		s.CostRefUSD, s.LatencyRefMs, s.ExploreRate*100)
	classes := []Class{ClassTrivial, ClassMedium, ClassHigh}
	for _, c := range classes {
		w := s.Weights[c]
		fmt.Fprintf(&sb, "    %s:  q=%.2f c=%.2f l=%.2f  tau=%.1f\n", c, w.Q, w.C, w.L, s.Tau[c])
	}
	if len(s.Priors) > 0 {
		legs := make([]Leg, 0, len(s.Priors))
		for l := range s.Priors {
			legs = append(legs, l)
		}
		sort.Slice(legs, func(i, j int) bool { return legs[i] < legs[j] })
		var priors []string
		for _, l := range legs {
			priors = append(priors, fmt.Sprintf("%s=%.1f", l, s.Priors[l]))
		}
		fmt.Fprintf(&sb, "    priors:    %s\n", strings.Join(priors, " "))
	}
	if s.Notes != "" {
		fmt.Fprintf(&sb, "    notes:     %s\n", s.Notes)
	}
	return sb.String()
}

// FormatCanary renders one canary for `captain policy canary`.
func FormatCanary(c Canary) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  %s", c.ID)
	status := "active"
	if !c.StoppedAt.IsZero() {
		status = "stopped: " + c.StopReason
	}
	fmt.Fprintf(&sb, " [%s]\n", status)
	fmt.Fprintf(&sb, "    candidate: %s  active: %s\n", c.CandidatePolicyID, c.ActivePolicyID)
	fmt.Fprintf(&sb, "    sample:    %.1f%%  max: %d tasks  $%.4f\n", c.SampleRate*100, c.MaxTasks, c.MaxCostUSD)
	fmt.Fprintf(&sb, "    routed:    %d tasks  $%.4f cost\n", c.TasksRouted, c.CostUSD)
	if len(c.ShadowDecisions) > 0 {
		diverged := 0
		for _, sd := range c.ShadowDecisions {
			if sd.Diverged {
				diverged++
			}
		}
		fmt.Fprintf(&sb, "    shadows:   %d (%d diverged)\n", len(c.ShadowDecisions), diverged)
	}
	if c.Holdout != nil {
		var parts []string
		if len(c.Holdout.Domains) > 0 {
			parts = append(parts, fmt.Sprintf("domains=%v", c.Holdout.Domains))
		}
		if len(c.Holdout.Classes) > 0 {
			parts = append(parts, fmt.Sprintf("classes=%v", c.Holdout.Classes))
		}
		if len(c.Holdout.TaskSubstr) > 0 {
			parts = append(parts, fmt.Sprintf("tasks=%v", c.Holdout.TaskSubstr))
		}
		fmt.Fprintf(&sb, "    holdout:   %s\n", strings.Join(parts, " "))
	}
	return sb.String()
}

// FormatPromotionReport renders a promotion report.
func FormatPromotionReport(r PromotionReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "promotion report: %s vs %s\n", r.ActivePolicyID, r.CandidatePolicyID)
	fmt.Fprintf(&sb, "  recommendation: %s\n\n", r.Recommendation)
	fmt.Fprintf(&sb, "  %-20s  %6s  %6s  %8s  %6s  %6s  %s\n",
		"cohort", "actN", "candN", "ΔQ", "actOk%", "candOk%", "regressed")
	for _, c := range r.Cohorts {
		fmt.Fprintf(&sb, "  %-20s  %6d  %6d  %8.2f  %5.1f%%  %5.1f%%  %v\n",
			c.Label, c.ActiveN, c.CandN, c.DeltaQ,
			c.ActiveOkRate*100, c.CandOkRate*100, c.Regressed)
	}
	return sb.String()
}
