// Priors sync: refresh the COLD-START quality priors from an online benchmark
// (Artificial Analysis coding index) instead of letting hand-written numbers
// rot. Design boundary: benchmarks rank BASE MODELS, not legs-in-harness - a
// leaderboard can't see that qwen thrashes at 8k context on our box or that a
// worker wedges in a given opencode version. So synced numbers only replace
// the static priors that anchor routing before local scored runs accumulate;
// the live scorecards (locally graded runs) always dominate afterwards.
package captaincode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AAModel is one model row from the Artificial Analysis data API
// (GET https://artificialanalysis.ai/api/v2/data/llms/models, x-api-key auth).
type AAModel struct {
	Name              string
	Slug              string
	CodingIndex       float64
	IntelligenceIndex float64
	MathIndex         float64
	PriceIn, PriceOut float64 // USD per 1M tokens
	TokensPerSecond   float64
	Creator           string // model creator's display name (upgrade detection stays within a creator)
	Released          string // YYYY-MM-DD release date, when the feed has it
}

// legAAPatterns maps each leg to ordered lowercase substrings matched against
// an AA model's slug+name. Order matters: the first pattern that hits wins,
// so a leg's own model comes before the predecessor it reads until the feed
// scores it (codex: GPT-6 Sol, then 5.6 Sol). Legs with no pattern hit keep their
// hand-written prior (cursor's Composer and grok-build are rarely listed).
var legAAPatterns = map[Leg][]string{
	// Family fallbacks (substring, shortest slug wins) for when the exact
	// registry slug is absent from the feed - a renamed snapshot must not
	// silently drop a leg's prior.
	LegCodex:    {"gpt-6-sol", "gpt-5-6-sol"},   // fast mode shares Sol's score; reads 5.6 Sol until AA scores GPT-6 Sol
	LegLuna:     {"gpt-6-luna", "gpt-5-6-luna"}, // likewise 5.6 Luna until GPT-6 Luna is scored
	LegGrok:     {"grok-build", "grok build"},
	LegGrokMax:  {"grok-4-7", "grok-4-6"},
	LegClaude:   {"claude-opus-5"}, // the Opus 5 row when the feed lacks 5.5 or lists it unscored, as grok-max reads 4.6 until 4.7 is scored
	LegCodexCLI: {"gpt-6-astra"},
	LegGemini:   {"gemini-3-7-flash"},
	LegKimi:     {"kimi-k3"},
	LegGLM:      {"glm-5-3"},
	LegGPTOSS:   {"gpt-oss-120b"},
	LegDeepSeek: {"deepseek-v4-pro"},
	LegQwen:     {"qwen3-5-397b"},
	LegMiniMax:  {"minimax-m3"},
	LegFree:     {"nemotron-3-5-lightning"},
}

// aaPatterns returns the match patterns for a leg: the registry's AA slug
// first, then any legacy patterns.
func aaPatterns(leg Leg) []string {
	var out []string
	if s, ok := specs[leg]; ok && s.AA != "" {
		out = append(out, strings.ToLower(s.AA), strings.ReplaceAll(strings.ToLower(s.AA), "-", " "), strings.ReplaceAll(strings.ToLower(s.AA), "-", "."))
	}
	return append(out, legAAPatterns[leg]...)
}

// MatchAA finds the AA row for a leg's underlying model (first pattern that
// hits, scanning patterns in order). ok=false → no benchmark data for this
// leg; the caller keeps the existing prior.
//
// AA lists several rows per model (effort variants, dated snapshots,
// fallbacks): an EXACT slug match wins; among substring matches the shortest
// slug is the base variant and is preferred (2026-09-10: substring-first
// matched codex-cli to "GPT-6 Astra (Non-reasoning)" and claude to a
// "…Opus 4.8 Fallback" row). A row with either index ranks.
func MatchAA(models []AAModel, leg Leg) (AAModel, bool) {
	return matchAA(models, leg, func(m AAModel) bool { return m.CodingIndex > 0 || m.IntelligenceIndex > 0 })
}

// MatchAACoding is MatchAA for the priors: a row without a coding index yet
// (the feed lists a model days before its coding evals land - Opus 5.5 and
// Grok 4.7 on 2026-09-22) is passed over for the newest family member that
// has one, so a sync the day a model ships neither divides by zero nor
// zeroes a leg's prior.
func MatchAACoding(models []AAModel, leg Leg) (AAModel, bool) {
	return matchAA(models, leg, func(m AAModel) bool { return m.CodingIndex > 0 })
}

func matchAA(models []AAModel, leg Leg, usable func(AAModel) bool) (AAModel, bool) {
	for _, pat := range aaPatterns(leg) {
		for _, m := range models {
			if strings.ToLower(m.Slug) == pat && usable(m) {
				return m, true
			}
		}
	}
	for _, pat := range aaPatterns(leg) {
		var best AAModel
		found := false
		for _, m := range models {
			hay := strings.ToLower(m.Slug + " " + m.Name)
			if !strings.Contains(hay, pat) || !usable(m) || strings.Contains(hay, "fallback") {
				continue
			}
			if !found || len(m.Slug) < len(best.Slug) {
				best, found = m, true
			}
		}
		if found {
			return best, true
		}
	}
	return AAModel{}, false
}

// ProposePriors converts benchmark coding indices into the local 0-10 prior
// scale, anchored so the claude leg keeps its hand-written 9.5 - the scale is
// relative capability, not an absolute unit. Without the anchor model in the
// data the scale would be meaningless, so that's an error, not a guess.
func ProposePriors(models []AAModel) (map[Leg]float64, error) {
	anchor, ok := MatchAACoding(models, LegClaude)
	if !ok || anchor.CodingIndex == 0 {
		return nil, errors.New("claude (anchor model) not found in benchmark data with a coding index - cannot scale priors")
	}
	out := map[Leg]float64{}
	for _, leg := range AllLegs {
		m, ok := MatchAACoding(models, leg)
		if !ok {
			continue
		}
		p := m.CodingIndex / anchor.CodingIndex * qualityPriorDefaults[LegClaude]
		// round to one decimal; clamp to a sane band
		p = float64(int(p*10+0.5)) / 10
		if p < 3 {
			p = 3
		}
		if p > qualityPriorDefaults[LegClaude] {
			p = qualityPriorDefaults[LegClaude]
		}
		out[leg] = p
	}
	return out, nil
}

// DomainPriors is one leg's per-domain prior vector ("all" = overall).
type DomainPriors map[string]float64

// ProposeDomainPriors maps the leaderboard's indices onto per-domain priors
// (0-10, claude-anchored): code ← coding index, research/general/editorial ←
// intelligence index (the prose domains have no benchmark of their own),
// all ← the mean of the two. Legs with no row are skipped.
func ProposeDomainPriors(models []AAModel) (map[Leg]DomainPriors, error) {
	anchor, ok := MatchAACoding(models, LegClaude)
	if !ok || anchor.CodingIndex == 0 || anchor.IntelligenceIndex == 0 {
		return nil, errors.New("claude (anchor model) not found in benchmark data with both indices - cannot scale priors")
	}
	top := qualityPriorDefaults[LegClaude]
	scale := func(v, ref float64) float64 {
		if ref == 0 || v == 0 {
			return 0
		}
		p := v / ref * top
		p = float64(int(p*10+0.5)) / 10
		if p < 3 {
			p = 3
		}
		if p > top {
			p = top
		}
		return p
	}
	out := map[Leg]DomainPriors{}
	for _, leg := range AllLegs {
		m, ok := MatchAACoding(models, leg)
		if !ok {
			continue
		}
		code := scale(m.CodingIndex, anchor.CodingIndex)
		intel := scale(m.IntelligenceIndex, anchor.IntelligenceIndex)
		dp := DomainPriors{}
		if code > 0 {
			dp[string(DomainCode)] = code
		}
		if intel > 0 {
			dp[string(DomainResearch)] = intel
			dp[string(DomainGeneral)] = intel
			dp[string(DomainEditorial)] = intel
		}
		switch {
		case code > 0 && intel > 0:
			dp["all"] = float64(int((code+intel)/2*10+0.5)) / 10
		case code > 0:
			dp["all"] = code
		default:
			dp["all"] = intel
		}
		out[leg] = dp
	}
	return out, nil
}

// SaveDomainPriorOverrides writes the v2 override file.
func SaveDomainPriorOverrides(path string, priors map[Leg]DomainPriors) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(priors, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ── override persistence ──
//
// Synced priors live in ~/.captaincode/priors.json and are merged over the
// compiled defaults at startup. The file is written only by an explicit
// `captain priors sync --apply` - a human confirms every routing-behavior
// change; nothing rewrites priors silently.

// PriorOverridesPath is where synced priors are stored.
func PriorOverridesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".captaincode", "priors.json")
}

// SavePriorOverrides writes the override file (creating parent dirs).
func SavePriorOverrides(path string, priors map[Leg]float64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(priors, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadPriorOverridesFrom merges an override file into the active priors and
// returns how many legs were overridden. A missing file is the normal state.
func LoadPriorOverridesFrom(path string) (int, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// Two formats: v1 {"leg": 7.5} and v2 {"leg": {"all": 7.5, "code": 7.4, …}}.
	var raw map[Leg]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	n := 0
	for leg, v := range raw {
		if !KnownLeg(leg) {
			continue
		}
		var p float64
		if json.Unmarshal(v, &p) == nil {
			if p > 0 {
				qualityPriors[leg] = p
				n++
			}
			continue
		}
		var dp DomainPriors
		if json.Unmarshal(v, &dp) != nil {
			continue
		}
		if all, ok := dp["all"]; ok && all > 0 {
			qualityPriors[leg] = all
		}
		vec := map[Domain]float64{}
		for k, val := range dp {
			if k != "all" && val > 0 {
				vec[Domain(k)] = val
			}
		}
		if len(vec) > 0 {
			domainPriors[leg] = vec
		}
		n++
	}
	rebuildLadder()
	return n, nil
}

// LoadPriorOverrides loads from the default path (call at startup).
func LoadPriorOverrides() (int, error) {
	p := PriorOverridesPath()
	if p == "" {
		return 0, nil
	}
	return LoadPriorOverridesFrom(p)
}

// resetPriors restores compiled defaults (tests).
func resetPriors() {
	qualityPriors = map[Leg]float64{}
	domainPriors = map[Leg]map[Domain]float64{}
	for l, p := range qualityPriorDefaults {
		qualityPriors[l] = p
	}
	for l, s := range specs {
		if len(s.DomainPrior) > 0 {
			domainPriors[l] = s.DomainPrior
		}
	}
	rebuildLadder()
}
