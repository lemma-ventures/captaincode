package captaincode

// The Captain command language (docs/LANGUAGE.md): the words, connectors and
// groups a turn is written in. The brain reads a turn with the parsers this
// package owns - HoistLeading, ParseProgram, ParseWorkflow, the /repeat head
// and the mid-prompt modifier readers - in the order its dispatch tries them
// (brain_openai.go chatCompletions). ParseTurn composes them in that same
// order and renders the result, so `captain parse` and the conformance suite
// (testdata/language/conformance.txt) show exactly how a line is read.

import (
	"fmt"
	"strings"
)

// LanguageVersion is the version of docs/LANGUAGE.md this parser implements.
// A change that alters how an existing line is read bumps the major version;
// a new word or form that leaves every existing line alone bumps the minor.
const LanguageVersion = "2.0"

// ownLineWords are the control words that take the rest of the line as
// their argument: nothing after them is read as a command.
var ownLineWords = map[string]bool{
	"btw": true, "interrupt": true, "init": true, "context": true, "euclid": true,
	"private": true, "captain": true, "help": true, "wf": true, "workflow": true,
	"run": true, "wfrun": true, "rename": true,
}

// Turn is how one line is read: what runs it, with which modifiers, on
// which text, and the turns it contains.
type Turn struct {
	Kind      string   // control, chain, fallback, workflow, repeat, repeat-control, parallel, team, frontier, openshell, lane, leg, auto, error
	Head      string   // the control word, the leg, or the repeat control word
	Count     int      // /repeat rounds (0 = until stopped)
	Until     string   // /repeat's exit check
	Group     bool     // written in parentheses
	Modifiers []string // lanes, pools, skills, the frontier ceiling, in canonical order
	Text      string   // the task, a control word's argument, or the error
	Gate      string
	Steps     []Turn   // a chain's steps, a fallback's alternatives, a /repeat's or /parallel's body
	Stages    [][]Turn // a workflow's stages, each a list of parallel legs
}

// ParseTurn reads raw as the brain's dispatch does.
func ParseTurn(raw string) Turn {
	t := strings.TrimSpace(raw)
	if w := slashWord(t); ownLineWords[w] {
		return Turn{Kind: "control", Head: w, Text: strings.TrimSpace(t[1+len(w):])}
	}
	t = HoistLeading(t)
	// Programs come first: handleProgram runs before any head is read.
	if p, ok, err := ParseProgram(t); ok {
		if err != nil {
			return Turn{Kind: "error", Text: err.Error()}
		}
		return programTurn(p)
	}
	return readTurn(t)
}

// readTurn reads a line that is not a program, in the order the brain tries
// the remaining paths.
func readTurn(t string) Turn {
	// A turn led by /openshell, or a /team that names it, runs whole in the
	// sandbox (brain_openshell.go): nothing after the head is topology.
	if LeadingForced(t) == string(LegOpenShell) {
		return headed("openshell", t)
	}
	if slashWord(t) == "team" && leadingWordsInclude(strings.TrimLeft(t[len("/team"):], " \t:"), "openshell") {
		out := headed("openshell", t)
		out.Head = "team"
		out.Text = strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(out.Text, "/openshell"), ":"))
		return out
	}
	wf, wfErr := ParseWorkflow(t)
	if wfErr == nil && (wf.MultiStage() || wf.HasGate()) {
		out := Turn{Kind: "workflow", Modifiers: turnModifiers(t)}
		for _, st := range wf.Stages {
			var legs []Turn
			for _, l := range st.Legs {
				legs = append(legs, Turn{Kind: "leg", Head: string(l.Leg), Text: l.Prompt, Gate: l.Gate})
			}
			out.Stages = append(out.Stages, legs)
		}
		return out
	}
	// /frontier and /team outrank the control words (the TUI forces their
	// pseudo-model), as in the brain's dispatch.
	if w := slashWord(t); w == "team" || w == "frontier" {
		return headed(w, t)
	}
	if count, rest, ok := SplitRepeat(t); ok {
		if rest == "" {
			return Turn{Kind: "repeat-control", Head: "status"}
		}
		if word, arg, ok := RepeatControl(rest); ok {
			return Turn{Kind: "repeat-control", Head: word, Text: arg}
		}
		return Turn{Kind: "repeat", Count: count, Steps: []Turn{readTurn(HoistLeading(rest))}}
	}
	w := slashWord(t)
	if w == "parallel" {
		rest := strings.TrimSpace(t[len("/parallel"):])
		if f := strings.Fields(strings.ToLower(rest)); len(f) == 1 && (f[0] == "show" || f[0] == "status" || f[0] == "stop") {
			return Turn{Kind: "control", Head: "parallel", Text: f[0]}
		}
		return Turn{Kind: "parallel", Steps: []Turn{ParseTurn(rest)}}
	}
	// A malformed leg workflow is reported only after the control words had
	// their turn.
	if wfErr != nil && LooksLikeWorkflow(t) {
		return Turn{Kind: "error", Text: wfErr.Error()}
	}
	switch {
	case w == "auto":
		return headed("auto", t)
	case w != "" && KnownLeg(Leg(w)):
		out := headed("leg", t)
		out.Head = w
		return out
	}
	return Turn{Kind: "auto", Modifiers: turnModifiers(t), Text: stripLeadingModifiers(t)}
}

// programTurn renders a parsed program as turns: each of its turns is read
// the way the runner dispatches it, as one typed prompt.
func programTurn(p Program) Turn {
	var out Turn
	switch p.Kind {
	case ProgramChain, ProgramAlt:
		out.Kind = "chain"
		if p.Kind == ProgramAlt {
			out.Kind = "fallback"
		}
		for _, st := range p.Steps {
			out.Steps = append(out.Steps, programTurn(st))
		}
	case ProgramLoop:
		out = Turn{Kind: "repeat", Count: p.Count, Until: p.Until, Steps: []Turn{programTurn(p.Steps[0])}}
	default:
		out = readTurn(HoistLeading(p.Text))
		if p.Turn == TurnLane && out.Kind == "auto" {
			out.Kind = "lane"
		}
		if p.Gate != "" {
			out.Gate = p.Gate
		}
	}
	out.Group = p.Group
	return out
}

// leadingWordsInclude reports whether word is among the /words text opens with.
func leadingWordsInclude(text, word string) bool {
	for _, f := range strings.Fields(text) {
		if !strings.HasPrefix(f, "/") {
			return false
		}
		if strings.EqualFold(strings.Trim(f, "/:"), word) {
			return true
		}
	}
	return false
}

func headed(kind, t string) Turn {
	w := slashWord(t)
	body := strings.TrimLeft(t[1+len(w):], " \t:")
	return Turn{Kind: kind, Modifiers: turnModifiers(body), Text: stripLeadingModifiers(body)}
}

// slashWord is the lowercase /word a text opens with, or "" (a path such as
// "/usr/bin" is not a word).
func slashWord(t string) string {
	m := legHeadRe.FindStringSubmatch(t)
	if m == nil || !legLikeAt(t) {
		return ""
	}
	return strings.ToLower(m[1])
}

// turnModifiers are the modifiers a turn states anywhere in its text, in
// canonical order: lane, pools, skills, the frontier ceiling.
func turnModifiers(text string) []string {
	var out []string
	// The brain reads the lane, then lets a /frontier anywhere but at the
	// head override it. The text here has its head removed, so it is
	// prefixed to keep a leading /frontier from reading as one.
	frontier := MidPromptFrontier("_ " + text)
	if p := MidPromptPrefer(text); p != "" && !frontier {
		out = append(out, p)
	}
	pool := MidPromptPool(text)
	if pool.OSS {
		out = append(out, "oss")
	}
	if pool.Deterministic {
		out = append(out, "deterministic")
	}
	for _, s := range AskedSkills(text) {
		out = append(out, s.Word)
	}
	if frontier {
		out = append(out, "frontier")
	}
	return out
}

func stripLeadingModifiers(text string) string {
	for {
		m := modifierWordRe.FindString(text)
		if m == "" {
			return strings.TrimSpace(text)
		}
		text = text[len(m):]
	}
}

// String renders the canonical parse tree: (kind head [modifiers] "text"
// children…). The conformance suite compares these strings.
func (t Turn) String() string {
	var b strings.Builder
	b.WriteString("(")
	if t.Group {
		b.WriteString("group ")
	}
	b.WriteString(t.Kind)
	if t.Head != "" {
		b.WriteString(" " + t.Head)
	}
	if t.Kind == "repeat" {
		if t.Count == 0 {
			b.WriteString(" until-stopped")
		} else {
			fmt.Fprintf(&b, " %d", t.Count)
		}
	}
	if len(t.Modifiers) > 0 {
		b.WriteString(" [" + strings.Join(t.Modifiers, " ") + "]")
	}
	if t.Text != "" {
		fmt.Fprintf(&b, " %q", t.Text)
	}
	if t.Gate != "" {
		fmt.Fprintf(&b, " (gate %q)", t.Gate)
	}
	if t.Until != "" {
		fmt.Fprintf(&b, " (until %q)", t.Until)
	}
	for _, s := range t.Steps {
		b.WriteString(" " + s.String())
	}
	for _, st := range t.Stages {
		b.WriteString(" (stage")
		for _, l := range st {
			b.WriteString(" " + l.String())
		}
		b.WriteString(")")
	}
	b.WriteString(")")
	return b.String()
}
