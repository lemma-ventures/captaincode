package main

// Stocking the shelf (ROADMAP M3.9, pkg skills.go / skilluse.go).
//
// Selection happens POST PROMPT, at spawn: the task text is known, the
// triage has already answered class and domain, and each synced skill's
// `description` - the field the standard designs for exactly this - is
// matched against it. Captain writes nothing into the prompt; it decides
// which skills EXIST where the worker runs, and the worker's own runtime
// does the rest. The one exception is the always-on security-audit skill,
// which the security-first line names (securityContract, brain.go) because
// no task's words would ever select it.
//
// Where the shelf goes depends on the path:
//
//   - a parallel worker has its own M3.1 worktree, and the shelf dies with
//     it. Nothing touches the user's repository at all.
//   - a solo worker runs in the user's own directory. The shelf is staged
//     there for the turn, excluded from `git status` while it is there, and
//     removed when the turn ends. It is the dominant path, so a feature that
//     skipped it would be a feature that never ran.
//
// With nothing synced, stockShelf returns nil before doing any work: no
// triage, no directory, no listing - the run is byte-for-byte what it was
// before this file existed.

import (
	"fmt"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// stockShelf selects and stages the skills for one worker. The returned
// shelf must be Removed by the caller (defer at the dispatch site); a nil
// shelf is the normal, empty case and Remove on it is a no-op.
func (b *brain) stockShelf(dir, task string) *captaincode.Shelf {
	if dir == "" {
		return nil
	}
	cat := captaincode.Catalog()
	if len(cat) == 0 {
		return nil
	}
	tr := captaincode.TriageTask(task)
	picks := captaincode.SelectSkills(cat, task, tr.Class, tr.Domain, captaincode.SkillCap())
	if len(picks) == 0 {
		return nil
	}
	sh, err := captaincode.StageSkills(dir, picks)
	if err != nil {
		fmt.Printf("captain brain: skills not staged (%v) - the worker runs without a shelf\n", err)
		return nil
	}
	if sh == nil {
		return nil
	}
	fmt.Printf("captain brain: staged %d skill(s) for this worker: %s\n", len(sh.Stocked), joinNames(sh))
	return sh
}

func joinNames(sh *captaincode.Shelf) string {
	names := sh.Names()
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// recordShelf files what the shelf was worth: one row per stocked skill,
// carrying the director's grade when the run was scored and nothing but the
// stocking when it was not. The two counts are kept apart on purpose - a
// skill stocked forty times and used twice is selection's failure, and a
// report that only counted the graded rows would call it a good skill.
func (b *brain) recordShelf(taskID string, leg captaincode.Leg, class captaincode.Class, domain captaincode.Domain,
	stocked []string, grades []captaincode.SkillGrade) {
	if len(stocked) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ledger.GradeSkills(taskID, leg, class, domain, stocked, grades)
}

// teamShelfRefs is the union of what a stage's workers held. Each worker in
// an isolated stage gets its own copy of the same selection, so the union is
// that selection once - the director is shown one list, not the same list N
// times.
func teamShelfRefs(shelves []*captaincode.Shelf) []captaincode.SkillRef {
	seen := map[string]bool{}
	var out []captaincode.SkillRef
	for _, sh := range shelves {
		for _, r := range sh.Refs() {
			if seen[r.Name] {
				continue
			}
			seen[r.Name] = true
			out = append(out, r)
		}
	}
	return out
}

// skillRefNames is the names alone, for the usage rows.
func skillRefNames(refs []captaincode.SkillRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return out
}
