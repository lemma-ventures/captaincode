package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What a ladder SAYS when it runs out of legs. The ladder ends at the free
// tier, so naming only the last error reliably reported the least informative
// failure of the run - the credential and login faults that explained it had
// scrolled past under 500-character JSON blobs (2026-09-22).

func TestLadderExhaustedNamesEveryLeg(t *testing.T) {
	err := ladderExhausted([]legFailure{
		{captaincode.LegQwen, errors.New("openrouter/qwen: provider not configured - `opencode auth login`")},
		{captaincode.LegGrok, errors.New("xai/grok-build-0.1: credentials rejected")},
		{captaincode.LegFree, errors.New("empty output")},
	}, errors.New("empty output"))
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "all 3 legs failed")
	for _, want := range []string{"qwen", "grok", "free", "provider not configured", "credentials rejected"} {
		assert.Contains(t, msg, want)
	}
	assert.Contains(t, msg, "captain doctor", "the summary points at the command that explains the blocked legs")
}

func TestLadderExhaustedStaysTerseForASingleLeg(t *testing.T) {
	// One leg (a forced /claude, say) needs no roll call.
	err := ladderExhausted([]legFailure{{captaincode.LegClaude, errors.New("boom")}}, errors.New("boom"))
	require.Error(t, err)
	assert.Equal(t, "all legs failed; last error: boom", err.Error())
}

func TestLegReasonCollapsesAProviderBlob(t *testing.T) {
	blob := errors.New("opencode HTTP 500: {\n  \"name\": \"UnknownError\",\n  \"data\": {\n    \"message\": \"Unexpected server error.\"\n  }\n}")
	got := legReason(blob)
	assert.NotContains(t, got, "\n", "one leg, one line")
	assert.LessOrEqual(t, len(got), 161)
	assert.Contains(t, got, "UnknownError")
}

func TestLegReasonKeepsShortReasonsWhole(t *testing.T) {
	assert.Equal(t, "not logged in - run `claude /login`", legReason(errors.New("not logged in - run `claude /login`")))
}

// brainErrorText: the brain answers with two error shapes, and decoding one
// into the other printed "• bootstrap failed:" with nothing after it while
// the real reason sat in the body.

func TestBrainErrorTextReadsBothShapes(t *testing.T) {
	assert.Equal(t, "no repo brain at /x - run `captain euclid init --repo`",
		brainErrorText(500, []byte(`{"error":"no repo brain at /x - run `+"`captain euclid init --repo`"+`"}`)))
	assert.Equal(t, "the director is down",
		brainErrorText(503, []byte(`{"error":{"message":"the director is down"}}`)))
}

// benchPolicy is shared by the brain and the CLI: the CLI benched rate limits
// only, so an unconfigured leg was dispatched again on the very next turn.

func TestBenchPolicyBenchesFaultsThatDoNotHeal(t *testing.T) {
	d, why := benchPolicy(captaincode.LegQwen, captaincode.ErrProviderNotConfigured)
	assert.Equal(t, time.Hour, d)
	assert.Contains(t, why, "opencode auth login", "the bench reason names the fix")

	d, why = benchPolicy(captaincode.LegGrok, captaincode.ErrProviderAuth)
	assert.Equal(t, time.Hour, d)
	assert.Contains(t, why, "credentials")

	d, _ = benchPolicy(captaincode.LegKimi, captaincode.ErrProviderDown)
	assert.Equal(t, 10*time.Minute, d, "an outage is transient: a short bench, not an hour")
}

func TestBenchPolicyLeavesTheUnclassifiedAlone(t *testing.T) {
	// An unexplained failure is not evidence about the leg.
	d, why := benchPolicy(captaincode.LegFree, errors.New("the model said something odd"))
	assert.Zero(t, d)
	assert.Empty(t, why)
}

func TestBrainErrorTextFallsBackToTheBody(t *testing.T) {
	assert.Equal(t, "upstream said no", brainErrorText(502, []byte("upstream said no")))
	got := brainErrorText(500, nil)
	assert.True(t, strings.Contains(got, "500"), "an empty body still says what happened: %q", got)
}
