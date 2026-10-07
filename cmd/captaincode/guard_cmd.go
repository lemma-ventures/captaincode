package main

// `captain guard-exec <tool> <args…>` - what a CLI worker's git, gh and
// package-tool shims run (pkg workerguard.go): the worker guard for this
// turn, then the real program. Not meant to be typed.

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdGuardExec(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: captain guard-exec <tool> [args…]")
		os.Exit(2)
	}
	tool := args[0]
	real, err := captaincode.RealBinary(tool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "captain guard: "+err.Error())
		os.Exit(127)
	}
	if os.Getenv(captaincode.GuardEnv) == "1" {
		line := tool
		for _, a := range args[1:] {
			line += " " + shellWord(a)
		}
		if why := captaincode.WorkerGuardRefusal(line, os.Getenv(captaincode.MayPublishEnv) == "1", os.Getenv(captaincode.DirtyBeforeEnv) == "1"); why != "" {
			cwd, _ := os.Getwd()
			captaincode.AppendConduct(captaincode.ConductEvent{Rule: why, Command: line, Dir: cwd,
				Leg: os.Getenv(captaincode.GateLegEnv), TaskID: os.Getenv(captaincode.GateTaskIDEnv)})
			fmt.Fprintln(os.Stderr, "captain: "+why)
			os.Exit(126)
		}
	}
	err = syscall.Exec(real, append([]string{tool}, args[1:]...), os.Environ())
	fmt.Fprintln(os.Stderr, "captain guard: "+err.Error())
	os.Exit(126)
}

// shellWord quotes an argument only when it needs it, so the guard reads
// `git tag v1` and `git commit -m 'a b'` the way they were typed.
func shellWord(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"$`\\|;&<>()*?") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
