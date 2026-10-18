package captaincode

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A deferred ledger writes on SaveDirty, once, and holds the caller's lock
// only while it encodes - never through the disk write.
func TestDeferredSaveWritesOnceOutsideTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	l := NewLedger(path)
	l.DeferSaves()
	l.Events = append(l.Events, Event{TaskID: "t1"})
	for i := 0; i < 5; i++ {
		if err := l.Save(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a deferred Save wrote the file: %v", err)
	}
	var mu sync.Mutex
	if err := l.SaveDirty(&mu); err != nil {
		t.Fatal(err)
	}
	if !mu.TryLock() {
		t.Fatal("SaveDirty kept the caller's lock")
	}
	mu.Unlock()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("SaveDirty wrote nothing: %v", err)
	}
	if err := l.SaveDirty(&mu); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(path); !st2.ModTime().Equal(st.ModTime()) {
		t.Error("a clean ledger was written again")
	}
	back, err := os.ReadFile(path)
	if err != nil || len(back) == 0 {
		t.Fatalf("read back: %v", err)
	}
}
