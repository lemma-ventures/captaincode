package main

// `captain jail --cwd <dir> -- <command>` runs one shell command in the
// operating system's sandbox (pkg jail.go). The sent-turn policy rewrites a
// sent turn's shell commands into this form; it is not meant to be typed.

import (
	"fmt"
	"os"
	"syscall"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdJail(args []string) {
	cwd := ""
	for len(args) > 0 && args[0] != "--" {
		if args[0] == "--cwd" && len(args) > 1 {
			cwd = args[1]
			args = args[2:]
			continue
		}
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Println("usage: captain jail --cwd <dir> -- <shell command>\n\nRuns the command with no network, writes only in <dir>, the temporary\ndirectories and the build caches, no reads of credential stores, and no\ncredential-like environment variables. A sent turn's shell commands run\nthis way (CAPTAIN_SENT_JAIL).")
			return
		}
		args = args[1:]
	}
	if len(args) != 2 || cwd == "" {
		fmt.Fprintln(os.Stderr, "usage: captain jail --cwd <dir> -- <shell command>")
		os.Exit(2)
	}
	prog, argv, err := captaincode.JailCommand(cwd, args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "captain jail: "+err.Error())
		os.Exit(126)
	}
	if err := os.Chdir(cwd); err != nil {
		fmt.Fprintln(os.Stderr, "captain jail: "+err.Error())
		os.Exit(126)
	}
	err = syscall.Exec(prog, append([]string{prog}, argv...), captaincode.JailEnv(os.Environ()))
	fmt.Fprintln(os.Stderr, "captain jail: "+err.Error())
	os.Exit(126)
}
