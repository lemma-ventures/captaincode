package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/require"
)

func TestTurnStopChatHeaderBindsAndReleasesWorker(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	dir := t.TempDir()
	var bound *captaincode.Steer
	stopped := false
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		b.steers.mu.Lock()
		bound = b.steers.turns["turn_request_1234"]
		b.steers.mu.Unlock()
		if bound != nil {
			detach := bound.AttachStop(leg, func() { stopped = true })
			defer detach()
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/interrupt?turn=turn_request_1234&cwd="+url.QueryEscape(dir), strings.NewReader(`{"stop":true}`))
		b.interruptHTTP(w, r)
		return leg, captaincode.Result{Text: "partial output", Partial: true}, captaincode.ErrInterrupted
	}
	r := btwReq(dir, "claude", "implement the parser")
	r.Header.Set(turnHeader, "turn_request_1234")
	w := httptest.NewRecorder()
	b.chatCompletions(w, r)
	require.NotNil(t, bound)
	require.True(t, stopped)
	require.Contains(t, w.Body.String(), "partial output")
	b.steers.mu.Lock()
	defer b.steers.mu.Unlock()
	require.Empty(t, b.steers.turns)
}

func TestTurnStopTerminatesSelectedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sleep")
	}
	b := &brain{}
	dir := t.TempDir()
	s := b.steerOpen(dir)
	defer b.steerClose(s)
	require.True(t, b.bindTurn("turn_process_1234", s))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	worker := exec.CommandContext(ctx, "sleep", "30")
	require.NoError(t, worker.Start())
	wait := make(chan error, 1)
	go func() {
		wait <- worker.Wait()
		close(wait)
	}()
	defer func() {
		cancel()
		for range wait {
		}
	}()
	s.AttachStop(captaincode.LegClaude, cancel)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/interrupt?turn=turn_process_1234&cwd="+url.QueryEscape(dir), strings.NewReader(`{"stop":true}`))
	b.interruptHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	select {
	case err := <-wait:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("selected process did not stop")
	}
}

func TestTurnStopIsScopedAndMarksRoutingTurn(t *testing.T) {
	b := &brain{}
	dir := t.TempDir()
	first := b.steerOpen(dir)
	second := b.steerOpen(dir)
	defer b.steerClose(first)
	defer b.steerClose(second)
	require.True(t, b.bindTurn("turn_first_123456", first))
	require.True(t, b.bindTurn("turn_second_12345", second))
	require.False(t, b.bindTurn("turn_first_123456", second))
	stops := 0
	second.AttachStop(captaincode.LegClaude, func() { stops++ })
	r := httptest.NewRequest(http.MethodPost, "/v1/interrupt?turn=turn_first_123456&cwd="+url.QueryEscape(dir), strings.NewReader(`{"stop":true}`))
	w := httptest.NewRecorder()
	b.interruptHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.False(t, first.Interrupted().IsZero())
	require.True(t, second.Interrupted().IsZero())
	require.Zero(t, stops)
	late := 0
	first.AttachStop(captaincode.LegClaude, func() { late++ })
	require.Equal(t, 1, late)
}

func TestTurnStopCallsOnlySelectedWorker(t *testing.T) {
	b := &brain{}
	dir := t.TempDir()
	first := b.steerOpen(dir)
	second := b.steerOpen(dir)
	defer b.steerClose(first)
	defer b.steerClose(second)
	require.True(t, b.bindTurn("turn_first_123456", first))
	stops := [2]int{}
	first.AttachStop(captaincode.LegClaude, func() { stops[0]++ })
	second.AttachStop(captaincode.LegClaude, func() { stops[1]++ })
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/interrupt?turn=turn_first_123456&cwd="+url.QueryEscape(dir), strings.NewReader(`{"stop":true}`))
	b.interruptHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, [2]int{1, 0}, stops)
	b.steerClose(first)
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, r.URL.String(), strings.NewReader(`{"stop":true}`))
	b.interruptHTTP(w, r)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Equal(t, [2]int{1, 0}, stops)
}

func TestTurnStopRejectsInvalidRequestsWithoutFolderFallback(t *testing.T) {
	b := &brain{}
	dir := t.TempDir()
	s := b.steerOpen(dir)
	defer b.steerClose(s)
	require.True(t, b.bindTurn("turn_first_123456", s))
	for _, tc := range []struct {
		name, method, turn, cwd, body string
		status                        int
	}{
		{"method", "GET", "turn_first_123456", dir, `{"stop":true}`, 405},
		{"empty", "POST", "", dir, `{"stop":true}`, 400},
		{"short", "POST", "bad", dir, `{"stop":true}`, 400},
		{"unknown", "POST", "turn_missing_1234", dir, `{"stop":true}`, 404},
		{"workspace", "POST", "turn_first_123456", t.TempDir(), `{"stop":true}`, 404},
		{"missing workspace", "POST", "turn_first_123456", "", `{"stop":true}`, 400},
		{"relative workspace", "POST", "turn_first_123456", ".", `{"stop":true}`, 400},
		{"unknown handoff", "POST", "turn_missing_1234", dir, `{"reason":"stop"}`, 404},
		{"wrong handoff workspace", "POST", "turn_first_123456", t.TempDir(), `{"reason":"stop"}`, 404},
		{"conflicting actions", "POST", "turn_first_123456", dir, `{"stop":true,"reason":"stop"}`, 400},
		{"null reason", "POST", "turn_first_123456", dir, `{"reason":null}`, 400},
		{"handoff", "POST", "turn_first_123456", dir, `{"stop":false}`, 400},
		{"unknown field", "POST", "turn_first_123456", dir, `{"stop":true,"extra":1}`, 400},
		{"trailing object", "POST", "turn_first_123456", dir, `{"stop":true}{}`, 400},
		{"large body", "POST", "turn_first_123456", dir, `{"stop":true}` + strings.Repeat(" ", 1024), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(tc.method, "/v1/interrupt?turn="+url.QueryEscape(tc.turn)+"&cwd="+url.QueryEscape(tc.cwd), strings.NewReader(tc.body))
			b.interruptHTTP(w, r)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.True(t, s.Interrupted().IsZero())
		})
	}
}

func TestTurnStopQueuePreservesOutputAndDoesNotStartNextPrompt(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	t.Setenv("HOME", t.TempDir())
	b := teamBrain()
	dir := t.TempDir()
	unrelated := b.steerOpen(dir)
	defer b.steerClose(unrelated)
	require.True(t, b.bindTurn("turn_unrelated_1234", unrelated))
	var runs []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs = append(runs, lastUserTurn(prompt))
		b.steers.mu.Lock()
		parent := b.steers.turns["turn_queue_123456"]
		b.steers.mu.Unlock()
		require.NotNil(t, parent)
		stopped := false
		detach := parent.AttachStop(leg, func() { stopped = true })
		defer detach()
		onDelta("partial queue output")
		control := httptest.NewRecorder()
		b.interruptHTTP(control, httptest.NewRequest(http.MethodPost,
			"/v1/interrupt?turn=turn_queue_123456&cwd="+url.QueryEscape(dir), strings.NewReader(`{"stop":true}`)))
		require.Equal(t, http.StatusOK, control.Code)
		require.True(t, stopped)
		return leg, captaincode.Result{Text: "partial queue output", Partial: true}, captaincode.ErrInterrupted
	}
	body, err := json.Marshal(map[string]any{
		"model": "claude", "stream": true,
		"messages": []map[string]string{
			{"role": "user", "content": "/claude implement the parser"},
			{"role": "user", "content": "/claude implement the serializer"},
		},
	})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	r.Header.Set(workspaceHeader, dir)
	r.Header.Set(turnHeader, "turn_queue_123456")
	w := httptest.NewRecorder()
	b.chatCompletions(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, runs, 1)
	require.Contains(t, runs[0], "implement the parser")
	require.Contains(t, w.Body.String(), "partial queue output")
	require.NotContains(t, w.Body.String(), "queued 2/2")
	require.True(t, unrelated.Interrupted().IsZero())
	b.steers.mu.Lock()
	defer b.steers.mu.Unlock()
	require.NotContains(t, b.steers.turns, "turn_queue_123456")
	require.Same(t, unrelated, b.steers.turns["turn_unrelated_1234"])
}

func TestTurnNoteStaysWithinSelectedTurn(t *testing.T) {
	b := &brain{}
	dir := t.TempDir()
	first, second := b.steerOpen(dir), b.steerOpen(dir)
	defer b.steerClose(first)
	defer b.steerClose(second)
	require.True(t, b.bindTurn("turn_note_first_1234", first))
	var notesA, notesB []string
	first.Began(captaincode.Leg("claude"))
	second.Began(captaincode.Leg("claude"))
	defer first.Attach(captaincode.Leg("claude"), func(text string) error { notesA = append(notesA, text); return nil })()
	defer second.Attach(captaincode.Leg("claude"), func(text string) error { notesB = append(notesB, text); return nil })()
	w := httptest.NewRecorder()
	b.btwHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/btw?turn=turn_note_first_1234&cwd="+url.QueryEscape(dir), strings.NewReader(`{"text":"use the smaller fixture"}`)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"use the smaller fixture"}, notesA)
	require.Empty(t, notesB)
	_, consumed := b.btwTakeDone(dir, "use the smaller fixture")
	require.False(t, consumed, "a scoped note must not acknowledge another session's prompt")
}

func TestTurnNoteRejectsInvalidScope(t *testing.T) {
	for _, tc := range []struct {
		name, turn, cwd, body string
		status                int
	}{
		{"empty", "", "/fixture", `{"text":"note"}`, 400},
		{"invalid", "bad", "/fixture", `{"text":"note"}`, 400},
		{"missing workspace", "turn_note_first_1234", "", `{"text":"note"}`, 400},
		{"wrong workspace", "turn_note_first_1234", "/different", `{"text":"note"}`, 404},
		{"unknown", "turn_unknown_1234", "/fixture", `{"text":"note"}`, 404},
		{"extra object", "turn_note_first_1234", "/fixture", `{"text":"note"} {}`, 400},
		{"unknown field", "turn_note_first_1234", "/fixture", `{"text":"note","extra":true}`, 400},
		{"empty note", "turn_note_first_1234", "/fixture", `{"text":" "}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &brain{}
			s := b.steerOpen("/fixture")
			defer b.steerClose(s)
			require.True(t, b.bindTurn("turn_note_first_1234", s))
			w := httptest.NewRecorder()
			b.btwHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/btw?turn="+url.QueryEscape(tc.turn)+"&cwd="+url.QueryEscape(tc.cwd), strings.NewReader(tc.body)))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Zero(t, s.Held())
		})
	}
}

func TestTurnNoteHeldAndClosedTurnRejected(t *testing.T) {
	b := &brain{}
	dir := t.TempDir()
	first, second := b.steerOpen(dir), b.steerOpen(dir)
	defer b.steerClose(first)
	defer b.steerClose(second)
	require.True(t, b.bindTurn("turn_note_first_1234", first))
	send := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		b.btwHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/btw?turn=turn_note_first_1234&cwd="+url.QueryEscape(dir), strings.NewReader(`{"text":"wait for the fixture"}`)))
		return w
	}
	w := send()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, first.Held())
	require.Zero(t, second.Held())
	var received []string
	defer first.Attach("claude", func(text string) error { received = append(received, text); return nil })()
	require.Equal(t, []string{"wait for the fixture"}, received)
	b.steerClose(first)
	require.Equal(t, http.StatusNotFound, send().Code)
	require.Zero(t, second.Held())
}
func TestTurnHandoffStaysWithinSelectedTurn(t *testing.T) {
	t.Setenv("CAPTAIN_INTERRUPT_GRACE", "1ms")
	b := &brain{}
	dir := t.TempDir()
	first, second := b.steerOpen(dir), b.steerOpen(dir)
	defer b.steerClose(first)
	defer b.steerClose(second)
	require.True(t, b.bindTurn("turn_handoff_123456", first))
	var notesA, notesB []string
	first.Began("claude")
	second.Began("claude")
	defer first.Attach("claude", func(text string) error { notesA = append(notesA, text); return nil })()
	defer second.Attach("claude", func(text string) error { notesB = append(notesB, text); return nil })()
	w := httptest.NewRecorder()
	b.interruptHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/interrupt?turn=turn_handoff_123456&cwd="+url.QueryEscape(dir), strings.NewReader(`{"reason":"fixture handoff"}`)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, notesA, 1)
	require.Contains(t, notesA[0], "fixture handoff")
	require.Empty(t, notesB)
	require.True(t, second.Interrupted().IsZero())
	require.Empty(t, b.steers.interrupts)
}

func TestTurnHandoffFallbackDoesNotInterruptOtherSessions(t *testing.T) {
	b := &brain{}
	dir := t.TempDir()
	first, second := b.steerOpen(dir), b.steerOpen(dir)
	defer b.steerClose(first)
	defer b.steerClose(second)
	require.True(t, b.bindTurn("turn_fallback_123456", first))
	w := httptest.NewRecorder()
	require.True(t, b.handleInterrupt(w, oaiChatReq{ws: captaincode.Workspace{Dir: dir, Steer: first}}, "/interrupt idle"))
	require.True(t, second.Interrupted().IsZero())
	require.Empty(t, b.steers.interrupts)
}

func TestDetachedControlRequiresExactThreadAndWorkspace(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	other := t.TempDir()
	first, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	second, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	b.repeatState()["rp_first"] = &repeatThread{id: "rp_first", dir: dir, cancel: cancelFirst}
	b.repeatState()["rp_second"] = &repeatThread{id: "rp_second", dir: dir, cancel: cancelSecond}
	for _, tc := range []struct {
		thread, cwd, body string
		code              int
	}{
		{"all", dir, `{"word":"abort"}`, 400},
		{"rp_missing", dir, `{"word":"abort"}`, 404},
		{"rp_first", other, `{"word":"abort"}`, 404},
		{"rp_first", dir, `{"word":"abort","arg":"rp_second"}`, 400},
		{"rp_first", dir, `{"word":"abort","extra":true}`, 400},
		{"rp_first", dir, `{"word":"abort"} {}`, 400},
	} {
		w := httptest.NewRecorder()
		b.repeatCtlHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/repeat/ctl?thread="+tc.thread+"&cwd="+url.QueryEscape(tc.cwd), strings.NewReader(tc.body)))
		require.Equal(t, tc.code, w.Code, w.Body.String())
		require.NoError(t, first.Err())
		require.NoError(t, second.Err())
	}
	w := httptest.NewRecorder()
	b.repeatCtlHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/repeat/ctl?thread=rp_first&cwd="+url.QueryEscape(dir), strings.NewReader(`{"word":"abort","arg":"rp_first"}`)))
	require.Equal(t, http.StatusOK, w.Code)
	require.ErrorIs(t, first.Err(), context.Canceled)
	require.NoError(t, second.Err())
}

func TestScopedRepeatFallbackCannotStopAnotherThread(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.repeatState()["rp_first"] = &repeatThread{id: "rp_first", dir: dir, cancel: cancel}
	turn := b.steerOpen(dir)
	defer b.steerClose(turn)
	require.True(t, b.bindTurn("turn_repeat_123456", turn))
	for _, command := range []string{"/repeat abort", "/repeat stop all", "/repeat finish last"} {
		w := httptest.NewRecorder()
		require.True(t, b.handleRepeat(context.Background(), w, oaiChatReq{ws: captaincode.Workspace{Dir: dir, Steer: turn}}, command))
		require.Contains(t, w.Body.String(), "exact thread")
		require.NoError(t, ctx.Err())
		require.False(t, b.repeatState()["rp_first"].stopped)
	}
}
