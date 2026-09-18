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
	"sync"
	"time"
)

type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortMax    Effort = "max"
)

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
