package main

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// Live 2026-10-21: a loop started in one TUI named another repository in its
// task, so its rounds ran there (followTask) and `/repeat finish` typed in the
// TUI that started it answered "nothing to stop". The loop belongs to both.
func TestRepeatControlsReachALoopFromTheTUIThatStartedIt(t *testing.T) {
	origin, ran := t.TempDir(), t.TempDir()
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	release := make(chan struct{})
	b.chatFn = func(w *captureWriter) { <-release } // one round, held in flight
	defer close(release)

	req := oaiChatReq{ws: captaincode.Workspace{Dir: ran, Origin: origin}}
	th := b.startRepeat(req, "re-pin the other repository", 0, nil)
	require.Eventually(t, func() bool {
		b.rmu.Lock()
		defer b.rmu.Unlock()
		return !th.roundFrom.IsZero()
	}, 5*time.Second, 10*time.Millisecond)

	status := b.repeatStatus(origin)
	assert.Contains(t, status, th.id, "the starting TUI lists its loop")
	assert.Contains(t, status, "runs in `"+ran+"`", "and says where it runs")
	assert.Contains(t, b.repeatStatus(ran), th.id, "the folder it runs in lists it too")
	assert.Contains(t, b.repeatStatus(ran), "started from `"+origin+"`")
	assert.NotContains(t, b.repeatStatus(t.TempDir()), th.id, "a third folder does not")

	assert.Contains(t, b.repeatStop(origin, ""), "finishing: "+th.id)
	b.rmu.Lock()
	assert.True(t, th.stopped)
	b.rmu.Unlock()
}

func TestRepeatAbortFromTheStartingTUI(t *testing.T) {
	origin, ran := t.TempDir(), t.TempDir()
	b := teamBrain()
	_, cancel := context.WithCancel(context.Background())
	b.rmu.Lock()
	b.repeatState()["rp_abc"] = &repeatThread{id: "rp_abc", dir: ran, origin: origin, cancel: cancel, started: time.Now()}
	b.rmu.Unlock()
	assert.Equal(t, "nothing to abort.", b.repeatAbort(t.TempDir(), ""))
	assert.Contains(t, b.repeatAbort(origin, ""), "rp_abc")
}

// A round re-enters the brain with only the folder it runs in; the TUI that
// started its loop must survive, or /v1/workers and the worker log name the
// other repository as the TUI.
func TestARepeatRoundKeepsTheTUIThatStartedIt(t *testing.T) {
	origin, ran := t.TempDir(), t.TempDir()
	r := chatRequestFrom(context.Background(), oaiChatReq{ws: captaincode.Workspace{Dir: ran, Origin: origin}})
	assert.Equal(t, ran, r.Header.Get(workspaceHeader))
	assert.Equal(t, origin, r.Context().Value(originKey{}))

	home := t.TempDir()
	t.Setenv("HOME", home)
	l := openWorkerLog(captaincode.Workspace{Dir: ran, Origin: origin}, "claude", "re-pin")
	require.NotNil(t, l)
	l.f.Close()
	head, err := os.ReadFile(l.path)
	require.NoError(t, err)
	assert.Contains(t, string(head), "cwd: "+ran+"\norigin: "+origin+"\ntask: ")
	assert.Contains(t, string(head), "\nmodel: "+captaincode.ModelIDAt("claude", "")+"\n", "the header names the model the leg dispatched")

	same := openWorkerLog(captaincode.Workspace{Dir: ran, Origin: ran}, "claude", "local")
	require.NotNil(t, same)
	same.f.Close()
	head, _ = os.ReadFile(same.path)
	assert.NotContains(t, string(head), "origin:", "a run in its own TUI's folder keeps the old header")
}

// Live 2026-10-21: a command sent from a dashboard reaches the brain with the
// inbox's footer after it. `/repeat finish` must still finish the loop.
func TestASentRepeatFinishIgnoresTheSentByFooter(t *testing.T) {
	b := teamBrain()
	b.rmu.Lock()
	b.repeatState()["rp_sent"] = &repeatThread{id: "rp_sent", dir: defaultWorkspace().Dir, cancel: func() {}, started: time.Now()}
	b.rmu.Unlock()
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/repeat finish\n\n- sent by dashboard in captaincode · not screened by a model"))
	assert.Contains(t, rec.Body.String(), "finishing: rp_sent")
	b.rmu.Lock()
	assert.True(t, b.repeatState()["rp_sent"].stopped)
	assert.Len(t, b.repeatState(), 1, "no new loop started on the footer text")
	b.rmu.Unlock()
	assert.Equal(t, "/repeat finish", withoutSentFooter("/repeat finish\n\n- sent by dashboard"))
	assert.Equal(t, "fix it\n\n- sent by x\nmore", withoutSentFooter("fix it\n\n- sent by x\nmore"), "only a last-line footer")
}

// Live 2026-10-21: a 128 KB replay went to grok-cli, whose CLI keeps ~100 KB
// inline and offloads the rest, so the model said the request was cut.
func TestGrokCLIPromptsStayUnderTheCLIInlineLimit(t *testing.T) {
	assert.Equal(t, grokCLIInline, promptBudget(captaincode.LegGrokCLI))
	assert.Less(t, grokCLIInline, 100_000)
	assert.Greater(t, promptBudget(captaincode.LegClaude), grokCLIInline, "other legs keep their window")
}
