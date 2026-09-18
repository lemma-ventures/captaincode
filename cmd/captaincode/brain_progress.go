package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Live progress for the TUI.
//
// The wrapper used to forward answer text and nothing else, so a worker that
// thought or ran tools for minutes produced a stream with zero visible output:
// a healthy long run and a wedged one looked identical (live 2026-07-29 - "it
// looks stuck while surely not"). The brain now narrates the run on the
// OpenAI `reasoning_content` delta, which the fork's openai-compatible
// provider maps to a reasoning part (the TUI's thinking block). It is visible,
// it is not part of the deliverable, and it never reaches the ledger.
//
// Nothing is emitted before progressFirstDelay: the first SSE byte commits the
// response to HTTP 200, and worker auth/quota/outage failures - the ones the
// fork must see as a real 429/503/400 - all land in the first second. Tool
// activity is exempt: a worker that is already calling tools is past that
// window by construction.
var (
	progressFirstDelay = 5 * time.Second
	progressEvery      = 60 * time.Second
)

// progressFeed narrates one worker run: tool activity as it happens, plus an
// elapsed-time heartbeat whenever the run goes quiet.
type progressFeed struct {
	lmu    sync.Mutex // guards label (a workflow retargets it per stage)
	label  string
	emit   func(string)
	start  time.Time
	last   atomic.Int64 // unix nanos of the last visible sign of life
	headed atomic.Bool  // the reasoning block's title has been written
	stop   chan struct{}
	// what the heartbeat can say beyond "still working": the last concrete
	// line and when it happened, and how many tools ran (2026-09-13: an hour
	// of "still working (1h20m)" lines said nothing a reader could use).
	nmu      sync.Mutex
	lastNote string
	noteAt   time.Time
	tools    int
}

// write sends one progress line, prefixing the block's title on first use.
// The TUI reads a leading **bold** line as the reasoning block's title (see
// reasoningSummary in the fork), which is what a collapsed thinking block
// shows - so a minimized block still reads "captain · claude" instead of an
// anonymous "Thinking".
func (p *progressFeed) write(s string) {
	if p.headed.CompareAndSwap(false, true) {
		p.emit("**captain · " + p.currentLabel() + "**\n\n")
	}
	p.emit(s)
}

// newProgressFeed starts narrating a run by label (the leg doing the work).
// emit receives ready-to-display lines; nil disables the feed entirely.
func newProgressFeed(label string, emit func(string)) *progressFeed {
	p := &progressFeed{label: label, emit: emit, start: time.Now(), stop: make(chan struct{})}
	p.last.Store(time.Now().UnixNano())
	if emit == nil {
		return p
	}
	go p.heartbeat()
	return p
}

// note reports concrete worker activity ("⚙ Read pkg/x.go").
func (p *progressFeed) note(s string) {
	if p == nil || p.emit == nil || s == "" {
		return
	}
	p.last.Store(time.Now().UnixNano())
	p.nmu.Lock()
	if strings.HasPrefix(s, "⚙") {
		p.tools++
	}
	if !strings.HasPrefix(s, "…") { // a heartbeat is not activity
		p.lastNote, p.noteAt = s, time.Now()
	}
	p.nmu.Unlock()
	p.write(s + "\n")
}

// relabel renames what the heartbeat says is working - a workflow retargets it
// per stage ("stage 2/3 · cursor") so a quiet minute still says WHERE it is.
func (p *progressFeed) relabel(label string) {
	if p == nil || label == "" {
		return
	}
	p.lmu.Lock()
	p.label = label
	p.lmu.Unlock()
}

func (p *progressFeed) currentLabel() string {
	p.lmu.Lock()
	defer p.lmu.Unlock()
	return p.label
}

// touch records that the run showed a sign of life (answer text arriving), so
// the heartbeat stays quiet while output is flowing.
func (p *progressFeed) touch() {
	if p == nil {
		return
	}
	p.last.Store(time.Now().UnixNano())
}

func (p *progressFeed) close() {
	if p == nil {
		return
	}
	select {
	case <-p.stop: // already closed
	default:
		close(p.stop)
	}
}

func (p *progressFeed) heartbeat() {
	select {
	case <-p.stop:
		return
	case <-time.After(progressFirstDelay):
	}
	t := time.NewTicker(progressEvery)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			if time.Since(time.Unix(0, p.last.Load())) < progressEvery {
				continue // output is flowing - the user can see it already
			}
			p.last.Store(time.Now().UnixNano())
			// Say what it is waiting on, not only that it waits.
			p.nmu.Lock()
			last, at, tools := p.lastNote, p.noteAt, p.tools
			p.nmu.Unlock()
			line := fmt.Sprintf("… %s still working (%s)", p.currentLabel(), time.Since(p.start).Round(time.Minute))
			if tools > 0 {
				line += fmt.Sprintf(" · %d tool calls", tools)
			}
			if last != "" {
				if len(last) > 90 {
					last = last[:90] + "…"
				}
				line += fmt.Sprintf(" · %s ago: %s", time.Since(at).Round(time.Minute), last)
			}
			p.write(line + "\n")
		}
	}
}
