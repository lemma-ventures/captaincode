package main

// One execution per (leg, task) for ordinary single-leg turns.
//
// The fork re-issues a turn's request (retry, reconnect, resend after seeing
// nothing). For a workflow that already attaches; for a solo run it used to
// start a second full run - live 2026-07-30, a rerouted 8-minute codex job whose
// answer arrived at a request nobody was listening to any more. Attaching costs
// nothing and guarantees one answer per turn.

import (
	"fmt"
	"net/http"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// soloResultTTL keeps a finished answer servable to a repeat of the same
// prompt. Long enough to cover a retry or a frustrated resend, short enough
// that "run that again" still means what it says.
const soloResultTTL = 3 * time.Minute

type soloRun struct {
	started  time.Time
	done     chan struct{}
	finished time.Time
	text     string
	err      error
}

// beginSolo registers a run, or reports the one already in flight (or just
// finished) for the same work.
func (b *brain) beginSolo(key string) (*soloRun, bool) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	if b.solo == nil {
		b.solo = map[string]*soloRun{}
	}
	for k, v := range b.solo {
		if !v.finished.IsZero() && time.Since(v.finished) > soloResultTTL {
			delete(b.solo, k)
		}
	}
	if cur, ok := b.solo[key]; ok {
		// A FINISHED run that failed is not an answer to replay: a resend
		// after a failure means "try again" - the user fixed the login and
		// got the cached "Authentication required" four times in three
		// minutes (cursor, 2026-09-13). In-flight runs still attach.
		if !cur.finished.IsZero() && cur.err != nil {
			delete(b.solo, key)
		} else {
			return cur, true
		}
	}
	cur := &soloRun{started: time.Now(), done: make(chan struct{})}
	b.solo[key] = cur
	return cur, false
}

func (b *brain) recordSolo(key, text string, err error) {
	b.wmu.Lock()
	if cur, ok := b.solo[key]; ok {
		cur.text, cur.err, cur.finished = text, err, time.Now()
	}
	b.wmu.Unlock()
}

// finishSolo closes the run so attached requests stop waiting, whatever
// happened (including a panic or an early return).
func (b *brain) finishSolo(key string) {
	b.wmu.Lock()
	cur, ok := b.solo[key]
	if ok && cur.finished.IsZero() {
		cur.finished = time.Now()
	}
	b.wmu.Unlock()
	if !ok {
		return
	}
	select {
	case <-cur.done:
	default:
		close(cur.done)
	}
}

// serveAttachedSolo waits for the in-flight run and returns its answer.
func (b *brain) serveAttachedSolo(w http.ResponseWriter, req oaiChatReq, leg captaincode.Leg, cur *soloRun) {
	emit, status, finish := newCompletionWriter(w, req, string(leg))
	defer finish() // idempotent; the attach path missing this crashed the brain (2026-08-28)
	feed := newProgressFeed(fmt.Sprintf("%s (already running) - live: captain watch", leg), status)
	defer feed.close()

	select {
	case <-cur.done:
	default:
		fmt.Printf("captain brain: request attached to the %s run already in flight (%s in)\n",
			leg, time.Since(cur.started).Round(time.Second))
		feed.note(fmt.Sprintf("attached to this prompt's run, already %s in - not starting a second one\n",
			time.Since(cur.started).Round(time.Second)))
		<-cur.done
	}
	feed.close()

	b.wmu.Lock()
	text, err := cur.text, cur.err
	b.wmu.Unlock()
	if err != nil {
		writeWorkerError(w, leg, err)
		return
	}
	emit(text)
	finish()
}
