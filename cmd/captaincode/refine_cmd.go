package main

// `captain refine` - R4: review the ledger's evidence and let the director
// propose small overlay updates (leg notes + prior nudges). Nothing is written
// without --apply; --rollback restores the previous snapshot.
//
//	captain refine            # propose and show the diff
//	captain refine --apply    # propose, show, snapshot, write
//	captain refine --rollback # restore the last snapshot

import (
	"flag"
	"fmt"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdRefine(args []string, ledger *captaincode.Ledger) {
	fs := flag.NewFlagSet("refine", flag.ExitOnError)
	apply := fs.Bool("apply", false, "write the proposal (with snapshot)")
	rollback := fs.Bool("rollback", false, "restore the previous overlay snapshot")
	_ = fs.Parse(args)

	if *rollback {
		if err := captaincode.RollbackRefine(); err != nil {
			fatal(err)
		}
		fmt.Println("rolled back to the previous overlay snapshot")
		return
	}

	stats := ledger.Stats()
	byDomain := map[captaincode.Leg]map[string]struct {
		N, Scored int
		Q         float64
	}{}
	for _, e := range ledger.Events {
		if e.Leg == "" || e.Outcome != "ok" || e.Domain == "" {
			continue
		}
		if byDomain[e.Leg] == nil {
			byDomain[e.Leg] = map[string]struct {
				N, Scored int
				Q         float64
			}{}
		}
		v := byDomain[e.Leg][e.Domain]
		v.N++
		if e.Quality > 0 {
			// running mean over scored runs
			v.Q = (v.Q*float64(v.Scored) + e.Quality) / float64(v.Scored+1)
			v.Scored++
		}
		byDomain[e.Leg][e.Domain] = v
	}
	notes, err := captaincode.LoadLegNotes()
	if err != nil {
		fatal(err)
	}
	priors := map[captaincode.Leg]float64{}
	for _, l := range captaincode.AllLegs {
		priors[l] = captaincode.QualityPrior(l)
	}

	fmt.Println("asking the director for overlay proposals (evidence: ledger since first event)…")
	mgr := captaincode.Manager{Director: captaincode.Director, Port: opencodePort}
	p, err := mgr.Refine(captaincode.RefineEvidence{Stats: stats, ByDomain: byDomain, Notes: notes, Priors: priors})
	if err != nil {
		fatal(err)
	}
	if len(p.Notes) == 0 && len(p.Priors) == 0 {
		fmt.Println("no changes proposed - the evidence supports the current overlays")
		return
	}
	fmt.Printf("\nproposal (%s):\n", p.Rationale)
	for leg, note := range p.Notes {
		old := notes[leg]
		if old == "" {
			old = "(none)"
		}
		fmt.Printf("  note %-8s %q\n            → %q\n", leg, old, note)
	}
	for leg, v := range p.Priors {
		fmt.Printf("  prior %-7s %.1f → %.1f\n", leg, priors[leg], v)
	}
	if !*apply {
		fmt.Println("\ndry run - `captain refine --apply` writes this (snapshot + rollback available)")
		return
	}
	if err := captaincode.ApplyRefine(p); err != nil {
		fatal(err)
	}
	fmt.Println("\napplied - the running brain picks the notes up on its next restart; `captain refine --rollback` undoes this")
}
