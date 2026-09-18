package main

// The usage a turn reports back to the TUI (2026-09-18).
//
// opencode's Context panel ("N tokens · N% used · $ spent") reads the usage
// block of each assistant message the provider returned. The brain answered
// zeros on every completion, so the panel never moved. It now reports the
// worker's real token count and the dollar cost the brain already accounts
// for (measured where the leg reports it - claude -p - else the registry
// estimate; a subscription leg's marginal cost is 0). Tokens are split the
// way EstimateCost assumes (¾ prompt, ¼ completion) when the leg reports
// only a total; the split is what the panel's "% used" reads, and it is a
// replayed-conversation figure either way.

import (
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// usageBlock is the OpenAI usage object for a run, with the cost the
// brain knows under the extension key the AI SDK forwards as metadata.
func usageBlock(leg captaincode.Leg, res captaincode.Result) map[string]any {
	u := captaincode.CallUsage(leg, res.Tokens, res.CostUSD, nil)
	prompt := res.Tokens * 3 / 4
	completion := res.Tokens - prompt
	return map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      res.Tokens,
		"cost":              u.CostUSD, // USD; openrouter's field name, read by clients that know it
		"captain_cost_usd":  u.CostUSD,
		"captain_cost":      string(u.CostStatus),
	}
}

// zeroUsage is what a turn with no worker run reports (control words).
func zeroUsage() map[string]any {
	return map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
}
