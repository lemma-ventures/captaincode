package main

// `captain policy` - M5.4 policy evaluation and M5.5 promotion/rollback.
//
//	captain policy                          # list snapshots
//	captain policy snapshot [name]          # capture current routing state
//	captain policy activate <id>           # mark a snapshot active
//	captain policy accept <id>              # mark a snapshot accepted
//	captain policy show <id>                # show one snapshot
//	captain policy canary                   # list canaries
//	captain policy canary start <candidate> # start a bounded canary
//	captain policy canary stop [reason]     # stop the active canary
//	captain policy promote <candidate>      # compare candidate vs active
//	captain policy rollback                 # return to last accepted

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdPolicy(ledger *captaincode.Ledger, args []string) {
	if len(args) == 0 {
		listSnapshots(ledger)
		return
	}
	switch args[0] {
	case "snapshot":
		name := "current"
		if len(args) > 1 {
			name = strings.Join(args[1:], " ")
		}
		s := captaincode.SnapshotPolicy(name)
		ledger.RecordSnapshot(s)
		if err := ledger.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "captain: save snapshot: %v\n", err)
		}
		fmt.Printf("snapshot %s captured: %s\n", s.ID, s.Name)
		fmt.Print(captaincode.FormatSnapshot(s))
	case "activate":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain policy activate <id>"))
		}
		if !ledger.ActivateSnapshot(args[1]) {
			fatal(fmt.Errorf("snapshot %q not found", args[1]))
		}
		if err := ledger.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "captain: save: %v\n", err)
		}
		fmt.Printf("snapshot %s activated\n", args[1])
	case "accept":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain policy accept <id>"))
		}
		if !ledger.AcceptSnapshot(args[1], time.Now()) {
			fatal(fmt.Errorf("snapshot %q not found", args[1]))
		}
		if err := ledger.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "captain: save: %v\n", err)
		}
		fmt.Printf("snapshot %s accepted\n", args[1])
	case "show":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain policy show <id>"))
		}
		s := ledger.SnapshotFor(args[1])
		if s == nil {
			fatal(fmt.Errorf("snapshot %q not found", args[1]))
		}
		fmt.Print(captaincode.FormatSnapshot(*s))
	case "canary":
		cmdCanary(ledger, args[1:])
	case "promote":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain policy promote <candidate-id>"))
		}
		cand := ledger.SnapshotFor(args[1])
		if cand == nil {
			fatal(fmt.Errorf("snapshot %q not found", args[1]))
		}
		active := ledger.ActiveSnapshot()
		if active == nil {
			cur := captaincode.SnapshotPolicy("active")
			active = &cur
		}
		report := captaincode.Promote(*active, *cand, ledger.Decisions, ledger.Decisions, ledger.Events, ledger.Events)
		fmt.Print(captaincode.FormatPromotionReport(report))
		if !report.OverallRegressed && report.Recommendation == "promote: no cohort regressed" {
			fmt.Println("\nto accept: captain policy accept " + cand.ID)
		}
	case "rollback":
		last := ledger.LastAcceptedSnapshot()
		if last == nil {
			fatal(fmt.Errorf("no accepted snapshot to roll back to"))
		}
		ledger.ActivateSnapshot(last.ID)
		if err := ledger.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "captain: save: %v\n", err)
		}
		fmt.Printf("rolled back to %s %s\n", last.ID, last.Name)
		fmt.Print(captaincode.FormatSnapshot(*last))
	case "--json":
		out, _ := json.MarshalIndent(ledger.RecentSnapshots(), "", "  ")
		fmt.Println(string(out))
	default:
		fatal(fmt.Errorf("unknown policy subcommand %q (snapshot|activate|accept|show|canary|promote|rollback)", args[0]))
	}
}

func listSnapshots(ledger *captaincode.Ledger) {
	snaps := ledger.RecentSnapshots()
	if len(snaps) == 0 {
		fmt.Println("captain: no policy snapshots recorded yet")
		fmt.Println("\ncapture: captain policy snapshot [name]")
		return
	}
	fmt.Printf("%d policy snapshots:\n\n", len(snaps))
	for _, s := range snaps {
		fmt.Print(captaincode.FormatSnapshot(s))
	}
	cans := ledger.RecentCanaries()
	if len(cans) > 0 {
		fmt.Printf("\n%d canaries:\n\n", len(cans))
		for _, c := range cans {
			fmt.Print(captaincode.FormatCanary(c))
		}
	}
}

func cmdCanary(ledger *captaincode.Ledger, args []string) {
	if len(args) == 0 {
		cans := ledger.RecentCanaries()
		if len(cans) == 0 {
			fmt.Println("captain: no canaries recorded")
			fmt.Println("\nstart: captain policy canary start <candidate-id> [rate] [max-tasks]")
			return
		}
		for _, c := range cans {
			fmt.Print(captaincode.FormatCanary(c))
		}
		return
	}
	switch args[0] {
	case "start":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain policy canary start <candidate-id> [rate] [max-tasks]"))
		}
		cand := ledger.SnapshotFor(args[1])
		if cand == nil {
			fatal(fmt.Errorf("snapshot %q not found", args[1]))
		}
		if existing := ledger.ActiveCanary(); existing != nil {
			fatal(fmt.Errorf("canary %s is already running - stop it first", existing.ID))
		}
		rate := 0.1
		if len(args) > 2 {
			if r, err := strconvParseFloat(args[2]); err == nil && r > 0 && r <= 1 {
				rate = r
			}
		}
		maxTasks := 20
		if len(args) > 3 {
			if n, err := strconvAtoi(args[3]); err == nil && n > 0 {
				maxTasks = n
			}
		}
		active := ledger.ActiveSnapshot()
		activeID := ""
		if active != nil {
			activeID = active.ID
		}
		canary := captaincode.Canary{
			ID:                fmt.Sprintf("canary_%s_%s", cand.ID, time.Now().Format("150405")),
			CandidatePolicyID: cand.ID,
			ActivePolicyID:    activeID,
			SampleRate:        rate,
			MaxTasks:          maxTasks,
			MaxCostUSD:        5.0,
			StartedAt:         time.Now(),
		}
		ledger.RecordCanary(canary)
		if err := ledger.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "captain: save: %v\n", err)
		}
		fmt.Printf("canary %s started\n", canary.ID)
		fmt.Print(captaincode.FormatCanary(canary))
	case "stop":
		c := ledger.ActiveCanary()
		if c == nil {
			fatal(fmt.Errorf("no active canary"))
		}
		reason := "manual stop"
		if len(args) > 1 {
			reason = strings.Join(args[1:], " ")
		}
		c.Stop(reason)
		ledger.RecordCanary(*c)
		if err := ledger.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "captain: save: %v\n", err)
		}
		fmt.Printf("canary %s stopped: %s\n", c.ID, reason)
	default:
		fatal(fmt.Errorf("unknown canary subcommand %q (start|stop)", args[0]))
	}
}

func strconvParseFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

func strconvAtoi(s string) (int, error) {
	return strconv.Atoi(s)
}

var _ = rand.New
