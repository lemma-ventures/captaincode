package captaincode

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSteerFansOutToAttachedChannelsOnly(t *testing.T) {
	s := NewSteer("/src/arc")
	assert.Empty(t, s.Running())
	assert.Empty(t, s.Attached())

	s.Began(LegCodexCLI) // a codex worker: running, nothing attached
	var got []string
	detach := s.Attach(LegClaude, func(n string) error { got = append(got, n); return nil })
	s.Began(LegClaude)
	assert.Equal(t, []Leg{LegClaude}, s.Attached())
	assert.Equal(t, []Leg{LegClaude, LegCodexCLI}, s.Running())

	took, err := s.Send("  also update the changelog ")
	require.NoError(t, err)
	assert.Equal(t, []Leg{LegClaude}, took)
	assert.Equal(t, []string{"also update the changelog"}, got, "trimmed, delivered once")
	require.Len(t, s.Notes(), 1)
	assert.Equal(t, []Leg{LegClaude}, s.Notes()[0].Took)

	detach()
	s.Ended(LegClaude)
	assert.Empty(t, s.Attached())
	assert.Equal(t, []Leg{LegCodexCLI}, s.Running(), "codex still runs, deaf")
	took, err = s.Send("another")
	require.NoError(t, err)
	assert.Empty(t, took, "nobody attached: recorded, taken by no one")

	// A channel whose worker just ended reports the failure, the others still take it.
	s.Attach(LegGLM, func(string) error { return errors.New("the worker's turn has ended") })
	s.Attach(LegGrok, func(string) error { return nil })
	took, err = s.Send("late")
	assert.Equal(t, []Leg{LegGrok}, took)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "glm")

	var nilSteer *Steer
	nilSteer.Began(LegClaude) // nil-safe: a request without a handle
	_, err = nilSteer.Send("x")
	assert.Error(t, err)
	assert.Empty(t, nilSteer.Running())
}

// claude -p in stream-json input mode: the task is the first user line on
// stdin, a /btw note is the next one, and the turn folds it in. The stub
// reads its lines the way claude does and echoes the note into the result.
func TestClaudeStreamTakesABtwNoteOnStdin(t *testing.T) {
	fakeBin(t, "claude", `#!/bin/sh
read -r first
echo "$first" | grep -q '"type":"user"' || { echo "not a stream-json user line: $first" >&2; exit 2; }
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"sleep 1"}}]}}'
read -r second
note=$(printf '%s' "$second" | sed -n 's/.*"content":"\([^"]*\)".*/\1/p')
printf '{"type":"result","is_error":false,"result":"FINISHED %s"}\n' "$(printf '%s' "$note" | tr -d '\\' | tail -c 40)"
# stdin must be closed by captain after the result: block until it is
cat >/dev/null
`)
	s := NewSteer(t.TempDir())
	var statuses []string
	var smu sync.Mutex
	type out struct {
		res Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := runClaudeStreamOpts("", "the task", 20*time.Second, 0, nil,
			func(st string) { smu.Lock(); statuses = append(statuses, st); smu.Unlock() }, false, "", s)
		done <- out{res, err}
	}()
	require.Eventually(t, func() bool { return len(s.Attached()) == 1 }, 5*time.Second, 10*time.Millisecond, "the runner attaches its stdin")
	took, err := s.Send("BANANA")
	require.NoError(t, err)
	assert.Equal(t, []Leg{LegClaude}, took)
	select {
	case o := <-done:
		require.NoError(t, o.err)
		assert.True(t, strings.HasSuffix(o.res.Text, "BANANA"), "the note reached the turn: %q", o.res.Text)
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end - stdin was not closed after the result")
	}
	assert.Empty(t, s.Attached(), "detached when the run ends")
	smu.Lock()
	defer smu.Unlock()
	assert.Contains(t, strings.Join(statuses, "\n"), "btw from the user taken: BANANA", "the feed says the note landed")
}

// An opencode leg takes a note as a second POST to its busy session, which
// the serve merges into the running turn; the pending POST's answer is the
// merged one and the note POST's reply is dropped.
func TestOpencodeDispatcherPostsABtwNoteToTheBusySession(t *testing.T) {
	var mu sync.Mutex
	var posts []string
	first := make(chan struct{})
	note := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses_1/message" {
			w.Write([]byte(`{"id":"ses_1"}`))
			return
		}
		var body struct {
			Parts []struct{ Text string } `json:"parts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		posts = append(posts, body.Parts[0].Text)
		n := len(posts)
		mu.Unlock()
		if n == 1 {
			close(first)
			select { // the turn "runs" until the note arrives
			case <-note:
			case <-time.After(5 * time.Second):
			}
			w.Write([]byte(`{"info":{"tokens":{"total":3}},"parts":[{"type":"text","text":"FINISHED BANANA"}]}`))
			return
		}
		close(note)
		w.Write([]byte(`{"info":{"tokens":{"total":0}},"parts":[]}`))
	}))
	defer srv.Close()

	s := NewSteer(t.TempDir())
	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false, Steer: s}
	type out struct {
		res Result
		err error
	}
	done := make(chan out, 1)
	go func() { res, err := d.Run(LegGLM, "the task"); done <- out{res, err} }()
	<-first
	require.Eventually(t, func() bool { return len(s.Attached()) == 1 }, 2*time.Second, 10*time.Millisecond)
	took, err := s.Send("BANANA")
	require.NoError(t, err)
	assert.Equal(t, []Leg{LegGLM}, took)
	o := <-done
	require.NoError(t, o.err)
	assert.Equal(t, "FINISHED BANANA", o.res.Text)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, posts, 2)
	assert.Equal(t, "the task", posts[0])
	assert.Contains(t, posts[1], "[captain /btw]")
	assert.Contains(t, posts[1], "BANANA")
	assert.Empty(t, s.Attached())
}

// /interrupt on a claude run: the handoff request goes down stdin and the
// worker's answer IS the handoff. A worker that ignores it is stopped by
// StopAll and what it streamed comes back as an interrupted partial.
func TestClaudeInterruptHandsOffOrIsStoppedWithItsOutput(t *testing.T) {
	fakeBin(t, "claude", `#!/bin/sh
read -r first
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"working on the parser..."}}}'
read -r second
case "$second" in
  *interrupt*) printf '{"type":"result","is_error":false,"result":"HANDOFF: implemented lexer.go; left: parser tests; resume with go test"}\n';;
  *) printf '{"type":"result","is_error":false,"result":"no interrupt seen"}\n';;
esac
cat >/dev/null
`)
	s := NewSteer(t.TempDir())
	type out struct {
		res Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := runClaudeStreamOpts("", "the task", 20*time.Second, 0, nil, nil, false, "", s)
		done <- out{res, err}
	}()
	require.Eventually(t, func() bool { return len(s.Attached()) == 1 }, 5*time.Second, 10*time.Millisecond)
	asked, stopped := s.Interrupt("need the machine")
	assert.Equal(t, []Leg{LegClaude}, asked, "claude takes the handoff request on stdin")
	assert.Empty(t, stopped, "…so it is not stopped")
	o := <-done
	require.NoError(t, o.err)
	assert.Contains(t, o.res.Text, "HANDOFF: implemented lexer.go")
	assert.False(t, s.Interrupted().IsZero())

	// A worker that never reads the request: StopAll ends it, output kept.
	fakeBin(t, "claude", `#!/bin/sh
read -r first
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"half a report"}}}'
sleep 30
`)
	s2 := NewSteer(t.TempDir())
	go func() {
		res, err := runClaudeStreamOpts("", "the task", 60*time.Second, 0, nil, nil, false, "", s2)
		done <- out{res, err}
	}()
	require.Eventually(t, func() bool { return len(s2.Attached()) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // let the delta land
	s2.Interrupt("")
	assert.Equal(t, []Leg{LegClaude}, s2.StopAll(), "the grace ran out: stopped")
	select {
	case o := <-done:
		require.ErrorIs(t, o.err, ErrInterrupted)
		assert.True(t, o.res.Partial)
		assert.Equal(t, "half a report", o.res.Text)
		assert.True(t, WorthKeeping(o.res, o.err), "an interrupted partial is kept, never rerouted")
	case <-time.After(10 * time.Second):
		t.Fatal("the stop did not end the run")
	}
}

// A codex-style worker has no channel: Interrupt stops it at once.
func TestInterruptStopsALegWithNoChannel(t *testing.T) {
	s := NewSteer(t.TempDir())
	stopped := false
	detach := s.AttachStop(LegCodexCLI, func() { stopped = true })
	defer detach()
	s.Began(LegCodexCLI)
	asked, st := s.Interrupt("")
	assert.Empty(t, asked)
	assert.Equal(t, []Leg{LegCodexCLI}, st)
	assert.True(t, stopped)
}

// An opencode run stopped by /interrupt aborts the session and keeps the
// streamed text.
func TestOpencodeDispatcherStopKeepsTheStreamedText(t *testing.T) {
	aborted := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/abort"):
			aborted <- struct{}{}
			w.Write([]byte(`{}`))
		case r.URL.Path == "/session/ses_1/message":
			select { // the turn "runs" until the client goes away
			case <-r.Context().Done():
			case <-time.After(20 * time.Second):
			}
		default:
			w.Write([]byte(`{"id":"ses_1"}`))
		}
	}))
	defer srv.Close()
	s := NewSteer(t.TempDir())
	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false, Steer: s}
	d.acc.WriteString("streamed so far")
	type out struct {
		res Result
		err error
	}
	done := make(chan out, 1)
	go func() { res, err := d.Run(LegGLM, "the task"); done <- out{res, err} }()
	require.Eventually(t, func() bool { return len(s.Attached()) == 1 }, 2*time.Second, 10*time.Millisecond)
	s.StopAll()
	o := <-done
	require.ErrorIs(t, o.err, ErrInterrupted)
	assert.Equal(t, "streamed so far", o.res.Text)
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("the server-side generation was not aborted")
	}
}

// A note sent while the turn is still being prepared - nothing attached,
// nothing running - waits for the first channel and is that worker's
// first input ("/btw Cerebras sorry" arrived during a 300k-char compaction
// and was told "no worker is running", 2026-09-17).
func TestNoteSentBeforeTheWorkerStartsIsHeldForIt(t *testing.T) {
	s := NewSteer(t.TempDir())
	took, err := s.Send("Cerebras, not Cerberus")
	require.NoError(t, err)
	assert.Empty(t, took)
	assert.Equal(t, 1, s.Held())
	var got []string
	s.Attach(LegGrok, func(n string) error { got = append(got, n); return nil })
	assert.Equal(t, []string{"Cerebras, not Cerberus"}, got, "delivered on attach")
	assert.Equal(t, 0, s.Held())
	require.Len(t, s.Notes(), 1)
	assert.Equal(t, []Leg{LegGrok}, s.Notes()[0].Took)
}

func TestNoteAddressNamesTheWorkers(t *testing.T) {
	legs, rest := NoteAddress("@grok use the 80GB cap")
	assert.Equal(t, []Leg{LegGrok}, legs)
	assert.Equal(t, "use the 80GB cap", rest)
	legs, rest = NoteAddress("/claude: skip the docs pass")
	assert.Equal(t, []Leg{LegClaude}, legs)
	assert.Equal(t, "skip the docs pass", rest)
	legs, rest = NoteAddress("grok, claude: both of you stop touching main")
	assert.Equal(t, []Leg{LegGrok, LegClaude}, legs)
	assert.Equal(t, "both of you stop touching main", rest)
	legs, rest = NoteAddress("Cerebras, not Cerberus")
	assert.Nil(t, legs, "a word that is not a leg is the note")
	assert.Equal(t, "Cerebras, not Cerberus", rest)
}

func TestSendToReachesOnlyTheNamedLegs(t *testing.T) {
	s := NewSteer(t.TempDir())
	var grok, claude []string
	s.Attach(LegGrok, func(n string) error { grok = append(grok, n); return nil })
	s.Attach(LegClaude, func(n string) error { claude = append(claude, n); return nil })
	s.Began(LegGrok)
	s.Began(LegClaude)
	took, err := s.SendTo("only grok", []Leg{LegGrok})
	require.NoError(t, err)
	assert.Equal(t, []Leg{LegGrok}, took)
	assert.Equal(t, []string{"only grok"}, grok)
	assert.Empty(t, claude)
	took, _ = s.SendTo("everyone", nil)
	assert.Equal(t, []Leg{LegClaude, LegGrok}, took)

	var shown []string
	s.SetAnnounce(func(t string) { shown = append(shown, t) })
	s.Announce("> /btw → grok: only grok")
	assert.Equal(t, []string{"> /btw → grok: only grok"}, shown)
	s.Describe(LegGrok, "run the benchmark")
	assert.Equal(t, "run the benchmark", s.Briefs()[LegGrok])
}

// Narration between tool calls streams as separate text blocks; joined bare
// they read as one run-on line. A block boundary is a paragraph break, in the
// stream and in the kept text (2026-09-18).
func TestClaudeStreamSeparatesTextBlocksWithParagraphs(t *testing.T) {
	fakeBin(t, "claude", `#!/bin/sh
read -r first
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"I will start by checking the tree."}}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_stop"}}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go build ./..."}}]}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Build passes. "}}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Let me check the wiring."}}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_stop"}}'
printf '%s\n' '{"type":"result","is_error":false,"result":""}'
cat >/dev/null
`)
	var streamed strings.Builder
	res, err := runClaudeStreamOpts("", "task", 20*time.Second, 0, func(d string) { streamed.WriteString(d) }, nil, false, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "I will start by checking the tree.\n\nBuild passes. Let me check the wiring.", streamed.String())
	assert.Equal(t, "I will start by checking the tree.\n\nBuild passes. Let me check the wiring.", res.Text)
}
