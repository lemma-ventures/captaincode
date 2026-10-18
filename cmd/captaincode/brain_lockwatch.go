package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// watchedMutex is b.mu with a memory of who took it. On 2026-10-03 the brain
// was found with b.mu locked and NO goroutine holding it: a SIGQUIT dump of
// 18,508 goroutines showed every one waiting in Lock - the sidebar's polls
// (12,634 sockets in CLOSED state), every finishing worker's supervisor close,
// every compaction's charge - and none inside a locked region. Whoever took
// it had returned or been recovered without unlocking, and the dump cannot
// name a goroutine that no longer exists. This one can: Lock keeps the
// caller's stack, and the watchdog prints it when the lock is held too long.
type watchedMutex struct {
	mu    sync.Mutex
	since atomic.Int64 // unix nanos the current hold began; 0 when free
	pcs   atomic.Pointer[[]uintptr]
}

func (m *watchedMutex) Lock() {
	m.mu.Lock()
	m.held()
}

func (m *watchedMutex) TryLock() bool {
	if !m.mu.TryLock() {
		return false
	}
	m.held()
	return true
}

// lockWithin takes and releases the lock, giving up after d. It reports
// whether the lock was had.
func (m *watchedMutex) lockWithin(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for !m.TryLock() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.Unlock()
	return true
}

// lockCtx takes the lock and keeps it, giving up after d or when ctx ends.
// For the panel's polls: a handler queued in Lock outlives the client that
// abandoned it, and on 2026-10-18 16 TUIs polling every second left about
// 116k of them queued on b.mu (every one a socket in CLOSED state), with the
// finishing turns waiting behind them.
func (m *watchedMutex) lockCtx(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for !m.TryLock() {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// pollLockWait is how long a sidebar poll waits for b.mu before it answers
// 503. The panel abandons its request at 2.5s; waiting past that serves
// nobody.
var pollLockWait = 2 * time.Second

// lockForPoll takes b.mu for a polled read, or answers 503 and reports
// false. The handler returns either way, so no poll queues on a busy lock.
func (b *brain) lockForPoll(w http.ResponseWriter, r *http.Request) bool {
	if b.mu.lockCtx(r.Context(), pollLockWait) {
		return true
	}
	pollBusy.Add(1)
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "brain busy: b.mu held", "held_for_ms": b.mu.heldFor().Milliseconds()})
	return false
}

// pollBusy counts polls answered 503 because b.mu was held.
var pollBusy atomic.Int64

// heldFor is how long the current hold has lasted; 0 when the lock is free.
func (m *watchedMutex) heldFor() time.Duration {
	since := m.since.Load()
	if since == 0 {
		return 0
	}
	return time.Since(time.Unix(0, since))
}

// shutdownLockWait bounds how long a stopping brain waits for b.mu.
var shutdownLockWait = 10 * time.Second

func (m *watchedMutex) Unlock() {
	m.since.Store(0)
	m.mu.Unlock()
}

func (m *watchedMutex) held() {
	pcs := make([]uintptr, 24)
	pcs = pcs[:runtime.Callers(3, pcs)]
	m.pcs.Store(&pcs)
	m.since.Store(time.Now().UnixNano())
}

func (m *watchedMutex) holder() string {
	p := m.pcs.Load()
	if p == nil {
		return ""
	}
	var sb strings.Builder
	frames := runtime.CallersFrames(*p)
	for {
		f, more := frames.Next()
		if f.Function != "" {
			fmt.Fprintf(&sb, "\n    %s\n        %s:%d", f.Function, f.File, f.Line)
		}
		if !more {
			break
		}
	}
	return sb.String()
}

// lockHeldWarn is how long b.mu may be held before the brain says who has
// it. Holds are microseconds to a few seconds (a director call under the
// lock is the slowest); a minute is already every sidebar frozen.
var lockHeldWarn = time.Minute

// watchLock reports a hold of b.mu past lockHeldWarn, once per hold, with
// the stack that took it, and again when it is finally released.
func (b *brain) watchLock(done <-chan struct{}, out io.Writer) {
	t := time.NewTicker(lockHeldWarn / 4)
	defer t.Stop()
	var reported, busySeen int64
	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			// Polls turned away with no single hold past lockHeldWarn: b.mu
			// is contended by many short holds, which is the queue-up a
			// one-hold report never shows.
			if n := pollBusy.Load(); n != busySeen {
				fmt.Fprintf(out, "captain brain: %d sidebar poll(s) answered 503 in the last %s - b.mu is contended (current hold %s)\n", n-busySeen, lockHeldWarn/4, b.mu.heldFor().Round(time.Millisecond))
				busySeen = n
			}
			since := b.mu.since.Load()
			if reported != 0 && since != reported {
				fmt.Fprintf(out, "captain brain: b.mu released after about %s\n", now.Sub(time.Unix(0, reported)).Round(time.Second))
				reported = 0
			}
			if since == 0 || since == reported || now.Sub(time.Unix(0, since)) < lockHeldWarn {
				continue
			}
			reported = since
			fmt.Fprintf(out, "captain brain: b.mu held for %s - every sidebar poll and every finishing turn waits on it. Taken at:%s\n", now.Sub(time.Unix(0, since)).Round(time.Second), b.mu.holder())
		}
	}
}
