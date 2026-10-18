package main

import (
	"fmt"
	"os"
	"time"
)

// ledgerSaveEvery is how often the brain writes a ledger that a Save marked
// dirty. Saves are deferred (Ledger.DeferSaves): a turn's dozen Save calls
// become one write, and the disk write happens outside b.mu.
var ledgerSaveEvery = time.Second

// saveLoop writes the deferred ledger until done, then once more.
func (b *brain) saveLoop(done <-chan struct{}) {
	t := time.NewTicker(ledgerSaveEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			b.flushLedger(10 * time.Second)
			return
		case <-t.C:
			if err := b.ledger.SaveDirty(&b.mu); err != nil {
				fmt.Fprintf(os.Stderr, "captain brain: save run record: %v\n", err)
			}
		}
	}
}

// flushLedger writes pending ledger rows before the brain exits, giving up
// after d: a wedged b.mu must not keep a stopping brain alive.
func (b *brain) flushLedger(d time.Duration) {
	done := make(chan error, 1)
	go func() { done <- b.ledger.SaveDirty(&b.mu) }()
	select {
	case err := <-done:
		if err != nil {
			fmt.Fprintf(os.Stderr, "captain brain: final save: %v\n", err)
		}
	case <-time.After(d):
		fmt.Fprintf(os.Stderr, "captain brain: final save did not get b.mu within %s - the last second of run records may be lost\n", d)
	}
}
