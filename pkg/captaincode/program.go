package captaincode

// Captain Workflow Language, level 2: programs (2026-10-05).
// Spec: docs/WORKFLOW_LANGUAGE.md §11.
//
//	/frontier write the spec > (/repeat 4 /codex implement the next item gate: go test ./...) > /claude review the diff
//	/repeat 10 /codex fix the failing tests until: go test ./... || /claude explain why they still fail
//
// A program composes whole TURNS. A turn is anything captain already runs as
// one typed prompt: a CWL workflow (legs joined by > and +), a /team task, a
// lane turn (/quality …), or plain text in a loop body. A program adds four
// things a workflow cannot say:
//
//   - parentheses, which group steps;
//   - chains of whole turns: `/team … > /claude …` (a failed step stops the chain);
//   - loops inside chains: `/repeat [N] … until: <command>`;
//   - fallbacks: `A || B` runs B only when A failed.
//
// The connector rule of CWL still holds: an operator counts only when a
// command head follows it, and only the symbols `>`, `->` and `||` join
// whole turns. The words `then` and `and` stay CWL's, inside a run of legs,
// so prose that mentions /team or /repeat never becomes topology.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Program limits. A program counts turns and nesting, not workers: each turn
// keeps its own limits (a workflow turn still runs at most 8 workers).
const (
	MaxProgramSteps = 8 // steps in one chain, alternatives in one fallback
	MaxProgramDepth = 3 // nesting: each group and each loop is one level
)

// ProgramKind is the shape of one program node.
type ProgramKind string

const (
	ProgramTurn  ProgramKind = "turn"  // one typed turn
	ProgramChain ProgramKind = "chain" // steps in order; a failed step fails the chain
	ProgramAlt   ProgramKind = "alt"   // alternatives in order; the first success ends it (||)
	ProgramLoop  ProgramKind = "loop"  // /repeat: rounds of one body
)

// TurnKind says how a turn is dispatched.
type TurnKind string

const (
	TurnWorkflow TurnKind = "workflow" // one leg, or legs joined by > and + (CWL)
	TurnTeam     TurnKind = "team"     // /team: the director plans an ensemble
	TurnLane     TurnKind = "lane"     // a lane word and a task (/quality …): routed
	TurnAuto     TurnKind = "auto"     // plain text in a loop body: routed
)

// Program is a parsed program node.
type Program struct {
	Kind ProgramKind `json:"kind"`
	// Turn: the text dispatched as one typed turn. A workflow turn keeps its
	// own gate: in the text (the workflow executor owns it); a team, lane or
	// auto turn has its gate in Gate, checked by the program runner.
	Text string   `json:"text,omitempty"`
	Turn TurnKind `json:"turn,omitempty"`
	Gate string   `json:"gate,omitempty"`
	// Chain, Alt, Loop: the children. A loop has exactly one, its body.
	Steps []Program `json:"steps,omitempty"`
	// Loop: rounds (0 = until stopped or the turn budget ends) and the exit
	// check (a shell command; exit 0 ends the loop).
	Count int    `json:"count,omitempty"`
	Until string `json:"until,omitempty"`
	// Group: the user wrote this node in parentheses.
	Group bool `json:"group,omitempty"`
}

var (
	// stepPrefixRe is what keeps a scan at a step start: lane words and a
	// /repeat head with its count. A "(" right after it opens a group.
	stepPrefixRe = regexp.MustCompile(`(?i)^(?:/(?:quality|q|best|speed|fast|save|cheap|oss|open|deterministic|det|adi|noslop|frontier)\b[ \t]*)*(?:/repeat\b[ \t]*(?:\d+[ \t]*)?)?(?:/(?:quality|q|best|speed|fast|save|cheap|oss|open|deterministic|det|adi|noslop|frontier)\b[ \t]*)*`)
	loopHeadRe   = regexp.MustCompile(`(?i)^/repeat\b[ \t]*`)
)

// repeatControlWords are /repeat's own commands, never a loop body.
var repeatControlWords = map[string]bool{
	"status": true, "stop": true, "finish": true, "wrapup": true, "abort": true, "show": true, "watch": true,
}

// ParseProgram reads a typed turn as a program. ok=false with no error means
// the text uses no program syntax: route it as before (a leg, a workflow, a
// plain /repeat). ok=true with an error is a program attempt that does not
// parse: report it, never run it as prose.
func ParseProgram(s string) (p Program, ok bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" || !programHeadAt(s) {
		return Program{}, false, nil
	}
	attempt := usesProgramSyntax(s)
	p, err = parseAlt(s, 0)
	if err != nil {
		if attempt {
			return Program{}, true, err
		}
		return Program{}, false, nil
	}
	if !p.NeedsRunner() {
		return p, false, nil
	}
	if p.usesOpenShell() {
		return Program{}, true, fmt.Errorf("/openshell cannot be a step of a program: a sandbox's answer may not feed a host step, and an until: or gate: check runs on your checkout, which a sandbox never changes - run /openshell as its own turn (a plain `/repeat N /openshell …` still works)")
	}
	return p, true, nil
}

// NeedsRunner reports whether the program needs the program runner. A plain
// turn and a plain /repeat of one turn run on the paths that existed before.
func (p Program) NeedsRunner() bool {
	switch p.Kind {
	case ProgramTurn:
		return p.Group || p.Gate != ""
	case ProgramLoop:
		body := p.Steps[0]
		return p.Group || p.Until != "" || body.Kind != ProgramTurn || body.NeedsRunner()
	}
	return true
}

// programHeadAt: s starts the way a program can start - a group, or a
// /word that is a leg, /frontier, /team, /repeat or a lane word.
func programHeadAt(s string) bool {
	if strings.HasPrefix(s, "(") {
		return commandHeadAt(s[1:])
	}
	switch k, _ := classifyHead(s); k {
	case headLeg, headTeam, headLoop, headLane:
		return true
	}
	return false
}

// usesProgramSyntax reports a program attempt from the top-level shape: a
// group, a fallback, a loop with until:, or a chain that joins whole turns
// other than legs. A text that is only legs and a CWL parse error is not an
// attempt: the workflow path reports its own error, as before.
func usesProgramSyntax(s string) bool {
	sc, err := scanTop(s)
	if err != nil || len(sc.groups) > 0 {
		return true
	}
	for _, op := range sc.ops {
		if op.alt {
			return true
		}
	}
	segs := segmentsOf(s, sc.ops)
	for _, seg := range segs {
		k, _ := classifyHead(strings.TrimSpace(s[seg[0]:seg[1]]))
		if k == headLoop && sc.until >= 0 {
			return true
		}
		if len(segs) > 1 && (k == headTeam || k == headLoop || k == headLane) {
			return true
		}
	}
	return false
}

// ── scanning ────────────────────────────────────────────────────────────────

// progOp is one top-level operator: the connector span (the head after it is
// never consumed).
type progOp struct {
	start, end int
	alt        bool // || (else > or ->)
}

type scanResult struct {
	ops    []progOp
	groups [][2]int // step-start groups: open and close index
	until  int      // index of the last top-level "until:", -1 if none
}

// scanTop finds the top-level structure of s: operators outside groups and
// backtick code, before a command head, and not past a paragraph break since
// the previous operator (pasted content is never topology).
func scanTop(s string) (scanResult, error) {
	sc := scanResult{until: -1}
	atStart := true // only blanks seen since the step began
	plainFrom := 0  // the paragraph check starts here: after the last op or group
	for i := 0; i < len(s); {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c == '`' {
			i = skipCode(s, i)
			atStart = false
			continue
		}
		if atStart {
			// Lane words and a /repeat head keep the step open for a group:
			// "/repeat 5 (/a > /b)" groups its body.
			if n := len(stepPrefixRe.FindString(s[i:])); n > 0 || c == '(' {
				k := i + n
				for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
					k++
				}
				if k < len(s) && s[k] == '(' && commandHeadAt(s[k+1:]) {
					end := matchParen(s, k)
					if end < 0 {
						return sc, fmt.Errorf("the group at %q is never closed - every ( needs its )", snippet(s[k:]))
					}
					sc.groups = append(sc.groups, [2]int{k, end})
					i, atStart, plainFrom = end+1, false, end+1
					continue
				}
			}
		}
		if n, alt := opAt(s[i:]); n > 0 && commandHeadAt(s[i+n:]) && !blankLineRe.MatchString(s[plainFrom:i]) {
			sc.ops = append(sc.ops, progOp{start: i, end: i + n, alt: alt})
			i += n
			atStart, plainFrom = true, i
			continue
		}
		if strings.HasPrefix(s[i:], "until:") {
			sc.until = i
		}
		atStart = false
		i++
	}
	return sc, nil
}

// opAt returns the length of a program operator at the start of s.
func opAt(s string) (n int, alt bool) {
	switch {
	case strings.HasPrefix(s, "||"):
		return 2, true
	case strings.HasPrefix(s, "->"):
		return 2, false
	case strings.HasPrefix(s, ">") && !strings.HasPrefix(s, ">>") && !strings.HasPrefix(s, ">="):
		return 1, false
	}
	return 0, false
}

// commandHeadAt reports whether s (after blanks) starts a step: a "(" that
// opens on a command, or a /word that is not a path. An unknown /word counts,
// so a typo after a symbol operator is reported, not run as prose - the same
// rule CWL applies to its symbol connectors.
func commandHeadAt(s string) bool {
	t := strings.TrimLeft(s, " \t\r\n")
	if strings.HasPrefix(t, "(") {
		return commandHeadAt(t[1:])
	}
	m := legHeadRe.FindStringSubmatchIndex(t)
	if m == nil {
		return false
	}
	return m[3] >= len(t) || t[m[3]] != '/' // "/usr/bin/claude" is a path
}

// skipCode returns the index after the backtick span that starts at i. An
// unclosed span runs to the end: code is never syntax.
func skipCode(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	fence := strings.Repeat("`", n)
	if j := strings.Index(s[i+n:], fence); j >= 0 {
		return i + n + j + n
	}
	return len(s)
}

// matchParen returns the index of the ")" that closes the "(" at open,
// counting every parenthesis outside backtick code, or -1.
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); {
		switch s[i] {
		case '`':
			i = skipCode(s, i)
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}

// segmentsOf returns the [start, end) spans between operators.
func segmentsOf(s string, ops []progOp) [][2]int {
	var out [][2]int
	start := 0
	for _, op := range ops {
		out = append(out, [2]int{start, op.start})
		start = op.end
	}
	return append(out, [2]int{start, len(s)})
}

func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 40 {
		s = CutHead(s, 40) + "…"
	}
	return s
}

// ── classifying a step ──────────────────────────────────────────────────────

type headKind int

const (
	headNone     headKind = iota // no /word: plain text
	headLeg                      // a worker leg, or /frontier as the pseudo-leg
	headTeam                     // /team
	headLoop                     // /repeat
	headLane                     // lane words and a task
	headControl                  // a control word that is not a step (/wf, /parallel …)
	headDecision                 // a decision leg (answers questions, never a stage)
	headUnknown                  // a /word captain does not know
)

// classifyHead reads the head of one step, after the lane words in front of
// it are hoisted past the leg or control word they modify.
func classifyHead(text string) (headKind, string) {
	h := HoistLeading(strings.TrimSpace(text))
	m := legHeadRe.FindStringSubmatch(h)
	if m == nil {
		return headNone, ""
	}
	name := strings.ToLower(m[1])
	switch {
	case name == "repeat":
		return headLoop, name
	case name == "team":
		return headTeam, name
	case name == "frontier":
		return headLeg, name
	case KnownLeg(Leg(name)) && ServesTasks(Leg(name)):
		return headLeg, name
	case KnownLeg(Leg(name)):
		return headDecision, name
	case modifierWordRe.MatchString(h):
		return headLane, name
	}
	for _, w := range slashControlWords {
		if w == name {
			return headControl, name
		}
	}
	return headUnknown, name
}

// ── parsing ─────────────────────────────────────────────────────────────────

// parseAlt parses s as alternatives joined by ||.
func parseAlt(s string, depth int) (Program, error) {
	sc, err := scanTop(s)
	if err != nil {
		return Program{}, err
	}
	var alts []string
	start := 0
	for _, op := range sc.ops {
		if op.alt {
			alts = append(alts, s[start:op.start])
			start = op.end
		}
	}
	alts = append(alts, s[start:])
	if len(alts) > MaxProgramSteps {
		return Program{}, fmt.Errorf("a fallback tries at most %d alternatives (this one has %d)", MaxProgramSteps, len(alts))
	}
	var out []Program
	for _, a := range alts {
		c, err := parseChain(strings.TrimSpace(a), depth, false)
		if err != nil {
			return Program{}, err
		}
		out = append(out, c)
	}
	if len(out) == 1 {
		return out[0], nil
	}
	return Program{Kind: ProgramAlt, Steps: out}, nil
}

// parseChain parses s as steps joined by > (s holds no top-level ||). A loop
// takes the rest of its chain. loopBody allows plain text as the first step:
// "/repeat 3 tidy the changelog".
func parseChain(s string, depth int, loopBody bool) (Program, error) {
	if s == "" {
		return Program{}, fmt.Errorf("empty step - every > and || needs a step on both sides")
	}
	sc, err := scanTop(s)
	if err != nil {
		return Program{}, err
	}
	segs := segmentsOf(s, sc.ops)
	var steps []Program
	for i := 0; i < len(segs); i++ {
		start, end := segs[i][0], segs[i][1]
		t := strings.TrimSpace(s[start:end])
		if t == "" {
			return Program{}, fmt.Errorf("empty step - every > needs a step on both sides")
		}
		lead := start + strings.Index(s[start:end], t)
		if open := groupOpen(t); open >= 0 {
			if open > 0 {
				return Program{}, fmt.Errorf("%q cannot apply to a whole (group) - put it inside the group, in front of each step", strings.TrimSpace(t[:open]))
			}
			close := matchParen(s, lead)
			if close < 0 {
				return Program{}, fmt.Errorf("the group at %q is never closed - every ( needs its )", snippet(t))
			}
			if rest := strings.TrimSpace(s[close+1 : end]); rest != "" {
				return Program{}, fmt.Errorf("unexpected %q after a group - join steps with > or ||, and put until: right after the loop it ends", snippet(rest))
			}
			if depth+1 > MaxProgramDepth {
				return Program{}, fmt.Errorf("a program nests at most %d levels of groups and loops", MaxProgramDepth)
			}
			inner, err := parseAlt(strings.TrimSpace(s[lead+1:close]), depth+1)
			if err != nil {
				return Program{}, err
			}
			inner.Group = true
			steps = append(steps, inner)
			continue
		}
		kind, name := classifyHead(t)
		switch kind {
		case headLoop:
			if depth+1 > MaxProgramDepth {
				return Program{}, fmt.Errorf("a program nests at most %d levels of groups and loops", MaxProgramDepth)
			}
			loop, err := parseLoop(strings.TrimSpace(s[lead:]), depth+1)
			if err != nil {
				return Program{}, err
			}
			steps = append(steps, loop)
			i = len(segs) // the loop took the rest of the chain
		case headLeg:
			j := i
			for j+1 < len(segs) {
				nk, _ := classifyHead(strings.TrimSpace(s[segs[j+1][0]:segs[j+1][1]]))
				if nk != headLeg || groupOpen(strings.TrimSpace(s[segs[j+1][0]:segs[j+1][1]])) >= 0 {
					break
				}
				j++
			}
			text := strings.TrimSpace(s[lead:segs[j][1]])
			if _, err := ParseWorkflow(HoistLeading(text)); err != nil {
				return Program{}, fmt.Errorf("%v (in %q)", err, snippet(text))
			}
			steps = append(steps, Program{Kind: ProgramTurn, Turn: TurnWorkflow, Text: text})
			i = j
		case headTeam, headLane:
			turn := TurnTeam
			if kind == headLane {
				turn = TurnLane
			}
			if p := parallelInside(t); p != "" {
				return Program{}, fmt.Errorf("%s joins legs inside one stage - it cannot join a /team or lane step (%q); use /team, or name the legs: /grok … + /codex …", p, snippet(t))
			}
			text, gate := SplitGate(t)
			if bareStep(text) {
				return Program{}, fmt.Errorf("%q has no task - a /team or lane step needs one (a bare leg is the only step that refines the previous answer)", snippet(text))
			}
			steps = append(steps, Program{Kind: ProgramTurn, Turn: turn, Text: text, Gate: gate})
		case headNone:
			if !(loopBody && i == 0) {
				return Program{}, fmt.Errorf("a step must start with a leg, /team, /repeat, a lane word or a ( group: %q", snippet(t))
			}
			text, gate := SplitGate(t)
			steps = append(steps, Program{Kind: ProgramTurn, Turn: TurnAuto, Text: text, Gate: gate})
		case headControl:
			return Program{}, fmt.Errorf("/%s is a control word - it cannot be a step of a program", name)
		case headDecision:
			return Program{}, fmt.Errorf("/%s is a decision leg - it answers questions, not steps; name a worker leg", name)
		default:
			return Program{}, fmt.Errorf("unknown command /%s (known: %s, frontier, team, repeat)", name, legNames())
		}
	}
	if len(steps) > MaxProgramSteps {
		return Program{}, fmt.Errorf("a chain runs at most %d steps (this one has %d) - group some in ( )", MaxProgramSteps, len(steps))
	}
	if len(steps) == 1 {
		return steps[0], nil
	}
	return Program{Kind: ProgramChain, Steps: steps}, nil
}

// parseLoop parses "/repeat [N] <body> [until: <command>]". The body is the
// rest of the chain; until: belongs to the innermost loop of its group and
// must end it.
func parseLoop(s string, depth int) (Program, error) {
	h := HoistLeading(s) // "/oss /repeat 5 x" is "/repeat 5 /oss x"
	rest := h[len(loopHeadRe.FindString(h)):]
	count := 0
	if c := countRe.FindString(rest); c != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(c)); err == nil && n > 0 {
			count, rest = n, rest[len(c):]
		}
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return Program{}, fmt.Errorf("/repeat needs something to repeat")
	}
	if f := strings.Fields(rest); len(f) <= 2 && repeatControlWords[strings.ToLower(f[0])] {
		return Program{}, fmt.Errorf("/repeat %s is a control word - it cannot be a step of a program", strings.ToLower(f[0]))
	}
	until := ""
	if !hasLoopStep(rest) {
		var err error
		if rest, until, err = splitUntil(rest); err != nil {
			return Program{}, err
		}
	}
	body, err := parseChain(rest, depth, true)
	if err != nil {
		return Program{}, err
	}
	return Program{Kind: ProgramLoop, Count: count, Until: until, Steps: []Program{body}}, nil
}

// splitUntil takes the loop's exit check off its body: the last top-level
// "until:" and the command after it, which ends at a blank line.
func splitUntil(body string) (string, string, error) {
	sc, err := scanTop(body)
	if err != nil {
		return "", "", err
	}
	if sc.until < 0 {
		return body, "", nil
	}
	cmd := body[sc.until+len("until:"):]
	if loc := blankLineRe.FindStringIndex(cmd); loc != nil {
		if strings.TrimSpace(cmd[loc[1]:]) != "" {
			return "", "", fmt.Errorf("until: ends its loop - nothing may follow its command")
		}
		cmd = cmd[:loc[0]]
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", "", fmt.Errorf("until: needs a command that exits 0 when the loop is done, e.g. until: go test ./...")
	}
	if tail, err := scanTop(cmd); err == nil && len(tail.ops) > 0 {
		return "", "", fmt.Errorf("until: ends its loop - to run steps after the loop, put it in parentheses: (/repeat … until: %s) > …", snippet(cmd[:tail.ops[0].start]))
	}
	return strings.TrimSpace(body[:sc.until]), cmd, nil
}

// hasLoopStep: one of s's top-level steps is itself a /repeat. The inner
// loop then owns any until: that follows.
func hasLoopStep(s string) bool {
	sc, err := scanTop(s)
	if err != nil {
		return false
	}
	for _, seg := range segmentsOf(s, sc.ops) {
		t := strings.TrimSpace(s[seg[0]:seg[1]])
		if groupOpen(t) >= 0 {
			continue
		}
		if k, _ := classifyHead(t); k == headLoop {
			return true
		}
	}
	return false
}

// groupOpen returns the index of the "(" that opens a group at the start of a
// step (behind any lane words), or -1. A /repeat head is not part of it: a
// loop over a group is a loop, not a group.
func groupOpen(t string) int {
	n := 0
	for {
		m := modifierWordRe.FindString(t[n:])
		if m == "" {
			break
		}
		n += len(m)
	}
	rest := strings.TrimLeft(t[n:], " \t")
	if strings.HasPrefix(rest, "(") && commandHeadAt(rest[1:]) {
		return len(t) - len(rest)
	}
	return -1
}

// parallelInside returns the symbol connector that would make a /team or lane
// step parallel ("+" or "&" before a /word), or "".
func parallelInside(t string) string {
	for _, sym := range []string{"+", "&"} {
		for i := strings.Index(t, sym); i >= 0; {
			if commandHeadAt(t[i+1:]) && !strings.HasPrefix(strings.TrimLeft(t[i+1:], " \t"), "(") {
				return sym
			}
			j := strings.Index(t[i+1:], sym)
			if j < 0 {
				break
			}
			i += 1 + j
		}
	}
	return ""
}

// bareStep: the step is only slash words - "/team", "/quality /team".
func bareStep(text string) bool {
	for _, f := range strings.Fields(text) {
		if !strings.HasPrefix(f, "/") {
			return false
		}
	}
	return true
}

// usesOpenShell reports a sandbox step anywhere in the program.
func (p Program) usesOpenShell() bool {
	for _, t := range p.Turns() {
		switch t.Turn {
		case TurnWorkflow:
			if wf, err := ParseWorkflow(HoistLeading(t.Text)); err == nil {
				for _, l := range wf.Legs() {
					if l == LegOpenShell {
						return true
					}
				}
			}
		case TurnTeam:
			after := strings.TrimSpace(HoistLeading(t.Text)[len("/team"):])
			for {
				m := modifierWordRe.FindString(after)
				if m == "" {
					break
				}
				after = after[len(m):]
			}
			if LeadingForced(after) == string(LegOpenShell) {
				return true
			}
		}
	}
	return false
}

// ── reading a program ───────────────────────────────────────────────────────

// Turns lists the program's turns in order.
func (p Program) Turns() []Program {
	if p.Kind == ProgramTurn {
		return []Program{p}
	}
	var out []Program
	for _, st := range p.Steps {
		out = append(out, st.Turns()...)
	}
	return out
}

// MaxTurns is the most turns the program can dispatch: a gated non-leg turn
// may add one repair, a fallback may run every alternative, and an open loop
// runs until the budget ends.
func (p Program) MaxTurns(budget int) int {
	const ceiling = 1 << 30
	switch p.Kind {
	case ProgramTurn:
		if p.Gate != "" {
			return 2
		}
		return 1
	case ProgramLoop:
		if p.Count == 0 {
			return budget
		}
		n := p.Count * p.Steps[0].MaxTurns(budget)
		if n > ceiling || n < 0 {
			return ceiling
		}
		return n
	}
	n := 0
	for _, st := range p.Steps {
		n += st.MaxTurns(budget)
		if n > ceiling {
			return ceiling
		}
	}
	return n
}

// String renders the canonical program; it parses back to an equal Program.
func (p Program) String() string {
	var s string
	switch p.Kind {
	case ProgramTurn:
		s = p.Text
		if p.Gate != "" {
			s += " gate: " + p.Gate
		}
	case ProgramChain, ProgramAlt:
		sep := " > "
		if p.Kind == ProgramAlt {
			sep = " || "
		}
		parts := make([]string, len(p.Steps))
		for i, st := range p.Steps {
			parts[i] = st.String()
		}
		s = strings.Join(parts, sep)
	case ProgramLoop:
		s = "/repeat "
		if p.Count > 0 {
			s += strconv.Itoa(p.Count) + " "
		}
		s += p.Steps[0].String()
		if p.Until != "" {
			s += " until: " + p.Until
		}
	}
	if p.Group {
		return "(" + s + ")"
	}
	return s
}

// Outline renders the program as an indented plan, for the preview a user
// reads before - and while - it runs.
func (p Program) Outline() string {
	var sb strings.Builder
	p.outline(&sb, "", "")
	return strings.TrimRight(sb.String(), "\n")
}

func (p Program) outline(sb *strings.Builder, indent, label string) {
	switch p.Kind {
	case ProgramTurn:
		line := strings.Join(strings.Fields(p.Text), " ")
		if p.Gate != "" {
			line += "   [gate: " + p.Gate + "]"
		}
		fmt.Fprintf(sb, "%s%s%s\n", indent, label, line)
	case ProgramChain:
		if label != "" {
			fmt.Fprintf(sb, "%s%sin order:\n", indent, label)
			indent += "   "
		}
		for i, st := range p.Steps {
			st.outline(sb, indent, fmt.Sprintf("%d. ", i+1))
		}
	case ProgramAlt:
		if label != "" {
			fmt.Fprintf(sb, "%s%s\n", indent, strings.TrimSpace(label))
			indent += "   "
		}
		for i, st := range p.Steps {
			head := "if that fails:"
			if i == 0 {
				head = "try:"
			}
			fmt.Fprintf(sb, "%s%s\n", indent, head)
			st.outline(sb, indent+"   ", "")
		}
	case ProgramLoop:
		times := "until stopped (turn budget)"
		if p.Count > 0 {
			times = fmt.Sprintf("%d times", p.Count)
		}
		if p.Until != "" {
			times += ", until `" + p.Until + "` passes"
		}
		fmt.Fprintf(sb, "%s%srepeat %s:\n", indent, label, times)
		p.Steps[0].outline(sb, indent+"   ", "")
	}
}

// SplitRepeat splits "/repeat [N] <rest>" into its count and the rest.
// count 0 means until stopped. ok is false when raw does not open with
// /repeat.
func SplitRepeat(raw string) (count int, rest string, ok bool) {
	t := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(t), "/repeat") || (len(t) > 7 && isWordByte(t[7])) {
		return 0, "", false
	}
	t = strings.TrimSpace(t[len("/repeat"):])
	if t == "" {
		return 0, "", true
	}
	fields := strings.Fields(t)
	if n, err := strconv.Atoi(fields[0]); err == nil && n > 0 {
		return n, strings.TrimSpace(strings.TrimPrefix(t, fields[0])), true
	}
	return 0, t, true
}

// RepeatControl recognizes "/repeat status|stop|finish|abort|show|watch
// [last|all|thread-id]". ok is false when rest is not a control directive.
func RepeatControl(rest string) (word, arg string, ok bool) {
	f := strings.Fields(strings.ToLower(rest))
	if len(f) == 0 || !repeatControlWords[f[0]] {
		return "", "", false
	}
	if len(f) == 1 {
		return f[0], "", true
	}
	if len(f) == 2 && (f[1] == "last" || f[1] == "all" ||
		strings.HasPrefix(f[1], "rp_") || strings.HasPrefix(f[1], "ch_")) {
		return f[0], f[1], true
	}
	return "", "", false
}

// StartsLoop reports whether a typed prompt would start a /repeat loop or
// a program. /repeat's control words (status, show, finish …) start nothing.
func StartsLoop(text string) bool {
	h := HoistLeading(strings.TrimSpace(text))
	if _, rest, ok := SplitRepeat(h); ok {
		_, _, ctl := RepeatControl(strings.ToLower(strings.TrimSpace(rest)))
		return strings.TrimSpace(rest) != "" && !ctl
	}
	_, ok, _ := ParseProgram(h)
	return ok
}
