package main

// `captain parse "<line>"` prints how the brain will read a line - its
// parse tree in the command language (docs/LANGUAGE.md) - without sending
// it. The same tree is what the conformance suite checks.

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
	fmt.Println(captaincode.ParseTurn(strings.Join(args, " ")).String())
}
