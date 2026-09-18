package captaincode

// Modifiers compose with control words in either order (2026-09-17):
// "/oss /repeat 5 <task>" means what "/repeat 5 /oss <task>" means. The
// control-word handlers (/repeat, /parallel, /team, /frontier, a leg) read
// the head of the turn, so leading modifiers are hoisted past the control
// word - and its count - before anything else looks at the text. The
// modifiers stay in the turn (the round, the team prompt, the worker prompt
// all still see them; stripCaptainDirectives removes them for the worker).

import (
	"regexp"
	"strings"
)

var (
	modifierWordRe = regexp.MustCompile(`(?i)^/(quality|q|best|speed|fast|save|cheap|oss|open|deterministic|det|adi)\b[\s:]*`)
	controlHeadRe  = regexp.MustCompile(`(?i)^/(repeat|parallel|team|frontier|wf|run)\b[\s:]*`)
	countRe        = regexp.MustCompile(`^\d+\s*`)
)

// HoistLeading moves the modifier words at the head of raw past the control
// word that follows them. Text with no leading modifier, or with no control
// word after them, is returned unchanged.
func HoistLeading(raw string) string {
	t := strings.TrimLeft(raw, " \t")
	var mods []string
	rest := t
	for {
		m := modifierWordRe.FindString(rest)
		if m == "" {
			break
		}
		mods = append(mods, "/"+strings.ToLower(strings.TrimSpace(strings.Trim(m, "/ \t:"))))
		rest = rest[len(m):]
	}
	if len(mods) == 0 {
		return raw
	}
	head := controlHeadRe.FindString(rest)
	if head == "" {
		// A leg name is a control word too: "/oss /grok fix it" → "/grok /oss fix it".
		if leg := leadingLeg(rest); leg != "" {
			head = rest[:len(leg)]
		}
	}
	if head == "" {
		return raw
	}
	after := rest[len(head):]
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(head)), "/repeat") {
		if c := countRe.FindString(after); c != "" {
			head += c
			after = after[len(c):]
		}
	}
	return strings.TrimSpace(head) + " " + strings.Join(mods, " ") + " " + strings.TrimSpace(after)
}

// leadingLeg returns the "/<leg>[\s:]*" head of s when s starts with an
// active leg's name, else "".
func leadingLeg(s string) string {
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "/") {
		return ""
	}
	best := ""
	for _, id := range LegIDs() {
		if strings.HasPrefix(low, "/"+id) {
			end := 1 + len(id)
			if end < len(low) && isWordByte(low[end]) {
				continue
			}
			for end < len(s) && (s[end] == ' ' || s[end] == '\t' || s[end] == ':') {
				end++
			}
			if end > len(best) {
				best = s[:end]
			}
		}
	}
	return best
}

// LeadingForced names the pseudo-model or leg a turn starts with after
// hoisting ("team", "frontier", "grok"), or "" - what the plugin would have
// forced had the modifiers not stood in front.
func LeadingForced(raw string) string {
	t := strings.TrimLeft(raw, " \t")
	low := strings.ToLower(t)
	for _, w := range []string{"team", "frontier"} {
		if strings.HasPrefix(low, "/"+w) && (len(low) == 1+len(w) || !isWordByte(low[1+len(w)])) {
			return w
		}
	}
	if leg := leadingLeg(t); leg != "" {
		return strings.ToLower(strings.TrimRight(strings.TrimPrefix(leg, "/"), " \t:"))
	}
	return ""
}
