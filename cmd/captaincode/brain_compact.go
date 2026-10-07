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
// is keyed by the folder and the conversation's opening (a TUI session
// replays from its beginning every turn), so each session compacts once per
// growth step, not once per turn. It is saved under ~/.captaincode/compact,
// so a brain restart does not summarize a long session from scratch, and a
// summary is reused only while the history it covers is unchanged.
//
// A summary is a model call before the worker starts, so it is only worth it
// when it saves much (live 2026-10-08: 1m43s spent to save 3k of 403k chars).
// When the replay is at most CAPTAIN_COMPACT_SKIP_PCT (15) percent over
// budget after pruning, it is cut (brain_cut.go) with no call. Otherwise the
// worker waits at most CAPTAIN_COMPACT_WAIT (20s) for the summary: past that
// it starts on the cut, and the summary keeps running in the background for
// the next turn. CAPTAIN_COMPACT=0 always cuts.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const (
	compactSummaryReserve = 8_000 // chars of budget reserved for the summary block
	compactKeyHead        = 2_048 // conversation head that fingerprints a session
	compactCacheMax       = 16    // sessions worth of summaries kept in memory
)

// compactState is one session's summary. Offsets count from the end of the
// framing (splitLead), so a framing that changes between turns does not
// invalidate the summary of the conversation after it.
type compactState struct {
	boundary int    // chars of the conversation already folded into summary
	summary  string // iterative summary of conv[:boundary]
	prefix   string // sha256 of conv[:boundary]: reused only while it matches
	at       time.Time
	// ready is closed when the fold running for this session ends; err is
	// that fold's error.
	ready chan struct{}
	err   error
}

// errFoldPending: the summary did not finish within the wait; it goes on in
// the background and the turn is cut instead.
var errFoldPending = errors.New("summary still running")

// compactSkipPct is how far over budget a pruned replay may be and still be
// cut without a summary (CAPTAIN_COMPACT_SKIP_PCT, default 15).
func compactSkipPct() int {
	if n, err := strconv.Atoi(os.Getenv("CAPTAIN_COMPACT_SKIP_PCT")); err == nil && n >= 0 {
		return n
	}
	return 15
}

// compactWait is how long a worker waits for a summary (CAPTAIN_COMPACT_WAIT,
// a Go duration or seconds; default 20s).
func compactWait() time.Duration {
	v := strings.TrimSpace(os.Getenv("CAPTAIN_COMPACT_WAIT"))
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 20 * time.Second
}

func prefixHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// compactKey names a session: its folder, the start of its replay and its
// opening request. The replay's start alone is opencode's system prompt, the
// same for every session - two sessions shared one summary.
func compactKey(ws captaincode.Workspace, prompt string) string {
	head := prompt
	if len(head) > compactKeyHead {
		head = head[:compactKeyHead]
	}
	h := sha256.Sum256([]byte(ws.Dir + "\x00" + head + "\x00" + firstUserTurn(prompt)))
	return hex.EncodeToString(h[:8])
}

func compactDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "compact")
}

type savedCompact struct {
	Boundary int       `json:"boundary"`
	Prefix   string    `json:"prefix"`
	Summary  string    `json:"summary"`
	At       time.Time `json:"at"`
}

// stateFor returns the session's state, loading a saved summary after a
// restart, and drops a summary whose covered history no longer matches.
// Call with b.cmu held.
func (b *brain) stateFor(key, conv string) *compactState {
	if b.compact == nil {
		b.compact = map[string]*compactState{}
	}
	st := b.compact[key]
	if st == nil {
		if len(b.compact) > compactCacheMax {
			for k, v := range b.compact {
				if time.Since(v.at) > time.Hour && v.ready == nil {
					delete(b.compact, k)
				}
			}
		}
		st = &compactState{}
		if raw, err := os.ReadFile(filepath.Join(compactDir(), key+".json")); err == nil {
			var sv savedCompact
			if json.Unmarshal(raw, &sv) == nil {
				st.boundary, st.prefix, st.summary, st.at = sv.Boundary, sv.Prefix, sv.Summary, sv.At
			}
		}
		b.compact[key] = st
	}
	if st.boundary > len(conv) || (st.boundary > 0 && prefixHash(conv[:st.boundary]) != st.prefix) {
		st.boundary, st.summary, st.prefix = 0, "", ""
	}
	return st
}

// saveCompact writes one session's summary; files older than two weeks go.
func saveCompact(key string, st savedCompact) {
	dir := compactDir()
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	raw, _ := json.Marshal(st)
	_ = os.WriteFile(filepath.Join(dir, key+".json"), raw, 0o600)
	if olds, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(olds) > 64 {
		for _, f := range olds {
			if fi, err := os.Stat(f); err == nil && time.Since(fi.ModTime()) > 14*24*time.Hour {
				_ = os.Remove(f)
			}
		}
	}
}

// cachedSummary is the session's latest summary and how much of the
// conversation it covers, for the cut.
func (b *brain) cachedSummary(ws captaincode.Workspace, prompt string) (string, int) {
	_, conv := splitLead(prompt)
	b.cmu.Lock()
	defer b.cmu.Unlock()
	st := b.stateFor(compactKey(ws, prompt), conv)
	return st.summary, st.boundary
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

// fitPrompt fits an over-budget replay into budget chars: pruning, then a
// summary when it is worth it and ready in time, else the cut.
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
		fmt.Printf("captain brain: %s prompt pruned %d → %d chars (still over budget)\n", leg, len(prompt), len(pruned))
		prompt = pruned
	}
	cut := func(why string) string {
		summary, covered := b.cachedSummary(ws, prompt)
		out := cutToFit(prompt, budget, summary, covered)
		fmt.Printf("captain brain: %s prompt cut %d → %d chars, oldest turns first (%s)\n", leg, len(prompt), len(out), why)
		return out
	}
	if os.Getenv("CAPTAIN_COMPACT") == "0" {
		return cut("compaction disabled")
	}
	if over := len(prompt) - budget; over*100 <= compactSkipPct()*budget {
		return cut(fmt.Sprintf("%d%% over budget: not worth a summary", (over*100+budget-1)/budget))
	}
	// Stage 2 - LLM fold of whatever survived.
	// Compaction is a provider call the turn pays for, on the whole
	// conversation - the most expensive overhead call captain makes. Bill it
	// to the turn it serves rather than letting it run off the books (M1.2).
	// The sidebar's activity feed says what is happening while it runs.
	t0 := time.Now()
	b.pushActivity(activity{Dir: ws.Dir, Kind: "route", Leg: string(leg), Model: string(leg),
		Text: fmt.Sprintf("compacting a %dk-char conversation on %s before %s starts (at most %s)", len(prompt)/1000, compactLeg(), leg, compactWait())})
	out, err := b.compactPrompt(ws, prompt, budget, b.chargeOverhead(lastUserTurn(prompt)), compactWait())
	if err != nil {
		why := fmt.Sprintf("compaction failed: %v", err)
		feed := "compaction failed - the oldest turns are left out instead"
		if errors.Is(err, errFoldPending) {
			why = fmt.Sprintf("the summary is not ready after %s; it finishes in the background for the next turn", compactWait())
			feed = "the summary is still running - this turn starts without the oldest turns, the next one gets the summary"
		}
		b.pushActivity(activity{Dir: ws.Dir, Kind: "route", Leg: string(leg), Model: string(leg), Text: feed, Ms: time.Since(t0).Milliseconds()})
		return cut(why)
	}
	fmt.Printf("captain brain: %s prompt compacted %d → %d chars (summary + recent turns)\n", leg, len(prompt), len(out))
	b.pushActivity(activity{Dir: ws.Dir, Kind: "route", Leg: string(leg), Model: string(leg),
		Text: fmt.Sprintf("compacted %dk → %dk chars in %s", len(prompt)/1000, len(out)/1000, time.Since(t0).Round(time.Second)),
		Ms:   time.Since(t0).Milliseconds()})
	return out
}

// compactPrompt folds the conversation up to the recent turns into the
// session's summary, waiting at most wait for the fold. A fold already
// running for the session is joined, not repeated; one that outlasts the
// wait finishes in the background and is saved for the next turn.
func (b *brain) compactPrompt(ws captaincode.Workspace, prompt string, budget int, onCall captaincode.CallHook, wait time.Duration) (string, error) {
	lead, conv := splitLead(prompt)
	tailBudget := budget - compactSummaryReserve - len(lead)
	if tailBudget < 4_000 {
		tailBudget = budget / 2
	}
	cut := turnBoundaryBefore(conv, len(conv)-tailBudget)
	if cut <= 0 {
		return "", fmt.Errorf("no turn boundary to cut at")
	}
	key := compactKey(ws, prompt)
	deadline := time.After(wait)
	for attempt := 0; attempt < 3; attempt++ {
		b.cmu.Lock()
		st := b.stateFor(key, conv)
		if st.boundary > cut {
			// Covers more than today's cut: the conversation shrank.
			st.boundary, st.summary, st.prefix = 0, "", ""
		}
		if st.boundary == cut {
			summary := st.summary
			b.cmu.Unlock()
			return assembleCompacted(lead, conv, summary, cut), nil
		}
		ready := st.ready
		if ready == nil {
			ready = make(chan struct{})
			st.ready, st.err = ready, nil
			go b.foldInBackground(ws, key, st, conv, st.boundary, cut, st.summary, onCall, ready)
		}
		b.cmu.Unlock()
		select {
		case <-ready:
		case <-deadline:
			return "", errFoldPending
		}
		b.cmu.Lock()
		ferr, boundary := st.err, st.boundary
		b.cmu.Unlock()
		if ferr != nil && boundary < cut {
			return "", ferr
		}
	}
	return "", fmt.Errorf("fold incomplete")
}

// foldInBackground folds conv[from:cut] into prev and records it on st, even
// partly, so the next turn resumes where it stopped.
func (b *brain) foldInBackground(ws captaincode.Workspace, key string, st *compactState, conv string, from, cut int, prev string, onCall captaincode.CallHook, ready chan struct{}) {
	summary, consumed, err := b.foldSpan(conv[from:cut], prev, onCall)
	b.cmu.Lock()
	if consumed > 0 {
		st.boundary, st.summary, st.at = from+consumed, summary, time.Now()
		st.prefix = prefixHash(conv[:st.boundary])
		saveCompact(key, savedCompact{Boundary: st.boundary, Prefix: st.prefix, Summary: st.summary, At: st.at})
	}
	st.err, st.ready = err, nil
	b.cmu.Unlock()
	if consumed > 0 {
		if _, perr := publishDigest(currentProject(ws), summary); perr != nil {
			fmt.Printf("captain brain: shared digest not published: %v\n", perr)
		}
	}
	close(ready)
}

// assembleCompacted is the framing, the opening request verbatim, the summary
// of the conversation up to cut, and the recent turns from cut on.
func assembleCompacted(lead, conv, summary string, cut int) string {
	// The opening turn rides along verbatim: a summary that drops the original
	// ask is how a long session quietly loses its constraints.
	head := firstUserTurn(conv)
	if head != "" {
		head = "[system]\n[captain: the session's opening request, kept verbatim]\n" + head + "\n\n"
	}
	return lead + head + "[system]\n[captain: compacted summary of the earlier conversation - kept recent turns follow]\n" +
		summary + "\n\n" + conv[cut:]
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
