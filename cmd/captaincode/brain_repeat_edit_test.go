package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepeatEditParsing(t *testing.T) {
	w, id, text, ok := captaincode.RepeatEdit(" edit: Normalize ratios by their GCD")
	require.True(t, ok)
	assert.Equal(t, []string{"edit", "", "Normalize ratios by their GCD"}, []string{w, id, text}, "the text keeps its case")
	w, id, text, ok = captaincode.RepeatEdit("add rp_1a2b: also check negative ratios")
	require.True(t, ok)
	assert.Equal(t, []string{"add", "rp_1a2b", "also check negative ratios"}, []string{w, id, text})
	_, _, _, ok = captaincode.RepeatEdit("edit the README")
	assert.False(t, ok, "without the colon it is a task that starts with 'edit'")
	_, _, _, ok = captaincode.RepeatEdit("edit:   ")
	assert.False(t, ok, "an edit needs text")

	assert.True(t, captaincode.StartsLoop("/repeat edit: something else"), "an edit rewrites a loop: a sent turn may not do it")
	assert.True(t, captaincode.StartsLoop("/repeat 5 edit the README"), "a counted task is a loop, not an edit")
	assert.True(t, sqControl("/repeat edit: x"), "a signed edit does not wait behind the loop it changes")
	assert.True(t, sqControl("/repeat add rp_1a2b: x"))
	assert.False(t, sqControl("/repeat 5 edit the README"))
}

// A prompt edited while a round runs applies from the next round; the round
// in flight keeps the prompt it started with (2 Nov: the only way to change
// a ten-round loop of 2-4 h rounds was to stop it).
func TestRepeatEditAppliesFromTheNextRound(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	var mu sync.Mutex
	var prompts []string
	release := make(chan struct{})
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		n := len(prompts)
		mu.Unlock()
		if n == 1 {
			<-release // the first round is still running when the edit arrives
		}
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(prompts) }
	go b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat 3 /claude define the next items"))
	require.Eventually(t, func() bool { return count() == 1 }, 20*time.Second, 20*time.Millisecond)

	edit := httptest.NewRecorder()
	b.chatCompletions(edit, repeatReq("/repeat edit: normalize ratios by their GCD"))
	require.Equal(t, 200, edit.Code)
	assert.Contains(t, edit.Body.String(), "Round 2 and later run it")

	add := httptest.NewRecorder()
	b.chatCompletions(add, repeatReq("/repeat add: Also accept negative ratios, sign on the numerator."))
	assert.Contains(t, add.Body.String(), "prompt extended")
	close(release)
	require.Eventually(t, func() bool { return count() == 3 }, 30*time.Second, 50*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, prompts[0], "define the next items", "the round in flight keeps its prompt")
	for _, p := range prompts[1:] {
		assert.Contains(t, p, "normalize ratios by their GCD")
		assert.Contains(t, p, "Also accept negative ratios")
		assert.NotContains(t, p, "define the next items", "edit replaces the prompt")
		assert.NotContains(t, p, "/repeat", "the control word never reaches a worker")
	}

	st := httptest.NewRecorder()
	b.chatCompletions(st, repeatReq("/repeat status"))
	assert.Contains(t, st.Body.String(), "prompt edited 2×")
}

func TestRepeatEditKeepsTheLaneAndNeedsOneThread(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		time.Sleep(50 * time.Millisecond)
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	none := httptest.NewRecorder()
	b.chatCompletions(none, repeatReq("/repeat edit: x"))
	assert.Contains(t, none.Body.String(), "no running repeat thread to edit")

	go b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat /frontier first task"))
	require.Eventually(t, func() bool { b.rmu.Lock(); defer b.rmu.Unlock(); return len(b.repeatState()) == 1 }, 10*time.Second, 20*time.Millisecond)
	b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat edit: second task"))
	b.rmu.Lock()
	var th *repeatThread
	for _, x := range b.repeatState() {
		th = x
	}
	task, model := th.task, th.model
	b.rmu.Unlock()
	assert.Equal(t, "/frontier second task", task, "an edit without a directive keeps the loop's lane")
	assert.Equal(t, "frontier", model)

	go b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat another loop"))
	require.Eventually(t, func() bool { b.rmu.Lock(); defer b.rmu.Unlock(); return len(b.repeatState()) == 2 }, 10*time.Second, 20*time.Millisecond)
	two := httptest.NewRecorder()
	b.chatCompletions(two, repeatReq("/repeat edit: x"))
	assert.Contains(t, two.Body.String(), "2 repeat threads run here: name one")

	// Out of band, as the TUI plugin sends it.
	body := `{"word":"edit","arg":"` + th.id + `","text":"third task"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/repeat/ctl?cwd="+url.QueryEscape(th.dir), bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	b.repeatCtlHTTP(rec, r)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.True(t, strings.Contains(rec.Body.String(), "prompt replaced"), rec.Body.String())
	b.rmu.Lock()
	assert.Equal(t, "/frontier third task", th.task)
	b.rmu.Unlock()
	b.repeatAbort("", "everywhere")
}
