package captaincode

// Triage - tier 0 of routing: class + domain + confidence, deterministically,
// in <1ms. Measured on our own traffic (2026-08-01): the LLM director costs a
// median 17.5s per plan and shares the frontier leg's quota bucket, while 78.5%
// of all tokens landed on that leg - much of it prose edits that never needed
// the bazooka. The strategy is three tiers:
//
//	tier 0  this file            every turn, free, confident majority
//	tier 1  ClassifyWithLLM      low-confidence only - the FREE leg, ~5s,
//	                             never claude (using claude to decide whether
//	                             to use claude is the problem itself)
//	tier 2  director Plan        high class, fan-out, /quality, named legs
//
// Misclassification is bounded: reroute/escalation and the assess loop correct
// upward, and the user can always force a leg.

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Domain is the work's subject area. Priors from coding benchmarks mis-rank
// models for editorial work (free 6.4, codex 6.2 on a prose-dominated mix), so
// domain is a first-class routing input, not a nice-to-have.
type Domain string

const (
	DomainCode      Domain = "code"
	DomainEditorial Domain = "editorial"
	DomainResearch  Domain = "research"
	DomainGeneral   Domain = "general"
)

// TriageResult is tier 0's verdict.
type TriageResult struct {
	Class      Class
	Domain     Domain
	Confidence float64 // 0..1 - margin-based; below ~0.6 consider tier 1
	Why        string  // one line for logs and `captain why`
}

// NeedsDirector reports whether this task justifies the LLM director call.
func (t TriageResult) NeedsDirector() bool { return t.Class == ClassHigh }

// signal is one weighted feature. Weights are small integers; the score
// margins, not absolute values, drive class and confidence.
type signal struct {
	re *regexp.Regexp
	w  int
}

func sigs(w int, pats ...string) []signal {
	out := make([]signal, 0, len(pats))
	for _, p := range pats {
		out = append(out, signal{regexp.MustCompile(`(?i)\b(?:` + p + `)`), w})
	}
	return out
}

var (
	// ---- complexity
	highSigs = append(
		sigs(3, "architect", "refactor", "migrat", "concurren", "race condition", "deadlock",
			"security", "vulnerab", "threat model", "red[- ]?team", "audit",
			"across the (?:codebase|repo)", "end[- ]?to[- ]?end", "debug"),
		append(sigs(2, "investigate", "performance", "optimi[sz]e", "prove", "soundness",
			"formal", "simulation", "demonstrat", "exhaustive", "comprehensive",
			"all (?:problems|cases|jurisdictions)"),
			sigs(1, "design", "review the (?:paper|research|spec|architecture)", "ensure that")...)...)

	trivialSigs = append(
		sigs(3, "typo", "rename", "one[- ]?(?:word|liner|sentence)", "in one sentence",
			"lint", "whitespace", "changelog", "bump", "null check"),
		sigs(2, "readme", "comment", "docstring", "one short", "single sentence",
			"quick", "rate this", "grade this")...)

	// mediumAnchors mark constructive work: short imperatives like "write a
	// unit test" are 9 words but real tasks - brevity alone must not demote
	// them to trivial.
	mediumAnchors = sigs(2, "implement", "write (?:a|an|the|unit)", "build", "create",
		"develop", "integrate", "unit test", "integration test")

	// ---- domain
	codeSigs = append(
		sigs(3, `\w+\.(?:go|ts|tsx|js|py|rs|java|rb|sql|sh|yml|yaml|json|md)\b`,
			"func ", "class ", "implement", "unit test", "null check", "compile",
			"stack ?trace", "endpoint", `api\b`, `bug\b`, "build fail", "harness", "seam"),
		sigs(2, `code\b`, "codebase", "function", `test\b`, `repo\b`, "branch", "commit",
			"merge", "deploy", "config", "readme", "docstring", "parse", `proxy\b`,
			`auth\b`, "race condition", "deadlock", "debug", `worker\b`, `queue\b`,
			`server\b`, `thread\b`)...)

	editorialSigs = append(
		sigs(3, "abstract", "paragraph", "sentence", "wording", "phrasing", "prose",
			"writing style", "my style", "reads weird", "readab", "condense",
			"tighten", "rewrite this", "rephrase", "tone", "digestable", "digestible"),
		sigs(2, "word(?:s)? max", "words max", "reader", "grade", "rate", "editorial",
			"draft", "intro", "conclusion", "title", "elegan")...)

	researchSigs = append(
		sigs(3, "research paper", "the paper", "thesis", "hypothes", "literature",
			"citation", "regulat", "jurisdiction", "attestation", "proof of reserves"),
		sigs(2, "study", "survey", "analy[sz]e the", "evidence", "sound solutions",
			"methodolog")...)

	questionish = regexp.MustCompile(`(?i)^\s*(?:what|who|when|where|which|why|how|is|are|does|do|can)\b`)
)

func score(t string, ss []signal) (int, []string) {
	n := 0
	var hits []string
	for _, s := range ss {
		if m := s.re.FindString(t); m != "" {
			n += s.w
			hits = append(hits, strings.ToLower(m))
		}
	}
	return n, hits
}

// TriageTask classifies one task. Deterministic, <1ms, no allocation beyond
// the result - safe on every routing hot path.
func TriageTask(task string) TriageResult {
	t := strings.TrimSpace(task)
	words := len(strings.Fields(t))

	hi, hiHits := score(t, highSigs)
	realLo, loHits := score(t, trivialSigs)
	anchors, _ := score(t, mediumAnchors)

	// Length pressure: long multi-requirement prompts trend high; fragments and
	// one-liners trend trivial. Brevity is weaker evidence than a real signal -
	// and it never outranks a constructive anchor ("write a unit test" is nine
	// words of REAL work).
	if words > 120 {
		hi += 2
	} else if words > 60 {
		hi++
	}
	lo := realLo
	if words <= 12 && anchors == 0 {
		lo += 2
	} else if words <= 25 && realLo > 0 {
		lo++
	}
	if questionish.MatchString(t) && words <= 20 && anchors == 0 {
		lo++
	}

	var class Class
	var margin int
	switch {
	case hi >= 3 && hi > lo:
		class, margin = ClassHigh, hi-lo
	case lo > hi && (realLo > 0 || (words <= 12 && anchors == 0)):
		class, margin = ClassTrivial, lo-hi
	default:
		class, margin = ClassMedium, 1
	}

	cd, cdHits := score(t, codeSigs)
	ed, edHits := score(t, editorialSigs)
	rs, rsHits := score(t, researchSigs)
	domain, dHits, dTop, dSecond := DomainGeneral, []string(nil), 0, 0
	for _, cand := range []struct {
		d    Domain
		n    int
		hits []string
	}{{DomainCode, cd, cdHits}, {DomainEditorial, ed, edHits}, {DomainResearch, rs, rsHits}} {
		if cand.n > dTop {
			dSecond = dTop
			domain, dHits, dTop = cand.d, cand.hits, cand.n
		} else if cand.n > dSecond {
			dSecond = cand.n
		}
	}
	// Research needs corroboration: a lone weak hit ("the paper" in passing)
	// must not steal editorial's or code's task.
	if domain == DomainResearch && dTop < 3 {
		if ed >= cd && ed > 0 {
			domain, dHits, dTop = DomainEditorial, edHits, ed
		} else if cd > 0 {
			domain, dHits, dTop = DomainCode, cdHits, cd
		}
	}

	// Confidence: class margin + domain margin + having any evidence at all.
	conf := 0.35
	conf += 0.15 * float64(min(margin, 3))
	if dTop > 0 {
		conf += 0.1
	}
	if dTop-dSecond >= 2 {
		conf += 0.1
	}
	if words < 4 && hi == 0 && lo <= 2 { // bare fragments are guesses
		conf = 0.4
	}
	if conf > 0.95 {
		conf = 0.95
	}

	why := fmt.Sprintf("hi=%d%v lo=%d%v dom=%s%v", hi, trim3(hiHits), lo, trim3(loHits), domain, trim3(dHits))
	return TriageResult{Class: class, Domain: domain, Confidence: conf, Why: why}
}

func trim3(h []string) []string {
	if len(h) > 3 {
		return h[:3]
	}
	return h
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// FastLadder is the try-order for a triaged trivial/medium task, by domain.
// Two hard rules: claude never appears (high-class-only worker - the bazooka
// gate; force it or say /quality to override), and the DIRECTOR's leg never
// appears (it is excluded from auto-assignment everywhere else too). Orders
// come from the Jul 19–31 scorecards: grok 7.7 leads prose, free is the
// zero-cost trivial default, codex/cursor lead code.
func FastLadder(c Class, d Domain) []Leg {
	var pref []Leg
	if c == ClassTrivial {
		switch d {
		case DomainCode:
			pref = []Leg{LegFree, LegCodex, LegGrok, LegMiniMax}
		default: // editorial / research / general one-liners
			pref = []Leg{LegFree, LegGrok, LegMiniMax, LegGLM}
		}
	} else {
		switch d {
		case DomainCode:
			pref = []Leg{LegCursor, LegCodex, LegGrok, LegFree}
		case DomainResearch:
			pref = []Leg{LegGrok, LegGLM, LegMiniMax, LegCursor}
		default: // editorial / general
			pref = []Leg{LegGrok, LegMiniMax, LegGLM, LegFree}
		}
	}
	out := make([]Leg, 0, len(pref))
	for _, l := range pref {
		if l == LegClaude || IsFrontierClass(l) || l == Director || !KnownLeg(l) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// ClassifyWithLLM is tier 1: a classify-only call on the FREE leg for
// low-confidence triage. Strict JSON, no tools, short deadline; any failure
// falls back to the heuristic verdict - this tier may only ever ADD signal.
// onCall, when set, receives the call whatever it returns: tier 1 is a real
// provider call on a real quota, and until M1.2 threaded it the free leg's
// classification was the one routing cost nobody could see.
func ClassifyWithLLM(task string, port int, onCall CallHook) (Class, Domain, error) {
	d := NewDispatcher(port)
	d.Title = "triage"
	d.NoTools = true
	d.Timeout = 25 * time.Second
	prompt := fmt.Sprintf(`Classify this task. Reply with STRICT JSON only, no prose:
{"class":"<trivial|medium|high>","domain":"<code|editorial|research|general>"}

trivial: one-liner, typo, single-sentence edit or question
medium: ordinary focused work (a rewrite, a function, a review of one thing)
high: architecture, security, concurrency, multi-part audit, formal soundness

Task:
%s`, truncateStr(task, 1500))
	res, err := d.Run(LegFree, prompt)
	if onCall != nil {
		onCall(LegFree, "classify", res, err)
	}
	if err != nil {
		return "", "", err
	}
	var out struct {
		Class  string `json:"class"`
		Domain string `json:"domain"`
	}
	if err := extractJSON(res.Text, &out); err != nil {
		return "", "", err
	}
	c, ok := ParseClass(out.Class)
	if !ok {
		return "", "", fmt.Errorf("triage LLM returned class %q", out.Class)
	}
	switch Domain(strings.ToLower(strings.TrimSpace(out.Domain))) {
	case DomainCode, DomainEditorial, DomainResearch, DomainGeneral:
		return c, Domain(strings.ToLower(out.Domain)), nil
	}
	return c, DomainGeneral, nil
}

// midPrefer finds a preference directive anywhere in a turn: whitespace-or-start
// before the slash, a word boundary after, and never inside a path (/quality/x).
// The short alias /q stays leading-only - mid-prompt it collides with too much
// real text. The fork parses prefixes at position 0 only, so a user who writes
// "…called OPSIS. /quality review…" was silently ignored (2026-08-01).
var midPrefer = regexp.MustCompile(`(?i)(?:^|\s)/(quality|best|speed|fast|save|cheap|frontier)\b`)

// MidPromptFrontier reports a /frontier stated anywhere in the turn but its
// head (the head is the pseudo-leg, read by the dispatch): the request's
// effort is the ceiling on whichever leg runs - "/claude /frontier X" after
// hoisting, or a workflow whose stages each carry it.
func MidPromptFrontier(task string) bool {
	head := len(task) - len(strings.TrimLeft(task, " \t"))
	for _, m := range midPrefer.FindAllStringSubmatchIndex(task, -1) {
		if m[2] == head+1 || (m[3] < len(task) && task[m[3]] == '/') {
			continue // the head, or a path segment ("/frontier/notes.md")
		}
		if strings.EqualFold(task[m[2]:m[3]], "frontier") {
			return true
		}
	}
	return false
}

// MidPromptPrefer returns the routing preference stated anywhere in the task,
// or "" when none. Leading prefixes keep working via the fork; this catches
// the rest. It does not modify the task - workers cope with seeing the token,
// and stripping mid-text risks mangling prose and paths.
func MidPromptPrefer(task string) string {
	m := midPrefer.FindStringSubmatchIndex(task)
	if m == nil {
		return ""
	}
	// A path segment follows immediately: "/quality/report.md" is a file, not a wish.
	end := m[3]
	if end < len(task) && task[end] == '/' {
		return ""
	}
	switch strings.ToLower(task[m[2]:m[3]]) {
	case "speed", "fast":
		return "speed"
	case "save", "cheap":
		return "save"
	}
	return "quality"
}
