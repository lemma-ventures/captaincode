package captaincode

import (
	"os"
	"strings"
)

// AutoUpgrade is what CAPTAIN_AUTO_UPGRADE allows while the brain is idle.
// The default is propose-only. A set value may write a model pin, run a CLI
// updater, or both. An unknown value stays off.
type AutoUpgrade struct {
	Models bool
	CLI    bool
}

// ParseAutoUpgrade reads CAPTAIN_AUTO_UPGRADE.
//
//	unset, 0, off, propose — flag only
//	1, on, models, idle    — write model pins when idle
//	cli                    — run each CLI's own updater when idle
//	all                    — both
func ParseAutoUpgrade(v string) AutoUpgrade {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes", "models", "idle":
		return AutoUpgrade{Models: true}
	case "cli":
		return AutoUpgrade{CLI: true}
	case "all", "models,cli", "cli,models":
		return AutoUpgrade{Models: true, CLI: true}
	default:
		return AutoUpgrade{}
	}
}

// AutoUpgradeFromEnv is the process value of CAPTAIN_AUTO_UPGRADE.
func AutoUpgradeFromEnv() AutoUpgrade {
	return ParseAutoUpgrade(os.Getenv("CAPTAIN_AUTO_UPGRADE"))
}

// Label is the short name doctor and the sidebar print.
func (m AutoUpgrade) Label() string {
	switch {
	case m.Models && m.CLI:
		return "all"
	case m.Models:
		return "models"
	case m.CLI:
		return "cli"
	default:
		return "off"
	}
}

const (
	HoldNotPin       = "not a model pin"
	HoldEnv          = "env pin wins over the overlay"
	HoldCoding       = "coding index drops by more than 1"
	HoldSpeed        = "speed drops by more than half"
	HoldCost         = "price rises more than 50%"
	HoldPriced       = "upgrade is priced and the current pin is not"
	HoldUnknownPrice = "upgrade price is unknown"
	HoldClosed       = "upgrade is a closed model and this leg is open-weights"
)

// UpgradePair is the feed row a leg reads and the newer family member
// UpgradeIn would flag. ok is false when the leg is current or unlisted.
func UpgradePair(models []AAModel, leg Leg) (cur, next AAModel, ok bool) {
	cur, ok = MatchAA(models, leg)
	if !ok {
		return AAModel{}, AAModel{}, false
	}
	up, ok := UpgradeIn(models, leg)
	if !ok {
		return cur, AAModel{}, false
	}
	for _, m := range models {
		if m.Slug == up.Slug {
			return cur, m, true
		}
	}
	return cur, AAModel{Slug: up.Slug, Name: up.Name, IntelligenceIndex: up.Perf, CodingIndex: up.Coding, Released: up.Created}, true
}

// HoldUpgrade reports why an idle auto-upgrade must not write this pin.
// An empty string means the pin keeps the same provider, stays in family,
// and does not lose coding score, halve speed, jump in price, or move an
// open-weights leg onto a closed model. A manual `captain upgrade --models
// --apply` may still write a held pin.
func HoldUpgrade(spec LegSpec, cur, next AAModel) string {
	if spec.Transport != "" && spec.Transport != TransportOpencode {
		return HoldNotPin
	}
	if spec.Provider == "" || spec.Model == "" {
		return HoldNotPin
	}
	prefix := spec.EnvPrefix()
	if strings.TrimSpace(os.Getenv(prefix+"_MODEL")) != "" || strings.TrimSpace(os.Getenv(prefix+"_PROVIDER")) != "" {
		return HoldEnv
	}
	if cur.CodingIndex > 0 && next.CodingIndex > 0 && next.CodingIndex+1 < cur.CodingIndex {
		return HoldCoding
	}
	if cur.TokensPerSecond > 0 && next.TokensPerSecond > 0 && next.TokensPerSecond*2 < cur.TokensPerSecond {
		return HoldSpeed
	}
	if !spec.Subscription {
		if spec.PriceIn == 0 && spec.PriceOut == 0 && (next.PriceIn > 0 || next.PriceOut > 0) {
			return HoldPriced
		}
		if (spec.PriceIn > 0 && next.PriceIn == 0) || (spec.PriceOut > 0 && next.PriceOut == 0) {
			return HoldUnknownPrice
		}
		if spec.PriceIn > 0 && next.PriceIn > spec.PriceIn*1.5 {
			return HoldCost
		}
		if spec.PriceOut > 0 && next.PriceOut > spec.PriceOut*1.5 {
			return HoldCost
		}
	}
	if spec.Open != nil && *spec.Open && !closedModel(cur.Slug+" "+cur.Name) && closedModel(next.Slug+" "+next.Name) {
		return HoldClosed
	}
	return ""
}

func closedModel(s string) bool {
	s = strings.ToLower(s)
	for _, w := range []string{"gpt", "claude", "gemini", "grok", "composer"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
