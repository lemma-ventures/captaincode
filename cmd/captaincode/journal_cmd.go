package main

// `captain journal import <state.json>…` - add the run events of older state
// files (the backups in ~/.captaincode/backups) that the routing journal
// lacks, so the scorecards and the reports see them.

import (
	"fmt"
	"os"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdJournal(l *captaincode.Ledger, args []string) {
	if len(args) < 2 || args[0] != "import" {
		fmt.Fprintln(os.Stderr, "usage: captain journal import <state.json>…")
		os.Exit(2)
	}
	n, err := l.ImportEvents(args[1:]...)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("journal: imported %d run events the journal did not hold\n", n)
}
