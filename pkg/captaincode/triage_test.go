package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Triage is tier 0 of routing: class + domain + confidence in <1ms, so the
// 17.5s-median director call is reserved for work that deserves it (usage
// analysis F2/F4, 2026-08-01). Cases below are real tasks from the ledger.

func TestTriageClassAndDomain(t *testing.T) {
	cases := []struct {
		task   string
		class  Class
		domain Domain
	}{
		// -- editorial (the actual majority workload)
		{"keep it in my writing style, condense it to 200 words max, drop what's redundant", ClassMedium, DomainEditorial},
		{"I believe the second paragraph reads weird, in fact sentence 2, 3 and 4 state things without explaining", ClassMedium, DomainEditorial},
		{"Name one benefit of proof of reserves in one sentence.", ClassTrivial, DomainEditorial},
		{"rate this abstract", ClassTrivial, DomainEditorial},
		{"this sentence is not digestable for the reader", ClassTrivial, DomainEditorial},
		{"fix typo in README", ClassTrivial, DomainCode},

		// -- high complexity, various domains
		{"review the research paper to ensure that we are exactly addressing all problems mentioned and have sound solutions and demonstrations (simulations) for it", ClassHigh, DomainResearch},
		{"red-team the threat model of the attestation design", ClassHigh, DomainResearch},
		{"debug the race condition in the worker drain", ClassHigh, DomainCode},
		{"audit the security of the auth proxy across the codebase", ClassHigh, DomainCode},

		// -- code
		{"implement what's missing for the p0 seam harness", ClassMedium, DomainCode},
		{"add a null check in parse()", ClassTrivial, DomainCode},
		{"write a unit test for the ledger Save function", ClassMedium, DomainCode},

		// -- general fallback
		{"what time zone is UTC+2 in winter", ClassTrivial, DomainGeneral},
	}
	for _, c := range cases {
		t.Run(c.task[:min(40, len(c.task))], func(t *testing.T) {
			tr := TriageTask(c.task)
			assert.Equal(t, c.class, tr.Class, "class for %q (why: %s)", c.task, tr.Why)
			assert.Equal(t, c.domain, tr.Domain, "domain for %q (why: %s)", c.task, tr.Why)
		})
	}
}

func TestTriageConfidence(t *testing.T) {
	clear := TriageTask("fix typo in README")
	assert.GreaterOrEqual(t, clear.Confidence, 0.7, "an unambiguous one-liner is high-confidence")

	vague := TriageTask("thoughts?")
	assert.Less(t, vague.Confidence, 0.6, "a bare fragment is low-confidence: %s", vague.Why)

	long := TriageTask("Here is my updated version, try to integrate the fixes above and make sure the flow stays intact while keeping the terminology consistent with the rest of the paper and not losing the qualifications we added yesterday")
	assert.NotEqual(t, ClassHigh, long.Class, "long prose iteration is not architecture work")
}

// The gate itself: high → director; anything else confident → no LLM at all.
func TestTriageNeedsDirector(t *testing.T) {
	assert.True(t, TriageTask("red-team the threat model of this design").NeedsDirector())
	assert.False(t, TriageTask("fix typo in README").NeedsDirector())
	assert.False(t, TriageTask("condense this paragraph, keep my style").NeedsDirector())
}

// Domain ladders: claude is NEVER in a fast-path menu (the bazooka gate), the
// director's leg is never auto-assigned, and order reflects the domain.
func TestFastLadderExcludesClaudeAndDirector(t *testing.T) {
	// Production semantics: claude directs (CAPTAIN_DIRECTOR=claude). Tests
	// default the director to grok, which would exclude grok from every ladder
	// and mask the real behaviour.
	SetDirector(LegClaude)
	t.Cleanup(func() { SetDirector(LegGrok) })
	for _, d := range []Domain{DomainEditorial, DomainCode, DomainResearch, DomainGeneral} {
		for _, c := range []Class{ClassTrivial, ClassMedium} {
			order := FastLadder(c, d)
			assert.NotEmpty(t, order, "%s/%s has a ladder", c, d)
			for _, l := range order {
				assert.NotEqual(t, LegClaude, l, "claude is high-class-only (%s/%s)", c, d)
				assert.NotEqual(t, Director, l, "the director is never auto-assigned (%s/%s)", c, d)
			}
		}
	}
	// Editorial medium leads with the leg the data rates for prose (grok 7.7),
	// not the coding legs (codex 6.2).
	assert.Equal(t, LegGrok, FastLadder(ClassMedium, DomainEditorial)[0])
	// Trivial anything starts free (zero marginal cost, 4.8s median).
	assert.Equal(t, LegFree, FastLadder(ClassTrivial, DomainEditorial)[0])
	assert.Equal(t, LegFree, FastLadder(ClassTrivial, DomainCode)[0])
	assert.Equal(t, LegLuna, FastLadder(ClassTrivial, DomainCode)[1], "OpenAI's cheap tier takes trivial code; Sol (codex) is kept for medium")
	assert.Contains(t, FastLadder(ClassMedium, DomainCode), LegCodex)
}

// "…it will be called OPSIS. /quality review…" - the fork only parses
// preference prefixes at position 0, so a mid-prompt /quality was silently
// dropped and the turn fast-pathed to grok against the user's stated intent
// (live 2026-08-01, the one real misroute in the post-MM35 sample).
func TestMidPromptPrefer(t *testing.T) {
	cases := []struct{ task, want string }{
		{"I changed the paper title, it will be called OPSIS. /quality review the paper end to end", "quality"},
		{"/quality leading still works", "quality"},
		{"tighten this up /best you can manage", "quality"},
		{"summarize the thread /fast", "speed"},
		{"draft it /cheap and I'll polish", "save"},
		{"run /quality on it", "quality"}, // referencing the command IS asking for it

		// The short alias counts at the head of the turn, after any leg prefix.
		{"/q refactor the lexer", "quality"},
		{"/codex /q refactor the lexer", "quality"},

		// Never fire on paths, code, or absent tokens.
		{"open /quality/report.md and check the numbers", ""},
		{"the binary is at /usr/bin/quality", ""},
		{"we value quality and speed here", ""}, // words without the slash are prose
		{"grep -r /q the repo", ""},             // the short alias is too ambiguous mid-prompt
		{"/qa the release", ""},
		{"/q/notes.md is stale", ""},
		{"", ""},
	}
	for _, c := range cases {
		name := c.task
		if name == "" {
			name = "(empty)"
		} else if len(name) > 30 {
			name = name[:30]
		}
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, MidPromptPrefer(c.task), "task: %q", c.task)
		})
	}
}
