package captaincode

// `captain euclid learn` is the persona learning loop: one path that reads
// what no pass has seen yet (journal entries newer than the last distill,
// memory sections newer than the last crystallize), folds it into the persona
// registers, then reads its own output again. It stops when a pass has no new
// input left and no edits to make, so a current persona costs one call and a
// stale one costs a few calls in a row - not an unbounded review.
//
// It exists because a persona updated only inside a distillation of a busy
// repo stays behind: the explorer showed WISDOM, FAILURES and open-questions
// from 2026-09-22 days later, and INTUITION and AFFECT had never been written
// at all (2026-10-03).

import (
	"fmt"
	"sort"
	"strings"
)

const learnMarker = "[euclid learn]"

// The window one prompt reads. Both are bounded so a pass fits the context
// budget: a backlog is worked through by several passes, not one giant call.
const (
	learnJournalWindow  = 40
	learnMemoryWindow   = 12
	learnRegisterClips  = 4000
	learnDefaultPassCap = 4
	learnReviewCap      = 2
)

// learnFiles is the register set a learn pass may write. Every kind gets the
// same set: the persona is one shape, and the shared repo brain is never a
// learn target (it is written by `captain euclid share` on main).
var learnFiles = map[string]bool{
	"BRAIN.md":                   true,
	"WISDOM.md":                  true,
	"INTUITION.md":               true,
	"AFFECT.md":                  true,
	"memory/MEMORIES.md":         true,
	"memory/FAILURES.md":         true,
	"memory/decisions-ledger.md": true,
	"memory/open-questions.md":   true,
}

// LearnFiles is the register set a learn pass may write.
func LearnFiles(EuclidBrain) map[string]bool { return learnFiles }

// LearnInputs is what one pass has not seen: journal entries after the last
// distillation and memory sections after the last crystallize.
type LearnInputs struct {
	Journal         []JournalEntry
	Memories        []JournalEntry
	PendingJournal  int
	PendingMemories int
	Err             error
	base            map[string][]byte
	cursor          learnCursor
	journalKeys     []string
	memoryKeys      []string
}

// LearnInputsOf reads the two cursors. A read error on one side (no journal
// yet, no MEMORIES yet) leaves that side empty; the pass still runs.
func LearnInputsOf(brain EuclidBrain) LearnInputs {
	if memoryMCPEnabled() {
		return memoryLearnInputs(brain)
	}
	in, err := readLearnInputs(brain)
	in.Err = err
	return in
}

// learnRegisterNames is the order the registers are shown and the order the
// JSON allowed-file list is sent in - both follow the same list, so the model
// never writes a file the prompt never named.
var learnRegisterNames = []string{
	"BRAIN.md", "WISDOM.md", "INTUITION.md", "AFFECT.md",
	"memory/FAILURES.md", "memory/open-questions.md", "memory/decisions-ledger.md", "memory/MEMORIES.md",
}

// LearnPrompt builds one pass: the registers, then the new input, then the
// rules and the JSON envelope.
func LearnPrompt(brain EuclidBrain, in LearnInputs) string {
	if in.base == nil {
		in.base, _ = snapshotBrain(brain.Root, learnRegisterNames)
	}
	var allowed []string
	for _, n := range learnRegisterNames {
		if learnFiles[n] {
			allowed = append(allowed, n)
		}
	}
	var sb strings.Builder
	sb.WriteString(learnMarker + " " + distillMarker + " You are Euclid's learner for the brain at " + brain.Root + " (" + brain.Kind + ").\n")
	sb.WriteString("One pass over the persona. Fold what the inputs show into the registers, and take out what they supersede.\n")
	sb.WriteString("Rules: replace BRAIN's current state; merge new lessons into WISDOM on one line each and drop the ones they supersede; keep INTUITION to one line each tagged [open] and close a hunch the inputs resolve (proven -> WISDOM, failed -> FAILURES) by deleting it from INTUITION; replace AFFECT's Stance section when the runs change how sure to be; fold an incident into FAILURES as one guardrail line; append a question the inputs raise; append a dated memory line only from the journal of this brain. Never edit SOUL or VISION. No speculation, no secrets, no restating inputs: only what the inputs and the registers already say.\n")
	sb.WriteString("A pass with nothing to change is a valid answer: {\"summary\":\"\",\"edits\":[]} - it ends the loop.\n")
	sb.WriteString("Respond with JSON only: {\"summary\": \"one paragraph\", \"edits\": [{\"file\": \"<one of " + strings.Join(allowed, "|") + ">\", \"mode\": \"append|replace_section\", \"anchor\": \"## heading (replace_section only)\", \"text\": \"markdown\", \"why\": \"one line\"}]}\n\n")
	for _, n := range learnRegisterNames {
		if b, ok := in.base[n]; ok {
			sb.WriteString("=== current " + n + " ===\n" + clipText(string(b), learnRegisterClips) + "\n\n")
		}
	}
	if len(in.Journal) > 0 {
		sb.WriteString(fmt.Sprintf("=== new journal entries (%d) ===\n", len(in.Journal)))
		for _, e := range in.Journal {
			sb.WriteString(e.At.Format("2006-01-02 15:04") + " " + e.Kind + " " + e.Outcome + "\n" + strings.TrimSpace(e.Task) + "\n")
			if s := strings.TrimSpace(e.Summary); s != "" {
				sb.WriteString(s + "\n")
			}
			sb.WriteString("\n")
		}
	}
	if len(in.Memories) > 0 {
		sb.WriteString(fmt.Sprintf("=== memory sections not yet folded (%d) ===\n", len(in.Memories)))
		for _, e := range in.Memories {
			sb.WriteString(e.Summary + "\n\n")
		}
	}
	if len(in.Journal) == 0 && len(in.Memories) == 0 {
		sb.WriteString("=== new input ===\nnone: this pass only reviews the registers above\n\n")
	}
	return sb.String()
}

// ParseLearn reads a learn reply, dropping any file outside the persona set.
func ParseLearn(text string) (Distillation, error) {
	return parseEdits(text, learnFiles)
}

// LearnPass is one model call of a loop: what it read, what it changed.
type LearnPass struct {
	Journal  int      `json:"journal"`
	Memories int      `json:"memories"`
	Edits    []string `json:"edits"`
	Summary  string   `json:"summary"`
}

// LearnLoop runs passes over one brain until a pass has neither new input nor
// edits to make (or the pass cap hits). apply=false is a dry run: nothing is
// written and neither cursor moves, so the next real pass sees the same
// inputs again. run is one model call; the caller owns the leg and the cost.
func LearnLoop(brain EuclidBrain, max int, apply bool, run func(prompt string) (string, error)) ([]LearnPass, error) {
	if memoryMCPEnabled() {
		return learnMemoryMCP(brain, max, apply, run)
	}
	if max <= 0 {
		max = learnDefaultPassCap
	}
	if apply {
		if err := RecoverBrainEdits(brain); err != nil {
			return nil, err
		}
	}
	var passes []LearnPass
	reviews := 0
	for i := 0; i < max; i++ {
		in := LearnInputsOf(brain)
		if in.Err != nil {
			return passes, in.Err
		}
		text, err := run(LearnPrompt(brain, in))
		if err != nil {
			return passes, fmt.Errorf("learn pass %d: %w", i+1, err)
		}
		d, err := ParseLearn(text)
		if err != nil {
			return passes, fmt.Errorf("learn pass %d: %w", i+1, err)
		}
		if len(in.Journal) == 0 && len(in.Memories) == 0 {
			reviews++
		}
		pass := LearnPass{Journal: len(in.Journal), Memories: len(in.Memories), Summary: strings.TrimSpace(d.Summary)}
		for _, e := range d.Edits {
			pass.Edits = append(pass.Edits, e.File)
		}
		if apply {
			if _, err := ApplyLearnInputs(brain, in, d.Edits); err != nil {
				return passes, fmt.Errorf("learn pass %d: %w", i+1, err)
			}
		}
		passes = append(passes, pass)
		if !apply {
			break // a dry run is one pass: show what it would write, consume nothing
		}
		if len(pass.Edits) == 0 && pass.Journal == 0 && pass.Memories == 0 {
			break // converged: nothing read, nothing left to clean
		}
		if reviews >= learnReviewCap {
			break // two passes already edited registers with no input between them
		}
	}
	return passes, nil
}

// RenderLearnPass is one `learn: …` line: what the pass read and what it wrote.
func RenderLearnPass(label string, i, cap int, p LearnPass, converged bool) string {
	var in []string
	if p.Journal > 0 {
		in = append(in, fmt.Sprintf("%d journal entr%s", p.Journal, plural(p.Journal, "y", "ies")))
	}
	if p.Memories > 0 {
		in = append(in, fmt.Sprintf("%d memory section%s", p.Memories, plural(p.Memories, "", "s")))
	}
	if len(in) == 0 {
		in = append(in, "no new input")
	}
	files := append([]string(nil), p.Edits...)
	sort.Strings(files)
	uniq := files[:0]
	for _, f := range files {
		if len(uniq) == 0 || uniq[len(uniq)-1] != f {
			uniq = append(uniq, f)
		}
	}
	out := fmt.Sprintf("learn: %s · pass %d/%d: %s", label, i, cap, strings.Join(in, ", "))
	if len(uniq) > 0 {
		return out + fmt.Sprintf(" → %d edit(s) (%s)", len(p.Edits), strings.Join(uniq, ", "))
	}
	if converged {
		return out + " → no edits (converged)"
	}
	return out + " → edits to review on the next pass"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
