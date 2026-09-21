// Package superagent is a local-first policy router over the user's own
// coding-agent subscriptions: it classifies a task, picks the cheapest leg
// with quota headroom (free OpenCode models -> ChatGPT/Codex sub -> Claude
// Max sub), dispatches, and records the outcome so `sa why` / `sa quota`
// can explain decisions. See docs/ARCHITECTURE.md.
package captaincode

import (
	"strings"
	"time"
)

type Class string

const (
	ClassTrivial Class = "trivial"
	ClassMedium  Class = "medium"
	ClassHigh    Class = "high"
)

// Leg identifies a dispatch backend. Order of Rungs is the escalation ladder.
type Leg string

const (
	LegFree    Leg = "free"    // opencode serve -> zero-quota models
	LegQwen    Leg = "qwen"    // opencode serve -> Scaleway self-hosted Qwen (OSS coder)
	LegGPTOSS  Leg = "gpt-oss" // opencode serve -> gpt-oss-120b via OpenRouter, pinned to Cerebras: the ADI-green open-weight leg (adi.go)
	LegGrok    Leg = "grok"    // opencode serve -> SuperGrok OAuth (burn-first sub: daily reset)
	LegCodex   Leg = "codex"   // opencode serve -> ChatGPT-subscription OAuth
	LegGLM     Leg = "glm"     // opencode serve -> GLM-5.3 via OpenRouter (best open-weights leg; NIM only carries the flash variant)
	LegMiniMax Leg = "minimax" // opencode serve -> MiniMax M3 via OpenRouter (NIM retired it 2026-09-09)
	LegClaude  Leg = "claude"  // claude -p headless (Claude Max)
	LegCursor  Leg = "cursor"  // cursor-agent -p headless (Cursor subscription)
	// OpenRouter OSS legs (2026-08-24): paid per-token, multi-host failover.
	LegDeepSeek Leg = "deepseek" // opencode serve -> DeepSeek V4 Pro via OpenRouter
	LegGemini   Leg = "gemini"   // opencode serve -> Gemini 3.7 Flash via OpenRouter (vision)
	LegKimi     Leg = "kimi"     // opencode serve -> Kimi K3 via NIM (flagship, FREE with the NVIDIA key)
	// Hugging Face router (2026-09-20): open-weight legs on the HF Inference
	// Providers router; join the /oss pool. Same credential (HF_TOKEN).
	LegStep     Leg = "step"      // opencode serve -> Step 3.5 Flash via Hugging Face
	LegDS4Flash Leg = "ds4-flash" // opencode serve -> DeepSeek V4 Flash via Hugging Face
	// Frontier-class (2026-09-09): the Codex CLI driving gpt-6-astra at xhigh
	// reasoning on the ChatGPT subscription - see codex-cli.go. NOT the codex leg.
	LegCodexCLI Leg = "codex-cli" // codex exec headless (Codex CLI, ~/.codex/auth.json)
	// grok-max (2026-09-13): xAI's flagship grok-4.7 as a WORKER, frontier-class
	// (2× budget, never auto-assigned to routine work, third in the /frontier
	// failover after claude and codex-cli). The grok leg keeps grok-build-0.1,
	// the fast daily-reset burner; the two share the SuperGrok credential.
	// /frontier /grok upgrades the burner to grok-4.7 via directorModels.
	LegGrokMax Leg = "grok-max" // opencode serve -> xai/grok-4.7 (SuperGrok)
	// jev (2026-09-17): TypeSafe's System One model, a DECISION leg. It answers
	// typed questions (choice/score/noul) with calibrated probabilities in
	// ~100-500ms and never generates text or runs a tool, so it is in the
	// registry (doctor, pricing, `captain jev`) but never a worker rung: the
	// brain asks it the triage questions instead (systemone.go).
	LegJev Leg = "jev" // POST api.typesafe.ai/v1/systemone (TYPESAFE_API_KEY)
)

// AllLegs is every ACTIVE leg in ascending prior order - derived from the
// registry (registry.go) at init and on every LoadRegistry/prior change, so
// the ladder never escalates DOWN in quality (TestQualityPriorOrderingMatchesLadder).
var AllLegs []Leg

// LegIDs returns every leg name in ladder order - the single roster that the
// wrapper's models list, the directive regex and /init's config entries are
// generated from, so adding a leg here is adding it everywhere.
func LegIDs() []string {
	ids := make([]string, 0, len(AllLegs))
	for _, l := range AllLegs {
		ids = append(ids, string(l))
	}
	return ids
}

// LegDisplayName is the TUI model-picker label for a leg (opencode.jsonc's
// captain provider block, written by /init) - from the registry.
func LegDisplayName(l Leg) string {
	if s, ok := specs[l]; ok && s.Display != "" {
		return s.Display
	}
	return string(l) + " (captain)"
}

// Director is the model that plans, directs, and assesses. It is NEVER a worker
// leg, so the router can never assign a task to itself (the whole point of a
// director). Configurable via SetDirector; defaults to grok. The director can
// still be run as an explicit worker with `captain with <leg> …`.
var Director = LegGrok

// Rungs is the worker escalation ladder: every leg that takes tasks, except
// the director. A decision leg (ServesTasks false) is never a rung.
var Rungs = workerLadder(Director)

// KnownLeg reports whether l is a real backend leg.
func KnownLeg(l Leg) bool {
	for _, x := range AllLegs {
		if x == l {
			return true
		}
	}
	return false
}

func workerLadder(d Leg) []Leg {
	r := make([]Leg, 0, len(AllLegs))
	for _, l := range AllLegs {
		if l != d && ServesTasks(l) {
			r = append(r, l)
		}
	}
	return r
}

// SetDirector chooses which model directs and rebuilds the worker ladder to
// exclude it. Call once at startup before routing.
func SetDirector(d Leg) {
	Director = d
	Rungs = workerLadder(d)
}

var highSignals = []string{
	"architect", "design", "refactor", "migration", "migrate", "concurrency",
	"race", "deadlock", "security", "vulnerab", "debug", "investigate",
	"performance", "optimize", "rewrite", "across the codebase",
}

var trivialSignals = []string{
	"typo", "rename", "comment", "readme", "lint", "format", "bump",
	"whitespace", "todo", "changelog",
}

// Classify buckets a task with cheap deterministic heuristics (ROUTING.md §2:
// no LLM on the routing hot path). It is a prior, not an oracle - failures
// escalate up the ladder anyway.
func Classify(task string) Class {
	t := strings.ToLower(task)
	for _, s := range highSignals {
		if strings.Contains(t, s) {
			return ClassHigh
		}
	}
	for _, s := range trivialSignals {
		if strings.Contains(t, s) {
			return ClassTrivial
		}
	}
	if len(t) > 400 { // long detailed specs correlate with complex tasks
		return ClassHigh
	}
	return ClassMedium
}

func ParseClass(class string) (Class, bool) {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case string(ClassTrivial):
		return ClassTrivial, true
	case string(ClassMedium):
		return ClassMedium, true
	case string(ClassHigh):
		return ClassHigh, true
	case "":
		return "", false
	}
	return "", false
}

// RouteResult is the resolved routing decision after applying a director Plan
// (or falling back to the heuristic ladder). Shared by the CLI and brain HTTP
// surface so both treat director-returned class as authoritative.
type RouteResult struct {
	Class   Class
	Order   []Leg  // try-order; manager pick first when present
	Brief   string // worker brief (task when falling back)
	Reason  string // human-readable why string for logs / `captain why`
	FanOut  bool   // true → multi-worker plan; Order/Brief unused
	Plan    Plan   // full director plan when FanOut or used manager
	Managed bool   // true when director plan was applied
}

// ResolveRoute applies a director Plan over the heuristic ladder.
// Director-returned class is authoritative when plan is valid; classHint is
// used only on director skip/failure (and for forced/`--no-manager` callers
// that never invoke this with a plan).
func ResolveRoute(task string, classHint Class, ladderOrder []Leg, plan Plan, planErr error) RouteResult {
	if planErr != nil || len(plan.Workers) == 0 {
		return RouteResult{
			Class:  classHint,
			Order:  ladderOrder,
			Brief:  task,
			Reason: "director unavailable → ladder",
		}
	}
	class := plan.Class
	if _, ok := ParseClass(string(class)); !ok {
		class = ClassMedium
	}
	if len(plan.Workers) > 1 {
		return RouteResult{
			Class:   class,
			Reason:  "manager: " + plan.Rationale,
			FanOut:  true,
			Plan:    plan,
			Managed: true,
		}
	}
	w := plan.Workers[0]
	brief := strings.TrimSpace(w.Brief)
	if brief == "" {
		brief = task
	}
	return RouteResult{
		Class:   class,
		Order:   prependLeg(w.Leg, ladderOrder),
		Brief:   brief,
		Reason:  "manager: " + plan.Rationale,
		Plan:    plan,
		Managed: true,
	}
}

func prependLeg(l Leg, order []Leg) []Leg {
	out := []Leg{l}
	for _, o := range order {
		if o != l {
			out = append(out, o)
		}
	}
	return out
}

// StartRung maps class + preference to the first ladder rung to try.
func StartRung(c Class, prefer string) int {
	switch prefer {
	case "save":
		return 0
	case "quality":
		return len(Rungs) - 1
	case "speed":
		return 1 // grok leg: fast models, daily-reset subscription capacity
	}
	switch c {
	case ClassTrivial:
		return 0
	case ClassHigh:
		return len(Rungs) - 1
	default:
		return 1
	}
}

// Pick returns the ladder starting at rung, skipping legs cooling down.
// The returned slice is the try-order; empty means everything is cooling down.
func Pick(rung int, cooldowns map[Leg]time.Time, now time.Time) []Leg {
	var order []Leg
	for i := rung; i < len(Rungs); i++ {
		order = append(order, Rungs[i])
	}
	// fall back downward if upper rungs are cooling down
	for i := rung - 1; i >= 0; i-- {
		order = append(order, Rungs[i])
	}
	open := make([]Leg, 0, len(order))
	for _, l := range order {
		if until, ok := cooldowns[l]; ok && now.Before(until) {
			continue
		}
		open = append(open, l)
	}
	return open
}
