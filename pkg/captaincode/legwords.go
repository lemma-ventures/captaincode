package captaincode

// Route words: a leg's model and the route it runs on, named the way the
// legs added for a route are (grok-cli, codex-cli, codex-openrouter). A leg
// id names the model only - /codex is GPT-6.1 Sol through opencode, /grok is
// xAI through opencode - so every leg also answers to <id>-<route>:
//
//	-cli         the vendor's own CLI on this host (claude -p, cursor-agent, codex exec, grok -p)
//	-openrouter  opencode, through OpenRouter
//	-opencode    opencode, on the provider's own credential (ChatGPT, SuperGrok, Hugging Face, NIM)
//
// The ids stay what the ledger, the priors and the overlays key on (2026-10-13:
// new words only); a route word resolves to its leg before anything reads
// the turn. A leg whose id already ends in its route needs no second word,
// and a word never shadows a leg of that name.

import (
	"regexp"
	"sort"
	"strings"
)

// routeSuffix is the route a spec runs on, as its word spells it.
func routeSuffix(s LegSpec) string {
	switch s.Transport {
	case TransportClaudeCLI, TransportCursorCLI, TransportCodexCLI, TransportGrokCLI:
		return "cli"
	case "", TransportOpencode:
		if s.Provider == "openrouter" {
			return "openrouter"
		}
		return "opencode"
	}
	return ""
}

// RouteWord is the <id>-<route> word for a leg, or "" when the id already
// names its route or the leg takes no task.
func RouteWord(l Leg) string {
	s, ok := specs[l]
	if !ok || !ServesTasks(l) {
		return ""
	}
	suf := routeSuffix(s)
	if suf == "" || strings.HasSuffix(string(l), "-"+suf) {
		return ""
	}
	return string(l) + "-" + suf
}

// RouteWords maps every route word to its leg, active task-serving legs only.
func RouteWords() map[string]Leg {
	out := map[string]Leg{}
	for _, l := range AllLegs {
		w := RouteWord(l)
		if w == "" || KnownLeg(Leg(w)) {
			continue
		}
		out[w] = l
	}
	return out
}

// ResolveLegWord reads a leg id or a route word (case-insensitive, an optional
// leading slash) as the leg it names.
func ResolveLegWord(w string) (Leg, bool) {
	w = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(w), "/"))
	if KnownLeg(Leg(w)) {
		return Leg(w), true
	}
	l, ok := RouteWords()[w]
	return l, ok
}

// CanonicalLegWords rewrites every /<route word> in a turn as /<leg>, so the
// forced head, a workflow stage (/grok-cli fix A + /codex-opencode fix B) and
// a team member all read the leg id. Only a whole slash word at the start
// of the text or after a space or an operator is touched: a path such as
// /Users/me/codex-opencode is left alone.
func CanonicalLegWords(text string) string {
	words := RouteWords()
	if len(words) == 0 || !strings.Contains(text, "/") {
		return text
	}
	keys := make([]string, 0, len(words))
	for w := range words {
		keys = append(keys, regexp.QuoteMeta(w))
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	re := regexp.MustCompile(`(?i)(^|[\s(+>&|])/(` + strings.Join(keys, "|") + `)([\s:)]|$)`)
	// A match consumes its delimiters, so two words one space apart need a
	// second pass.
	for i := 0; i < 2; i++ {
		text = re.ReplaceAllStringFunc(text, func(m string) string {
			sm := re.FindStringSubmatch(m)
			return sm[1] + "/" + string(words[strings.ToLower(sm[2])]) + sm[3]
		})
	}
	return text
}
