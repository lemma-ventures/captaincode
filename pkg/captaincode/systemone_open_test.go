package captaincode

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A sidecar beside the vendor: what it is allowed to decide, what it is only
// allowed to watch, and the two states it must never be handed.

// openOnly configures a sidecar and no primary backend at all.
func openOnly(t *testing.T, promotions string) SystemOneBackends {
	t.Helper()
	noDecisionLeg(t)
	t.Setenv(SystemOneOpenURLEnv, "http://127.0.0.1:8181")
	t.Setenv(SystemOneOpenForEnv, promotions)
	return SystemOneBackendsFromEnv()
}

// oversizedState is the shape the action gate sends: a command, its directory and
// the worker's assignment. It is the state a 512-token backend truncates.
var oversizedState = "tool: bash\ncommand: rm -rf " + strings.Repeat("some/nested/path/", 90) +
	"\nworking directory: /home/dev/src/captaincode\nassignment: " + strings.Repeat("fix the failing test ", 40)

func TestNoOpenURLIsNoSidecar(t *testing.T) {
	noDecisionLeg(t)
	t.Setenv(SystemOneOpenURLEnv, "")
	b := SystemOneBackendsFromEnv()
	assert.Nil(t, b.Open, "the sidecar is opt-in: nothing about captain changes until its URL is set")
	assert.Nil(t, b.Primary)
	c, bar, _ := b.Decider(CapTriage, "fix the typo")
	assert.Nil(t, c)
	assert.Zero(t, bar)
}

func TestTheSidecarIsKeylessAndCarriesItsOwnContextAndTimeout(t *testing.T) {
	noDecisionLeg(t)
	t.Setenv(SystemOneOpenURLEnv, "http://127.0.0.1:8181/")
	c := SystemOneOpen()
	require.NotNil(t, c)
	assert.True(t, c.Keyless(), "a credential travelling to loopback buys nothing")
	assert.Equal(t, "http://127.0.0.1:8181", c.BaseURL, "the trailing slash would double up on every path")
	assert.Equal(t, "127.0.0.1:8181", c.Backend(), "the host is the stamp every shadow row carries")
	assert.Equal(t, SystemOneOpenContext, c.ContextTokens)
	assert.Equal(t, SystemOneOpenTimeout, c.HTTP.Timeout, "the primary's 20s belongs to a call crossing the internet")

	t.Setenv(SystemOneOpenContextEnv, "1024") // laya's multilingual checkpoint
	t.Setenv(SystemOneOpenTimeoutEnv, "5s")
	c = SystemOneOpen()
	assert.Equal(t, 1024, c.ContextTokens)
	assert.Equal(t, 5*time.Second, c.HTTP.Timeout)
}

// The rule the file exists for: a backend decides nothing on the strength of
// another backend's calibration.
func TestAnUnpromotedSidecarDecidesNothingAndIsShadowedInstead(t *testing.T) {
	b := openOnly(t, "")
	require.NotNil(t, b.Open)
	c, bar, _ := b.Decider(CapTriage, "fix the typo in the README")
	assert.Nil(t, c, "no primary and no promotion means no answer is acted on - triage falls back as if no decision leg existed")
	assert.Zero(t, bar)
	assert.Same(t, b.Open, b.Shadowing(CapTriage, "fix the typo in the README"),
		"it still answers beside captain's own choice: that is where its rows come from")
}

func TestPromotingTheSidecarTakesTheBarYouReadOffItsOwnRows(t *testing.T) {
	b := openOnly(t, "triage=0.85")
	bar, ok := b.Promoted(CapTriage)
	require.True(t, ok)
	assert.Equal(t, 0.85, bar)
	c, bar, why := b.Decider(CapTriage, "fix the typo in the README")
	assert.Same(t, b.Open, c)
	assert.Equal(t, 0.85, bar, "its own bar, never the primary's")
	assert.Contains(t, why, "127.0.0.1:8181")
	assert.Nil(t, b.Shadowing(CapTriage, "fix the typo"), "the decider's answer is already on the record as the decision's own")

	_, ok = b.Promoted(CapRoute)
	assert.False(t, ok, "promotion is per capability: triage says nothing about ranking legs")
	assert.Same(t, b.Open, b.Shadowing(CapRoute, "fix the typo"))
}

func TestAPromotionWithoutABarIsRefusedRatherThanDefaulted(t *testing.T) {
	b := openOnly(t, "triage")
	_, ok := b.Promoted(CapTriage)
	assert.False(t, ok, "a promotion with no number is someone believing a backend was tested")
	require.Len(t, b.Refused, 1)
	assert.Contains(t, b.Refused[0], "no bar")
}

func TestGateAndSuperviseAreNotPromotable(t *testing.T) {
	b := openOnly(t, "gate=0.9,supervise=0.9,keep=0.9,triage=0.8")
	for _, capability := range []string{CapGate, CapSupervise, CapKeep} {
		_, ok := b.Promoted(capability)
		assert.False(t, ok, "%s stays on the primary whatever the operator writes", capability)
	}
	_, ok := b.Promoted(CapTriage)
	assert.True(t, ok, "one refused entry must not take the honoured ones down with it")
	assert.Len(t, b.Refused, 3)
}

func TestNonsensePromotionsAreNamedNotDropped(t *testing.T) {
	b := openOnly(t, "triage=2,route=nope,nosuchthing=0.9")
	assert.Nil(t, b.Bars)
	require.Len(t, b.Refused, 3)
	assert.Contains(t, strings.Join(b.Refused, "\n"), "not a confidence in (0,1]")
	assert.Contains(t, strings.Join(b.Refused, "\n"), "no such capability")
}

// A truncated state is not a small state: laya cuts an oversized state and
// answers anyway, so the call must not be sent in the first place.
func TestAStateTheSidecarCannotHoldGoesToThePrimary(t *testing.T) {
	noDecisionLeg(t)
	t.Setenv(SystemOneKeyEnv, "k-test")
	t.Setenv(SystemOneOpenURLEnv, "http://127.0.0.1:8181")
	t.Setenv(SystemOneOpenForEnv, "triage=0.85,route=0.85")
	b := SystemOneBackendsFromEnv()
	require.NotNil(t, b.Primary)

	assert.False(t, b.Open.Holds(oversizedState))
	assert.True(t, b.Primary.Holds(oversizedState), "the vendor reads 32k and refuses past it rather than truncating")

	c, bar, why := b.Decider(CapTriage, oversizedState)
	assert.Same(t, b.Primary, c)
	assert.Zero(t, bar, "the primary answers at its own tuned bar, not the sidecar's")
	assert.Contains(t, why, "overruns")
	assert.Nil(t, b.Shadowing(CapTriage, oversizedState),
		"an answer read off a truncated state is not a datum, and a hundred of them would promote a backend on questions it never saw")

	c, _, _ = b.Decider(CapTriage, "fix the typo in the README")
	assert.Same(t, b.Open, c, "a task head fits, and that is the whole win")
}

func TestAnUnboundedBackendHoldsAnything(t *testing.T) {
	c := &SystemOneClient{BaseURL: "http://127.0.0.1:9", ContextTokens: 0}
	assert.True(t, c.Holds(oversizedState), "0 means nothing is known to bound it, not that it holds nothing")
	assert.Greater(t, StateTokens(oversizedState), SystemOneOpenContext)
	assert.Equal(t, 0, StateTokens(""))
}

func TestTheVendorClientCarriesItsMeasuredCeiling(t *testing.T) {
	noDecisionLeg(t)
	t.Setenv(SystemOneKeyEnv, "k-test")
	c := SystemOneFromEnv()
	require.NotNil(t, c)
	assert.Equal(t, SystemOneContextTokens, c.ContextTokens)
}

func TestTheStartupLineSaysShadowOnlyUntilItIsPromoted(t *testing.T) {
	b := openOnly(t, "")
	lines := strings.Join(b.Describe(), "\n")
	assert.Contains(t, lines, "SHADOW ONLY")
	assert.Contains(t, lines, "--backend 127.0.0.1:8181")

	b = openOnly(t, "triage=0.85,gate=0.9")
	lines = strings.Join(b.Describe(), "\n")
	assert.Contains(t, lines, "decides triage ≥0.85")
	assert.Contains(t, lines, "refused")
	assert.NotContains(t, lines, "SHADOW ONLY")
}
