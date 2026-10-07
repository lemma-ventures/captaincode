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
	"encoding/base64"
	"net/url"
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
	// Quoted: a high pattern inside backtick code, rated medium until a
	// judge reads the context (brain_inbox.go).
	Quoted bool `json:"quoted,omitempty"`
}

// HasQuotedHigh reports a high pattern that only appeared inside code.
func HasQuotedHigh(fs []Finding) bool {
	for _, f := range fs {
		if f.Quoted {
			return true
		}
	}
	return false
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

// confusables folds letters that look like Latin ones (Cyrillic, Greek,
// fullwidth) to Latin, so "іgnore previous instructions" written with a
// Cyrillic і is read as what it shows. Only for scanning: the delivered text
// keeps its letters.
var confusables = map[rune]rune{
	'а': 'a', 'е': 'e', 'о': 'o', 'р': 'p', 'с': 'c', 'у': 'y', 'х': 'x', 'і': 'i', 'ј': 'j', 'ѕ': 's',
	'ԁ': 'd', 'һ': 'h', 'ӏ': 'l', 'А': 'A', 'В': 'B', 'Е': 'E', 'К': 'K', 'М': 'M', 'Н': 'H', 'О': 'O',
	'Р': 'P', 'С': 'C', 'Т': 'T', 'Х': 'X', 'І': 'I', 'α': 'a', 'ε': 'e', 'ο': 'o', 'ρ': 'p', 'ι': 'i',
	'κ': 'k', 'ν': 'v', 'τ': 't', 'υ': 'u', 'Α': 'A', 'Β': 'B', 'Ε': 'E', 'Ι': 'I', 'Κ': 'K', 'Μ': 'M',
	'Ν': 'N', 'Ο': 'O', 'Ρ': 'P', 'Τ': 'T', 'Χ': 'X',
}

func foldConfusables(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r >= 0xFF01 && r <= 0xFF5E { // fullwidth ASCII
			r -= 0xFEE0
		}
		if l, ok := confusables[r]; ok {
			r = l
		}
		b.WriteRune(r)
	}
	return b.String()
}

// decodedViews are the encodings an instruction can hide in, decoded: base64
// blobs and percent-encoding. Each view is scanned like the text itself.
var base64BlobRe = regexp.MustCompile(`[A-Za-z0-9+/_-]{40,}={0,2}`)

func decodedViews(text string) []string {
	var out []string
	for _, blob := range base64BlobRe.FindAllString(text, 8) {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if raw, err := enc.DecodeString(blob); err == nil && printableShare(raw) > 0.9 {
				out = append(out, string(raw))
				break
			}
		}
	}
	if strings.Contains(text, "%") {
		if u, err := url.QueryUnescape(text); err == nil && u != text {
			out = append(out, u)
		}
	}
	return out
}

func printableShare(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	n := 0
	for _, c := range b {
		if c == '\n' || c == '\t' || (c >= 32 && c < 127) {
			n++
		}
	}
	return float64(n) / float64(len(b))
}

// ScanInjection lists the suspicious patterns in text, the most severe first.
// Run it on the raw text: hidden characters and forged markers count too.
// Lookalike letters are folded and encoded payloads decoded before the
// patterns run, so neither hides an instruction.
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
	seen := map[string]bool{}
	views := append([]string{text, foldConfusables(text)}, decodedViews(text)...)
	for i, view := range views {
		for _, r := range injectionRules {
			if seen[r.kind] {
				continue
			}
			if loc := r.re.FindStringIndex(view); loc != nil {
				m := view[loc[0]:loc[1]]
				seen[r.kind] = true
				sev := r.sev
				quoted := false
				// Inside backtick code a pattern is usually discussed, not
				// commanded ("add a test that refuses `git push --force`"):
				// medium, and the judge decides from the context.
				if i == 0 && insideCode(view, loc[0]) && sev == SevHigh {
					sev, quoted = SevMedium, true
				}
				excerpt := CutHead(strings.Join(strings.Fields(m), " "), 80)
				switch {
				case i == 1 && view != text:
					excerpt += " (written with lookalike letters)"
				case i >= 2:
					excerpt += " (inside an encoded payload)"
				}
				out = append(out, Finding{Kind: r.kind, Severity: sev, Excerpt: excerpt, Quoted: quoted})
			}
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

// insideCode reports whether position i of text falls inside a backtick span.
func insideCode(text string, i int) bool {
	return strings.Count(text[:i], "`")%2 == 1
}
