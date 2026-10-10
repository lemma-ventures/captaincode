package main

// The behaviour report (pkg behaviour.go), kept current by the brain: it
// looks every ten minutes and rewrites the report when CAPTAIN_REPORT_EVERY
// runs (50) have passed since the last one, or a leg or host appeared that
// the last one did not have. `captain behaviour` prints the current state.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func (b *brain) behaviourLoop(done <-chan struct{}) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		b.refreshBehaviour(captaincode.BehaviourPath(), time.Now())
		select {
		case <-done:
			return
		case <-t.C:
		}
	}
}

// refreshBehaviour writes a new report when one is due; it returns why, or
// "" when none was.
func (b *brain) refreshBehaviour(path string, now time.Time) string {
	b.mu.Lock()
	cur := captaincode.BuildBehaviour(b.ledger, b.inCaptainLegs, now)
	b.mu.Unlock()
	prev, ok := captaincode.LoadBehaviour(path)
	due, why := captaincode.BehaviourDue(prev, ok, cur)
	if !due {
		return ""
	}
	cur.Reason = why
	if err := captaincode.WriteBehaviour(path, cur); err != nil {
		fmt.Printf("captain brain: behaviour report: %v\n", err)
		return ""
	}
	fmt.Printf("captain brain: behaviour report rewritten (%s, %d note(s))\n", why, len(cur.Notes))
	return why
}

// inCaptainLegs: CAPTAIN_LEGS lets the leg take turns. Caller holds b.mu.
func (b *brain) inCaptainLegs(l captaincode.Leg) bool { return len(b.allowed) == 0 || b.allowed[l] }

// cmdBehaviour prints the behaviour report from the ledger, as of now.
func cmdBehaviour(l *captaincode.Ledger, args []string) {
	allowed := map[captaincode.Leg]bool{}
	for _, s := range strings.Split(os.Getenv("CAPTAIN_LEGS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			allowed[captaincode.Leg(s)] = true
		}
	}
	in := func(leg captaincode.Leg) bool { return len(allowed) == 0 || allowed[leg] }
	rep := captaincode.BuildBehaviour(l, in, time.Now())
	rep.Reason = "on request"
	if len(args) > 0 && args[0] == "--json" {
		raw, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(raw))
		return
	}
	fmt.Print(rep.Markdown())
}
