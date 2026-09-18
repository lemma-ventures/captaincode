package main

// Context compaction (R1, PRIME_AGENT_NOTES.md - lifted from prime-agent's
// design, reshaped for a stateless wrapper): when the replayed conversation
// exceeds the class budget, the elided span is SUMMARIZED once on the free leg
// and cached, instead of cut out with an "[elided]" marker. The mechanics that
// matter, per prime's compaction.md:
//
//   - cut at TURN boundaries ([user]/[assistant] markers), never mid-turn
//   - iterative: growth summarizes only the NEW span, folding in the previous
//     summary - nothing silently exits history
//   - fail-open: any summarizer failure falls back to the old windowing
//
// The cache is the stateless wrapper's analog of prime's resident session: it
// is keyed by the conversation's stable head (a TUI session replays from its
// beginning every turn), so each session compacts once per growth step, not
// once per turn. CAPTAIN_COMPACT=0 restores plain windowing.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const (
	compactSummaryReserve = 8_000 // chars of budget reserved for the summary block
	compactKeyHead        = 2_048 // conversation head that fingerprints a session
	compactCacheMax       = 16    // sessions worth of summaries kept in memory
)

type compactState struct {
	boundary int    // chars of the conversation already folded into summary
	summary  string // iterative summary of prompt[:boundary]
	at       time.Time
}

// turnBoundaryBefore finds the last turn start at or before pos, so the kept
// tail always begins with a whole [user]/[assistant] turn (prime's cut rule).
func turnBoundaryBefore(prompt string, pos int) int {
	if pos >= len(prompt) {
		pos = len(prompt) - 1
	}
	u := strings.LastIndex(prompt[:pos], "\n[user]\n")
	a := strings.LastIndex(prompt[:pos], "\n[assistant]\n")
	cut := u
	if a > cut {
		cut = a
	}
	if cut <= 0 {
		return -1
	}
	return cut + 1 // keep the leading newline out, start at "[user]…"
}

// compactLeg picks the summarizer. Default free (zero marginal cost), but a
// big session on a slow free model made compaction miss its window on ~half of
// turns (live 2026-08-30: 116 ok / 129 timed out) - and a failed fold is pure
// latency. CAPTAIN_COMPACT_LEG=gemini (or deepseek) buys that time back for
// fractions of a cent, and summaries are cached per boundary.
func compactLeg() captaincode.Leg {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_COMPACT_LEG")); v != "" {
		if l := captaincode.Leg(v); captaincode.KnownLeg(l) {
			return l
		}
	}
	// Default preference: gemini (gemini-3.7-flash). Compaction ships the WHOLE
	// conversation, so it is the most latency-critical call captain makes, and
	// flash-class models are built for exactly this. Measured on the same 78k
	// prose span (2026-08-30): gemini 6.4s, kimi-k3 >5min, free-tier nemotron
	// timed out at 2m06s. (kimi looked fast on a first probe only because that
	// span was repetitive - always benchmark summarizers on DISTINCT prose.)
	legs := os.Getenv("CAPTAIN_LEGS")
	if legs == "" || strings.Contains(legs, string(captaincode.LegGemini)) {
		return captaincode.LegGemini
	}
	return captaincode.LegFree
}

// summarizeSpan is the default tier: the FREE leg, no tools, bounded. The
// structured asks mirror prime's summary format, plus the style note the
// editorial workload depends on.
func summarizeSpan(span, prev string, onCall captaincode.CallHook) (string, error) {
	d := captaincode.NewDispatcher(opencodePort)
	d.Title = "compact"
	d.NoTools = true
	// Scale with span size: 90s strangled a 1MB session (live 2026-08-28 -
	// compaction timed out every turn, windowing fallback each time). Cached
	// per boundary, so the long first pass amortizes.
	d.Timeout = 90*time.Second + time.Duration(len(span)/4_000)*time.Second
	if d.Timeout > 6*time.Minute {
		d.Timeout = 6 * time.Minute
	}
	var sb strings.Builder
	sb.WriteString("Summarize this conversation span for an AI assistant that must continue the work seamlessly. STRUCTURE: 1) task state & open goals, 2) decisions made (with the user's exact key wordings), 3) essential content produced (titles, names, numbers, file paths touched), 4) the user's style/preferences. Be dense; keep exact terms; max ~600 words.\n\n")
	if prev != "" {
		sb.WriteString("Previous summary (fold it in - do not lose anything from it):\n" + prev + "\n\n")
	}
	sb.WriteString("New span:\n" + span)
	leg := compactLeg()
	res, err := d.Run(leg, sb.String())
	if onCall != nil {
		onCall(leg, "compaction", res, err)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("empty summary")
	}
	return strings.TrimSpace(res.Text), nil
}

func (b *brain) summarize(span, prev string, onCall captaincode.CallHook) (string, error) {
	if b.summarizeFn != nil {
		return b.summarizeFn(span, prev)
	}
	return summarizeSpan(span, prev, onCall)
}

// fitPrompt fits an over-budget replay into budget chars: compaction when
// enabled and possible, the old lossy window otherwise.
func (b *brain) fitPrompt(ws captaincode.Workspace, leg captaincode.Leg, prompt string, budget int) string {
	if len(prompt) <= budget {
		return prompt
	}
	// Stage 1 - deterministic snip. Costs nothing; on a coding session it is
	// usually most of the win, and when it alone fits the budget the turn pays
	// NO summarization latency at all.
	if pruned, saved := pruneSpan(prompt); saved > 0 {
		if len(pruned) <= budget {
			fmt.Printf("captain brain: %s prompt pruned %d → %d chars (no LLM needed)\n", leg, len(prompt), len(pruned))
			return pruned
		}
		fmt.Printf("captain brain: %s prompt pruned %d → %d chars (still over budget → summarizing)\n", leg, len(prompt), len(pruned))
		prompt = pruned
	}
	if os.Getenv("CAPTAIN_COMPACT") == "0" {
		fmt.Printf("captain brain: %s prompt windowed %d → %d chars (compaction disabled)\n", leg, len(prompt), budget)
		return windowPrompt(prompt, budget)
	}
	// Stage 2 - LLM fold of whatever survived.
	// Compaction is a provider call the turn pays for, on the whole
	// conversation - the most expensive overhead call captain makes. Bill it
	// to the turn it serves rather than letting it run off the books (M1.2).
	out, err := b.compactPrompt(ws, prompt, budget, b.chargeOverhead(lastUserTurn(prompt)))
	if err != nil {
		fmt.Printf("captain brain: compaction failed (%v) - windowing %d → %d chars\n", err, len(prompt), budget)
		return windowPrompt(prompt, budget)
	}
	fmt.Printf("captain brain: %s prompt compacted %d → %d chars (summary + recent turns)\n", leg, len(prompt), len(out))
	return out
}

func (b *brain) compactPrompt(ws captaincode.Workspace, prompt string, budget int, onCall captaincode.CallHook) (string, error) {
	tailBudget := budget - compactSummaryReserve
	if tailBudget < 4_000 {
		tailBudget = budget / 2
	}
	cut := turnBoundaryBefore(prompt, len(prompt)-tailBudget)
	if cut <= 0 {
		return "", fmt.Errorf("no turn boundary to cut at")
	}

	key := prompt
	if len(key) > compactKeyHead {
		key = key[:compactKeyHead]
	}
	h := sha256.Sum256([]byte(key))
	sessionKey := hex.EncodeToString(h[:8])

	b.cmu.Lock()
	if b.compact == nil {
		b.compact = map[string]*compactState{}
	}
	if len(b.compact) > compactCacheMax { // crude but sufficient eviction
		for k, v := range b.compact {
			if time.Since(v.at) > time.Hour {
				delete(b.compact, k)
			}
		}
		if len(b.compact) > compactCacheMax {
			b.compact = map[string]*compactState{}
		}
	}
	st := b.compact[sessionKey]
	if st == nil {
		st = &compactState{}
		b.compact[sessionKey] = st
	}
	boundary, summary := st.boundary, st.summary
	b.cmu.Unlock()

	// A cached boundary beyond today's cut can only mean the "session" isn't
	// what we fingerprinted (or shrank) - resummarize from scratch.
	if boundary > cut {
		boundary, summary = 0, ""
	}
	if boundary < cut {
		newSummary, consumed, err := b.foldSpan(prompt[boundary:cut], summary, onCall)
		// Persist whatever folded, even on failure: the next turn resumes from
		// the new boundary instead of re-attempting the whole span forever.
		if consumed > 0 {
			summary, boundary = newSummary, boundary+consumed
			if _, err := publishDigest(currentProject(ws), summary); err != nil {
				fmt.Printf("captain brain: shared digest not published: %v\n", err)
			}
			b.cmu.Lock()
			st.boundary, st.summary, st.at = boundary, summary, time.Now()
			b.cmu.Unlock()
		}
		if err != nil {
			return "", err
		}
		if boundary < cut { // shouldn't happen, but never silently drop content
			return "", fmt.Errorf("fold incomplete (%d of %d chars)", consumed, cut)
		}
	}

	// The opening turn rides along verbatim: a summary that drops the original
	// ask is how a long session quietly loses its constraints.
	head := firstUserTurn(prompt)
	if head != "" {
		head = "[system]\n[captain: the session's opening request, kept verbatim]\n" + head + "\n\n"
	}
	return head + "[system]\n[captain: compacted summary of the earlier conversation - kept recent turns follow]\n" +
		summary + "\n\n" + prompt[cut:], nil
}

// compactChunk bounds ONE summarize call. A single ~700k-char span exceeded
// the free model's context window and returned nothing in 5 minutes, so every
// turn paid the timeout and fell back to lossy windowing anyway (live
// 2026-08-28). ~150k chars ≈ 37k tokens fits any worker model comfortably.
const compactChunk = 80_000

// foldSpan summarizes span in bounded slices, folding each into the running
// summary. Returns how many chars were consumed so a partial fold can be
// persisted and resumed on the next turn.
func (b *brain) foldSpan(span, prev string, onCall captaincode.CallHook) (summary string, consumed int, err error) {
	summary = prev
	for consumed < len(span) {
		end := consumed + compactChunk
		if end >= len(span) {
			end = len(span)
		} else if bnd := turnBoundaryBefore(span, end); bnd > consumed {
			end = bnd // never split a turn mid-sentence
		}
		out, ferr := b.summarize(span[consumed:end], summary, onCall)
		if ferr != nil {
			return summary, consumed, ferr
		}
		summary = out
		consumed = end
	}
	return summary, consumed, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
