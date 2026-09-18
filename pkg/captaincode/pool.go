package captaincode

// Pools (2026-09-17): `/oss` and `/deterministic` narrow WHICH legs a turn
// may run on; the preference words (`/quality`, `/speed`, `/save`) still say
// which of those to favour. Both are modifiers, not control words, so they
// compose with everything - `/oss /repeat 5 <task>`, `/repeat 5 /oss <task>`,
// `/team /deterministic <task>` - and, like a preference, they count wherever
// they stand in the turn.
//
//   - oss: legs serving open-weight models (registry `open`, else the
//     model family), so the work never leaves the open ecosystem.
//   - deterministic: legs whose serving tuple is green in the Agentic
//     Determinism Index right now (adi.go); the request is pinned to that
//     tuple. Both together: open weights AND green.

import (
	"regexp"
	"strings"
)

// Pool is the set of constraints a turn states.
type Pool struct {
	OSS           bool
	Deterministic bool
}

func (p Pool) Empty() bool { return !p.OSS && !p.Deterministic }

// String names the pool the way the user wrote it.
func (p Pool) String() string {
	var w []string
	if p.OSS {
		w = append(w, "oss")
	}
	if p.Deterministic {
		w = append(w, "deterministic")
	}
	return strings.Join(w, "+")
}

// PoolWords are the directive words, canonical first.
var PoolWords = []string{"oss", "open", "deterministic", "det", "adi"}

var midPool = regexp.MustCompile(`(?i)(?:^|\s)/(oss|open|deterministic|det|adi)\b`)

// MidPromptPool reads the pool words stated anywhere in the task.
func MidPromptPool(task string) Pool {
	var p Pool
	for _, m := range midPool.FindAllStringSubmatchIndex(task, -1) {
		end := m[3]
		if end < len(task) && task[end] == '/' {
			continue // "/open/api.md" is a path
		}
		switch strings.ToLower(task[m[2]:m[3]]) {
		case "oss", "open":
			p.OSS = true
		case "deterministic", "det", "adi":
			p.Deterministic = true
		}
	}
	return p
}

// openWeightFamilies are model-id fragments that name open-weight families.
// A registry entry's `open` flag overrides the guess either way.
var openWeightFamilies = []string{
	"llama", "qwen", "glm", "deepseek", "kimi", "moonshot", "minimax", "gpt-oss",
	"mistral", "mixtral", "devstral", "magistral", "gemma", "phi-", "nemotron",
	"olmo", "yi-", "internlm", "falcon", "dbrx", "jamba", "granite", "hunyuan",
	"codestral", "starcoder", "seed-oss", "exaone", "smollm",
}

// OpenWeights reports whether a leg serves an open-weight model.
func OpenWeights(l Leg) bool {
	spec, ok := Spec(l)
	if !ok {
		return false
	}
	if spec.Open != nil {
		return *spec.Open
	}
	m := strings.ToLower(spec.Model)
	for _, f := range openWeightFamilies {
		if strings.Contains(m, f) {
			return true
		}
	}
	return false
}

// PoolAllows reports whether a leg belongs to the pool.
func PoolAllows(p Pool, l Leg) bool {
	if p.OSS && !OpenWeights(l) {
		return false
	}
	if p.Deterministic {
		t, ok := ADIFor(l)
		if !ok || !t.Green {
			return false
		}
	}
	return true
}

// FilterPool keeps the legs of the pool, in the order given.
func FilterPool(p Pool, legs []Leg) []Leg {
	if p.Empty() {
		return legs
	}
	out := make([]Leg, 0, len(legs))
	for _, l := range legs {
		if PoolAllows(p, l) {
			out = append(out, l)
		}
	}
	return out
}

// PoolLegs is every active leg of the pool, ladder order.
func PoolLegs(p Pool) []Leg { return FilterPool(p, AllLegs) }
