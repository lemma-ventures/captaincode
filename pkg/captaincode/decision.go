package captaincode

// Decision evidence (ROADMAP M2.1). A routing choice used to leave behind one
// sentence of rationale, which is enough to read and not enough to argue with:
// it named the winner but never the field, never what ruled a leg out, never
// how much evidence stood behind the quality number, and never which set of
// tunables produced the ranking. Two installs with different CAPTAIN_VALUE_*
// settings wrote indistinguishable rationales for different decisions.
//
// A Decision is the whole record: the candidates with their terms, the
// exclusions with their reasons, the policy that ranked them, and the task
// identity (M1.2) the resulting charges hang off - so `captain why` can show
// what was chosen, against what, and what it then cost.

import (
	"fmt"
	"time"
)

// DecisionVersion is the record schema. A reader that does not recognise a
// version refuses the record rather than mis-reporting it.
const DecisionVersion = 1

const maxDecisions = 200

// Selection paths. Which mechanism actually chose the leg - a value ranking
// and a user's forced leg are both "routing", and reporting them the same way
// is how a fast-path decision gets mistaken for a considered one.
const (
	PathValue    = "value"           // ranked on the cheap path, τ and all
	PathLadder   = "ladder"          // value routing off or nothing cleared τ: hand-written FastLadder
	PathDirector = "director"        // tier 2, an LLM plan chose it
	PathForced   = "forced"          // the user named the leg
	PathNamedSeq = "named-sequence"  // the user named legs in order; compiled to a workflow
	PathWorkflow = "workflow-intent" // a workflow control word, not a routing choice
	PathReroute  = "reroute"         // a provider fault moved the work
)

// Policy is the tunable state a ranking was produced under. Recorded with the
// decision because the weights, the threshold and the normalisation references
// are all environment-overridable: a ranking is only reproducible alongside them.
type Policy struct {
	Name         string       `json:"name"`
	Version      string       `json:"version"` // fingerprint of every field below
	Weights      ValueWeights `json:"weights"`
	Tau          float64      `json:"tau"`
	CostRefUSD   float64      `json:"cost_ref_usd"`
	LatencyRefMs float64      `json:"latency_ref_ms"`
	EstTokens    int          `json:"est_tokens"`
	ExploreRate  float64      `json:"explore_rate"`
}

// PolicyFor snapshots the routing policy in effect for a class.
func PolicyFor(c Class, estTokens int, exploreRate float64) Policy {
	p := Policy{
		Name:         "value",
		Weights:      valueWeights(c),
		Tau:          GoodEnough(c),
		CostRefUSD:   costRef(),
		LatencyRefMs: latRef(),
		EstTokens:    estTokens,
		ExploreRate:  exploreRate,
	}
	p.Version = fmt.Sprintf("value/%d q%.2f c%.2f l%.2f tau%.2f cref%.2f lref%.0f tok%d eps%.2f",
		DecisionVersion, p.Weights.Q, p.Weights.C, p.Weights.L, p.Tau, p.CostRefUSD, p.LatencyRefMs, p.EstTokens, p.ExploreRate)
	return p
}

// Decision is one routing choice with the evidence that produced it.
type Decision struct {
	Version    int       `json:"version"`
	At         time.Time `json:"at"`
	TaskID     string    `json:"task_id,omitempty"` // joins to the M1.2 charge tree
	Task       string    `json:"task"`
	Class      Class     `json:"class"`
	Domain     Domain    `json:"domain,omitempty"`
	Path       string    `json:"path"`
	Chosen     Leg       `json:"chosen"`
	Rationale  string    `json:"rationale,omitempty"`
	NeedVision bool      `json:"need_vision,omitempty"`
	DecidedMs  int64     `json:"decided_ms,omitempty"`

	// Exploration is recorded as what it is: a deliberate departure from the
	// ranking, naming the leg that was passed over. Without PassedOver an
	// explored turn reads as the router preferring the runner-up.
	Explored   bool `json:"explored,omitempty"`
	PassedOver Leg  `json:"passed_over,omitempty"`

	// Shape says whether the turn ran one worker or a team; Workers names a
	// team's legs (Chosen is empty then: no single leg was chosen).
	Shape   string `json:"shape,omitempty"`
	Workers []Leg  `json:"workers,omitempty"`

	Policy     Policy   `json:"policy"`
	Candidates []Scored `json:"candidates,omitempty"` // eligible first, then exclusions with their reasons

	// Shadow is what the decision leg answered at the same points, recorded
	// beside what actually decided and never acted on (shadow.go).
	Shadow *Shadow `json:"shadow,omitempty"`
}

// Excluded returns the candidates that were ruled out, with their reasons.
func (d Decision) Excluded() []Scored {
	var out []Scored
	for _, c := range d.Candidates {
		if !c.Eligible() {
			out = append(out, c)
		}
	}
	return out
}

// Considered returns the candidates that were eligible to run.
func (d Decision) Considered() []Scored { return Eligible(d.Candidates) }

// RecordDecision appends a decision. A decision for a task identity already
// recorded REPLACES it: routing may revise itself within one turn (tier 1
// refines the class, a reroute moves the work), and two rows for one task
// would read as two tasks.
func (l *Ledger) RecordDecision(d Decision) {
	d.Version = DecisionVersion
	if d.At.IsZero() {
		d.At = time.Now()
	}
	if d.TaskID != "" {
		for i := range l.Decisions {
			if l.Decisions[i].TaskID == d.TaskID {
				l.Decisions[i] = d
				return
			}
		}
	}
	l.Decisions = append(l.Decisions, d)
}

// DecisionFor returns the decision behind a task identity.
func (l *Ledger) DecisionFor(taskID string) (Decision, bool) {
	if taskID == "" {
		return Decision{}, false
	}
	for i := len(l.Decisions) - 1; i >= 0; i-- {
		if l.Decisions[i].TaskID == taskID && l.Decisions[i].Version == DecisionVersion {
			return l.Decisions[i], true
		}
	}
	return Decision{}, false
}

// LastDecision returns the most recent readable decision.
func (l *Ledger) LastDecision() (Decision, bool) {
	for i := len(l.Decisions) - 1; i >= 0; i-- {
		if l.Decisions[i].Version == DecisionVersion {
			return l.Decisions[i], true
		}
	}
	return Decision{}, false
}
