package main

// `captain why --export <file>` - the routing dataset (P7 step 1).
//
// The export builds one row per attempt from the routing journal and the
// ledger: decision, task class, leg, gate result, repair count, billed cost
// and verdict, with the prompt replaced by its SHA-256. No model call, no
// routing change: the file is input for the offline evaluation in P7 steps
// 2 and 3.
//
// The file is created 0600 and refuses an existing path. `-` streams the
// rows to stdout for a pipeline.

import (
	"errors"
	"fmt"
	"os"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// whyExportFlags is the one flag combination the export must refuse: the
// share report and a dataset export read the same journal differently, and
// silently doing both would answer the wrong question.
func whyExportFlags(share bool, export string) error {
	if share && export != "" {
		return errors.New("--export does not combine with --share")
	}
	return nil
}

// exportDataset writes the ledger's routing history as JSONL rows. It
// returns how many rows it wrote and any file error, without printing.
func exportDataset(l *captaincode.Ledger, path string) (int, error) {
	rows := captaincode.BuildRoutingDataset(captaincode.RoutingHistory(l), l.Charges)
	if path == "-" {
		if err := captaincode.WriteRoutingDatasetTo(os.Stdout, rows); err != nil {
			return 0, err
		}
		return len(rows), nil
	}
	if err := captaincode.WriteRoutingDataset(path, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// exportSummary is the count line printed after a file export. A stdout
// export prints only its rows, so a pipeline stays clean.
func exportSummary(path string, rows int) string {
	return fmt.Sprintf("captain: wrote %d dataset rows to %s\n", rows, path)
}
