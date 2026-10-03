package main

import (
	"fmt"
	"io"
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
	var reported int64
	for {
		select {
		case <-done:
			return
		case now := <-t.C:
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
