package captaincode

// Choosing the director by policy rather than by name (2026-09-16).
//
// `/captain claude` pins a leg. The three modes below pick one for you and
// keep picking as the world changes - the perf feed refreshes daily, usage
// and rate limits move by the hour:
//
//   - frontier: the highest-ranked leg that can direct.
//   - quality:  the best of tier 2 - the strongest leg NOT in the top band,
//     the "strong but not the flagship" choice.
//   - auto:     the least-used capable leg over a trailing window, so the
//     planning load rotates across subscriptions instead of exhausting one.
//
// A leg can direct when captain can run it as a judge with no tools: claude
// through claude -p, or an opencode-served model pin. The CLI agents (codex
// exec, cursor-agent) are agents, not judges - they get tools whether asked or
// not - so they never direct even when they rank first.

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DirectorMode is the policy under which the director is chosen.
type DirectorMode string

const (
	DirectorFixed    DirectorMode = ""         // a named leg
	DirectorFrontier DirectorMode = "frontier" // best ranked
	DirectorQuality  DirectorMode = "quality"  // best of tier 2
	DirectorAuto     DirectorMode = "auto"     // least used, capable
)

// ParseDirectorMode recognises the mode words; anything else is a leg name.
func ParseDirectorMode(word string) (DirectorMode, bool) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "frontier":
		return DirectorFrontier, true
	case "quality":
		return DirectorQuality, true
	case "auto":
		return DirectorAuto, true
	}
	return DirectorFixed, false
}

// DirectorCandidate is one leg that can direct, with the number it is ranked by.
type DirectorCandidate struct {
	Leg   Leg
	Perf  float64 // the perf index of the model the leg DIRECTS with (grok directs as grok-4.7)
	Model string
}

// DirectorCapable reports whether a leg can serve as the judge.
func DirectorCapable(l Leg) bool {
	spec, ok := Spec(l)
	if !ok || spec.Disabled {
		return false
	}
	switch spec.Transport {
	case TransportClaudeCLI, TransportOpencode:
		return true
	}
	return false
}

// directorPerf ranks a leg by the model it directs with: legs with a director
// override (grok → grok-4.7, codex → gpt-6-sol) are scored on that model, found
// through whichever registry leg pins it, else by the override's name in the
// feed; a leg without an override is scored as itself.
func directorPerf(l Leg) (float64, string) {
	if dm, ok := directorModels[l]; ok {
		for _, other := range AllLegs {
			if s, ok := Spec(other); ok && s.Model == dm.Model && s.Provider == dm.Provider {
				return perfOrPrior(other), dm.Model
			}
		}
		models, _, _ := PerfModels()
		want := strings.ToLower(strings.NewReplacer(".", "-", "/", "-").Replace(dm.Model))
		for _, m := range models {
			if strings.ToLower(m.Slug) == want {
				return m.IntelligenceIndex, dm.Model
			}
		}
		// Unlisted (a model released today): scored as the leg itself, but it
		// still directs with the override - naming the worker model here told
		// "/captain codex-cli" that codex directs as gpt-6-sol-fast (2026-09-22).
		return perfOrPrior(l), dm.Model
	}
	spec, _ := Spec(l)
	return perfOrPrior(l), spec.Model
}

// DirectorCandidates lists every leg that can direct, best ranked first. Two
// legs that direct with the same model on the same provider (grok and
// grok-max both direct as grok-4.7 on the SuperGrok credential) are one
// candidate - the first in ladder order - or auto would rotate between two
// names for one quota.
func DirectorCandidates() []DirectorCandidate {
	var out []DirectorCandidate
	seen := map[string]bool{}
	for _, l := range AllLegs {
		if !DirectorCapable(l) {
			continue
		}
		p, m := directorPerf(l)
		spec, _ := Spec(l)
		provider := spec.Provider
		if dm, ok := directorModels[l]; ok {
			provider = dm.Provider
		}
		if key := provider + "/" + m; seen[key] {
			continue
		} else {
			seen[key] = true
		}
		out = append(out, DirectorCandidate{Leg: l, Perf: p, Model: m})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Perf > out[j].Perf })
	return out
}

// judgeTwins names, for each agent leg, the judge that runs the same
// vendor's models on the same credential: codex exec is gpt-6-astra on the
// ChatGPT subscription, and the codex leg directs as gpt-6-sol on that same
// subscription with tools off. Cursor's Composer is served nowhere else, so
// it has no twin. Used to answer "/captain codex-cli" with the nearest thing
// that CAN direct instead of a flat refusal (2026-09-22).
var judgeTwins = map[Leg]Leg{LegCodexCLI: LegCodex}

// JudgeTwin returns the director-capable sibling of an agent leg.
func JudgeTwin(l Leg) (Leg, bool) {
	twin, ok := judgeTwins[l]
	if !ok || !DirectorCapable(twin) {
		return "", false
	}
	return twin, true
}

// DirectorModelOf is the model a leg would direct with, for messages that
// name it ("codex directs as gpt-6-sol").
func DirectorModelOf(l Leg) string {
	_, m := directorPerf(l)
	return m
}

// directorTierBand is how far below the best a leg can rank and still be
// tier 1 (the frontier band); tier 2 starts below it. CAPTAIN_DIRECTOR_TIER_BAND
// as a fraction, default 0.10.
func directorTierBand() float64 {
	if v := os.Getenv("CAPTAIN_DIRECTOR_TIER_BAND"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f < 1 {
			return f
		}
	}
	return 0.10
}

// DirectorPick is a mode's choice and the sentence that justifies it.
type DirectorPick struct {
	Leg    Leg
	Reason string
}

// PickFrontierDirector: the best-ranked capable leg.
func PickFrontierDirector() (DirectorPick, bool) {
	c := DirectorCandidates()
	if len(c) == 0 {
		return DirectorPick{}, false
	}
	return DirectorPick{Leg: c[0].Leg, Reason: fmt.Sprintf("frontier: %s ranks first (%s, index %.1f)", c[0].Leg, c[0].Model, c[0].Perf)}, true
}

// PickQualityDirector: the best leg of tier 2 - below the frontier band
// (within directorTierBand of the top) but the strongest of what remains.
// With one candidate, or none below the band, it is the frontier pick.
func PickQualityDirector() (DirectorPick, bool) {
	c := DirectorCandidates()
	if len(c) == 0 {
		return DirectorPick{}, false
	}
	floor := c[0].Perf * (1 - directorTierBand())
	for _, x := range c {
		if x.Perf < floor {
			return DirectorPick{Leg: x.Leg, Reason: fmt.Sprintf("quality: %s is the best of tier 2 (%s, index %.1f; tier 1 is within %.0f%% of %s at %.1f)",
				x.Leg, x.Model, x.Perf, directorTierBand()*100, c[0].Leg, c[0].Perf)}, true
		}
	}
	return DirectorPick{Leg: c[0].Leg, Reason: fmt.Sprintf("quality: no leg ranks below the tier-1 band; %s (index %.1f)", c[0].Leg, c[0].Perf)}, true
}

// DirectorUsage is a leg's share of recent work: wall-clock seconds it spent
// answering (directing, reviewing, working) inside the trailing window - the
// thing a subscription window actually meters.
type DirectorUsage map[Leg]time.Duration

// directorAutoFloor: a leg is "capable" for auto when its director index is
// at least this fraction of the best candidate's. CAPTAIN_DIRECTOR_AUTO_FLOOR,
// default 0.80 - today that is claude, glm, grok (as grok-4.7) and kimi.
func directorAutoFloor() float64 {
	if v := os.Getenv("CAPTAIN_DIRECTOR_AUTO_FLOOR"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			return f
		}
	}
	return 0.80
}

// DirectorWindow is the trailing window auto looks back over.
// CAPTAIN_DIRECTOR_WINDOW (Go duration, or Nd / Nw), default 14d.
func DirectorWindow() time.Duration {
	v := strings.TrimSpace(os.Getenv("CAPTAIN_DIRECTOR_WINDOW"))
	if v == "" {
		return 14 * 24 * time.Hour
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && strings.HasSuffix(v, "d") {
		return time.Duration(n) * 24 * time.Hour
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(v, "w")); err == nil && strings.HasSuffix(v, "w") {
		return time.Duration(n) * 7 * 24 * time.Hour
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return 14 * 24 * time.Hour
}

// PickAutoDirector chooses the least-used capable leg. cooling names legs
// whose rate-limit window is closed (they are the opposite of underused).
// current is the standing auto pick: it keeps the helm unless another capable
// leg has used less than 60% of its time - hysteresis, so the director does
// not flap between two legs a few seconds apart.
func PickAutoDirector(usage DirectorUsage, cooling map[Leg]time.Time, current Leg, now time.Time) (DirectorPick, bool) {
	c := DirectorCandidates()
	if len(c) == 0 {
		return DirectorPick{}, false
	}
	floor := c[0].Perf * directorAutoFloor()
	var pool []DirectorCandidate
	for _, x := range c {
		if x.Perf < floor {
			continue
		}
		if until, ok := cooling[x.Leg]; ok && now.Before(until) {
			continue
		}
		pool = append(pool, x)
	}
	if len(pool) == 0 {
		return DirectorPick{Leg: c[0].Leg, Reason: "auto: every capable leg is cooling; " + string(c[0].Leg) + " by rank"}, true
	}
	// Least used first; ties (two idle legs) go to the higher rank, which the
	// candidate order already encodes.
	sort.SliceStable(pool, func(i, j int) bool { return usage[pool[i].Leg] < usage[pool[j].Leg] })
	best := pool[0]
	if current != "" {
		for _, x := range pool {
			if x.Leg == current {
				if float64(usage[best.Leg]) >= 0.6*float64(usage[current]) {
					best = x // the standing pick is not clearly overused: keep it
				}
				break
			}
		}
	}
	names := make([]string, 0, len(pool))
	for _, x := range pool {
		names = append(names, fmt.Sprintf("%s %s", x.Leg, usage[x.Leg].Round(time.Minute)))
	}
	return DirectorPick{Leg: best.Leg, Reason: fmt.Sprintf("auto: least used over %s among capable legs (%s)", DirectorWindow().Round(time.Hour), strings.Join(names, ", "))}, true
}

// DefaultDirectorForMachine returns the compile Director if it can run here
// (its bin present), otherwise the first director-capable leg whose bin is
// present. Used by init and CLI startup so an absent leg is never the initial
// value of the global (doctor, direct runs, early manager calls).
func DefaultDirectorForMachine() Leg {
	for _, l := range []Leg{Director} {
		if canRunHereAsDirector(l) {
			return l
		}
	}
	for _, c := range DirectorCandidates() {
		if canRunHereAsDirector(c.Leg) {
			return c.Leg
		}
	}
	return Director
}

func canRunHereAsDirector(l Leg) bool {
	spec, ok := Spec(l)
	if !ok || !DirectorCapable(l) {
		return false
	}
	bin := "opencode"
	if spec.Transport == TransportClaudeCLI {
		bin = "claude"
	}
	_, err := exec.LookPath(bin)
	return err == nil
}
