package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// leakLock takes b.mu and returns without releasing it - the shape of the
// 2026-10-03 wedge, where no goroutine was left holding the lock.
func leakLock(b *brain) { b.mu.Lock() }

func TestWatchLockNamesWhoTookALeakedLock(t *testing.T) {
	prev := lockHeldWarn
	lockHeldWarn = 40 * time.Millisecond
	defer func() { lockHeldWarn = prev }()

	var out syncBuffer
	b := &brain{}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() { b.watchLock(done, &out); close(stopped) }()
	leakLock(b)
	time.Sleep(200 * time.Millisecond)
	b.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	close(done)
	<-stopped

	log := out.String()
	assert.Equal(t, 1, strings.Count(log, "b.mu held for"), log)
	assert.Contains(t, log, "captaincode.leakLock")
	assert.Contains(t, log, "brain_lockwatch_test.go")
	assert.Contains(t, log, "b.mu released after")
}

func TestWatchLockIsQuietForShortHolds(t *testing.T) {
	b := &brain{}
	b.mu.Lock()
	b.mu.Unlock()
	assert.Zero(t, b.mu.since.Load())
	assert.True(t, b.mu.TryLock())
	assert.False(t, b.mu.TryLock())
	b.mu.Unlock()
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestLockWithinGivesUpOnAStuckLock(t *testing.T) {
	b := &brain{}
	assert.True(t, b.mu.lockWithin(time.Second), "a free lock is had at once")
	assert.Zero(t, b.mu.since.Load(), "and released")
	leakLock(b)
	start := time.Now()
	assert.False(t, b.mu.lockWithin(100*time.Millisecond))
	assert.Less(t, time.Since(start), time.Second)
	assert.Contains(t, b.mu.holder(), "captaincode.leakLock")
}

// A sidebar poll against a held b.mu answers 503 and returns: queued in Lock,
// it outlived the client that abandoned it, and 116k of them piled up on the
// brain of 2026-10-18 with every finishing turn waiting behind them.
func TestPollsDoNotQueueOnAHeldLock(t *testing.T) {
	prev := pollLockWait
	pollLockWait = 30 * time.Millisecond
	defer func() { pollLockWait = prev }()

	b := &brain{}
	b.mu.Lock()
	for name, h := range map[string]http.HandlerFunc{"/v1/stats": b.stats, "/v1/workers": b.workers} {
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { h(rec, httptest.NewRequest(http.MethodGet, name, nil)); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s queued on the held lock", name)
		}
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, name)
		assert.Contains(t, rec.Body.String(), "brain busy", name)
	}
	b.mu.Unlock()
	assert.True(t, b.mu.lockCtx(context.Background(), time.Second), "a free lock is taken")
	b.mu.Unlock()
}
