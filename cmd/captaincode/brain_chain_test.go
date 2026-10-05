package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chainBrain records each step's turn: its model and its last user message.
func chainBrain(t *testing.T, answer func(step string) (string, int)) (*brain, func() []oaiChatReq) {
	t.Helper()
	captaincode.LoadRegistry(filepath.Join(t.TempDir(), "none.json"))
	b := teamBrain()
	b.roundSummaryFn = func(string) string { return "did the step" }
	var mu sync.Mutex
	var seen []oaiChatReq
	b.chainStepFn = func(req oaiChatReq, w *captureWriter) {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		text, status := answer(lastUserRaw(req.Messages))
		w.WriteHeader(status)
		fmt.Fprint(w, text)
	}
	return b, func() []oaiChatReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]oaiChatReq(nil), seen...)
	}
}

func waitChain(t *testing.T, b *brain, th *repeatThread) {
	t.Helper()
	require.Eventually(t, func() bool {
		b.rmu.Lock()
		defer b.rmu.Unlock()
		return th.finished
	}, 5*time.Second, 10*time.Millisecond)
}

func TestChainRunsEachStepAfterThePreviousAndHandsItsAnswerOn(t *testing.T) {
	b, seen := chainBrain(t, func(step string) (string, int) {
		if strings.HasPrefix(step, "/frontier") {
			return "Wrote docs/specs/F3.md and five roadmap items.", 200
		}
		return "Implemented the first roadmap item.", 200
	})
	raw := "/frontier create detailed specs and roadmap items\nfor the repair experience > /repeat 5 /quality implement next items following specs"
	steps, ok := captaincode.SplitChain(raw)
	require.True(t, ok)
	req := oaiChatReq{Model: "frontier", Messages: []oaiMessage{
		{Role: "user", Content: jsonString("earlier question")},
		{Role: "assistant", Content: jsonString("earlier answer")},
		{Role: "user", Content: jsonString(raw)},
	}, ws: captaincode.Workspace{Dir: t.TempDir()}}

	th := b.startChain(req, raw, steps, nil)
	waitChain(t, b, th)

	got := seen()
	require.Len(t, got, 2)
	assert.Equal(t, steps[0], lastUserRaw(got[0].Messages))
	assert.Equal(t, "frontier", got[0].Model)
	assert.Equal(t, "/repeat 5 /quality implement next items following specs", lastUserRaw(got[1].Messages),
		"the step's command opens the turn, so /repeat reads it")
	assert.Equal(t, "auto", got[1].Model, "a /frontier chain must not run its later steps as frontier")
	transcript := ""
	for _, m := range got[1].Messages {
		transcript += messageText(m.Content) + "\n"
	}
	assert.Contains(t, transcript, "earlier question", "the conversation before the chain is kept")
	assert.Contains(t, transcript, "Wrote docs/specs/F3.md", "step 2 sees what step 1 answered")
	assert.Equal(t, 2, th.done)
	assert.Empty(t, th.stopReason)
}

func TestChainStopsAtAFailedStep(t *testing.T) {
	b, seen := chainBrain(t, func(step string) (string, int) {
		if strings.HasPrefix(step, "/team") {
			return `{"error":{"message":"every leg is down"}}`, 503
		}
		return "fine", 200
	})
	raw := "/team audit it > /claude fix it > /repeat 2 /quality polish it"
	steps, ok := captaincode.SplitChain(raw)
	require.True(t, ok)
	th := b.startChain(oaiChatReq{Messages: []oaiMessage{{Role: "user", Content: jsonString(raw)}}, ws: captaincode.Workspace{Dir: t.TempDir()}}, raw, steps, nil)
	waitChain(t, b, th)
	assert.Len(t, seen(), 1, "the steps after a failure build on it, so they do not run")
	assert.Contains(t, th.stopReason, "step 1 failed")
}

func TestChainGroupStepsRunAsTheirOwnTurn(t *testing.T) {
	captaincode.LoadRegistry(filepath.Join(t.TempDir(), "none.json"))
	assert.Equal(t, "(/team build it > /repeat 3 /quality polish it)",
		chainStepText("(/team build it > /repeat 3 /quality polish it)"), "a nested chain stays wrapped: its turn splits it")
	assert.Equal(t, "/team build it", chainStepText("(/team build it)"), "one command is unwrapped")
	assert.Equal(t, "/grok draft + /codex draft > /claude merge",
		chainStepText("(/grok draft + /codex draft > /claude merge)"), "a CWL workflow is unwrapped")
	assert.Equal(t, "/claude fix it", chainStepText("/claude fix it"))
}

func TestChainIsCaughtWhenTyped(t *testing.T) {
	b, _ := chainBrain(t, func(string) (string, int) { return "done", 200 })
	raw := "/frontier plan it > (/team build it > /repeat 2 /quality polish it)"
	rec := httptest.NewRecorder()
	req := oaiChatReq{Model: "frontier", Messages: []oaiMessage{{Role: "user", Content: jsonString(raw)}}, ws: captaincode.Workspace{Dir: t.TempDir()}}
	ctx := context.WithValue(context.Background(), queuedKey{}, true)
	require.True(t, b.handleChain(ctx, rec, req, raw))
	assert.Contains(t, rec.Body.String(), "2 steps")
	assert.False(t, b.handleChain(ctx, httptest.NewRecorder(), req, "/repeat 5 /quality implement it"), "one command is not a chain")
	assert.False(t, b.handleChain(ctx, httptest.NewRecorder(), req, "/grok draft > /claude review"), "leg stages stay CWL")

	b.rmu.Lock()
	var th *repeatThread
	for _, t := range b.repeatState() {
		th = t
	}
	b.rmu.Unlock()
	require.NotNil(t, th)
	waitChain(t, b, th)
	assert.Equal(t, 2, th.done)
}

// formal/CommandSafety/Execution.lean, run_turns_le: a chain and the loops
// its steps start share one budget.
func TestChainAndNestedLoopsShareOneRunBudget(t *testing.T) {
	b, seen := chainBrain(t, func(string) (string, int) { return "ok", 200 })
	raw := "/team a > /claude b > /claude c > /claude d"
	steps, ok := captaincode.SplitChain(raw)
	require.True(t, ok)
	budget := &runBudget{left: 2, total: 2}
	th := b.startChain(oaiChatReq{Messages: []oaiMessage{{Role: "user", Content: jsonString(raw)}}, ws: captaincode.Workspace{Dir: t.TempDir()}}, raw, steps, budget)
	waitChain(t, b, th)
	assert.Len(t, seen(), 2, "two turns of budget, two steps")
	assert.Contains(t, th.stopReason, "run budget spent before step 3")

	// A loop started inside the chain draws on the same budget.
	ctx := withBudget(context.Background(), budget)
	assert.Same(t, budget, budgetFor(ctx))
	assert.False(t, budgetFor(ctx).take(), "already spent")
	fresh := budgetFor(context.Background())
	assert.Equal(t, runBudgetSize(), fresh.size(), "a typed prompt starts with a full budget")
}

func TestRepeatStopsWhenTheRunBudgetIsSpent(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(string) string { return "did it" }
	work := []string{
		"Fixed the failing auth test: the token clock was compared before refresh, so it expired mid-request.",
		"Migrated the storage callers to the new interface and deleted the compatibility shim.",
		"Found the flaky watcher: fsevents coalesces two writes, and the test asserted on the first.",
		"Backfilled the missing migration for the priors table and re-ran the importer end to end.",
	}
	rounds := 0
	b.chatFn = func(w *captureWriter) {
		fmt.Fprint(w, work[rounds%len(work)])
		rounds++
	}
	th := &repeatThread{id: "rp_budget", task: "work the backlog", target: 10, budget: &runBudget{left: 3, total: 3}}
	b.runRepeat(context.Background(), th, oaiChatReq{Model: "free"})
	assert.Equal(t, 3, rounds)
	assert.Contains(t, th.stopReason, "run budget spent")
}
