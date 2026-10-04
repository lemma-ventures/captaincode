package captaincode

import "strings"

// OpenRouter serves an open-weight model from dozens of hosts at different
// quantizations and, by default, weights them by price: a GLM-5.3 request
// lands on an unnamed 4-bit host as readily as on Z.AI's own fp8 one. A leg's
// Hosts (registry overlay) says, band by band, which hosts may serve it:
// first-party or 8-bit for quality work, the cheapest host for cheap work.

// OpenRouterHosts is the `provider` routing block for an OpenRouter request
// naming model: the Hosts entry of the active leg and band that run that
// model, quality's when the band has none. nil when no leg sets one.
//
// The request carries only the model id, so a leg that runs one model in
// several bands (kimi) is routed by the first band that matches, quality
// before cheap.
func OpenRouterHosts(model string) map[string]any {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	for _, l := range AllLegs {
		sp := specs[l]
		if len(sp.Hosts) == 0 {
			continue
		}
		for _, t := range []Tier{TierQuality, TierCheap, TierFrontier} {
			mm, ok := modelForEffort(l, false, tierEffort(t))
			if !ok || mm.Provider != "openrouter" || strings.ToLower(mm.Model) != model {
				continue
			}
			if h := sp.Hosts[t]; h != nil {
				return h
			}
			return sp.Hosts[TierQuality]
		}
	}
	return nil
}

// AnyOpenRouterHosts reports whether an active leg sets Hosts, so the proxy
// can skip parsing request bodies when none does.
func AnyOpenRouterHosts() bool {
	for _, l := range AllLegs {
		if len(specs[l].Hosts) > 0 {
			return true
		}
	}
	return false
}

// tierEffort is the effort that runs in band t (the inverse of TierOf).
func tierEffort(t Tier) Effort {
	switch t {
	case TierCheap:
		return EffortLow
	case TierFrontier:
		return EffortMax
	}
	return EffortMedium
}
