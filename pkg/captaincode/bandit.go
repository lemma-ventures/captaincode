package captaincode

// Online learning and distillation (stage 5). A contextual bandit over the
// (leg, effort) arms: Thompson sampling on each arm's Beta posterior, built
// from the estimator's neighbour counts (already version-discounted and
// age-decayed), with the arm's expected cost as the price of pulling it. It
// is gated: below CAPTAIN_BANDIT_MIN_LABELED settled outcomes it refuses to
// act and says so, because sublinear regret is a promise about the limit,
// not about the first hundred pulls, and the ROADMAP's exit gate wants the
// existing default kept when evidence is insufficient.
//
// Distillation is an EXPORT, not a model: the labelled history as one JSONL
// row per task with the features a small router would train on. Training
// one is a separate job that needs a few thousand rows; captain writes the
// rows and refuses to pretend it has a router before then.

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// BanditMinLabeled is the settled-outcome count under which the bandit
// refuses to act. CAPTAIN_BANDIT_MIN_LABELED, default 200.
func BanditMinLabeled() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CAPTAIN_BANDIT_MIN_LABELED"))); err == nil && v >= 0 {
		return v
	}
	return 200
}

// BanditPick is what Thompson sampling chose and why, or why it refused.
type BanditPick struct {
	Row     ExpectedRow `json:"row"`
	Sampled float64     `json:"sampled"` // the sampled success probability that won
	Reward  float64     `json:"reward"`  // sampled P minus the priced cost
	Labeled int         `json:"labeled"` // settled outcomes the gate saw
	Refused string      `json:"refused,omitempty"`
}

// ThompsonPick samples one success probability per eligible arm from
// Beta(1 + successes, 1 + failures) - successes and failures reconstructed
// from the arm's P and evidence weight, plus the prior's pseudo-counts - and
// picks the arm with the best sampled reward, reward = P − cost/costRef.
// Refuses below the labelled gate, or when no arm is eligible.
func ThompsonPick(rows []ExpectedRow, labeled int, rng *rand.Rand) BanditPick {
	pick := BanditPick{Labeled: labeled}
	if min := BanditMinLabeled(); labeled < min {
		pick.Refused = "bandit gate: " + strconv.Itoa(labeled) + " labelled outcomes, needs " + strconv.Itoa(min)
		return pick
	}
	eligible := EligibleExpected(rows)
	if len(eligible) == 0 {
		pick.Refused = "bandit: no eligible arm"
		return pick
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	cRef := costRef()
	best, bestReward := ExpectedRow{}, math.Inf(-1)
	bestSample := 0.0
	for _, r := range eligible {
		// Evidence behind P: the neighbours' weight plus the prior's
		// pseudo-count, split by P into the two Beta parameters.
		w := float64(r.N) + estimatePriorWeight
		alpha := 1 + r.P*w
		beta := 1 + (1-r.P)*w
		s := sampleBeta(rng, alpha, beta)
		reward := s - r.CostUSD/cRef
		if reward > bestReward {
			best, bestReward, bestSample = r, reward, s
		}
	}
	pick.Row, pick.Reward, pick.Sampled = best, bestReward, bestSample
	return pick
}

// sampleBeta draws from Beta(a, b) through two gamma draws.
func sampleBeta(rng *rand.Rand, a, b float64) float64 {
	x := sampleGamma(rng, a)
	y := sampleGamma(rng, b)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

// sampleGamma is Marsaglia–Tsang for shape k ≥ 1, boosted for k < 1.
func sampleGamma(rng *rand.Rand, k float64) float64 {
	if k < 1 {
		return sampleGamma(rng, k+1) * math.Pow(rng.Float64(), 1/k)
	}
	d := k - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// DistillRow is one labelled task as a router would see it: the cheap
// features, the choice, and the label.
type DistillRow struct {
	TaskID       string    `json:"task_id"`
	At           time.Time `json:"at"`
	Task         string    `json:"task"` // the head captain keeps (120 chars), already redacted upstream
	Words        int       `json:"words"`
	Class        Class     `json:"class"`
	Domain       Domain    `json:"domain"`
	TriageBy     string    `json:"triage_by,omitempty"`
	Confidence   float64   `json:"confidence,omitempty"`
	Irreversible bool      `json:"irreversible,omitempty"`
	MidTierP     float64   `json:"mid_tier_p,omitempty"`
	Path         string    `json:"path"`
	Leg          Leg       `json:"leg"`
	Effort       Effort    `json:"effort,omitempty"`
	Model        string    `json:"model,omitempty"`
	Attempts     int       `json:"attempts"`
	Success      bool      `json:"success"`
	DecidedBy    string    `json:"decided_by"`
	Quality      float64   `json:"quality,omitempty"`
	CostUSD      float64   `json:"cost_usd"`
	DurationMs   int64     `json:"duration_ms"`
	Tokens       int       `json:"tokens,omitempty"`
}

// DistillRows turns the labelled history into training rows.
func DistillRows(history []RoutingSample) []DistillRow {
	var out []DistillRow
	for _, s := range history {
		if !s.Labeled() {
			continue
		}
		d := s.Decision
		row := DistillRow{TaskID: d.TaskID, At: d.At, Task: d.Task, Words: len(strings.Fields(d.Task)),
			Class: d.Class, Domain: d.Domain, TriageBy: d.TriageBy, Confidence: d.Confidence,
			Irreversible: d.Irreversible, MidTierP: d.MidTierP, Path: d.Path, Leg: d.Chosen, Effort: d.Effort,
			Success: s.Success(), DecidedBy: string(s.Outcome.DecidedBy)}
		if row.Task == "" {
			row.Task = s.Outcome.Task
		}
		if row.TaskID == "" {
			row.TaskID = s.Outcome.TaskID
		}
		for _, ev := range s.Events {
			if ev.Leg == "" {
				continue
			}
			row.Attempts++
			row.CostUSD += ev.CostUSD
			row.DurationMs += ev.Duration
			row.Tokens += ev.Tokens
			if ev.Outcome == "ok" {
				row.Leg, row.Model = ev.Leg, ev.Model
				if ev.Effort != "" {
					row.Effort = ev.Effort
				}
				if ev.Quality > 0 {
					row.Quality = ev.Quality
				}
			}
		}
		if row.Attempts == 0 {
			row.Attempts = 1
		}
		if row.Leg == "" {
			continue
		}
		out = append(out, row)
	}
	return out
}

// WriteDistill writes the rows as JSONL, one per line, creating the parent
// directory. Returns how many rows it wrote.
func WriteDistill(path string, rows []DistillRow) (int, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	for _, r := range rows {
		line, err := json.Marshal(r)
		if err != nil {
			continue
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
