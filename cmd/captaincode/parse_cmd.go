package main

// `captain parse "<line>"` prints how the brain will read a line - its
// parse tree (docs/WORKFLOW_LANGUAGE.md) - without sending it.

import (
	"fmt"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdParse(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Printf("usage: captain parse \"<line>\"\n\nPrints how captain reads a line (WORKFLOW_LANGUAGE.md) without sending it.\n")
		return
	}
	captaincode.LoadRegistry("")
	p, ok, err := captaincode.ParseProgram(strings.Join(args, " "))
	if err != nil {
		fmt.Println(err)
		return
	}
	if !ok {
		fmt.Println("(no program syntax - routes as plain text)")
		return
	}
	fmt.Println(p.String())
}
