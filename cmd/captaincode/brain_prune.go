package main

// Deterministic pre-compaction ("snip"), 2026-08-30.
//
// State of the art in agent harnesses is STAGED compaction: cheap mechanical
// stages run before the expensive LLM stage, and the LLM only ever sees what
// survives them (Claude Code's budget-reduction → snip → microcompact →
// collapse → auto-compact ladder). Captain jumped straight to the LLM stage,
// which is why an 855k-char coding session spent 2 minutes per turn failing to
// summarize content that is mostly mechanical bulk: re-pasted file dumps,
// repeated tool output, and blank-line drift.
//
// This file is that missing stage. It costs no tokens and no network, and when
// it alone brings the replay under budget the turn skips summarization
// entirely. It only ever removes REDUNDANCY (exact repeats) or the middle of
// enormous single blocks, and it always leaves a visible marker - the summary
// stage is where meaning gets compressed; this stage must not lose any.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"regexp"
	"strings"
)

const (
	// pruneBlockLimit: a single block longer than this is a paste or a tool
	// dump, not prose - keep its head and tail, elide the middle.
	pruneBlockLimit = 6_000
	pruneBlockHead  = 2_500
	pruneBlockTail  = 1_000
	// pruneDupMin: only blocks this long are worth deduping; short repeats
	// ("ok", "done") carry conversational meaning.
	pruneDupMin = 400
)

var blankRunRe = regexp.MustCompile(`\n{3,}`)

// pruneSpan removes mechanical bulk from a conversation replay. Returns the
// pruned text and how many chars it saved.
func pruneSpan(s string) (string, int) {
	before := len(s)
	// 1. blank-line drift: agent output accumulates runs of empty lines.
	s = blankRunRe.ReplaceAllString(s, "\n\n")

	// 2. exact-duplicate blocks: the SAME file dumped on five turns, the same
	//    error pasted repeatedly. The first copy stays in full; later copies
	//    become a pointer, so nothing becomes unknowable - only unrepeated.
	blocks := strings.Split(s, "\n\n")
	seen := map[string]int{}
	for i, b := range blocks {
		// Compare on trimmed content: the same dump pasted twice differs by a
		// trailing newline often enough that an exact match misses most repeats.
		b = strings.TrimRight(b, " \t\n")
		if len(b) < pruneDupMin {
			continue
		}
		h := sha256.Sum256([]byte(b))
		k := hex.EncodeToString(h[:8])
		if first, ok := seen[k]; ok {
			blocks[i] = fmt.Sprintf("[captain: identical to an earlier block (#%d), %d chars elided]", first+1, len(b))
			continue
		}
		seen[k] = i
	}
	s = strings.Join(blocks, "\n\n")

	// 3. giant single blocks: keep the head (what it is) and the tail (how it
	//    ended); elide the middle with a marker naming the loss.
	blocks = strings.Split(s, "\n\n")
	for i, b := range blocks {
		if len(b) <= pruneBlockLimit {
			continue
		}
		// Rune-safe cuts: a byte slice through a multibyte character hands
		// codex exec an argument it refuses ("invalid UTF-8 was detected in
		// one or more arguments"; every turn in the folder failed, 2026-09-17).
		blocks[i] = captaincode.CutHead(b, pruneBlockHead) +
			fmt.Sprintf("\n[captain: %d chars elided from this block]\n", len(b)-pruneBlockHead-pruneBlockTail) +
			captaincode.CutTail(b, pruneBlockTail)
	}
	s = strings.Join(blocks, "\n\n")
	return s, before - len(s)
}

// firstUserTurn returns the opening user turn, which carries the session's
// original intent. Compaction that drops it is how long-horizon agents lose
// the constraints they were given at the start ("governance decay"), so it is
// re-attached verbatim ahead of every summary.
func firstUserTurn(prompt string) string {
	i := strings.Index(prompt, "[user]\n")
	if i < 0 {
		return ""
	}
	rest := prompt[i:]
	if j := strings.Index(rest[len("[user]\n"):], "\n[user]\n"); j >= 0 {
		rest = rest[:j+len("[user]\n")]
	}
	if k := strings.Index(rest, "\n[assistant]\n"); k >= 0 {
		rest = rest[:k]
	}
	if len(rest) > 4_000 { // an opening paste is not intent
		rest = captaincode.CutHead(rest, 4_000) + "\n[captain: opening turn truncated]"
	}
	return rest
}
