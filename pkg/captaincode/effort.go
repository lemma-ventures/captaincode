package captaincode

// Effort is how hard a worker thinks on one request - a routing decision,
// not a leg's identity. It comes from what the user asked for (/frontier,
// /quality, /speed) or, on a bare prompt, from the task's difficulty rating;
// the model is picked separately, by the balanced ranking. Every transport
// with a knob gets it: claude -p --effort, codex exec model_reasoning_effort,
// an opencode message's variant (fitted to what the model offers).

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// effortRungs is the escalation order an effort climbs on a second attempt.
var effortRungs = []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

// EffortRank orders efforts; -1 for an unknown one.
func EffortRank(e Effort) int {
	for i, r := range effortRungs {
		if r == e {
			return i
		}
	}
	return -1
}

// NextEffort is one rung up, or the same effort at the top. "" climbs to
// medium: the transport's default is the floor, not a rung above it.
func NextEffort(e Effort) Effort {
	i := EffortRank(e)
	if i < 0 {
		return EffortMedium
	}
	if i+1 >= len(effortRungs) {
		return e
	}
	return effortRungs[i+1]
}

// EffortCeiling is the strongest effort a bare (un-prefixed) request may
// climb to on its own: /frontier alone reaches max. CAPTAIN_EFFORT_CEILING
// overrides (default xhigh).
func EffortCeiling() Effort {
	if v := Effort(os.Getenv("CAPTAIN_EFFORT_CEILING")); EffortRank(v) >= 0 {
		return v
	}
	return EffortXHigh
}

// DecideEffort is the per-task effort decision (ROUTING stage 3): a stated
// preference wins outright; otherwise the class sets the base rung, frontier
// work defaults to MEDIUM on a frontier-class leg (Anthropic's own curve:
// medium gives up ~2 points at half the cost), irreversible work climbs one
// rung, and every attempt after the first climbs one more - on the same
// model first, which keeps the prompt cache warm (escalation.go). The climb
// stops at EffortCeiling; only /frontier reaches max.
//
// /save on an open-weight leg is medium, not low: the cheap lane (lanes.go)
// saves by running open models and runs each at its quality tier - its own
// model rather than its flash sibling. /save on any other leg (no open one
// was open, or CAPTAIN_LANES=0) stays low.
func DecideEffort(prefer string, class Class, leg Leg, irreversible bool, attempt int) Effort {
	switch prefer {
	case "frontier":
		return EffortMax
	case "quality", "q", "best":
		return EffortHigh
	case "save", "cheap":
		if LanesEnabled() && OpenWeights(leg) {
			return EffortMedium
		}
		return EffortLow
	case "speed", "fast":
		return EffortLow
	}
	e := EffortMedium
	switch class {
	case ClassTrivial:
		e = EffortLow
	case ClassHigh:
		e = EffortHigh
		if leg == LegClaude || IsFrontierClass(leg) {
			e = EffortMedium
		}
	}
	if irreversible {
		e = NextEffort(e)
	}
	for i := 1; i < attempt; i++ {
		e = NextEffort(e)
	}
	if ceil := EffortCeiling(); EffortRank(e) > EffortRank(ceil) {
		e = ceil
	}
	return e
}

// EffortFor maps the request to an effort: an explicit preference first
// (/frontier → max, /quality → high, /speed and /save → low), else the
// difficulty rating (high → high, medium → medium, trivial → low).
func EffortFor(prefer string, class Class) Effort {
	switch prefer {
	case "frontier":
		return EffortMax
	case "quality", "q", "best":
		return EffortHigh
	case "speed", "fast", "save", "cheap":
		return EffortLow
	}
	switch class {
	case ClassHigh:
		return EffortHigh
	case ClassTrivial:
		return EffortLow
	}
	return EffortMedium
}

// ClaudeFlag is the value for claude -p --effort (low|medium|high|xhigh|max).
func (e Effort) ClaudeFlag() string { return string(e) }

// CostMultiplier scales a task's estimated spend by how hard the worker
// thinks: reasoning tokens are output tokens. CAPTAIN_EFFORT_COST="l,m,h,x,max"
// overrides (default 0.6,1,1.6,2.2,3).
func (e Effort) CostMultiplier() float64 {
	def := map[Effort]float64{EffortLow: 0.6, EffortMedium: 1, EffortHigh: 1.6, EffortXHigh: 2.2, EffortMax: 3}
	if v := os.Getenv("CAPTAIN_EFFORT_COST"); v != "" {
		f := strings.Split(v, ",")
		if len(f) == len(effortRungs) {
			for i, r := range effortRungs {
				if x, err := strconv.ParseFloat(strings.TrimSpace(f[i]), 64); err == nil && x > 0 {
					def[r] = x
				}
			}
		}
	}
	if m, ok := def[e]; ok {
		return m
	}
	return 1
}

// CodexFlag is the value for codex exec model_reasoning_effort: codex's top
// is xhigh.
func (e Effort) CodexFlag() string {
	if e == EffortMax {
		return "xhigh"
	}
	return string(e)
}

// variantLadder orders the reasoning variants opencode exposes, weakest
// first; an effort is fitted to the strongest available rung at or below
// its own ("high" on [low high max] is high; "medium" there is low).
var variantLadder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func variantRank(v string) int {
	for i, r := range variantLadder {
		if r == v {
			return i
		}
	}
	return -1
}

// Variant picks the model's variant for this effort from what it offers:
// max takes the strongest rung, anything else the strongest rung at or
// below the effort, else the weakest rung. "" when the model offers none.
func (e Effort) Variant(available []string) string {
	if e == "" || len(available) == 0 {
		return ""
	}
	want := variantRank(string(e))
	if e == EffortMax {
		want = len(variantLadder)
	}
	best, bestRank := "", -1
	low, lowRank := "", len(variantLadder)
	for _, v := range available {
		r := variantRank(v)
		if r < 0 {
			continue
		}
		if r <= want && r > bestRank {
			best, bestRank = v, r
		}
		if r < lowRank {
			low, lowRank = v, r
		}
	}
	if best != "" {
		return best
	}
	return low
}

// modelVariants is what an opencode serve reports as each model's variants
// (GET /config/providers), cached per serve for the life of the process -
// the roster changes at a restart, not per turn.
var (
	variantsMu    sync.Mutex
	variantsCache = map[string]map[string][]string{} // baseURL → "provider/model" → variants
)

func opencodeVariants(baseURL, provider, model string) []string {
	if os.Getenv("CAPTAIN_EFFORT_VARIANTS") == "0" {
		return nil
	}
	variantsMu.Lock()
	byModel, ok := variantsCache[baseURL]
	variantsMu.Unlock()
	if !ok {
		byModel = fetchOpencodeVariants(baseURL)
		variantsMu.Lock()
		variantsCache[baseURL] = byModel
		variantsMu.Unlock()
	}
	return byModel[provider+"/"+model]
}

func fetchOpencodeVariants(baseURL string) map[string][]string {
	out := map[string][]string{}
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(baseURL + "/config/providers")
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	var cfg struct {
		Providers []struct {
			ID     string `json:"id"`
			Models map[string]struct {
				Variants map[string]json.RawMessage `json:"variants"`
			} `json:"models"`
		} `json:"providers"`
	}
	if json.NewDecoder(resp.Body).Decode(&cfg) != nil {
		return out
	}
	for _, p := range cfg.Providers {
		for id, m := range p.Models {
			if len(m.Variants) == 0 {
				continue
			}
			vs := make([]string, 0, len(m.Variants))
			for v := range m.Variants {
				vs = append(vs, v)
			}
			out[p.ID+"/"+id] = vs
		}
	}
	return out
}

// resetVariantsCacheForTest drops the per-serve cache.
func resetVariantsCacheForTest() {
	variantsMu.Lock()
	variantsCache = map[string]map[string][]string{}
	variantsMu.Unlock()
}
