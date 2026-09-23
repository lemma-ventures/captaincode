package captaincode

// The leg registry (MM37, 2026-09-10): every leg is DATA - compiled defaults
// below plus an operator overlay at ~/.captaincode/legs.json - and everything
// that used to be a hand-maintained list derives from it: the ladder order
// (AllLegs, by prior), opencode provider/model pins, env overrides, display
// names, the vision set, the director's briefing, /init's config entries,
// the wrapper's model list and directive regex, the fork's prefix pattern.
// Adding a model is `captain legs add <id> <provider/model>`; no rebuild.
//
// Leg constants (LegClaude, LegGrok, …) remain as identifiers for code that
// special-cases a leg; the registry is the source of truth for its facts.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Transport is how a leg is driven.
type Transport string

const (
	TransportOpencode  Transport = "opencode"   // opencode serve session (provider/model pin)
	TransportClaudeCLI Transport = "claude-cli" // claude -p
	TransportCursorCLI Transport = "cursor-cli" // cursor-agent -p
	TransportCodexCLI  Transport = "codex-cli"  // codex exec
	// TransportSystemOne is TypeSafe's System One API (systemone.go): typed
	// questions in, calibrated answers out. A leg on it is a DECISION leg -
	// it can never take a task (ServesTasks), so it is no worker rung, no
	// model in the TUI's picker and no stage in a workflow.
	TransportSystemOne Transport = "system-one" // POST /v1/systemone
)

// LegSpec is one registry entry.
type LegSpec struct {
	ID           Leg                `json:"id"`
	Transport    Transport          `json:"transport"`
	Provider     string             `json:"provider,omitempty"` // opencode provider id
	Model        string             `json:"model,omitempty"`    // opencode model id (or the CLI's model name, informational)
	AA           string             `json:"aa,omitempty"`       // Artificial Analysis slug for priors sync: exact slug wins, else substring (shortest slug = base variant)
	PriceIn      float64            `json:"price_in,omitempty"` // USD per 1M input tokens (0 for subscription legs)
	PriceOut     float64            `json:"price_out,omitempty"`
	Ctx          int                `json:"ctx,omitempty"` // context window, tokens
	Vision       bool               `json:"vision,omitempty"`
	Frontier     bool               `json:"frontier,omitempty"`     // frontier-class semantics (2× budget, never auto-assigned cheaply)
	Subscription bool               `json:"subscription,omitempty"` // billed by a quota window, not per token
	Prior        float64            `json:"prior"`                  // cold-start quality 0-10 (overall)
	DomainPrior  map[Domain]float64 `json:"domain_prior,omitempty"`
	Display      string             `json:"display,omitempty"`
	Note         string             `json:"note,omitempty"` // the director's briefing line
	Env          string             `json:"env,omitempty"`  // env prefix for <ENV>_PROVIDER/_MODEL overrides (default CAPTAIN_<ID>)
	// Caps overrides the transport's declared capability contract for this leg
	// (M2.2): an operator driving a patched CLI knows something this build does
	// not. Keys are Capability values, values yes/no/unknown.
	Caps     map[Capability]Support `json:"caps,omitempty"`
	Disabled bool                   `json:"disabled,omitempty"`
	// Open says whether the model's weights are open (the /oss pool, pool.go);
	// unset → guessed from the model family.
	Open *bool `json:"open,omitempty"`
	// Tiers names the sibling model this leg runs for the cheap and frontier
	// bands (tiers.go); quality is Model. A band left out runs Model at that
	// band's effort.
	Tiers map[Tier]string `json:"tiers,omitempty"`
}

// EnvPrefix returns the env prefix for this leg's provider/model overrides.
func (s LegSpec) EnvPrefix() string {
	if s.Env != "" {
		return s.Env
	}
	return "CAPTAIN_" + strings.ToUpper(strings.ReplaceAll(string(s.ID), "-", "_"))
}

// defaultLegSpecs are the compiled legs, in cold-start prior order (ties keep
// this order: grok before codex splits on cost, not quality).
var defaultLegSpecs = []LegSpec{
	// jev carries no worker prior (0): it is not a worker. Priced per input
	// token only - output is free - so the registry estimate runs a little
	// under the bill (EstimateCost assumes a 3:1 split); cents per thousand
	// classifications.
	{ID: LegJev, Transport: TransportSystemOne, Provider: "typesafe", Model: "jev-latest",
		PriceIn: 0.042, Open: boolp(false), Display: "Jev (captain · TypeSafe System One)",
		Note: "TypeSafe Jev 1.13, a System One model: typed decisions (choice/score/noul) with calibrated probabilities in ~100-500ms, $0.042/M input, output free (2026-09-17). NOT a worker - it generates no text and runs no tools; captain asks it the triage questions (class, domain) when TYPESAFE_API_KEY is set, and `captain jev` asks it anything"},
	{ID: LegFree, Transport: TransportOpencode, Provider: "opencode", Model: "nemotron-3.5-lightning-free", AA: "nemotron-3-5-lightning",
		Ctx: 131072, Subscription: true, Prior: 6.0, Display: "Free (captain · opencode)",
		Note: "zero cost (opencode zen free roster, rotates); fine for simple/boilerplate work; benchmark claims unverified - don't trust with architecture"},
	{ID: LegQwen, Transport: TransportOpencode, Provider: "openrouter", Model: "qwen/qwen3.5-397b-a17b", AA: "qwen3-5-397b-a17b",
		PriceIn: 0.26, PriceOut: 1.0, Ctx: 262144, Prior: 6.8, Display: "Qwen (captain · opencode)",
		// Cheap only: qwen3.6-flash and -max-preview are served solely by
		// endpoints without zero data retention, which OpenRouter refuses
		// under a ZDR account setting (probed 2026-09-22).
		Tiers: map[Tier]string{TierCheap: "qwen/qwen3.6-35b-a3b"},
		Note:  "Qwen 3.5 (OpenRouter, paid per token); cheap durable capacity for routine code edits and refactors; not the architecture apex"},
	{ID: LegStep, Transport: TransportOpencode, Provider: "huggingface", Model: "stepfun-ai/Step-3.5-Flash",
		PriceIn: 0.1, PriceOut: 0.3, Ctx: 262144, Prior: 6.8, Open: boolp(true), Display: "Step 3.5 Flash (captain · Hugging Face)",
		Note: "Step 3.5 Flash via Hugging Face router (~$0.10/M in, 256k ctx); cheap open-weight reasoning worker, tool-calling, no vision"},
	{ID: LegGPTOSS, Transport: TransportOpencode, Provider: "openrouter", Model: "openai/gpt-oss-120b", AA: "gpt-oss-120b",
		PriceIn: 0.15, PriceOut: 0.6, Ctx: 131072, Prior: 6.9, Display: "gpt-oss 120B (captain · OpenRouter, Cerebras)",
		Note: "gpt-oss-120b (OpenAI open weights) via OpenRouter; served by Cerebras it is the one serving tuple the Agentic Determinism Index scores byte-exact run after run (streak 10, 2026-09-17) - the /deterministic pick, pinned to that tuple; a capable mid-tier coder, not a frontier reasoner"},
	{ID: LegGrok, Transport: TransportOpencode, Provider: "xai", Model: "grok-build-0.1", AA: "grok-build-0-1-06-16",
		Ctx: 262144, Vision: true, Subscription: true, Prior: 7.0, Display: "Grok (captain · opencode)",
		Tiers: map[Tier]string{TierFrontier: "grok-4.7"},
		Note:  "grok-build-0.1, SuperGrok sub, resets daily (cheapest marginal - burn first); fast agentic coding, ~71% SWE-bench Verified"},
	{ID: LegLuna, Transport: TransportOpencode, Provider: "openai", Model: "gpt-6-luna", AA: "gpt-6-luna",
		Ctx: 1050000, Vision: true, Subscription: true, Prior: 7.0, Display: "Luna (captain · opencode, gpt-6-luna)",
		Note: "GPT-6 Luna on the ChatGPT sub (the quota codex and codex-cli draw on): OpenAI's cheap fast tier (reads the GPT-5.6 Luna AA row, index 37 / coding 71 at ~150 tok/s, until AA scores 6 Luna) - routine edits, boilerplate, summaries and extraction at volume; not for architecture or hard bugs (that is codex, then codex-cli)"},
	{ID: LegDS4Flash, Transport: TransportOpencode, Provider: "huggingface", Model: "deepseek-ai/DeepSeek-V4-Flash",
		PriceIn: 0.14, PriceOut: 0.28, Ctx: 1048576, Prior: 7.2, Open: boolp(true), Display: "DeepSeek V4 Flash (captain · Hugging Face)",
		Note: "DeepSeek V4 Flash via Hugging Face router (~$0.14/M in, 1M ctx); the cheap V4 lane under the OpenRouter V4 Pro leg; tool-calling, no vision"},
	{ID: LegMiniMax, Transport: TransportOpencode, Provider: "openrouter", Model: "minimax/minimax-m3", AA: "minimax-m3",
		PriceIn: 0.3, PriceOut: 1.2, Ctx: 131072, Prior: 7.5, Display: "MiniMax M3 (captain · opencode)",
		Note: "MiniMax M3 via OpenRouter (~$0.30/M in); strong OSS agentic worker a notch below GLM-5.3; overflow lane when subscription legs are cooling. NIM retired every MiniMax model on 2026-09-09 (410 Gone), so the free lane is gone"},
	{ID: LegDeepSeek, Transport: TransportOpencode, Provider: "openrouter", Model: "deepseek/deepseek-v4-pro", AA: "deepseek-v4-pro",
		PriceIn: 0.52, PriceOut: 1.6, Ctx: 1048576, Prior: 7.6, Display: "DeepSeek V4 Pro (captain · opencode)",
		Tiers: map[Tier]string{TierCheap: "deepseek/deepseek-v4-flash"},
		Note:  "DeepSeek V4 Pro (OpenRouter, ~$0.52/M in); strong cheap coder/reasoner with 1M context; good grok alternative when xAI wobbles"},
	{ID: LegGLM, Transport: TransportOpencode, Provider: "openrouter", Model: "z-ai/glm-5.3", AA: "glm-5-3",
		PriceIn: 1.09, PriceOut: 3.43, Ctx: 1310720, Prior: 8.2, Display: "GLM-5.3 (captain · opencode)",
		Tiers: map[Tier]string{TierCheap: "z-ai/glm-5.3-flash"},
		Note:  "GLM-5.3 (z-ai) via OpenRouter (~$1.09/M in, 1.3M ctx), the best open-weights model (AA index 44.9, a point above grok-4.6 and kimi-k3, 2026-09-13); near-frontier agentic coder without burning Claude/Codex quota; the open-weights fallback of /frontier"},
	{ID: LegGemini, Transport: TransportOpencode, Provider: "openrouter", Model: "google/gemini-3.7-flash", AA: "gemini-3-7-flash",
		PriceIn: 0.38, PriceOut: 2.0, Ctx: 1048576, Vision: true, Prior: 7.8, Display: "Gemini 3.7 Flash (captain · opencode)",
		Tiers: map[Tier]string{TierCheap: "google/gemini-3.5-flash-lite", TierFrontier: "google/gemini-3.8-flash"},
		Note:  "Gemini 3.7 Flash (OpenRouter, ~$0.38/M in); vision-capable, 1M context, fast; the compaction leg; good for multimodal or long-context tasks"},
	{ID: LegKimi, Transport: TransportOpencode, Provider: "nim", Model: "moonshotai/kimi-k3", AA: "kimi-k3",
		Ctx: 262144, Prior: 8.0, Display: "Kimi K3 (captain · opencode)",
		Note: "Kimi K3 flagship via NVIDIA NIM (free with the NVIDIA key); strong pick for hard reasoning/code at zero marginal cost; single-host, watch for NIM stalls"},
	{ID: LegCursor, Transport: TransportCursorCLI, Model: "grok-4.7", AA: "cursor-composer",
		Ctx: 262144, Vision: true, Subscription: true, Prior: 8.0, Display: "Cursor (captain · cursor-agent)",
		Tiers: map[Tier]string{TierCheap: "composer-2.5", TierFrontier: "grok-4.7-xhigh"},
		Note:  "cursor-agent on the Cursor plan: Grok 4.7 at the request's effort rung (grok-4.7-low to -high, -xhigh under /frontier), Cursor Composer 2.5 for cheap work; near-frontier agentic coding at a fraction of frontier cost; strong default for repo-touching work"},
	{ID: LegCodex, Transport: TransportOpencode, Provider: "openai", Model: "gpt-6-sol-fast", AA: "gpt-6-sol",
		Ctx: 1050000, Vision: true, Subscription: true, Prior: 8.4, Display: "Codex (captain · opencode, gpt-6-sol-fast)",
		Tiers: map[Tier]string{TierCheap: "gpt-6-luna", TierFrontier: "gpt-6-astra"},
		Note:  "GPT-6 Sol in fast mode on the ChatGPT sub (5h+weekly windows): OpenAI's quality tier (reads the GPT-5.6 Sol AA row, index 47 / coding 77.4, until AA scores 6 Sol) - real features, refactors and debugging below the frontier; directs as gpt-6-sol. The cheap OpenAI lane is luna, the frontier one codex-cli. gpt-5.3-codex-spark was refused for ChatGPT accounts from 2026-09-15"},
	{ID: LegGrokMax, Transport: TransportOpencode, Provider: "xai", Model: "grok-4.7", AA: "grok-4-7",
		PriceIn: 2, PriceOut: 6, Ctx: 500000, Vision: true, Frontier: true, Subscription: true, Prior: 8.7, Display: "Grok Max (captain · opencode, grok-4.7)",
		Note: "grok-4.7, xAI's flagship reasoning model on the SuperGrok credential (reads the grok-4.6 AA row when the feed lacks 4.7); frontier-class: 2× budget, hard reasoning and architecture, third in the /frontier failover; not for routine edits (that is the grok leg)"},
	{ID: LegCodexCLI, Transport: TransportCodexCLI, Model: "gpt-6-astra", AA: "gpt-6-astra-xhigh",
		Ctx: 1050000, Vision: true, Frontier: true, Subscription: true, Prior: 9.3, Display: "Codex CLI (captain · codex exec, gpt-6-astra)",
		Tiers: map[Tier]string{TierCheap: "gpt-6-sol"},
		Note:  "gpt-6-astra via the Codex CLI (reasoning effort follows the request: /frontier xhigh, /quality high, else the difficulty rating), ChatGPT sub quota (5h+weekly windows); frontier-class like claude and SLOW (minutes per turn, double time budget) - reserve for the hardest architecture/debugging/reasoning, never routine edits; a second frontier opinion with a different refusal profile than claude"},
	{ID: LegClaude, Transport: TransportClaudeCLI, Model: "claude-opus", AA: "claude-opus-5-5",
		Ctx: 1000000, Vision: true, Subscription: true, Prior: 9.5, Display: "Claude (captain · claude -p)",
		Tiers: map[Tier]string{TierCheap: "sonnet", TierFrontier: "opus"},
		Note:  "Claude Opus 5.5 via Max sub (the `opus` alias on Claude Code 2.1.280+; 5h+weekly windows, no extra-usage spend); Anthropic's go-to for long-running agentic coding, /frontier runs it at max effort (reads the Opus 5 AA row when the feed lacks 5.5; CAPTAIN_FRONTIER_MODEL=fable brings Fable 5.1 back); architecture, gnarly debugging, escalation apex"},
}

// specs is the ACTIVE registry (defaults + overlay), keyed by id; specOrder
// keeps the insertion order that breaks prior ties.
var (
	specs     = map[Leg]LegSpec{}
	specOrder []Leg
)

// Spec returns the active entry for a leg.
func Spec(l Leg) (LegSpec, bool) {
	s, ok := specs[l]
	return s, ok
}

// ServesTasks reports whether a leg can take a task: its transport runs an
// agent. A decision leg (TransportSystemOne) answers typed questions and is
// never a worker rung, a model in the TUI's picker, a forced leg or a
// workflow stage - the brain asks it questions instead.
func ServesTasks(l Leg) bool {
	s, ok := specs[l]
	return ok && s.Transport != TransportSystemOne
}

func boolp(b bool) *bool { return &b }

// Registry lists the active (non-disabled) legs in ladder order.
func Registry() []LegSpec {
	out := make([]LegSpec, 0, len(AllLegs))
	for _, l := range AllLegs {
		out = append(out, specs[l])
	}
	return out
}

// registryOverlay is the on-disk shape of ~/.captaincode/legs.json.
type registryOverlay struct {
	Legs    []LegSpec `json:"legs"`
	Disable []Leg     `json:"disable,omitempty"`
}

// RegistryOverlayPath is where operator-added legs live.
func RegistryOverlayPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".captaincode", "legs.json")
}

func resetRegistry() {
	specs = map[Leg]LegSpec{}
	specOrder = specOrder[:0]
	for _, s := range defaultLegSpecs {
		specs[s.ID] = s
		specOrder = append(specOrder, s.ID)
	}
}

// LoadRegistry (re)builds the active registry from the compiled defaults and
// the overlay at path ("" → the default path; a missing file is normal), then
// re-derives every table that depends on it. Returns how many overlay entries
// were added or changed.
func LoadRegistry(path string) (int, error) {
	resetRegistry()
	n := 0
	if path == "" {
		path = RegistryOverlayPath()
	}
	var loadErr error
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			loadErr = err
		default:
			var ov registryOverlay
			if err := json.Unmarshal(data, &ov); err != nil {
				loadErr = fmt.Errorf("parse %s: %w", path, err)
			} else {
				for _, s := range ov.Legs {
					// An overlay entry for a compiled leg only carries the fields
					// it changes: validate the MERGED spec.
					merged := s
					if cur, ok := specs[s.ID]; ok {
						merged = mergeSpec(cur, s)
					}
					if err := validateSpec(merged); err != nil {
						loadErr = fmt.Errorf("%s: leg %q: %w", path, s.ID, err)
						continue
					}
					if _, ok := specs[s.ID]; !ok {
						specOrder = append(specOrder, s.ID)
					}
					specs[s.ID] = merged
					n++
				}
				for _, l := range ov.Disable {
					if s, ok := specs[l]; ok {
						s.Disabled = true
						specs[l] = s
						n++
					}
				}
			}
		}
	}
	applyRegistry()
	return n, loadErr
}

// validateSpec rejects entries that cannot run.
func validateSpec(s LegSpec) error {
	id := string(s.ID)
	if id == "" || strings.ContainsAny(id, " /\\\t\n") || strings.ToLower(id) != id {
		return errors.New("id must be a lowercase word (letters, digits, '-')")
	}
	if id == "team" || id == "frontier" || id == "workflow" {
		return errors.New("reserved name")
	}
	switch s.Transport {
	case "", TransportOpencode:
		if s.Provider == "" || s.Model == "" {
			return errors.New("opencode legs need provider and model")
		}
	case TransportSystemOne:
		if s.Provider == "" || s.Model == "" {
			return errors.New("system-one legs need provider and model")
		}
	case TransportClaudeCLI, TransportCursorCLI, TransportCodexCLI:
	default:
		return fmt.Errorf("unknown transport %q", s.Transport)
	}
	return nil
}

// mergeSpec overlays non-zero fields of o onto base (an overlay entry for a
// compiled leg only needs the fields it changes).
func mergeSpec(base, o LegSpec) LegSpec {
	out := base
	if o.Transport != "" {
		out.Transport = o.Transport
	}
	if o.Provider != "" {
		out.Provider = o.Provider
	}
	if o.Model != "" {
		out.Model = o.Model
	}
	if o.AA != "" {
		out.AA = o.AA
	}
	if o.PriceIn != 0 {
		out.PriceIn = o.PriceIn
	}
	if o.PriceOut != 0 {
		out.PriceOut = o.PriceOut
	}
	if o.Ctx != 0 {
		out.Ctx = o.Ctx
	}
	if o.Vision {
		out.Vision = true
	}
	if o.Frontier {
		out.Frontier = true
	}
	if o.Subscription {
		out.Subscription = true
	}
	if o.Prior != 0 {
		out.Prior = o.Prior
	}
	if len(o.DomainPrior) > 0 {
		out.DomainPrior = o.DomainPrior
	}
	if o.Display != "" {
		out.Display = o.Display
	}
	if o.Note != "" {
		out.Note = o.Note
	}
	if o.Env != "" {
		out.Env = o.Env
	}
	if len(o.Caps) > 0 {
		merged := map[Capability]Support{}
		for c, s := range out.Caps {
			merged[c] = s
		}
		for c, s := range o.Caps {
			merged[c] = s
		}
		out.Caps = merged
	}
	if o.Disabled {
		out.Disabled = true
	}
	if len(o.Tiers) > 0 {
		merged := map[Tier]string{}
		for t, m := range out.Tiers {
			merged[t] = m
		}
		for t, m := range o.Tiers {
			merged[t] = m
		}
		out.Tiers = merged
	}
	return out
}

// applyRegistry re-derives every registry-dependent table.
func applyRegistry() {
	// Priors: compiled defaults come from the registry; priors.json overrides
	// are re-applied by the caller (LoadPriorOverrides) after this.
	qualityPriorDefaults = map[Leg]float64{}
	qualityPriors = map[Leg]float64{}
	domainPriors = map[Leg]map[Domain]float64{}
	legModels = map[Leg]struct{ Provider, Model string }{}
	active := make([]Leg, 0, len(specOrder))
	for _, id := range specOrder {
		s := specs[id]
		if s.Disabled {
			continue
		}
		if s.Transport == "" {
			s.Transport = TransportOpencode
			specs[id] = s
		}
		active = append(active, id)
		qualityPriorDefaults[id] = s.Prior
		qualityPriors[id] = s.Prior
		if len(s.DomainPrior) > 0 {
			dp := map[Domain]float64{}
			for d, p := range s.DomainPrior {
				dp[d] = p
			}
			domainPriors[id] = dp
		}
		if s.Transport == TransportOpencode {
			legModels[id] = struct{ Provider, Model string }{s.Provider, s.Model}
		}
	}
	AllLegs = active
	applyLegModelEnv()
	rebuildLadder()
	reloadVisionLegs()
}

// rebuildLadder orders AllLegs by active prior (stable: registry order breaks
// ties) and rebuilds the worker ladder for the current director. Called after
// any prior change so the ladder never escalates DOWN in quality.
func rebuildLadder() {
	sort.SliceStable(AllLegs, func(i, j int) bool { return QualityPrior(AllLegs[i]) < QualityPrior(AllLegs[j]) })
	Rungs = workerLadder(Director)
}

// SaveRegistryOverlay writes an overlay file.
func SaveRegistryOverlay(path string, ov registryOverlay) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ReadRegistryOverlay reads the overlay (empty when absent).
func ReadRegistryOverlay(path string) (registryOverlay, error) {
	var ov registryOverlay
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ov, nil
	}
	if err != nil {
		return ov, err
	}
	return ov, json.Unmarshal(data, &ov)
}

// AddLeg adds or updates an overlay entry and reloads the registry.
func AddLeg(path string, s LegSpec) error {
	if path == "" {
		path = RegistryOverlayPath()
	}
	ov, err := ReadRegistryOverlay(path)
	if err != nil {
		return err
	}
	// Validate what the entry will BE: merged over the existing overlay entry
	// and/or the compiled spec, so an update may carry only the changed fields.
	merged := s
	for _, cur := range ov.Legs {
		if cur.ID == s.ID {
			merged = mergeSpec(cur, s)
		}
	}
	for _, cur := range defaultLegSpecs {
		if cur.ID == s.ID {
			merged = mergeSpec(mergeSpec(cur, merged), s)
		}
	}
	if err := validateSpec(merged); err != nil {
		return err
	}
	replaced := false
	for i := range ov.Legs {
		if ov.Legs[i].ID == s.ID {
			ov.Legs[i] = mergeSpec(ov.Legs[i], s)
			replaced = true
		}
	}
	if !replaced {
		ov.Legs = append(ov.Legs, s)
	}
	keep := ov.Disable[:0]
	for _, l := range ov.Disable {
		if l != s.ID {
			keep = append(keep, l)
		}
	}
	ov.Disable = keep
	if err := SaveRegistryOverlay(path, ov); err != nil {
		return err
	}
	_, err = LoadRegistry(path)
	return err
}

// RemoveLeg drops an overlay-defined leg, or disables a compiled one.
func RemoveLeg(path string, id Leg) error {
	if path == "" {
		path = RegistryOverlayPath()
	}
	ov, err := ReadRegistryOverlay(path)
	if err != nil {
		return err
	}
	kept := ov.Legs[:0]
	found := false
	for _, s := range ov.Legs {
		if s.ID == id {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	ov.Legs = kept
	if !found {
		compiled := false
		for _, s := range defaultLegSpecs {
			if s.ID == id {
				compiled = true
			}
		}
		if !compiled {
			return fmt.Errorf("unknown leg %q", id)
		}
		already := false
		for _, l := range ov.Disable {
			if l == id {
				already = true
			}
		}
		if !already {
			ov.Disable = append(ov.Disable, id)
		}
	}
	if err := SaveRegistryOverlay(path, ov); err != nil {
		return err
	}
	_, err = LoadRegistry(path)
	return err
}

func init() {
	resetRegistry()
	applyRegistry()
}
