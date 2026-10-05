package main

// Chains (pkg/captaincode/chain.go): "/frontier write the specs > /repeat 5
// /quality implement the next item" runs the first command to completion,
// then the second, each as a full turn on the ordinary dispatch path. A
// chain is a detached thread like /repeat's and shares its controls:
// `/repeat watch`, `/repeat show`, `/repeat finish` (no new step starts) and
// `captain stop` all work on it. A step that is a /repeat or a nested chain
// runs to its end before the next step starts.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// chainHandoffChars bounds how much of the previous step's answer the next
// step reads: its end, where a worker reports what it did.
const chainHandoffChars = 6_000

// handleChain starts a chain typed as the turn. Returns true when it answered.
func (b *brain) handleChain(ctx context.Context, w http.ResponseWriter, req oaiChatReq, raw string) bool {
	steps, ok := captaincode.SplitChain(raw)
	if !ok {
		return false
	}
	emit, status, finish := newCompletionWriter(w, req, "chain")
	defer finish()

	th := b.runningRepeat(req.ws.Dir, raw, len(steps))
	joined := th != nil
	if !joined {
		th = b.startChain(req, raw, steps, budgetFor(ctx))
	}
	var plan strings.Builder
	for i, st := range steps {
		fmt.Fprintf(&plan, "%d. %s\n", i+1, promptPeek(st))
	}
	if joined {
		emit(fmt.Sprintf("**chain %s is already running this** - not starting a second one.\n\n", th.id))
	} else {
		emit(fmt.Sprintf("**chain %s started** - %d steps, each after the previous one finishes:\n\n%s\n"+
			"Each step sees what the previous one answered. `/repeat finish` stops it after the current step; `! captain stop` works at any time.\n",
			th.id, len(steps), plan.String()))
	}
	// Queued behind other prompts, the chain runs in the background and the
	// queue moves on, as a /repeat does.
	if ctx.Value(queuedKey{}) != nil {
		finish()
		return true
	}
	b.repeatWatch(ctx, emit, status, req.ws.Dir, th.id)
	finish()
	return true
}

// startChain registers and launches a detached chain thread.
func (b *brain) startChain(req oaiChatReq, raw string, steps []string, budget *runBudget) *repeatThread {
	ctx, cancel := context.WithCancel(withBudget(context.Background(), budget))
	th := &repeatThread{
		budget:  budget,
		id:      "ch_" + fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff),
		dir:     req.ws.Dir,
		task:    raw,
		target:  len(steps),
		steps:   steps,
		started: time.Now(),
		cancel:  cancel,
	}
	b.rmu.Lock()
	b.repeatState()[th.id] = th
	b.rmu.Unlock()
	fmt.Printf("captain brain: chain %s started (%d steps) - %s\n", th.id, len(steps), promptPeek(raw))
	go b.runChain(ctx, th, req)
	return th
}

// runChain runs the steps in order. A failed step ends the chain: the next
// step was written to build on it.
func (b *brain) runChain(ctx context.Context, th *repeatThread, req oaiChatReq) {
	defer func() {
		b.rmu.Lock()
		th.finished = true
		b.rmu.Unlock()
		fmt.Printf("captain brain: chain %s finished (%d of %d steps)\n", th.id, th.done, len(th.steps))
	}()
	prev := ""
	for i, step := range th.steps {
		b.rmu.Lock()
		halted := th.stopped
		b.rmu.Unlock()
		if halted || ctx.Err() != nil {
			return
		}
		if !th.budget.take() {
			b.rmu.Lock()
			th.stopReason = fmt.Sprintf("run budget spent before step %d: this prompt and the loops it started ran %d turns (CAPTAIN_RUN_BUDGET)", i+1, th.budget.size())
			b.rmu.Unlock()
			return
		}
		start := time.Now()
		text := chainStepText(step)
		round := chainStepRequest(req, th.task, i, len(th.steps), text, prev)
		rec := &captureWriter{onStatus: func(s string) {
			b.rmu.Lock()
			th.live = append(th.live, s)
			th.liveSeq++
			if len(th.live) > repeatLiveKeep {
				th.live = th.live[len(th.live)-repeatLiveKeep:]
			}
			b.rmu.Unlock()
		}}
		b.rmu.Lock()
		th.live, th.roundFrom = nil, start
		b.rmu.Unlock()
		panicked := ""
		func() {
			defer func() { // a step must never take the brain down
				if r := recover(); r != nil {
					panicked = fmt.Sprintf("panic: %v", r)
				}
			}()
			if b.chainStepFn != nil { // test seam
				b.chainStepFn(round, rec)
				return
			}
			b.chatCompletions(rec, chatRequestFrom(ctx, round))
		}()
		out, errText := rec.answer()
		if panicked != "" {
			errText = panicked
		}
		if errText == "" && (rec.status >= 400 || (rec.status == 0 && b.chainStepFn == nil)) {
			errText = fmt.Sprintf("step returned HTTP %d", rec.status)
		}
		digest := ""
		if errText == "" {
			digest = b.roundSummary(text, out, roundFinal(text, start))
		}
		shown := out
		if len(shown) > repeatRoundChars {
			shown = shown[:repeatRoundChars] + "\n…[truncated - full text in `captain show`]"
		}
		b.rmu.Lock()
		th.done++
		th.rounds = append(th.rounds, roundRecord{n: th.done, at: start, dur: time.Since(start), text: shown, summary: digest, err: errText})
		if len(th.rounds) > repeatRoundKeep {
			th.rounds = th.rounds[len(th.rounds)-repeatRoundKeep:]
		}
		if errText != "" {
			th.failed++
			th.lastErr = promptPeek(errText)
			th.stopReason = fmt.Sprintf("step %d failed, so the steps after it did not run", i+1)
		}
		b.rmu.Unlock()
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: "chain", Model: "chain",
			Text: fmt.Sprintf("%s step %d/%d: %s", th.id, i+1, len(th.steps), promptPeek(text)),
			Ms:   time.Since(start).Milliseconds()})
		if errText != "" {
			return
		}
		prev = out
	}
}

// chainStepText is what a step's turn receives. A group holding a chain stays
// wrapped (its turn splits it again); a group holding one command or a CWL
// workflow is unwrapped, since a turn does not read parentheses.
func chainStepText(step string) string {
	if !captaincode.IsGroupStep(step) {
		return step
	}
	inner := captaincode.UnwrapGroup(step)
	if _, ok := captaincode.SplitChain(inner); ok {
		return step
	}
	return inner
}

// chainStepRequest replays the conversation up to the chain, then the chain
// as typed, what the earlier steps answered, and this step as the turn: the
// step's command must open the last user message for dispatch to read it.
func chainStepRequest(req oaiChatReq, chain string, i, n int, step, prev string) oaiChatReq {
	msgs := append([]oaiMessage(nil), req.Messages...)
	if j := lastUserIndex(msgs); j >= 0 {
		msgs = msgs[:j]
	}
	note := fmt.Sprintf("[captain] I am running this as a chain of %d steps, one after another. Step %d runs now.", n, i+1)
	if prev != "" {
		note += fmt.Sprintf(" Step %d answered (its end):\n\n%s", i, captaincode.CutTail(prev, chainHandoffChars))
	}
	msgs = append(msgs,
		oaiMessage{Role: "user", Content: jsonString(chain)},
		oaiMessage{Role: "assistant", Content: jsonString(note)},
		oaiMessage{Role: "user", Content: jsonString(step)})
	// "auto", not the chain's model: a chain typed as "/frontier … > /repeat …"
	// arrives as model=frontier, which would run every later step as frontier.
	return oaiChatReq{Model: modelForTask(step, "auto"), Stream: true, Messages: msgs, ws: req.ws}
}
