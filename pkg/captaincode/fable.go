package captaincode

// Fable 5.2 is a model of the claude leg, not a leg (2026-11-10): it runs
// through the same claude -p login, on the claude-cli row of the sidebar,
// and shows on the run line (claude-cli:fable-5.2@effort:max).
//
// Captain picks it for high-complexity work at max effort - the /frontier
// configuration of the claude leg on a task the triage rates high. Anything
// else stays on Opus (or Sonnet at low effort). /fable after /frontier or
// /claude-cli pins it for one turn; a bare /fable reads as /claude /fable.
//
// Fable is metered on the extra-usage spend cap: on 2026-09-22 "You've hit
// your monthly spend limit" refused /frontier 41 times in three days while
// opus answered. A Fable run refused by a limit reruns the same turn on Opus
// and keeps Fable closed (FableCooldownKey) until the limit resets.
// CAPTAIN_FABLE=0 turns the automatic pick off; /fable still works.

import (
	"os"
	"regexp"
	"strings"
)

// FableCooldownKey is the ledger cooldown that closes Fable after a limit.
// Not a leg: the claude leg stays open on Opus.
const FableCooldownKey Leg = "claude:fable"

// FableSlug is Fable 5.2's Artificial Analysis row.
const FableSlug = "claude-fable-5-2"

// FableModel is the claude -p --model for Fable: CAPTAIN_FABLE_MODEL, else
// claude-fable-5-2.
func FableModel() string {
	if m := strings.TrimSpace(os.Getenv("CAPTAIN_FABLE_MODEL")); m != "" {
		return m
	}
	return "claude-fable-5-2"
}

// FableAuto reports whether captain may pick Fable on its own.
func FableAuto() bool { return strings.TrimSpace(os.Getenv("CAPTAIN_FABLE")) != "0" }

// FableFor reports whether the claude leg runs Fable automatically: a
// high-complexity task at max effort.
func FableFor(c Class, e Effort) bool {
	return FableAuto() && c == ClassHigh && e == EffortMax
}

var fableWordRe = regexp.MustCompile(`(?i)(^|\s)/fable\b`)

// MidPromptFable reports whether the turn names /fable as a word (not a
// path such as /fable/notes.md).
func MidPromptFable(task string) bool {
	for _, m := range fableWordRe.FindAllStringIndex(task, -1) {
		if m[1] < len(task) && task[m[1]] == '/' {
			continue
		}
		return true
	}
	return false
}

// FablePerf is Fable's performance score: the live or cached feed row, else
// the compiled snapshot (65 on 2026-11-10).
func FablePerf() (float64, bool) {
	models, _, _ := PerfModels()
	for _, rows := range [][]AAModel{models, perfSnapshot.rows()} {
		for _, m := range rows {
			if strings.EqualFold(m.Slug, FableSlug) {
				return rowOf(m).Perf, true
			}
		}
	}
	return 0, false
}
