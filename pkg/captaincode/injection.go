package captaincode

// Prompt-injection screening for text that reaches a worker from another
// agent rather than from the user: a prompt sent with `captain send` (a
// watcher, a script, or a captain in another folder - cross-captain messages
// stay supported) arrives in the session as a user turn, the strongest
// authority a worker sees. An agent that read a hostile page or file can be
// made to send instructions on. Two layers, neither a model call:
//
//   - SanitizeSent removes what a person cannot see or should not be able to
//     forge: invisible and bidirectional control characters, Unicode tag
//     characters, and the markers captain uses for its own instructions.
//   - ScanInjection names patterns that carry an attack: overriding earlier
//     instructions, sending secrets out, running fetched code, switching off
//     a safety check, or keeping something from the user. A high finding holds
//     the message for the user; a medium one is delivered with a warning.
//
// Heuristics miss some attacks and flag some harmless text. The provenance
// line every sent turn carries into the worker prompt (brain_inbox.go) is the
// layer that does not depend on catching the wording.

import (
	"regexp"
	"strings"
	"unicode"
)

// Severity orders findings.
type Severity int

const (
	SevMedium Severity = iota + 1
	SevHigh
)

func (s Severity) String() string {
	if s == SevHigh {
		return "high"
	}
	return "medium"
}

// Finding is one suspicious pattern in a sent text.
type Finding struct {
	Kind     string   `json:"kind"`
	Severity Severity `json:"-"`
	Level    string   `json:"severity"`
	Excerpt  string   `json:"excerpt"`
}

type injectionRule struct {
	kind string
	sev  Severity
	re   *regexp.Regexp
}

var injectionRules = []injectionRule{
	{"instruction override", SevHigh, regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override)\b[^.\n]{0,40}\b(previous|prior|above|earlier|all|any|your|the system|system)\b[^.\n]{0,20}\b(instructions?|prompts?|rules|guidelines|directions)\b`)},
	{"role spoofing", SevHigh, regexp.MustCompile(`(?i)(<\|im_(start|end)\|>|<\|(system|assistant)\|>|^\s*#{1,3}\s*(system|developer)\s*(prompt|message)?\s*:?\s*$|\byou are now (a|an|the)\b|\bnew (system )?(prompt|instructions)\s*:)`)},
	{"secret exfiltration", SevHigh, regexp.MustCompile(`(?i)\b(send|post|upload|paste|email|exfiltrate|share|forward|curl)\b[^.\n]{0,60}\b(secret|token|api[ _-]?key|password|passwd|credential|private key|ssh key|\.env|cookie|session id)s?\b`)},
	{"secret read", SevHigh, regexp.MustCompile(`(?i)(cat|less|more|cp|scp|base64)\s+[^\n]{0,20}(~|\$HOME|/Users/[^/\s]+|/home/[^/\s]+)/\.(ssh|aws|gnupg|config/gh|netrc|docker/config)|\bprintenv\b|\benv\s*\|\s*(curl|nc|base64)`)},
	{"remote code execution", SevHigh, regexp.MustCompile(`(?i)(curl|wget)\b[^\n|]{0,200}\|\s*(sudo\s+)?(ba|z)?sh\b|\b(ba|z)?sh\s+<\(\s*(curl|wget)|\beval\s+"?\$\((curl|wget)`)},
	{"safety switched off", SevHigh, regexp.MustCompile(`(?i)(CAPTAIN_ACTION_GATE\s*=\s*off|CAPTAIN_REDACT\s*=\s*(0|off)|--dangerously-skip-permissions|--no-verify\b|disable (the )?(gate|redaction|leakcheck|hooks?|sandbox)|git push\s+(-f|--force)\b|chmod\s+-?R?\s*777)`)},
	{"hidden from the user", SevMedium, regexp.MustCompile(`(?i)\b(do not|don't|never)\s+(tell|inform|show|mention|ask)\s+(the )?(user|human|operator|owner)\b|\b(secretly|quietly|silently|without (asking|telling))\b`)},
	{"encoded payload", SevMedium, regexp.MustCompile(`[A-Za-z0-9+/]{200,}={0,2}`)},
}

// captainMarkerRe matches the bracketed markers captain writes into worker
// prompts ("[captain] …", "[system]", "[assistant]"): text from another agent
// must not be able to forge them.
var captainMarkerRe = regexp.MustCompile(`(?im)^\s*\[(captain|system|assistant)\b`)

// invisible reports characters a person cannot see in a terminal but a model
// reads: zero-width and formatting characters, bidirectional overrides and
// isolates, and the Unicode tag block used to hide ASCII instructions.
func invisible(r rune) bool {
	if r == '\u200d' {
		return false // the zero-width joiner builds emoji sequences
	}
	switch {
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x2069,
		r == 0xFEFF, r == 0x00AD, r >= 0xE0000 && r <= 0xE007F:
		return true
	}
	return unicode.Is(unicode.Cf, r) && r != '\u200d' // keep ZWJ: emoji sequences use it
}

// SanitizeSent removes invisible characters and neutralizes forged captain
// markers. removed counts what was taken out.
func SanitizeSent(text string) (clean string, removed int, forged int) {
	var b strings.Builder
	for _, r := range text {
		if invisible(r) {
			removed++
			continue
		}
		b.WriteRune(r)
	}
	clean = b.String()
	clean = captainMarkerRe.ReplaceAllStringFunc(clean, func(m string) string {
		forged++
		return strings.Replace(m, "[", "[quoted ", 1)
	})
	return clean, removed, forged
}

// ScanInjection lists the suspicious patterns in text, the most severe first.
// Run it on the raw text: hidden characters and forged markers count too.
func ScanInjection(text string) []Finding {
	var out []Finding
	if _, removed, forged := SanitizeSent(text); removed > 0 || forged > 0 {
		if removed > 0 {
			out = append(out, Finding{Kind: "hidden characters", Severity: SevHigh, Excerpt: itoa(removed) + " invisible or direction-changing characters"})
		}
		if forged > 0 {
			out = append(out, Finding{Kind: "forged captain marker", Severity: SevHigh, Excerpt: captainMarkerRe.FindString(text)})
		}
	}
	for _, r := range injectionRules {
		if m := r.re.FindString(text); m != "" {
			out = append(out, Finding{Kind: r.kind, Severity: r.sev, Excerpt: CutHead(strings.Join(strings.Fields(m), " "), 80)})
		}
	}
	for i := range out {
		out[i].Level = out[i].Severity.String()
	}
	// Most severe first; stable within a level.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Severity > out[j-1].Severity; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// MaxSeverity is the highest severity among findings, 0 when there are none.
func MaxSeverity(fs []Finding) Severity {
	var m Severity
	for _, f := range fs {
		if f.Severity > m {
			m = f.Severity
		}
	}
	return m
}

// FindingsLine renders findings in one line for a notice.
func FindingsLine(fs []Finding) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		parts = append(parts, f.Kind+" ("+f.Excerpt+")")
	}
	return strings.Join(parts, "; ")
}
