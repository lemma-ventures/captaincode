package main

// `captain adi` - the Agentic Determinism Index as captain sees it: every
// active leg's standing (green / red / not measured), and the green tuples
// no leg serves yet.
//
//	captain adi            legs against the cached or fetched feed
//	captain adi refresh    fetch the feed now (CAPTAIN_ADI_URL, else the published one)

import (
	"fmt"
	"os"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdADI(args []string) {
	captaincode.LoadCaptainEnv()
	refresh := len(args) > 0 && args[0] == "refresh"
	if !captaincode.LoadADICache() || refresh {
		if _, err := captaincode.FetchADI(); err != nil {
			fmt.Fprintf(os.Stderr, "captain adi: %v\n", err)
			if captaincode.ADISnapshot() == nil {
				os.Exit(1)
			}
		}
	}
	f := captaincode.ADISnapshot()
	fmt.Printf("ADI feed: run %s · %d tuples · fetched %s\n\n", f.RunStamp, len(f.Tuples), f.FetchedAt.Format("2006-01-02 15:04"))
	fmt.Printf("%-10s %-8s %-6s %s\n", "leg", "oss", "adi", "standing")
	for _, l := range captaincode.AllLegs {
		oss := "-"
		if captaincode.OpenWeights(l) {
			oss = "open"
		}
		state := "-"
		if t, ok := captaincode.ADIFor(l); ok {
			state = "red"
			if t.Green {
				state = "green"
			}
		}
		fmt.Printf("%-10s %-8s %-6s %s\n", l, oss, state, captaincode.ADIOneLine(l))
	}
	if green := captaincode.ADIGreenUnregistered(); len(green) > 0 {
		fmt.Println("\ngreen tuples no leg serves (captain legs add <id> <provider>/<model>):")
		for _, t := range green {
			pin := ""
			if o, ok := t.ProviderPrefs["order"].([]any); ok && len(o) > 0 {
				parts := make([]string, 0, len(o))
				for _, x := range o {
					parts = append(parts, fmt.Sprint(x))
				}
				pin = " · pin " + strings.Join(parts, ",")
			}
			fmt.Printf("  %s/%s  %s  (streak %d, mode share %.2f%s)\n", t.Provider, t.Model, t.Label, t.Streak, t.MeanModeShare, pin)
		}
	}
}
