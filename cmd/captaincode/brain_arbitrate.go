package main

// Who owns the final call when parallel workers disagree: the director.
//
// Workers in a team or a workflow stage run in their own git worktrees, so
// they cannot stomp each other's files while they work. Afterwards their
// changes have to land somewhere. Changes to different files land together.
// Changes to the SAME file are a disagreement, and captain does not splice
// them: the director reads each contender's report, files and evidence and
// picks one worker. That worker's changes land whole; the others' are set
// aside (their diffs stay on disk). If the director cannot be reached, or
// names a worker that was not in the running, nothing is applied and the
// feed says so - a guessed winner would be the harness making the call.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// doArbitrate asks the director whose changes land; stubbed in tests.
func (b *brain) doArbitrate(taskID, task string, contenders map[string]captaincode.Contender) (captaincode.Ruling, error) {
	if b.arbitrateFn != nil {
		return b.arbitrateFn(task, contenders)
	}
	b.mu.Lock()
	mgr := captaincode.Manager{Director: b.effectiveDirector(), Port: b.mgr.Port}
	b.mu.Unlock()
	// The ruling is coordination overhead billed to the task (M1.2).
	mgr.CallLabel, mgr.OnCall = "arbitration", b.chargeAux(taskID)
	return mgr.Arbitrate(task, contenders)
}

// settleConflict returns a conflicted candidate resolved on the director's
// pick, or the candidate unchanged when there is no ruling. texts holds each
// worker's report, keyed by manifest id.
func (b *brain) settleConflict(taskID, task string, ic captaincode.IntegrationCandidate, texts map[string]string, note func(string)) captaincode.IntegrationCandidate {
	if ic.Status != captaincode.IntegrationConflicted {
		return ic
	}
	contenders := map[string]captaincode.Contender{}
	for _, m := range ic.Manifests {
		for _, id := range ic.Contested() {
			if id == m.ManifestID() {
				contenders[id] = captaincode.Contender{Leg: captaincode.Leg(m.Leg), Text: texts[id],
					Files: m.ChangedFiles, Evidence: manifestEvidence(m)}
			}
		}
	}
	ruling, err := b.doArbitrate(taskID, task, contenders)
	if err != nil {
		note(fmt.Sprintf("[integration] no ruling on the conflict (%v) - nothing applied; the diffs are kept\n", err))
		return ic
	}
	resolved, err := ic.Resolve(ruling.Winner, ruling.Reason)
	if err != nil {
		note(fmt.Sprintf("[integration] ruling not usable (%v) - nothing applied; the diffs are kept\n", err))
		return ic
	}
	note(fmt.Sprintf("[integration] director's call: %s's changes land - %s\n", resolved.Winner, resolved.Ruling))
	if len(resolved.Dropped) > 0 {
		note(fmt.Sprintf("[integration] set aside: %s\n", strings.Join(resolved.Dropped, ", ")))
	}
	return resolved
}

// manifestEvidence is the objective evidence a worktree produced, in the
// words the director reads.
func manifestEvidence(m captaincode.PatchManifest) string {
	var parts []string
	for _, e := range []struct {
		name string
		ev   *captaincode.CheckEvidence
	}{{"gate", m.Check}, {"tests", m.TestEvidence}} {
		switch {
		case e.ev == nil:
		case e.ev.TimedOut:
			parts = append(parts, e.name+" timed out (no verdict)")
		case e.ev.Passed:
			parts = append(parts, e.name+" passed")
		default:
			parts = append(parts, e.name+" failed")
		}
	}
	return strings.Join(parts, ", ")
}

// rulingNote tells the synthesis or review what landed, so the answer does
// not describe changes that were set aside as if they were applied.
func rulingNote(ic captaincode.IntegrationCandidate, applyErr error) string {
	switch {
	case ic.Status == captaincode.IntegrationResolved && applyErr == nil:
		return fmt.Sprintf("Workers changed the same files. The director's call: only %s's changes were applied to the user's directory (%s); %s's changes were NOT applied. Deliver %s's approach and say in one line that the others were set aside.",
			ic.Winner, ic.Ruling, strings.Join(ic.Dropped, ", "), ic.Winner)
	case ic.Status == captaincode.IntegrationConflicted:
		return "Workers changed the same files and no ruling was made: NONE of their file changes were applied. Say so."
	case applyErr != nil:
		return fmt.Sprintf("The workers' file changes could NOT be applied (%v). Say so.", applyErr)
	}
	return ""
}

// runDiffDir is where captured worker diffs are kept for review.
func runDiffDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".captaincode", "runs", "diffs")
}

// teamSlot is what integrating one isolated team worker needs.
type teamSlot struct {
	id, dir, leg, text string
}

// integrateTeam captures each isolated team worker's changes, has the
// director settle any overlap, and applies what lands to the user's
// directory. It runs before the worktrees close.
func (b *brain) integrateTeam(ctx context.Context, userDir, rev, taskID, stageID, task string, slots []teamSlot, note func(string)) (captaincode.IntegrationCandidate, error) {
	var manifests []captaincode.PatchManifest
	texts := map[string]string{}
	for _, s := range slots {
		m, err := captaincode.CaptureManifest(ctx, s.dir, runDiffDir(), rev, taskID, stageID, "", s.leg)
		if err != nil {
			note(fmt.Sprintf("[integration] could not capture %s's changes: %v\n", s.id, err))
			continue
		}
		m.Worker = s.id
		manifests = append(manifests, m)
		texts[s.id] = s.text
	}
	ic := captaincode.BuildIntegrationCandidate(taskID, stageID, rev, manifests)
	if ic.Status == captaincode.IntegrationEmpty {
		return ic, nil
	}
	note(fmt.Sprintf("[integration] %s\n", ic.Summary()))
	for _, cf := range ic.Conflicts {
		note(fmt.Sprintf("[conflict] %s ← %s\n", cf.File, strings.Join(cf.Workers, ", ")))
	}
	ic = b.settleConflict(taskID, task, ic, texts, note)
	b.setLastIntegration(taskID, ic)
	if ic.Status != captaincode.IntegrationClean && ic.Status != captaincode.IntegrationResolved {
		return ic, nil
	}
	if err := captaincode.ApplyIntegrationCandidate(ctx, userDir, ic); err != nil {
		note(fmt.Sprintf("[integration] apply failed: %v\n", err))
		return ic, err
	}
	note("[integration] applied to workspace\n")
	return ic, nil
}
