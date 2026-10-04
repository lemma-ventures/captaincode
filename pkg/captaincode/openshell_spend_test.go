package captaincode

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func spendWorker(requests, priced int, cost float64) *OpenShellResult {
	attempts := 1
	return &OpenShellResult{Report: &OpenShellReport{WorkerAttempts: &attempts, Shield: &OpenShellShield{
		Requests: requests, Responses: requests, PricedResponses: priced,
		PromptTokens: 100 * requests, CompletionTokens: 10 * requests, ReasoningTokens: 5 * requests, CostUSD: cost}}}
}

func TestOpenShellSpendIsCompleteOnlyWhenEveryRequestWasPriced(t *testing.T) {
	neverStarted := &OpenShellResult{Error: "not started: context canceled"}
	noSandbox := &OpenShellResult{Report: &OpenShellReport{Verdict: "inconclusive", WorkerAttempts: new(int)}}
	attempts := 1
	untallied := &OpenShellResult{Report: &OpenShellReport{WorkerAttempts: &attempts}}
	for _, tc := range []struct {
		name     string
		tasks    []*OpenShellResult
		tokens   int
		cost     float64
		complete bool
	}{
		{"every request priced", []*OpenShellResult{spendWorker(2, 2, 0.25), spendWorker(1, 1, 0.5)}, 330, 0.75, true},
		{"an unpriced request", []*OpenShellResult{spendWorker(2, 2, 0.25), spendWorker(3, 2, 0.5)}, 550, 0.75, false},
		{"a worker stopped before Shield was tallied", []*OpenShellResult{spendWorker(1, 1, 0.25), untallied}, 110, 0.25, false},
		{"workers that never reached a model", []*OpenShellResult{neverStarted, noSandbox, spendWorker(1, 1, 0.25), nil}, 110, 0.25, true},
		{"no request at all", []*OpenShellResult{neverStarted, noSandbox}, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, cost, complete := (&OpenShellRun{Tasks: tc.tasks}).Spend()
			assert.Equal(t, tc.tokens, tokens, "reasoning tokens are a subset of completion tokens")
			assert.InDelta(t, tc.cost, cost, 1e-12)
			assert.Equal(t, tc.complete, complete)
		})
	}
}

func TestOpenShellChargeIsTheShieldBillNeverAFreeLeg(t *testing.T) {
	unpriced := CallUsage(LegOpenShell, 1000, 0, nil)
	assert.Equal(t, UsageUnknown, unpriced.CostStatus, "a sandbox lists no price because its workers are priced elsewhere: %+v", unpriced)
	billed := CallUsage(LegOpenShell, 1000, 0.25, nil)
	assert.Equal(t, UsageMeasured, billed.CostStatus)
	assert.Equal(t, "runtime", billed.PriceSource)

	runner := &OpenShellRunner{RunDir: t.TempDir()}
	failed := &OpenShellRun{Verdict: "fail", Tasks: []*OpenShellResult{spendWorker(2, 2, 0.25)}}
	res, err := finishOpenShellRun(context.Background(), runner, failed, errors.New("worker failed"), false)
	require.Error(t, err)
	assert.Equal(t, 220, res.Tokens, "a failed run still spent what its workers were billed")
	assert.InDelta(t, 0.25, res.CostUSD, 1e-12)

	partial := &OpenShellRun{Verdict: "fail", Tasks: []*OpenShellResult{spendWorker(2, 1, 0.25)}}
	res, err = finishOpenShellRun(context.Background(), runner, partial, nil, true)
	require.ErrorIs(t, err, ErrInterrupted)
	assert.Zero(t, res.Tokens, "an incomplete bill is left unknown, not understated")
	assert.Zero(t, res.CostUSD)
	assert.Zero(t, res.CostCommitted, "no Shield budget means the commitment is unknown")
}

func TestOpenShellIncompleteBillChargesTheCommitment(t *testing.T) {
	attempts := 1
	run := &OpenShellRun{Verdict: "fail", Tasks: []*OpenShellResult{{Report: &OpenShellReport{
		WorkerAttempts: &attempts,
		Shield: &OpenShellShield{Requests: 2, Responses: 2, PricedResponses: 1, CostUSD: 0.01,
			Budget: &OpenShellShieldBudget{LimitUSD: 0.25, CommittedUSD: 0.25}},
	}}}}
	res, err := finishOpenShellRun(context.Background(), &OpenShellRunner{RunDir: t.TempDir()}, run, errors.New("worker failed"), false)
	require.Error(t, err)
	assert.Zero(t, res.CostUSD, "an incomplete provider bill stays off the measured cost")
	assert.InDelta(t, 0.25, res.CostCommitted, 1e-12)
	usage := OpenShellUsage(res)
	assert.Equal(t, UsageEstimated, usage.CostStatus)
	assert.Equal(t, "shield-committed", usage.PriceSource)
	assert.InDelta(t, 0.25, usage.CostUSD, 1e-12)
}
