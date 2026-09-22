package main

// Availability-aware director selection (live 2026-09-16).
//
// On a machine without the compiled default director's credential - a fresh
// install with no xAI login holds grok as director - every plan call failed to
// reach the leg and the turn fell back to the heuristic ladder, silently, on
// every prompt. "Capable" says a leg CAN judge; "available" says it can judge
// HERE. Only the second belongs on the helm, so the ladder is filtered by
// directorAvailability before any policy resolves a primary.

import (
	"sort"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// directorAvailability reports which director-capable legs can run on this
// machine right now: a CLI leg needs its binary on PATH; an opencode leg needs
// the opencode binary AND a configured provider credential (an OAuth in
// opencode's auth store or a provider block in opencode.jsonc). Computed once
// at brain start: PATH and credentials do not change under a running brain.
func directorAvailability() map[captaincode.Leg]bool {
	// The shared verdict (pkg/captaincode/readiness.go). This used to be a
	// second copy of doctor's rules, so it inherited doctor's bugs: it called
	// a logged-out claude and an unkeyed xai available, and the brain elected
	// a director that could not answer (2026-09-22).
	ready := captaincode.LegReadiness()
	out := map[captaincode.Leg]bool{}
	for _, s := range captaincode.Registry() {
		if !captaincode.DirectorCapable(s.ID) {
			continue
		}
		out[s.ID] = ready[s.ID].OK
	}
	return out
}

// directorCandidateSummary is the one startup line that explains the helm: the
// ready legs first, then the ones skipped and why the operator would care.
func directorCandidateSummary(avail map[captaincode.Leg]bool) string {
	var ready, missing []string
	for _, l := range captaincode.DirectorCandidates() {
		if avail[l.Leg] {
			ready = append(ready, string(l.Leg))
			continue
		}
		missing = append(missing, string(l.Leg))
	}
	sort.Strings(missing)
	if len(ready) == 0 {
		return "no director is runnable here - configure a provider or install a CLI (`captain doctor`)"
	}
	line := strings.Join(ready, ", ") + " can direct"
	if len(missing) > 0 {
		line += "; not runnable here: " + strings.Join(missing, ", ")
	}
	return line
}
