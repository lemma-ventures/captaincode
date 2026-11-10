package main

import (
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// fableLimit reports whether a limit closes Fable rather than the Claude
// account: a model tier's own limit ("your Fable limit"), or the monthly
// extra-usage spend cap Fable is metered on (fable.go).
func fableLimit(rl *captaincode.RateLimitError) bool {
	if rl.Tier != "" {
		return true
	}
	m := strings.ToLower(rl.Msg)
	return strings.Contains(m, "spend limit") || strings.Contains(m, "spend cap") || strings.Contains(m, "fable")
}

// fableClosed reports whether a limit closed Fable, and until when.
func (b *brain) fableClosed() (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.ledger.Cooldowns[captaincode.FableCooldownKey]
	return until, ok && time.Now().Before(until)
}

// closeFable keeps Fable closed until the limit resets, or for an hour when
// the message names no reset.
func (b *brain) closeFable(rl *captaincode.RateLimitError) time.Time {
	until := rl.ResetAt
	if until.IsZero() || !until.After(time.Now()) {
		until = time.Now().Add(time.Hour)
	}
	b.mu.Lock()
	b.ledger.Cooldowns[captaincode.FableCooldownKey] = until
	b.mu.Unlock()
	return until
}

// fableNote names Fable on a frontier pick that runs it.
func fableNote(l captaincode.Leg, open bool) string {
	if l == captaincode.LegClaude && open {
		return " on " + captaincode.ModelShort(captaincode.FableModel())
	}
	return ""
}
