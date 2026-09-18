package captaincode

import "sort"

// Benchmark-grounded quality priors (0-10) per worker leg, seeding the
// director's scorecards until local assessments accumulate - cold-start local
// n is small and noisy, so an unseeded scorecard tells the director nothing.
// Priors are starting points, not final constants (ROUTING.md §2): local
// assessed quality overtakes them as scored runs accumulate (BlendedQuality).
//
// Sources (checked 2026-07-16):
//   - claude (Claude Fable 5 via claude -p): ~95% SWE-bench Verified - the
//     strongest available worker, full stop.
//   - cursor (Composer 2.5): ~79.8% SWE-bench Multilingual, #3 on the
//     Artificial Analysis Coding Agent Index at ~1/10th frontier cost -
//     near-frontier agentic coding, NOT a weak overflow lane.
//   - grok (grok-build-0.1): ~70.8% SWE-bench Verified (xAI internal
//     harness); fast, cheap, daily-reset quota.
//   - codex (gpt-5.3-codex-spark): latency-optimized (~1000 tok/s, Cerebras);
//     ~56% SWE-bench Pro vs standard Codex 5.3's ~72% - excellent for
//     interactive edits, weak on multi-step architecture/stateful debugging.
//     Tied with grok here: cross-benchmark mapping (Pro vs Verified) is too
//     uncertain to split them.
//   - free (deepseek-v4-flash): self-reported 79% SWE-bench Verified with no
//     independent verification (AA Intelligence Index 40) - discounted.
//   - qwen (Scaleway self-hosted coder): solid mid-tier coder; prior between
//     free and subscription legs until local scores accumulate.
//   - glm (z-ai/glm-5.2 via NVIDIA NIM): near-frontier OSS agentic coder;
//   - minimax (minimaxai/minimax-m3 via NIM): strong OSS agentic, a notch below glm;
//     prior below Cursor/Claude until graded runs dominate.
//
// qualityPriorDefaults are the compiled hand-written anchors; qualityPriors is
// the ACTIVE set - defaults merged with ~/.captaincode/priors.json overrides
// (see priors_sync.go / `captain priors sync`).
// qualityPriorDefaults / qualityPriors are populated from the registry
// (applyRegistry); priors.json overrides (LoadPriorOverrides) and per-domain
// vectors (domainPriors) layer on top.
var (
	qualityPriorDefaults = map[Leg]float64{}
	qualityPriors        = map[Leg]float64{}
	domainPriors         = map[Leg]map[Domain]float64{}
)

// QualityPrior returns the benchmark prior for a leg (0 when unknown).
func QualityPrior(l Leg) float64 { return qualityPriors[l] }

// QualityPriorFor returns the per-domain prior when one is known (synced from
// the leaderboard's per-index scores, or set in the registry), else the
// overall prior - coding indices mis-rank models for prose (MM35).
func QualityPriorFor(l Leg, d Domain) float64 {
	if dp, ok := domainPriors[l]; ok {
		if p, ok := dp[d]; ok && p > 0 {
			return p
		}
	}
	return QualityPrior(l)
}

// BlendedQualityFor blends the domain prior with local scored runs IN THAT
// DOMAIN (falling back to the leg's overall local average when the domain
// has no scored runs yet).
func BlendedQualityFor(l Leg, s LegStats, d Domain) float64 {
	p := QualityPriorFor(l, d)
	if ds, ok := s.ByDomain[d]; ok && ds.Scored > 0 {
		return (p*priorWeight + ds.AvgQuality*float64(ds.Scored)) / float64(priorWeight+ds.Scored)
	}
	if s.Scored == 0 {
		return p
	}
	return (p*priorWeight + s.AvgQuality*float64(s.Scored)) / float64(priorWeight+s.Scored)
}

// priorWeight is how many pseudo-observations the benchmark prior counts as:
// a handful of local scores nudge the blend, a scorecard's worth dominates it.
const priorWeight = 5

// BlendedQuality merges the benchmark prior with locally assessed quality via
// shrinkage, so the director always routes on a meaningful quality number:
// pure prior at n=0, mostly local evidence once Scored >> priorWeight.
func BlendedQuality(l Leg, s LegStats) float64 {
	p := QualityPrior(l)
	if s.Scored == 0 {
		return p
	}
	return (p*priorWeight + s.AvgQuality*float64(s.Scored)) / float64(priorWeight+s.Scored)
}

// TopQuality returns the n strongest legs from order by blended quality
// (benchmark prior + local scored runs), strongest first. Used by /quality:
// the user asked for the best model, so the menu contains only the best -
// a prompt-level instruction alone lets the director rationalize a budget
// leg off its per-class averages (observed live 2026-07-25).
//
// A leg that keeps failing is not the best whatever its prior: an
// unreliable leg (value.go Unreliable - ≥2 provider faults, a third or more
// of its runs) sorts after every reliable one, so it only makes the menu
// when nothing reliable is left. Live 2026-09-18: kimi (prior 8.0, 11 NIM
// stalls, 0 successes) headed every /quality menu and each simple prompt
// waited four minutes for the stall before rerouting.
func TopQuality(order []Leg, stats map[Leg]LegStats, n int) []Leg {
	out := append([]Leg(nil), order...)
	sort.SliceStable(out, func(i, j int) bool {
		ui, uj := Unreliable(stats[out[i]]), Unreliable(stats[out[j]])
		if ui != uj {
			return !ui
		}
		return BlendedQuality(out[i], stats[out[i]]) > BlendedQuality(out[j], stats[out[j]])
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
