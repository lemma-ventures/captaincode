package main

// `captain calibrate` - M5.2 calibrated quality/reliability estimates.
//
//	captain calibrate                 all legs, code domain
//	captain calibrate --domain editorial   one domain
//	captain calibrate glm             one leg
//	captain calibrate --json          machine-readable

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdCalibrate(l *captaincode.Ledger, args []string) {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	domain := fs.String("domain", "code", "work domain (code|editorial|research|general)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	fs.Parse(args)

	positional := fs.Args()
	now := time.Now()
	var d captaincode.Domain
	switch *domain {
	case "code", "":
		d = captaincode.DomainCode
	case "editorial":
		d = captaincode.DomainEditorial
	case "research":
		d = captaincode.DomainResearch
	default:
		d = captaincode.DomainGeneral
	}

	legs := captaincode.AllLegs
	if len(positional) > 0 {
		legs = nil
		for _, name := range positional {
			leg := captaincode.Leg(name)
			if !captaincode.KnownLeg(leg) {
				fatal(fmt.Errorf("unknown leg %q (known: %s)", name, captaincode.LegIDs()))
			}
			legs = append(legs, leg)
		}
	}

	type row struct {
		Leg         string                  `json:"leg"`
		Domain      string                  `json:"domain"`
		Calibration captaincode.Calibration `json:"calibration"`
	}

	rows := make([]row, 0, len(legs))
	for _, leg := range legs {
		c := captaincode.Calibrate(leg, d, l.Events, now)
		rows = append(rows, row{Leg: string(leg), Domain: string(d), Calibration: c})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Calibration.Quality > rows[j].Calibration.Quality
	})

	if *asJSON {
		out, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(out))
		return
	}

	fmt.Printf("calibrated estimates (domain=%s, half-life=%.0fd, stale<%.0f eff):\n", *domain, captaincode.CalibHalfLife(), captaincode.StaleThreshold())
	for _, r := range rows {
		fmt.Printf("  %-7s %s\n", r.Leg, r.Calibration.Format())
	}
}

var _ = os.Stdout
