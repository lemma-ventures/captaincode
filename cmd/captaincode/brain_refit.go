package main

// The weekly prior refit (quality_evidence.go): each leg's benchmark prior
// moves part of the way toward the cross-vendor judges' mean once it has
// enough of their scores. The brain checks daily and refits when the last
// refit is CAPTAIN_PRIOR_REFIT (a week) old; `captain priors refit` shows
// the proposal and --apply writes it.

import (
	"fmt"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func (b *brain) priorRefitLoop(done <-chan struct{}) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		b.refitPriorsIfDue(time.Now())
		select {
		case <-done:
			return
		case <-t.C:
		}
	}
}

// refitPriorsIfDue applies the refit when it is due and returns what changed.
func (b *brain) refitPriorsIfDue(now time.Time) []captaincode.PriorChange {
	path := captaincode.PriorOverridesPath()
	if path == "" || !captaincode.RefitDue(path, now) {
		return nil
	}
	b.mu.Lock()
	changes := captaincode.ProposeRefit(b.ledger.Stats())
	err := captaincode.ApplyRefit(path, changes, now)
	b.mu.Unlock()
	if err != nil {
		fmt.Printf("captain brain: prior refit: %v\n", err)
		return nil
	}
	for _, c := range changes {
		fmt.Printf("captain brain: prior refit %s %.1f → %.1f (judges' mean %.2f over %d scores)\n", c.Leg, c.From, c.To, c.Measured, c.Scored)
	}
	return changes
}

// cmdPriorsRefit prints the refit the local evidence calls for and, with
// --apply, writes it.
func cmdPriorsRefit(l *captaincode.Ledger, apply bool) {
	changes := captaincode.ProposeRefit(l.Stats())
	if len(changes) == 0 {
		fmt.Println("no prior moves: no leg has enough cross-vendor scores that disagree with its prior")
		return
	}
	fmt.Println("leg        prior → refit   (judges' mean over weighted scores)")
	for _, c := range changes {
		fmt.Printf("  %-10s %.1f → %.1f   (%.2f over %d)\n", c.Leg, c.From, c.To, c.Measured, c.Scored)
	}
	if !apply {
		fmt.Println("\napply with: captain priors refit --apply")
		return
	}
	if err := captaincode.ApplyRefit(captaincode.PriorOverridesPath(), changes, time.Now()); err != nil {
		fatal(err)
	}
	fmt.Println("\nwritten to", captaincode.PriorOverridesPath())
}
