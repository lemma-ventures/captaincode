package main

// `captain parse "<line>"` prints how the brain will read a line - its parse
// tree in the command language (docs/LANGUAGE.md) - without sending it. The
// same tree is what the conformance suite checks.

import (
	"fmt"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdParse(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Printf("usage: captain parse \"<line>\"\n\nPrints how captain reads a line (command language %s, docs/LANGUAGE.md) without sending it.\n", captaincode.LanguageVersion)
		return
	}
	captaincode.LoadRegistry("")
	line := strings.Join(args, " ")
	fmt.Println(captaincode.ParseTurn(line).String())
	// A program also prints the plan the runner shows before it starts.
	if p, ok, err := captaincode.ParseProgram(captaincode.HoistLeading(strings.TrimSpace(line))); ok && err == nil {
		fmt.Printf("\nprogram, at most %d turns:\n%s\n", p.MaxTurns(100), p.Outline())
	}
}
