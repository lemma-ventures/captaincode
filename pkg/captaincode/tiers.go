package captaincode

// Tiers: every worker leg answers three asks - cheap, quality and frontier -
// with the model its own credential offers for that band. A tier is the
// model; effort stays its own decision (effort.go) and rides on top.
//
//   - cheap: /save, /speed and trivial work (EffortLow). The leg's lighter
//     sibling on the same login: claude runs Sonnet, codex runs Luna, cursor
//     runs Composer, the OpenRouter legs their flash variants.
//   - quality: everything between (no tier entry: the leg's own Model).
//   - frontier: /frontier (EffortMax). The strongest model on that login:
//     claude's opus alias, codex's Astra, grok's 4.7.
//
// A leg whose provider ships one model has no entry for that band and runs
// its own model there at the tier's effort (kimi, minimax, the Hugging Face
// legs). The band follows the effort rather than the prefix so a verify
// climb from low to medium also climbs from the cheap model to the leg's
// own - a second attempt should not repeat the weaker model.

import (
	"os"
	"strings"
)

// Tier is the cost/quality band a request asks a leg for.
type Tier string

const (
	TierCheap    Tier = "cheap"
	TierQuality  Tier = "quality"
	TierFrontier Tier = "frontier"
)

// AllTiers orders the bands cheapest first.
var AllTiers = []Tier{TierCheap, TierQuality, TierFrontier}

// TierOf is the band an effort runs in: low is cheap, max is frontier,
// everything else (the transport default included) the leg's own model.
func TierOf(e Effort) Tier {
	switch e {
	case EffortLow:
		return TierCheap
	case EffortMax:
		return TierFrontier
	}
	return TierQuality
}

// tierModel is the model leg l runs at tier t when it is not the leg's own:
// <PREFIX>_CHEAP_MODEL / <PREFIX>_FRONTIER_MODEL first (CAPTAIN_CODEX_
// CHEAP_MODEL, CAPTAIN_CURSOR_FRONTIER_MODEL, …), else the registry's entry.
// "" means the leg's own model. CAPTAIN_CHEAP_TIER=0 keeps every leg on its
// own model at low effort (the cheap band is the one bare routing reaches
// without a prefix: trivial work runs at low).
func tierModel(l Leg, t Tier) string {
	if t == TierQuality {
		return ""
	}
	if t == TierCheap && os.Getenv("CAPTAIN_CHEAP_TIER") == "0" {
		return ""
	}
	sp, ok := specs[l]
	if !ok {
		return ""
	}
	if m := strings.TrimSpace(os.Getenv(sp.EnvPrefix() + "_" + strings.ToUpper(string(t)) + "_MODEL")); m != "" {
		return m
	}
	return sp.Tiers[t]
}

// TierModels lists the model each band runs on leg l, for `captain upgrade`
// and the docs table: quality is the leg's own model, a band with no entry
// repeats it.
func TierModels(l Leg) map[Tier]string {
	own := ModelIDAt(l, EffortMedium)
	out := map[Tier]string{TierQuality: own}
	for _, t := range []Tier{TierCheap, TierFrontier} {
		e := EffortLow
		if t == TierFrontier {
			e = EffortMax
		}
		out[t] = ModelIDAt(l, e)
	}
	return out
}
