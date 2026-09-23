package captaincode

// User-named assignees: when the prompt says WHO should do the work, that is a
// requirement, not a hint.
//
// Live 2026-07-30: "Have claude, grok and codex review these examples" was
// planned as cursor+grok+glm - the director excluded itself (claude directs, and
// a director never self-assigns) and swapped codex for glm on scorecard grounds.
// Both substitutions are correct policy for a task the ROUTER chose the legs
// for, and wrong for one where the user did.

import (
	"regexp"
	"sort"
	"strings"
)

// A name is an ASSIGNMENT only when the hand-off sits next to it: a cue
// right before the group ("have claude, grok and codex …", "ask grok and
// claude to …") or a task verb right after it ("claude and grok review
// this"). A cue anywhere in the text was not enough (live 2026-09-16: a
// pasted issue - "when i run the doctor … the director is listed as Grok …
// from what claude has just told me" - read as "the user named claude and
// grok", and a /quality turn ran as a team of the two).
//
// assignCue is the hand-off word that precedes a group of names; a negation
// in the same window ("i dont have grok installed") cancels it.
var assignCue = regexp.MustCompile(`(?i)\b(have|ask|get|let|assign|use|run|route|give|tell|want|each|both)\b`)
var negationCue = regexp.MustCompile(`(?i)\b(don'?t|do not|not|no|never|without|cannot|can'?t)\b`)

// taskVerb is what a group of names does, right after the last name.
var taskVerb = regexp.MustCompile(`(?i)^[\s,]*(each|both|all|then)?[\s,]*(should|must|to|will|can)?\s*(review|reviews|critique|critiques|red[- ]?team|audit|audits|check|checks|draft|drafts|analy[sz]e|analy[sz]es|assess|verify|verifies|fix|fixes|write|writes|implement|implements|answer|answers|take|takes|handle|handles|work|works|look|looks|compare|test|tests|do|does)\b`)

// cueWindow is how far before a group's first name the hand-off may sit
// ("have both of " fits; a cue in the previous sentence does not).
const cueWindow = 28

// listGap joins two names of one group: ", ", " and ", " & ", " + ", "/",
// "or", "then" - or, within a few words, a sequence cue ("…review this,
// after that claude checks it").
var listGap = regexp.MustCompile(`(?i)^[\s,;]*(and|&|\+|/|or|then|and then|,\s*then|to)?[\s,]*$`)

// distinctiveLegs are names that are unambiguous in English prose. "free" is
// excluded: it is an ordinary word, so it only counts when another leg is named
// alongside it. "team"/"workflow" are modes, not legs.
var distinctiveLegs = []Leg{LegClaude, LegGrok, LegGrokMax, LegCodex, LegLuna, LegCursor, LegGLM, LegMiniMax, LegQwen, LegFrontier, LegCodexCLI}

// sequenceCue marks a hand-off between named legs: "grok, codex and THEN
// claude" is a pipeline, not a fan-out. /team has exactly one parallel stage and
// cannot express it (live 2026-07-30: the director claimed to have "sequenced
// cheap→deep" while running all three at once), so sequenced prose is routed
// through the workflow engine instead.
var sequenceCue = regexp.MustCompile(`(?i)\b(then|afterwards?|after that|after which|followed by|finally|lastly|next|once .{0,24} (is )?done)\b`)

// NamedStages groups the user's named legs into ordered stages: names separated
// by commas/"and" share a stage (parallel), a sequence cue starts a new one.
// Returns nil when the prompt is not naming assignees at all.
func NamedStages(task string) [][]Leg {
	hits := namedHits(task)
	if len(hits) < 2 {
		return nil
	}
	low := strings.ToLower(task)
	stages := [][]Leg{{hits[0].leg}}
	for i := 1; i < len(hits); i++ {
		between := low[hits[i-1].at:hits[i].at]
		if sequenceCue.MatchString(between) {
			stages = append(stages, []Leg{hits[i].leg})
			continue
		}
		stages[len(stages)-1] = append(stages[len(stages)-1], hits[i].leg)
	}
	return stages
}

// WorkflowFromNamedStages turns named stages into an executable workflow, with
// the user's own request as every stage's assignment (stage 2+ additionally
// receives the upstream outputs, and the executor's framing explains they are
// material to work on). Reports false when the request exceeds the language's
// limits - the caller must then fall back rather than silently truncate.
func WorkflowFromNamedStages(stages [][]Leg, request string) (Workflow, bool) {
	if len(stages) == 0 || len(stages) > MaxWorkflowStages {
		return Workflow{}, false
	}
	var wf Workflow
	runs := 0
	for _, legs := range stages {
		if len(legs) == 0 || len(legs) > MaxStageWidth {
			return Workflow{}, false
		}
		stage := WorkflowStage{}
		for _, l := range legs {
			stage.Legs = append(stage.Legs, WorkflowLeg{Leg: l, Prompt: strings.TrimSpace(request)})
			runs++
		}
		wf.Stages = append(wf.Stages, stage)
	}
	if runs > MaxWorkflowRuns {
		return Workflow{}, false
	}
	return wf, true
}

// NamedAssignees returns the legs the user explicitly asked to do the work, in
// the order named, or nil when the prompt is not naming assignees. Requires an
// assignment cue AND at least two named legs: one name plus a cue is too often
// a passing reference, and a single leg is already served by the /leg prefix.
func NamedAssignees(task string) []Leg {
	hits := namedHits(task)
	if len(hits) < 2 {
		return nil
	}
	out := make([]Leg, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.leg)
	}
	return out
}

type namedHit struct {
	at  int
	leg Leg
}

// namedHits finds every named leg, in prose order, when the prompt is assigning
// work to it (see the file comment: the hand-off must sit next to the names).
func namedHits(task string) []namedHit {
	if strings.TrimSpace(task) == "" {
		return nil
	}
	low := strings.ToLower(task)
	// Every occurrence of every name, in text order; at one offset the
	// longest name wins ("codex-cli" ⊃ "codex").
	type occ struct {
		at, end int
		leg     Leg
	}
	var occs []occ
	names := append(append([]Leg{}, distinctiveLegs...), LegFree)
	for _, leg := range names {
		for _, i := range wordIndexes(low, string(leg)) {
			occs = append(occs, occ{at: i, end: i + len(leg), leg: leg})
		}
	}
	sort.SliceStable(occs, func(i, j int) bool {
		if occs[i].at != occs[j].at {
			return occs[i].at < occs[j].at
		}
		return occs[i].end > occs[j].end
	})
	var uniq []occ
	for _, o := range occs {
		if len(uniq) > 0 && o.at < uniq[len(uniq)-1].end {
			continue // inside a longer name at the same place
		}
		uniq = append(uniq, o)
	}
	// Group names joined by list separators or a sequence cue; a group is an
	// assignment when a cue precedes its head or a task verb follows its tail.
	var hits []namedHit
	seen := map[Leg]bool{}
	for i := 0; i < len(uniq); {
		j := i
		for j+1 < len(uniq) {
			gap := low[uniq[j].end:uniq[j+1].at]
			if len(gap) <= 40 && (listGap.MatchString(gap) || sequenceCue.MatchString(gap)) {
				j++
				continue
			}
			break
		}
		if assignedBefore(low, uniq[i].at) || taskVerb.MatchString(low[uniq[j].end:]) {
			for k := i; k <= j; k++ {
				if !seen[uniq[k].leg] {
					seen[uniq[k].leg] = true
					hits = append(hits, namedHit{at: uniq[k].at, leg: uniq[k].leg})
				}
			}
		}
		i = j + 1
	}
	// "free" is only a leg name in the company of a real one.
	real := 0
	for _, h := range hits {
		if h.leg != LegFree {
			real++
		}
	}
	if real == 0 {
		return nil
	}
	return hits
}

// assignedBefore: a hand-off cue within cueWindow before offset at, in the
// same sentence, and no negation in that window ("i dont have grok").
func assignedBefore(low string, at int) bool {
	from := at - cueWindow
	if from < 0 {
		from = 0
	}
	win := low[from:at]
	if i := strings.LastIndexAny(win, ".!?\n"); i >= 0 {
		win = win[i+1:]
	}
	return assignCue.MatchString(win) && !negationCue.MatchString(win)
}

// wordIndexes finds every whole-word occurrence of needle in hay.
func wordIndexes(hay, needle string) []int {
	var out []int
	from := 0
	for {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return out
		}
		i += from
		end := i + len(needle)
		if (i == 0 || !isWordByte(hay[i-1])) && (end >= len(hay) || !isWordByte(hay[end])) {
			out = append(out, i)
		}
		from = end
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b == '-' || (b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
