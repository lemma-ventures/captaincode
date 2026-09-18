package captaincode

// Refine (R4, PRIME_AGENT_NOTES.md): evidence-backed self-improvement with
// prime-agent's constraint discipline - supplemental OVERLAYS only (per-leg
// notes shown to the director, quality-prior nudges), small steps, snapshot
// before every apply, rollback. The compiled-in base prompt and descriptions
// are never touched.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RefineProposal is one round of overlay updates.
type RefineProposal struct {
	Notes     map[Leg]string  `json:"notes,omitempty"`  // ≤200 chars each, appended to the leg's menu line
	Priors    map[Leg]float64 `json:"priors,omitempty"` // absolute values; |Δ| vs current ≤ 0.5
	Rationale string          `json:"rationale,omitempty"`
}

const (
	maxNoteLen     = 220
	maxPriorNudge  = 0.5
	maxRefineEdits = 3 // per category per round - small steps by design
)

// ValidateRefineProposal enforces the discipline before anything is written.
func ValidateRefineProposal(p RefineProposal, currentPriors map[Leg]float64) error {
	if len(p.Notes) > maxRefineEdits || len(p.Priors) > maxRefineEdits {
		return fmt.Errorf("at most %d note edits and %d prior nudges per round", maxRefineEdits, maxRefineEdits)
	}
	for leg, note := range p.Notes {
		if !KnownLeg(leg) {
			return fmt.Errorf("unknown leg %q in notes", leg)
		}
		if len(note) > maxNoteLen {
			return fmt.Errorf("note for %s too long (%d chars, max %d)", leg, len(note), maxNoteLen)
		}
	}
	for leg, v := range p.Priors {
		if !KnownLeg(leg) {
			return fmt.Errorf("unknown leg %q in priors", leg)
		}
		if v < 1 || v > 10 {
			return fmt.Errorf("prior for %s out of range: %.1f", leg, v)
		}
		if cur, ok := currentPriors[leg]; ok {
			if d := v - cur; d > maxPriorNudge || d < -maxPriorNudge {
				return fmt.Errorf("prior nudge for %s too large (%.1f → %.1f; max ±%.1f per round)", leg, cur, v, maxPriorNudge)
			}
		}
	}
	return nil
}

func legNotesPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "leg_notes.json")
}

func refineBackupDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "backups", "refine")
}

// legNotes is the loaded overlay; buildPlanPrompt reads it.
var legNotes = map[Leg]string{}

// LoadLegNotes reads the overlay file (empty map when absent).
func LoadLegNotes() (map[Leg]string, error) {
	out := map[Leg]string{}
	body, err := os.ReadFile(legNotesPath())
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ReloadLegNotes refreshes the in-memory overlay (brain startup + after apply).
func ReloadLegNotes() (int, error) {
	n, err := LoadLegNotes()
	if err != nil {
		return 0, err
	}
	legNotes = n
	return len(n), nil
}

// ApplyRefine snapshots the current overlays, then merges the proposal in.
func ApplyRefine(p RefineProposal) error {
	// Snapshot both overlay files (whatever exists) before any write.
	stamp := time.Now().Format("20060102-150405")
	dir := filepath.Join(refineBackupDir(), stamp)
	for _, src := range []string{legNotesPath(), PriorOverridesPath()} {
		if body, err := os.ReadFile(src); err == nil {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, filepath.Base(src)), body, 0o644); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(legNotesPath()), 0o755); err != nil {
		return err
	}

	if len(p.Notes) > 0 {
		notes, err := LoadLegNotes()
		if err != nil {
			notes = map[Leg]string{}
		}
		for leg, note := range p.Notes {
			if strings.TrimSpace(note) == "" {
				delete(notes, leg)
				continue
			}
			notes[leg] = strings.TrimSpace(note)
		}
		body, _ := json.MarshalIndent(notes, "", "  ")
		if err := os.WriteFile(legNotesPath(), body, 0o644); err != nil {
			return err
		}
	}
	if len(p.Priors) > 0 {
		cur := map[Leg]float64{}
		if body, err := os.ReadFile(PriorOverridesPath()); err == nil {
			_ = json.Unmarshal(body, &cur)
		}
		for leg, v := range p.Priors {
			cur[leg] = v
		}
		body, _ := json.MarshalIndent(cur, "", "  ")
		if err := os.WriteFile(PriorOverridesPath(), body, 0o644); err != nil {
			return err
		}
	}
	_, _ = ReloadLegNotes()
	return nil
}

// RollbackRefine restores the most recent snapshot (missing files in the
// snapshot mean the overlay did not exist then - it is removed).
func RollbackRefine() error {
	snaps, _ := filepath.Glob(filepath.Join(refineBackupDir(), "*"))
	if len(snaps) == 0 {
		return fmt.Errorf("no refine snapshots to roll back to")
	}
	sort.Strings(snaps)
	latest := snaps[len(snaps)-1]
	for _, dst := range []string{legNotesPath(), PriorOverridesPath()} {
		src := filepath.Join(latest, filepath.Base(dst))
		if body, err := os.ReadFile(src); err == nil {
			if err := os.WriteFile(dst, body, 0o644); err != nil {
				return err
			}
		} else {
			_ = os.Remove(dst)
		}
	}
	_ = os.RemoveAll(latest) // a rollback consumes its snapshot
	_, _ = ReloadLegNotes()
	return nil
}

// RefineEvidence is the per-leg evidence block the proposer reasons over.
type RefineEvidence struct {
	Stats    map[Leg]LegStats
	ByDomain map[Leg]map[string]struct {
		N, Scored int
		Q         float64
	}
	Notes  map[Leg]string
	Priors map[Leg]float64
}

// Refine asks the director for at most maxRefineEdits small overlay updates,
// grounded in the evidence, and validates the reply.
func (m Manager) Refine(ev RefineEvidence) (RefineProposal, error) {
	var sb strings.Builder
	sb.WriteString(`You maintain a model-routing scorecard's SUPPLEMENTAL notes. Propose at most 3 note edits and 3 prior nudges (|change| <= 0.5), ONLY where the evidence below clearly supports them (>=5 scored runs, or a repeated failure pattern). Notes are one dense line (<=200 chars) a router will read when choosing a model: capability + reliability caveats, no fluff. Empty note string deletes a note.
Reply with STRICT JSON only: {"notes":{"<leg>":"..."},"priors":{"<leg>":<1-10>},"rationale":"<=200 chars"}

Evidence per leg (quality 0-10; fails are PROVIDER faults only):
`)
	legs := make([]string, 0, len(ev.Stats))
	for l := range ev.Stats {
		legs = append(legs, string(l))
	}
	sort.Strings(legs)
	for _, ls := range legs {
		l := Leg(ls)
		s := ev.Stats[l]
		fmt.Fprintf(&sb, "- %s: q=%.1f (%d scored / %d runs) fails=%d harness_fails=%d avg_s=%d prior=%.1f note=%q\n",
			l, s.AvgQuality, s.Scored, s.N, s.Fails, s.HarnessFails, s.AvgDurationMs/1000, ev.Priors[l], ev.Notes[l])
		if doms := ev.ByDomain[l]; len(doms) > 0 {
			keys := make([]string, 0, len(doms))
			for d := range doms {
				keys = append(keys, d)
			}
			sort.Strings(keys)
			for _, d := range keys {
				v := doms[d]
				fmt.Fprintf(&sb, "    %s: q=%.1f (%d scored / %d runs)\n", d, v.Q, v.Scored, v.N)
			}
		}
	}
	var p RefineProposal
	if err := m.directorJSON(sb.String(), &p); err != nil {
		return RefineProposal{}, err
	}
	if err := ValidateRefineProposal(p, ev.Priors); err != nil {
		return RefineProposal{}, fmt.Errorf("director's proposal rejected: %w", err)
	}
	return p, nil
}
