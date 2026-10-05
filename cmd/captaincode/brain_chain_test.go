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

	th := b.startChain(req, raw, steps)
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
	th := b.startChain(oaiChatReq{Messages: []oaiMessage{{Role: "user", Content: jsonString(raw)}}, ws: captaincode.Workspace{Dir: t.TempDir()}}, raw, steps)
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
