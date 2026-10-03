package main

import (
	"bytes"
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
