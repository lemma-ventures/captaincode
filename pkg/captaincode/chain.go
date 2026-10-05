package captaincode

// Chains: whole commands in sequence. CWL (workflow.go) sequences legs;
// a chain sequences anything a turn can start with:
//
//	/frontier write the specs > /repeat 5 /quality implement the next item
//	/team audit the deck > /claude fix what it found
//	/frontier plan it > (/team build it > /repeat 3 /quality polish it)
//
// Each step runs as a full turn, after the previous one finishes, and sees
// what the previous one answered. A step in parentheses is itself a chain
// (or any single command), so a workflow nests behind ">". A chain whose
// steps are all plain leg stages stays a CWL workflow: CWL runs those with
// parallel stages and a director review, which a chain does not.
//
// The connector rule is CWL's: ">" or "->" counts only when the next token
// is a slash command ("/repeat", "/team", a leg…) or a parenthesis opening
// one. "x > y" in prose, or "(a) > (b)", stays prose.

import (
	"regexp"
	"strings"
)

// Chain limits; exceeding one leaves the text to the ordinary dispatch,
// which reports what it can.
const (
	MaxChainSteps = 8
	MaxChainDepth = 3
)

var (
	chainHeadRe = regexp.MustCompile(`^/([A-Za-z][A-Za-z0-9-]*)\b`)
	chainSeqRe  = regexp.MustCompile(`^\s*(?:->|>)\s*`)
)

// SplitChain splits s into its top-level steps. ok is false when s is not a
// chain: a single command, prose, or a sequence of plain leg stages (CWL).
// A text wrapped whole in one group is unwrapped first, so "(/a x > /b y)"
// is the chain inside it; a group step keeps its parentheses and is split
// when it runs.
func SplitChain(s string) (steps []string, ok bool) {
	s = UnwrapGroup(strings.TrimSpace(s))
	if !chainStepAt(s) {
		return nil, false
	}
	start := 0
	for i := 0; i < len(s); i++ {
		// A group is atomic wherever it stands: "/repeat 3 (/a > /b)" is one
		// command whose task is a chain, not two steps.
		if groupAt(s[i:]) {
			end := groupEnd(s, i)
			if end < 0 {
				return nil, false
			}
			i = end
			continue
		}
		if s[i] != '>' && !strings.HasPrefix(s[i:], "->") {
			continue
		}
		m := chainSeqRe.FindString(s[i:])
		next := i + len(m)
		if m == "" || !chainStepAt(s[next:]) {
			continue
		}
		steps = append(steps, strings.TrimSpace(s[start:i]))
		start = next
		i = next - 1
	}
	steps = append(steps, strings.TrimSpace(s[start:]))
	if len(steps) < 2 || len(steps) > MaxChainSteps || chainDepth(s) > MaxChainDepth {
		return nil, false
	}
	for _, st := range steps {
		if st == "" {
			return nil, false
		}
	}
	if allLegStages(steps) {
		return nil, false
	}
	return steps, true
}

// IsGroupStep reports whether a step is a parenthesized sub-workflow.
func IsGroupStep(step string) bool {
	s := strings.TrimSpace(step)
	return groupAt(s) && groupEnd(s, 0) == len(s)-1
}

// chainStepAt: s begins with a step - a command that runs work, or a group.
func chainStepAt(s string) bool {
	if groupAt(s) {
		return true
	}
	m := chainHeadRe.FindStringSubmatch(s)
	return m != nil && legLikeAt(s) && chainable(strings.ToLower(m[1]))
}

// chainable: a command that runs work, so it can be a step. Control words
// (/wf save, /btw, /private …) take the rest of the line as their argument,
// so a ">" after them is theirs: "/wf save review /grok a > /claude b" saves
// a workflow, it is not a chain.
func chainable(name string) bool {
	switch name {
	case "team", "repeat", "auto":
		return true
	}
	if preferenceWords[name] || IsFrontier(Leg(name)) || KnownLeg(Leg(name)) {
		return true
	}
	if _, ok := SkillWords[name]; ok {
		return true
	}
	for _, w := range PoolWords {
		if w == name {
			return true
		}
	}
	return false
}

// groupAt: s opens a group - "(" followed by a slash command. A parenthesis
// opening prose ("(F3)", "(see below)") is not one.
func groupAt(s string) bool {
	if !strings.HasPrefix(s, "(") {
		return false
	}
	return chainHeadRe.MatchString(strings.TrimLeft(s[1:], " \t\n"))
}

// groupEnd is the index of the parenthesis closing the group opened at i,
// counting every parenthesis inside it (prose parentheses inside a group
// are balanced in practice); -1 when it never closes.
func groupEnd(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// UnwrapGroup strips parentheses that wrap the whole text.
func UnwrapGroup(s string) string {
	for groupAt(s) && groupEnd(s, 0) == len(s)-1 {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// chainDepth is how deep groups nest in s.
func chainDepth(s string) int {
	max := 0
	for i := 0; i < len(s); i++ {
		if !groupAt(s[i:]) {
			continue
		}
		end := groupEnd(s, i)
		if end < 0 {
			return MaxChainDepth + 1
		}
		if d := 1 + chainDepth(strings.TrimSpace(s[i+1:end])); d > max {
			max = d
		}
		i = end
	}
	return max
}

// allLegStages: every step is a plain worker-leg stage, which CWL runs.
func allLegStages(steps []string) bool {
	for _, st := range steps {
		m := chainHeadRe.FindStringSubmatch(st)
		if m == nil {
			return false
		}
		leg := Leg(strings.ToLower(m[1]))
		if !(IsFrontier(leg) || (KnownLeg(leg) && ServesTasks(leg))) {
			return false
		}
	}
	return true
}

// StartsLoop reports whether a turn would start work that repeats or
// sequences turns: a /repeat among its leading commands ("/oss /repeat 5 …"),
// or a chain. The inbox refuses these - only a person types a loop
// (formal/CommandSafety/Inbox.lean). A prompt that merely mentions /repeat in
// its prose does not start one.
func StartsLoop(text string) bool {
	t := UnwrapGroup(strings.TrimSpace(text))
	if _, ok := SplitChain(t); ok {
		return true
	}
	for _, f := range strings.Fields(t) {
		if !strings.HasPrefix(f, "/") {
			return false
		}
		if strings.EqualFold(strings.TrimRight(f, ":"), "/repeat") {
			return true
		}
	}
	return false
}
