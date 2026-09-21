package main

// /parallel - run a second task alongside the one you're chatting about
// (2026-08-30).
//
//	/parallel research the competitor pricing page
//	/parallel status · /parallel show <id|last> · /parallel stop [id]
//
// WHY DETACHED. The queue the user hits is the TUI's: the fork holds one turn
// at a time and queues typed input while a turn streams. The brain has no such
// limit - it serves concurrent requests and the director is stateless per call,
// so two plans can run at once today. What cannot work is streaming a second
// run into a turn that has already ended. So `/parallel` acks immediately with
// an id, runs the FULL dispatch path (director → solo/team/workflow) in the
// background, and keeps the answer for collection. The next streaming turn
// mentions any finished run on the progress channel, so you learn about it
// without leaving the TUI; `/parallel show` prints it in full.
//
// It is deliberately NOT a second director inside the same session: sharing one
// conversation between two concurrently-mutating turns is how transcripts get
// interleaved and unreadable. Each parallel run is its own turn against the
// conversation as it stood when you launched it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// parallelMax bounds concurrent detached runs - each one is real spend.
const parallelMax = 4

type parallelRun struct {
	id       string
	dir      string // the workspace the run belongs to
	task     string
	started  time.Time
	ended    time.Time
	text     string
	err      string
	done     bool
	reported bool // its completion has been surfaced in a turn already
	cancel   context.CancelFunc
}

func (b *brain) parallelState() map[string]*parallelRun {
	if b.parallels == nil {
		b.parallels = map[string]*parallelRun{}
	}
	return b.parallels
}

// handleParallel intercepts the /parallel control words.
func (b *brain) handleParallel(w http.ResponseWriter, req oaiChatReq, raw string) bool {
	t := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(t), "/parallel") {
		return false
	}
	rest := strings.TrimSpace(t[len("/parallel"):])
	low := strings.ToLower(rest)

	emit, _, finish := newCompletionWriter(w, req, "parallel")
	defer finish()

	switch {
	case rest == "" || low == "status":
		emit(b.parallelStatus(req.ws.Dir))
	case low == "stop" || strings.HasPrefix(low, "stop "):
		emit(b.parallelStop(req.ws.Dir, strings.TrimSpace(rest[len("stop"):])))
	case strings.HasPrefix(low, "show"):
		emit(b.parallelShow(req.ws.Dir, strings.TrimSpace(rest[len("show"):])))
	default:
		emit(b.parallelStart(req, rest))
	}
	finish()
	return true
}

func (b *brain) parallelStart(req oaiChatReq, task string) string {
	b.pmu.Lock()
	running := 0
	for _, r := range b.parallelState() {
		if !r.done {
			running++
		}
	}
	b.pmu.Unlock()
	if running >= parallelMax {
		return fmt.Sprintf("already running %d parallel runs (max %d). Use `/parallel status`, or stop one with `/parallel stop <id>`.", running, parallelMax)
	}

	ctx, cancel := context.WithCancel(context.Background())
	pr := &parallelRun{
		id:      "pl_" + fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff),
		dir:     req.ws.Dir,
		task:    task,
		started: time.Now(),
		cancel:  cancel,
	}
	b.pmu.Lock()
	b.parallelState()[pr.id] = pr
	b.pmu.Unlock()

	// The run replays the conversation as it stands, with the last user turn
	// replaced by the parallel task - same context, different question.
	iter := oaiChatReq{Model: modelForTask(task, req.Model), Messages: append([]oaiMessage(nil), req.Messages...), ws: req.ws}
	for i := len(iter.Messages) - 1; i >= 0; i-- {
		if iter.Messages[i].Role == "user" || iter.Messages[i].Role == "" {
			iter.Messages[i] = oaiMessage{Role: "user", Content: jsonString(task)}
			break
		}
	}

	fmt.Printf("captain brain: parallel %s started - %s\n", pr.id, promptPeek(task))
	go func() {
		cap := &captureWriter{}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					b.pmu.Lock()
					pr.err = fmt.Sprintf("panic: %v", rec)
					b.pmu.Unlock()
				}
			}()
			b.chatCompletions(cap, chatRequestFrom(ctx, iter))
		}()
		text, errText := cap.answer()
		b.pmu.Lock()
		pr.text, pr.done, pr.ended = text, true, time.Now()
		if errText != "" {
			pr.err = errText
		}
		b.pmu.Unlock()
		fmt.Printf("captain brain: parallel %s finished in %s (%d chars)%s\n",
			pr.id, time.Since(pr.started).Round(time.Second), len(text),
			map[bool]string{true: " - FAILED", false: ""}[errText != ""])
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: "parallel", Model: "parallel",
			Text: fmt.Sprintf("%s finished: %s", pr.id, promptPeek(task)),
			Ms:   time.Since(pr.started).Milliseconds()})
	}()

	return fmt.Sprintf(`**parallel run %s started** - it does not block this chat.

Task: %s

Keep typing here; it runs its own director → worker turn against the conversation as it stands now.
- when it finishes you'll see a note on this session's progress line
- read it: `+"`/parallel show %s`"+` (or `+"`/parallel show last`"+`)
- list: `+"`/parallel status`"+` · cancel: `+"`/parallel stop %s`"+``,
		pr.id, promptPeek(task), pr.id, pr.id)
}

func (b *brain) parallelStatus(dir string) string {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	st := b.parallelState()
	mine, elsewhere := 0, 0
	for _, r := range st {
		if r.dir == dir {
			mine++
		} else {
			elsewhere++
		}
	}
	if mine == 0 {
		msg := "no parallel runs in this folder.\n\nStart one with `/parallel <task>` - it runs alongside this chat."
		if elsewhere > 0 {
			msg += fmt.Sprintf("\n\n(%d run(s) belong to other folders.)", elsewhere)
		}
		return msg
	}
	var sb strings.Builder
	sb.WriteString("### parallel runs\n\n")
	for _, r := range st {
		if r.dir != dir {
			continue
		}
		switch {
		case !r.done:
			fmt.Fprintf(&sb, "- **%s** - running %s · %s\n", r.id, time.Since(r.started).Round(time.Second), promptPeek(r.task))
		case r.err != "":
			fmt.Fprintf(&sb, "- **%s** - ✗ failed after %s · %s\n  %s\n", r.id,
				r.ended.Sub(r.started).Round(time.Second), promptPeek(r.task), promptPeek(r.err))
		default:
			fmt.Fprintf(&sb, "- **%s** - ✓ done in %s, %d chars · %s\n", r.id,
				r.ended.Sub(r.started).Round(time.Second), len(r.text), promptPeek(r.task))
		}
	}
	if elsewhere > 0 {
		fmt.Fprintf(&sb, "\n(%d run(s) belong to other folders.)\n", elsewhere)
	}
	sb.WriteString("\n`/parallel show <id|last>` prints a finished run in full.")
	return sb.String()
}

func (b *brain) parallelShow(dir, id string) string {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	st := b.parallelState()
	var pick *parallelRun
	for _, r := range st {
		if id == "" || id == "last" {
			if r.dir == dir && r.done && (pick == nil || r.ended.After(pick.ended)) {
				pick = r
			}
			continue
		}
		if r.id == id {
			pick = r
		}
	}
	if pick == nil {
		return "no such finished parallel run (`/parallel status` to list them)."
	}
	if !pick.done {
		return fmt.Sprintf("%s is still running (%s so far).", pick.id, time.Since(pick.started).Round(time.Second))
	}
	pick.reported = true
	if pick.err != "" {
		return fmt.Sprintf("### %s - failed\n\nTask: %s\n\n%s", pick.id, pick.task, pick.err)
	}
	return fmt.Sprintf("### %s - parallel result\n\nTask: %s\n\n---\n\n%s", pick.id, pick.task, pick.text)
}

func (b *brain) parallelStop(dir, id string) string {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	var stopped []string
	for k, r := range b.parallelState() {
		if r.done {
			continue
		}
		if threadMatches(r.dir, k, dir, id) {
			r.cancel()
			stopped = append(stopped, k)
		}
	}
	if len(stopped) == 0 {
		return "nothing to stop - no parallel run is in flight."
	}
	return "cancelling: " + strings.Join(stopped, ", ")
}

// parallelNotice reports finished-but-unannounced runs, once each. It rides the
// progress channel of the next streaming turn, so the user learns a background
// run landed without leaving the TUI and without it being spliced into an
// unrelated answer.
func (b *brain) parallelNotice(dir string) string {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	var lines []string
	for _, r := range b.parallelState() {
		if !r.done || r.reported || r.dir != dir {
			continue // another folder's run is announced in that TUI
		}
		r.reported = true
		if r.err != "" {
			lines = append(lines, fmt.Sprintf("✗ parallel %s failed - %s", r.id, promptPeek(r.task)))
			continue
		}
		lines = append(lines, fmt.Sprintf("✓ parallel %s finished (%d chars) - `/parallel show %s`", r.id, len(r.text), r.id))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// sseAnswer folds a captured SSE stream into the assistant text: content
// deltas concatenated; an inline "[captain] <leg> failed: …" line (how a
// worker error is spoken into a committed stream) becomes the error.
func sseAnswer(body string, status int) (text, errText string) {
	var sb strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
			continue
		}
		if ev.Error != nil && ev.Error.Message != "" {
			return "", ev.Error.Message
		}
		if len(ev.Choices) > 0 {
			sb.WriteString(ev.Choices[0].Delta.Content)
		}
	}
	text = sb.String()
	if status >= 400 {
		return "", promptPeek(text)
	}
	// A worker failure after the stream was committed is spoken as text, in
	// one of two shapes: "[captain] <leg> failed: …" (writeInlineFailure) and
	// "[captain: <leg> error - …]" (the solo stream). Either is the round's
	// error, not its answer - a /repeat thread that took them for answers
	// never counted a failure and never gave up.
	if i := strings.Index(text, "\n[captain] "); i >= 0 && strings.Contains(text[i:], " failed: ") {
		return strings.TrimSpace(text[:i]), strings.TrimSpace(text[i:])
	}
	if i := strings.Index(text, "[captain: "); i >= 0 && strings.Contains(text[i:], " error - ") {
		return strings.TrimSpace(text[:i]), strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text[i:]), "]"))
	}
	if strings.TrimSpace(text) == "" {
		return "", "empty response"
	}
	return text, ""
}

// captureWriter keeps a detached run's full answer. The launching turn is over,
// so the bytes have nowhere to stream - but unlike a repeat iteration, a
// parallel result is meant to be READ, so the whole body is retained.
type captureWriter struct {
	hdr    http.Header
	status int
	buf    strings.Builder
	// onStatus, when set, receives each progress line (reasoning_content) of
	// a STREAMING response as it arrives - a /repeat watcher shows what the
	// round's worker is doing instead of a silent minutes-long wait
	// (2026-09-12: "/repeat show" was queued behind the streaming turn while
	// the user only wanted to see the worker work).
	onStatus func(string)
	// onContent, when set, receives each answer-text delta as it arrives:
	// the queue runner streams a prompt's answer through while it runs.
	onContent func(string)
	tail      string // partial SSE line carried across writes
	prose     string // answer text of the current paragraph, for narration
}

func (c *captureWriter) Header() http.Header {
	if c.hdr == nil {
		c.hdr = http.Header{}
	}
	return c.hdr
}
func (c *captureWriter) WriteHeader(s int) {
	if c.status == 0 {
		c.status = s
	}
}
func (c *captureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = 200
	}
	c.buf.Write(p)
	if c.onStatus != nil {
		c.tail += string(p)
		for {
			i := strings.Index(c.tail, "\n")
			if i < 0 {
				break
			}
			line := c.tail[:i]
			c.tail = c.tail[i+1:]
			if s := sseReasoning(line); s != "" {
				c.onStatus(s)
			}
			// The answer text itself is what a round mostly produces: each
			// finished paragraph is one line of narration for the watcher.
			if d := sseContent(line); d != "" {
				if c.onContent != nil {
					c.onContent(d)
				}
				c.prose += d
				for {
					j := strings.Index(c.prose, "\n\n")
					if j < 0 {
						break
					}
					para := c.prose[:j]
					c.prose = c.prose[j+2:]
					if s := captaincode.Narration(para); s != "" {
						c.onStatus(s)
					}
				}
			}
		}
	}
	return len(p), nil
}

// sseContent returns the answer-text delta of one SSE chunk line, "" for
// anything else.
func sseContent(line string) string {
	if !strings.HasPrefix(line, "data: ") {
		return ""
	}
	var ev struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil || len(ev.Choices) == 0 {
		return ""
	}
	return ev.Choices[0].Delta.Content
}

// sseReasoning returns the progress text of one SSE chunk line, "" for
// anything else (content deltas, keepalives, the [DONE] marker).
func sseReasoning(line string) string {
	if !strings.HasPrefix(line, "data: ") {
		return ""
	}
	var ev struct {
		Choices []struct {
			Delta struct {
				Reasoning string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil || len(ev.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(ev.Choices[0].Delta.Reasoning)
}

// answer extracts the assistant text from the captured response (the detached
// request is non-streaming, so this is one completion JSON), or an error string.
func (c *captureWriter) answer() (text, errText string) {
	body := c.buf.String()
	if strings.HasPrefix(body, "data: ") || strings.HasPrefix(body, ": ") {
		return sseAnswer(body, c.status)
	}
	var resp struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		if c.status >= 400 || c.status == 0 {
			return "", promptPeek(body)
		}
		return body, ""
	}
	if resp.Error != nil {
		return "", resp.Error.Message
	}
	if len(resp.Choices) > 0 {
		return resp.Choices[0].Message.Content, ""
	}
	if c.status >= 400 {
		return "", promptPeek(body)
	}
	return "", "empty response"
}

// parallelNoticeAvailable reports whether an unannounced finished run exists
// (used by tests to await completion without consuming the notice).
func (b *brain) parallelNoticeAvailable() bool {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	for _, r := range b.parallelState() {
		if r.done && !r.reported {
			return true
		}
	}
	return false
}
