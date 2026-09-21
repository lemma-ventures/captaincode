package main

// Queued prompts run one after the other (2026-09-21).
//
// opencode 1.18 stores a prompt typed while a turn runs as an ordinary user
// message behind the running one and, when the turn ends, starts ONE next
// turn over the whole transcript - so five prompts queued during a four-
// minute codex run reached the brain as one request with five trailing user
// messages, the last of them "the task" and the other four mere history.
// The worker handled them all at once, or only the last, depending on how
// it read the transcript. A queue is a sequence: each prompt gets its own
// turn - its own head (/codex-cli, /cursor…), its own routing, its own
// worker - in the order typed, each seeing the answers before it, and the
// answers stream into the one assistant message opencode is waiting for,
// each under a line naming the prompt it answers.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// queueNudge is opencode's own post-compaction prompt. Behind real queued
// prompts it is noise: the work is the prompts.
const queueNudge = "Continue if you have next steps, or stop and ask for clarification if you are unsure how to proceed."

// queuedPrompts returns the indices of the user messages that follow the
// last assistant message - the prompts waiting their turn - when there is
// more than one of them. Empty messages and opencode's nudge do not count.
func queuedPrompts(msgs []oaiMessage) []int {
	last := -1
	for i, m := range msgs {
		if m.Role == "assistant" {
			last = i
		}
	}
	var out []int
	for i := last + 1; i < len(msgs); i++ {
		if msgs[i].Role != "user" && msgs[i].Role != "" {
			continue
		}
		t := strings.TrimSpace(messageText(msgs[i].Content))
		if t == "" || t == queueNudge {
			continue
		}
		out = append(out, i)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// queueModel is the model the plugin would have forced for this prompt had
// it been the last one typed: its own head, else auto.
func queueModel(text string) string {
	if f := captaincode.LeadingForced(captaincode.HoistLeading(text)); f != "" {
		return f
	}
	return "auto"
}

// runQueued runs each pending prompt as its own turn, in order, and streams
// the answers into one response.
func (b *brain) runQueued(w http.ResponseWriter, r *http.Request, req oaiChatReq, pending []int) {
	n := len(pending)
	fmt.Printf("captain brain: %d queued prompts in %s - running them one after the other\n", n, req.ws.Dir)
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	flush, _ := w.(http.Flusher)
	var smu sync.Mutex
	wroteHeader := false
	var whole strings.Builder
	chunk := func(delta map[string]any, finish any) {
		if !req.Stream {
			return
		}
		smu.Lock()
		defer smu.Unlock()
		if !wroteHeader {
			wroteHeader = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(200)
		}
		obj := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": "queue",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		payload, _ := json.Marshal(obj)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flush != nil {
			flush.Flush()
		}
	}
	first := true
	say := func(text string) {
		whole.WriteString(text)
		if first {
			first = false
			chunk(map[string]any{"role": "assistant", "content": text}, nil)
			return
		}
		chunk(map[string]any{"content": text}, nil)
	}
	status := func(s string) { chunk(map[string]any{"reasoning_content": s}, nil) }
	// Heartbeats between prompts and through quiet stretches: a silent SSE
	// stream is idle-killed while the worker keeps going.
	stopKA := make(chan struct{})
	defer close(stopKA)
	go func() {
		t := time.NewTicker(sseKeepaliveEvery)
		defer t.Stop()
		for {
			select {
			case <-stopKA:
				return
			case <-t.C:
				smu.Lock()
				if wroteHeader {
					fmt.Fprint(w, ": keepalive\n\n")
					if flush != nil {
						flush.Flush()
					}
				}
				smu.Unlock()
			}
		}
	}()
	status(fmt.Sprintf("%d queued prompts - one after the other", n))

	// The history each turn sees: the transcript up to its prompt, with the
	// earlier queued prompts each followed by the answer it got.
	history := append([]oaiMessage(nil), req.Messages[:pending[0]]...)
	for k, idx := range pending {
		if r.Context().Err() != nil {
			fmt.Printf("captain brain: queue stopped after %d/%d - the turn went away\n", k, n)
			break
		}
		text := strings.TrimSpace(messageText(req.Messages[idx].Content))
		sub := req
		sub.Model = queueModel(text)
		sub.Stream = true
		sub.Messages = append(append([]oaiMessage(nil), history...), req.Messages[idx])
		label := fmt.Sprintf("%d/%d", k+1, n)
		fmt.Printf("captain brain: queue %s → %s: %s\n", label, sub.Model, promptPeek(text))
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: sub.Model, Model: sub.Model, Text: fmt.Sprintf("queued %s: %s", label, promptPeek(text))})
		if k > 0 {
			say("\n\n---\n\n")
		}
		say(fmt.Sprintf("**[captain] queued %s** · %s\n\n", label, promptPeek(text)))
		var pmu sync.Mutex
		cap := &captureWriter{
			onStatus:  func(s string) { status(label + " · " + s) },
			onContent: func(d string) { pmu.Lock(); say(d); pmu.Unlock() },
		}
		func() {
			defer func() { // one prompt's failure must not take the rest down
				if rec := recover(); rec != nil {
					cap.status = 500
					cap.buf.WriteString(fmt.Sprintf("panic: %v", rec))
				}
			}()
			if b.chatFn != nil { // test seam
				b.chatFn(cap)
				return
			}
			b.chatCompletions(cap, chatRequestFrom(context.WithValue(r.Context(), queuedKey{}, true), sub))
		}()
		answer, errText := cap.answer()
		if errText != "" || cap.status >= 400 {
			if errText == "" {
				errText = fmt.Sprintf("status %d", cap.status)
			}
			say(fmt.Sprintf("[captain: queued %s failed - %s]", label, promptPeek(errText)))
			answer = "[captain] failed: " + errText
		}
		history = append(history, req.Messages[idx], oaiMessage{Role: "assistant", Content: jsonString(answer)})
	}
	if !req.Stream {
		writeJSON(w, 200, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": "queue",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": whole.String()}}},
		})
		return
	}
	chunk(map[string]any{}, "stop")
	smu.Lock()
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flush != nil {
		flush.Flush()
	}
	smu.Unlock()
}

// queuedKey marks a request the queue runner issued: it never splits again.
type queuedKey struct{}
