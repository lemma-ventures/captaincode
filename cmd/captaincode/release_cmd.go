package main

// `captain release` - the M1.5 release package. Produces a reproducible
// manifest, a compatibility table, extraction checks, and a dated evidence
// report. The manifest names every version an evidence run was produced
// against; the checks verify the release contract is complete.
//
//	captain release manifest          # print the manifest
//	captain release manifest --json   # JSON
//	captain release compat            # print the compatibility table
//	captain release check             # run extraction checks
//	captain release report            # full dated evidence report
//	captain release report --json     # JSON

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type releaseReadFile func(string) ([]byte, error)

func releaseDeps() captaincode.ReleaseDeps {
	src := captainSourceDir()
	return captaincode.ReleaseDeps{
		ReadFile: func(p string) ([]byte, error) {
			return os.ReadFile(filepath.Join(src, p))
		},
	}
}

func cmdRelease(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: captain release <manifest|compat|check|report> [--json]")
		return
	}
	fs := flag.NewFlagSet("release", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	_ = fs.Parse(args[1:])

	deps := releaseDeps()
	switch args[0] {
	case "manifest":
		m := captaincode.BuildManifest(deps)
		if *asJSON {
			body, _ := captaincode.ManifestJSON(m)
			fmt.Println(string(body))
		} else {
			fmt.Print(captaincode.FormatReleaseManifest(m))
		}
	case "compat":
		rows := captaincode.BuildCompatTable()
		if *asJSON {
			body, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(body))
		} else {
			fmt.Print(formatCompatTable(rows))
		}
	case "check":
		checks := captaincode.RunReleaseChecks(deps)
		passed, failed := 0, 0
		for _, c := range checks {
			mark := "✓"
			if !c.Passed {
				mark = "✗"
				failed++
			} else {
				passed++
			}
			fmt.Printf("  %s %-28s %s\n", mark, c.ID, c.Title)
			if c.Detail != "" {
				fmt.Printf("     %s\n", truncate(c.Detail, 100))
			}
			if !c.Passed && c.Fix != "" {
				fmt.Printf("     fix: %s\n", c.Fix)
			}
		}
		fmt.Printf("\n%d passed, %d failed\n", passed, failed)
		if failed > 0 {
			os.Exit(1)
		}
	case "report":
		r := captaincode.BuildReleaseReport(deps)
		if *asJSON {
			body, _ := captaincode.ReleaseJSON(r)
			fmt.Println(string(body))
		} else {
			fmt.Print(captaincode.FormatReleaseReport(r))
		}
		if !r.ExitOK {
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "captain release: unknown subcommand %q (try manifest|compat|check|report)\n", args[0])
		os.Exit(2)
	}
}

func formatCompatTable(rows []captaincode.CompatRow) string {
	var b []byte
	b = append(b, fmt.Sprintf("%-13s %-12s %-12s  %s\n", "binary", "tested", "min", "capabilities")...)
	for _, row := range rows {
		var caps []byte
		for _, c := range row.Caps {
			caps = append(caps, fmt.Sprintf("%s=%s ", c.Cap, c.Support)...)
		}
		b = append(b, fmt.Sprintf("  %-13s %-12s %-12s  %s\n", row.Binary, row.Tested, row.Min, caps)...)
	}
	return string(b)
}
