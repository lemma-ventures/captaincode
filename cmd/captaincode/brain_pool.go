package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// poolFallbackNote says why a /oss or /deterministic turn runs outside its
// pool: no active leg qualifies right now. For /deterministic it also names
// the green tuples no leg serves yet - what `captain legs add` would bring in.
func (b *brain) poolFallbackNote(dir string, pool captaincode.Pool) {
	var why []string
	if pool.OSS {
		why = append(why, "no open-weight leg is open")
	}
	if pool.Deterministic {
		if captaincode.ADISnapshot() == nil {
			why = append(why, "the ADI feed is not loaded (CAPTAIN_ADI_URL / network)")
		} else if green := captaincode.ADIGreenUnregistered(); len(green) > 0 {
			names := make([]string, 0, len(green))
			for _, t := range green {
				names = append(names, t.Model+" ("+t.Label+")")
				if len(names) == 3 {
					break
				}
			}
			why = append(why, "no registered leg is green in ADI; green today: "+strings.Join(names, ", ")+" - `captain legs add <id> openrouter/<model>` registers one")
		} else {
			why = append(why, "no registered leg is green in ADI today")
		}
	}
	msg := fmt.Sprintf("pool %s: %s - routing without the pool", pool, strings.Join(why, "; "))
	fmt.Println("captain brain: " + msg)
	b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "pool", Model: pool.String(), Text: msg})
}

// startADIRefresh loads the cached feed and refreshes it on a slow cadence.
func (b *brain) startADIRefresh() {
	if captaincode.LoadADICache() {
		if f := captaincode.ADISnapshot(); f != nil {
			fmt.Printf("captain brain: ADI feed from cache (run %s, %d tuples)\n", f.RunStamp, len(f.Tuples))
		}
	}
	go func() {
		for {
			if f, err := captaincode.FetchADI(); err != nil {
				fmt.Printf("captain brain: ADI feed: %v\n", err)
			} else {
				fmt.Printf("captain brain: ADI feed refreshed (run %s, %d tuples, green legs: %s)\n",
					f.RunStamp, len(f.Tuples), legListShort(captaincode.ADIGreenLegs(), 6))
			}
			time.Sleep(captaincode.ADIRefreshEvery)
		}
	}()
}
