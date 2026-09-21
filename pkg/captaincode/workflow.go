package captaincode

// Captain Workflow Language (CWL) - one-line, user-authored routing topology.
// Spec: docs/WORKFLOW_LANGUAGE.md
//
//	/grok analyse @queue.ts > /cursor review it > /codex red-team it + /claude red-team it
//
// Stages run in sequence; legs inside a stage run in parallel; the director
// reviews the terminal stage and emits ONE aggregate (executed brain-side).
//
// The grammar's whole safety rests on one rule: a connector token counts as a
// connector ONLY when the next token is a leg prefix. "summarize this and
// explain why" therefore stays prose, and no existing prompt changes meaning.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// LegFrontier is the max-effort pseudo-leg (claude pinned to the strongest
// version with a maxed thinking budget). Legal in a workflow, not a member of
// AllLegs - it is a mode of the claude leg and records under it.
const LegFrontier Leg = "frontier"

// IsFrontier reports whether l is the frontier pseudo-leg (claude at max
// effort). IsFrontierClass (codex-cli.go) also covers the codex-cli leg.
func IsFrontier(l Leg) bool { return l == LegFrontier }

// Workflow limits. Exceeding one is a parse error, never a truncation: silent
// truncation reads as "we ran your workflow" when we did not.
const (
	MaxWorkflowStages = 4
	MaxStageWidth     = 4 // raised 3→4 (2026-08-25): 4-leg opinion panels are a real use
	MaxWorkflowRuns   = 8
)

// WorkflowLeg is one worker slot: a leg plus its instruction for that stage.
// An empty Prompt marks an INHERITED stage - the executor supplies a fixed
// "improve the previous output" instruction (spec §3.3/§4.3).
type WorkflowLeg struct {
	Leg    Leg
	Prompt string
	// Gate is an objective completion check: a shell command that must exit 0
	// for the stage to count as done (R2, PRIME_AGENT_NOTES.md). One bounded
	// retry with the gate's output; narration cannot pass a gate.
	Gate string
}

// WorkflowStage is one barrier: every leg in it runs concurrently.
type WorkflowStage struct{ Legs []WorkflowLeg }

// Workflow is an ordered list of stages.
type Workflow struct{ Stages []WorkflowStage }

// preferenceWords are the routing-preference prefixes. They are not legs: a
// workflow names its legs explicitly, so a preference inside one is a mistake
// worth reporting rather than ignoring.
var preferenceWords = map[string]bool{
	"quality": true, "q": true, "best": true,
	"speed": true, "fast": true, "save": true, "cheap": true,
	"oss": true, "open": true, "deterministic": true, "det": true, "adi": true, // pools (pool.go)
}

var (
	// A stage segment must begin with /<legname> at position 0.
	legHeadRe = regexp.MustCompile(`^/([A-Za-z][A-Za-z0-9-]*)\b[\s:]*`)
	// Connector boundaries. Symbol connectors may omit surrounding space; word
	// connectors may not (or "and" inside prose would split). Both only count
	// when a leg prefix follows - the rule that keeps prose intact. RE2 has no
	// lookahead, so the leg prefix is captured as group 2 and its start is the
	// boundary's end (the prefix itself is never consumed).
	seqBoundaryRe = regexp.MustCompile(`(?i)(\s*(?:->|>)\s*|\s+then\s+)(/[A-Za-z])`)
	parBoundaryRe = regexp.MustCompile(`(?i)(\s*(?:\+|&)\s*|\s+and\s+)(/[A-Za-z])`)
	// A NEWLINE before a leg prefix is a parallel connector too: writing one
	// task per line is how people naturally express "do these three things"
	// (live 2026-09-01 - a three-line prompt silently ran on cursor alone,
	// with the other two legs' lines handed to it as prose). Safe under the
	// same rule as every other connector: it only counts when a KNOWN leg
	// prefix follows, so ordinary prose that happens to start a line with a
	// slash stays prose.
	nlBoundaryRe = regexp.MustCompile(`(\n[ \t]*)(/[A-Za-z])`)
)

type boundary struct {
	start, end int
	parallel   bool
}

// IsWorkflowExpr reports whether s should be executed as a workflow: it starts
// with a leg prefix AND contains at least one connector at a leg boundary. A
// lone "/grok do X" is NOT a workflow - that is the existing forced-leg path.
func IsWorkflowExpr(s string) bool {
	s = strings.TrimSpace(s)
	if !legHeadRe.MatchString(s) {
		return false
	}
	if _, err := ParseWorkflow(s); err != nil {
		return false
	}
	return len(boundariesOf(s)) > 0
}

// LooksLikeWorkflow reports a workflow ATTEMPT - leg prefix + at least one
// connector-to-leg boundary - regardless of whether it parses. IsWorkflowExpr
// is false on parse errors, which made an over-limit expression silently
// collapse into a solo forced run (live 2026-08-25: a 4-leg panel ran as solo
// codex with "+ /cursor + /grok…" as prose). Dispatchers use this to report
// the parse error instead of swallowing it.
func LooksLikeWorkflow(s string) bool {
	s = strings.TrimSpace(s)
	return legHeadRe.MatchString(s) && len(boundariesOf(s)) > 0
}

// boundariesOf finds every connector position that qualifies under the
// connector rule, in input order.
func boundariesOf(s string) []boundary {
	var out []boundary
	// m = [full, full_end, g1, g1_end, g2, g2_end]; the boundary spans the
	// connector only, so it ends where the leg prefix begins (g2 start).
	for _, m := range seqBoundaryRe.FindAllStringSubmatchIndex(s, -1) {
		out = append(out, boundary{start: m[2], end: m[4]})
	}
	for _, m := range parBoundaryRe.FindAllStringSubmatchIndex(s, -1) {
		out = append(out, boundary{start: m[2], end: m[4], parallel: true})
	}
	for _, m := range nlBoundaryRe.FindAllStringSubmatchIndex(s, -1) {
		out = append(out, boundary{start: m[2], end: m[4], parallel: true})
	}
	// The token after the connector must be a REAL leg (or a preference prefix,
	// so a misplaced /quality can be reported instead of swallowed). Without
	// this, "compare /usr/bin/claude and /opt/homebrew/bin/claude" would split
	// on "and /opt" - paths and prose must never become topology.
	kept := out[:0]
	for _, b := range out {
		if !legLikeAt(s[b.end:]) {
			continue
		}
		// A WORD connector ("then", "and", a newline) before a name that no
		// stage can run - a preference, a pseudo-model - is prose about
		// captain, not topology ("then a /team then a /workflow … then
		// /speed", a demo-script request refused as workflow syntax,
		// 2026-09-19). A symbol connector (> + -> &) is deliberate syntax and
		// is still reported when its target is not a leg.
		if isWordConnector(s[b.start:b.end]) && !runnableLegAt(s[b.end:]) {
			continue
		}
		kept = append(kept, b)
	}
	out = kept
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	// Overlaps are impossible (disjoint operator sets), but a defensive filter
	// keeps a future operator from producing a nonsense split.
	filtered := out[:0]
	last := -1
	for _, b := range out {
		if b.start >= last {
			filtered = append(filtered, b)
			last = b.end
		}
	}
	// CWL is a one-line language: a stage's assignment never spans a
	// paragraph break. A connector whose preceding stage text holds a blank
	// line is inside pasted content - a "/cursor rework these sections…"
	// instruction followed by two paragraphs of website copy that quoted a
	// workflow example ran as cursor+grok>claude+codex (2026-09-21). The
	// blank line must lie entirely before the connector: "/grok do X\n\n/claude
	// do Y" keeps its newline connector, whose own newline is the second of
	// that pair.
	kept = filtered[:0]
	segStart := 0
	for _, b := range filtered {
		if loc := blankLineRe.FindStringIndex(s[segStart:b.start]); loc != nil && segStart+loc[1] <= b.start {
			continue
		}
		kept = append(kept, b)
		segStart = b.end
	}
	return kept
}

// blankLineRe: a paragraph break - two newlines with nothing but blanks between.
var blankLineRe = regexp.MustCompile(`\n[ \t]*\n`)

// ParseWorkflow parses a CWL expression. A single-leg expression is a valid
// one-stage workflow (useful for validation); use IsWorkflowExpr to decide
// whether to route something through the workflow executor at all.
func ParseWorkflow(s string) (Workflow, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Workflow{}, fmt.Errorf("empty workflow")
	}

	// Split into leg segments, remembering how each was joined to the previous.
	type segment struct {
		text     string
		parallel bool // joined to the previous segment with + / and
	}
	// Each boundary closes the current segment; the boundary's kind describes
	// how the NEXT segment joins to it.
	var segs []segment
	start, parallel := 0, false
	for _, b := range boundariesOf(s) {
		segs = append(segs, segment{text: s[start:b.start], parallel: parallel})
		start, parallel = b.end, b.parallel
	}
	segs = append(segs, segment{text: s[start:], parallel: parallel})

	var wf Workflow
	for i, seg := range segs {
		text := strings.TrimSpace(seg.text)
		head := legHeadRe.FindStringSubmatch(text)
		if head == nil {
			return Workflow{}, fmt.Errorf("stage %d must start with a leg prefix (e.g. /claude): %q", i+1, truncateStr(text, 40))
		}
		name := strings.ToLower(head[1])
		if preferenceWords[name] {
			return Workflow{}, fmt.Errorf("/%s is a preference prefix, not a leg - a workflow names its legs explicitly", name)
		}
		leg := Leg(name)
		if !KnownLeg(leg) && !IsFrontier(leg) {
			return Workflow{}, fmt.Errorf("unknown leg /%s (known: %s, frontier)", name, legNames())
		}
		if KnownLeg(leg) && !ServesTasks(leg) {
			return Workflow{}, fmt.Errorf("/%s is a decision leg - it answers questions, not stages; name a worker leg", name)
		}
		wl := WorkflowLeg{Leg: leg, Prompt: strings.TrimSpace(text[len(head[0]):])}
		// Trailing "gate: <command>" - the LAST marker wins so prose may
		// mention the word; everything after it is the command.
		if i := strings.LastIndex(wl.Prompt, "gate:"); i >= 0 {
			cmd := strings.TrimSpace(wl.Prompt[i+len("gate:"):])
			if cmd != "" {
				wl.Gate = cmd
				wl.Prompt = strings.TrimSpace(wl.Prompt[:i])
			}
		}
		// Bare leg inherits the assignment (2026-08-24): "/grok review X >
		// /codex" runs codex with the SAME prompt - plus, as for any later
		// stage, the upstream outputs. In parallel ("+ /codex") it mirrors
		// its stage's first leg. Gates are never inherited. At this point the
		// current segment hasn't been appended, so the last stage is the one
		// a sequential bare leg follows OR the one a parallel bare leg joins.
		if wl.Prompt == "" && len(wf.Stages) > 0 {
			wl.Prompt = wf.Stages[len(wf.Stages)-1].Legs[0].Prompt
		}
		if seg.parallel && len(wf.Stages) > 0 {
			last := &wf.Stages[len(wf.Stages)-1]
			if len(last.Legs)+1 > MaxStageWidth {
				return Workflow{}, fmt.Errorf("a stage runs at most %d legs in parallel", MaxStageWidth)
			}
			last.Legs = append(last.Legs, wl)
			continue
		}
		if len(wf.Stages)+1 > MaxWorkflowStages {
			return Workflow{}, fmt.Errorf("a workflow runs at most %d stages", MaxWorkflowStages)
		}
		wf.Stages = append(wf.Stages, WorkflowStage{Legs: []WorkflowLeg{wl}})
	}
	// Within each stage, bare legs adopt the stage's first non-empty prompt -
	// covers the natural "/codex + /cursor + /glm what do you think" style
	// where the task trails the leg list (2026-08-25).
	for si := range wf.Stages {
		fill := ""
		for _, l := range wf.Stages[si].Legs {
			if l.Prompt != "" {
				fill = l.Prompt
				break
			}
		}
		if fill != "" {
			for li := range wf.Stages[si].Legs {
				if wf.Stages[si].Legs[li].Prompt == "" {
					wf.Stages[si].Legs[li].Prompt = fill
				}
			}
		}
	}
	if wf.Runs() > MaxWorkflowRuns {
		return Workflow{}, fmt.Errorf("a workflow runs at most %d workers in total (this one has %d)", MaxWorkflowRuns, wf.Runs())
	}
	return wf, nil
}

// legLikeAt reports whether s begins with something meant as a leg reference:
// /word NOT continuing into a path. "/opt/homebrew/bin/claude" is a path and
// stays prose; "/gpt5" is a leg reference (an unknown one - reported by the
// parser rather than silently demoted to prose, so a typo'd leg name cannot
// quietly collapse a workflow into a single run).
func legLikeAt(s string) bool {
	m := legHeadRe.FindStringSubmatchIndex(s)
	if m == nil {
		return false
	}
	wordEnd := m[3] // end of the legname capture
	return wordEnd >= len(s) || s[wordEnd] != '/'
}

// runnableLegAt: s starts with a leg prefix a stage can run (or /frontier).
func runnableLegAt(s string) bool {
	m := legHeadRe.FindStringSubmatchIndex(s)
	if m == nil {
		return false
	}
	name := Leg(strings.ToLower(s[m[2]:m[3]]))
	return IsFrontier(name) || (KnownLeg(name) && ServesTasks(name))
}

// isWordConnector: "then" / "and" / a newline, as opposed to > + -> &.
func isWordConnector(c string) bool {
	t := strings.TrimSpace(c)
	return t == "" || strings.EqualFold(t, "then") || strings.EqualFold(t, "and")
}

func legNames() string {
	names := make([]string, 0, len(AllLegs))
	for _, l := range AllLegs {
		if ServesTasks(l) { // a stage needs a worker; the decision leg is not one
			names = append(names, string(l))
		}
	}
	return strings.Join(names, ", ")
}

// Runs is the number of worker runs the workflow will perform (the director
// review is counted separately - it is not a worker).
func (w Workflow) Runs() int {
	n := 0
	for _, st := range w.Stages {
		n += len(st.Legs)
	}
	return n
}

// HasGate reports whether any stage carries an objective completion gate.
func (w Workflow) HasGate() bool {
	for _, st := range w.Stages {
		for _, l := range st.Legs {
			if l.Gate != "" {
				return true
			}
		}
	}
	return false
}

// MultiStage reports whether this is a real workflow rather than one leg.
func (w Workflow) MultiStage() bool {
	return len(w.Stages) > 1 || (len(w.Stages) == 1 && len(w.Stages[0].Legs) > 1)
}

// String renders the canonical expression; it re-parses to an equal Workflow.
func (w Workflow) String() string {
	var sb strings.Builder
	for i, st := range w.Stages {
		if i > 0 {
			sb.WriteString(" > ")
		}
		for j, l := range st.Legs {
			if j > 0 {
				sb.WriteString(" + ")
			}
			sb.WriteString("/" + string(l.Leg))
			if l.Prompt != "" {
				sb.WriteString(" " + l.Prompt)
			}
			if l.Gate != "" {
				sb.WriteString(" gate: " + l.Gate)
			}
		}
	}
	return sb.String()
}

// Key is the canonical ledger key: stage order is significant, leg order
// inside a stage is not ("grok>claude+codex").
func (w Workflow) Key() string {
	parts := make([]string, 0, len(w.Stages))
	for _, st := range w.Stages {
		names := make([]string, 0, len(st.Legs))
		for _, l := range st.Legs {
			names = append(names, string(l.Leg))
		}
		sort.Strings(names)
		parts = append(parts, strings.Join(names, "+"))
	}
	return strings.Join(parts, ">")
}

// Legs lists every leg used, in stage order, deduplicated.
func (w Workflow) Legs() []Leg {
	seen := map[Leg]bool{}
	var out []Leg
	for _, st := range w.Stages {
		for _, l := range st.Legs {
			if !seen[l.Leg] {
				seen[l.Leg] = true
				out = append(out, l.Leg)
			}
		}
	}
	return out
}

// PlainRequest renders the user's instructions WITHOUT routing syntax, for the
// conversation the workers read. A worker that sees "/cursor review it"
// answers about an unknown slash command instead of doing the work.
func (w Workflow) PlainRequest() string {
	var lines []string
	for _, st := range w.Stages {
		for _, l := range st.Legs {
			if p := strings.TrimSpace(l.Prompt); p != "" && !containsLine(lines, p) {
				lines = append(lines, p)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func containsLine(ls []string, s string) bool {
	for _, l := range ls {
		if l == s {
			return true
		}
	}
	return false
}
