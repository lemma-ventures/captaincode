package main

import (
	"os"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func (b *brain) applySteerCommand(cmd captaincode.SteerCommand) string {
	if b.ledger == nil {
		return "No ledger, so the mix cannot be saved.\n"
	}
	if cmd.Err != "" {
		return "**Routing mix**: " + cmd.Err + "\n\n" + b.steerTargetsText()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch cmd.Kind {
	case "show":
		return b.formatSteerLocked()
	case "reset":
		b.ledger.Steer = captaincode.SteerMix{}
		_ = b.ledger.Save()
		return "Routing mix cleared. The defaults apply and do not steer.\n\n" + b.formatSteerLocked()
	case "adjust":
		next, note, err := b.ledger.Steer.Adjust(cmd.Axis, cmd.More, cmd.HasPct, cmd.Pct)
		if err != nil {
			return "**Routing mix**: " + err.Error() + "\n\n" + b.formatSteerLocked()
		}
		b.ledger.Steer = next
		_ = b.ledger.Save()
		return note + "\n\n" + b.formatSteerLocked()
	case "assign":
		next, note, err := b.ledger.Steer.Assign(cmd.Assign)
		if err != nil {
			return "**Routing mix**: " + err.Error() + "\n\n" + b.formatSteerLocked()
		}
		b.ledger.Steer = next
		_ = b.ledger.Save()
		return note + "\n\n" + b.formatSteerLocked()
	}
	return b.formatSteerLocked()
}

func (b *brain) steerTargetsText() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.formatSteerLocked()
}

func (b *brain) formatSteerLocked() string {
	recent := b.steerRecentLocked()
	shares, n := captaincode.SteerRecentShares(recent)
	return captaincode.FormatSteer(b.ledger.Steer, os.Getenv("CAPTAIN_STEER") == "0", shares, n)
}

func (b *brain) steerRecentLocked() []captaincode.Leg {
	if b.ledger == nil {
		return nil
	}
	var out []captaincode.Leg
	for i := len(b.ledger.Decisions) - 1; i >= 0 && len(out) < 40; i-- {
		if c := b.ledger.Decisions[i].Chosen; c != "" {
			out = append(out, c)
		}
	}
	return out
}

func (b *brain) steerActive() bool {
	return b.ledger != nil && b.ledger.Steer.Set && captaincode.SteerEnabled()
}

func (b *brain) steerNote() string {
	if !b.steerActive() {
		return ""
	}
	return b.ledger.Steer.PromptNote()
}

func (b *brain) steerRows(rows []captaincode.Scored) []captaincode.Scored {
	if !b.steerActive() {
		return rows
	}
	return captaincode.ApplySteer(rows, b.ledger.Steer, b.steerRecentLocked())
}

func (b *brain) orderBySteer(c captaincode.Class, d captaincode.Domain, legs []captaincode.Leg) []captaincode.Leg {
	if !b.steerActive() || len(legs) < 2 {
		return legs
	}
	rows := b.steerRows(captaincode.ValueRank(c, d, legs, b.ledger.Stats(), estTokensFor(c), b.pressure))
	ordered := captaincode.Legs(rows)
	seen := map[captaincode.Leg]bool{}
	for _, l := range ordered {
		seen[l] = true
	}
	for _, l := range legs {
		if !seen[l] {
			ordered = append(ordered, l)
		}
	}
	return ordered
}

func steerRationale(active bool) string {
	if !active {
		return ""
	}
	return " · mix"
}
