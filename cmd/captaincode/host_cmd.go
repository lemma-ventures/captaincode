package main

// Host integration CLI (ROADMAP M4.3–M4.5). The `captain host` command
// exposes the three host adapters (Pi, Jido, editor) as CLI verbs so a
// user or CI runner can delegate coding tasks to Captain from a terminal,
// an automation agent, or an editor without the TUI.
//
// Usage:
//
//	captain host pi <prompt>           # submit from a Pi terminal host
//	captain host pi inspect <task-id>  # inspect a task
//	captain host pi cancel <task-id>   # cancel a task
//	captain host pi artifacts <task-id>
//	captain host pi resume <task-id> <attempt-id>
//
//	captain host jido <prompt>         # submit from a Jido agent
//	captain host jido recover          # recover after restart
//
//	captain host cert                  # run certification checklist
//	captain host cert --host pi        # certify a specific host

import (
	"fmt"
	"os"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdHost(args []string) {
	if len(args) == 0 {
		printHostHelp()
		return
	}
	switch args[0] {
	case "pi":
		cmdHostPi(args[1:])
	case "jido":
		cmdHostJido(args[1:])
	case "cert":
		cmdHostCert(args[1:])
	default:
		printHostHelp()
	}
}

func printHostHelp() {
	fmt.Fprintln(os.Stderr, `host — delegate tasks from a host adapter

usage:
  captain host pi <prompt>              submit a task from a Pi terminal
  captain host pi inspect <task-id>     inspect a task
  captain host pi cancel <task-id>      cancel a task
  captain host pi artifacts <task-id>   show task artifacts
  captain host pi resume <task-id> <attempt-id>

  captain host jido <prompt>            submit a task from a Jido agent
  captain host jido recover             recover persisted tasks after restart

  captain host cert [--host pi|jido|editor]   run the certification checklist

the host adapters speak the task API (POST /v1/task) to the brain at
CAPTAIN_BRAIN_URL (default http://127.0.0.1:14097). Set CAPTAIN_TASK_TOKEN
to authenticate when the brain is exposed beyond loopback.`)
}

func cmdHostPi(args []string) {
	if len(args) == 0 {
		printHostHelp()
		return
	}
	switch args[0] {
	case "inspect":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain host pi inspect <task-id>"))
		}
		fatal(captaincode.PiInspect(args[1]))
	case "cancel":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain host pi cancel <task-id>"))
		}
		reason := "user cancel"
		if len(args) > 2 {
			reason = args[2]
		}
		fatal(captaincode.PiCancel(args[1], reason))
	case "artifacts":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain host pi artifacts <task-id>"))
		}
		fatal(captaincode.PiArtifacts(args[1]))
	case "resume":
		if len(args) < 3 {
			fatal(fmt.Errorf("usage: captain host pi resume <task-id> <attempt-id>"))
		}
		fatal(captaincode.PiResume(args[1], args[2]))
	default:
		prompt := args[0]
		result, err := captaincode.PiRun(prompt, captaincode.SubmitOptions{}, 2*time.Second, 10*time.Minute)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("\ntask %s %s\n", result.TaskID, result.State)
	}
}

func cmdHostJido(args []string) {
	if len(args) == 0 {
		printHostHelp()
		return
	}
	switch args[0] {
	case "recover":
		fatal(captaincode.JidoRecover())
	default:
		prompt := args[0]
		result, err := captaincode.JidoRun(prompt, captaincode.SubmitOptions{}, 2*time.Second, 10*time.Minute)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("\ntask %s %s\n", result.TaskID, result.State)
	}
}

func cmdHostCert(args []string) {
	hostKind := "pi"
	if len(args) > 0 && args[0] == "--host" && len(args) > 1 {
		hostKind = args[1]
	}
	client := captaincode.DefaultTaskClient()
	var adapter captaincode.HostAdapter
	switch captaincode.HostKind(hostKind) {
	case captaincode.HostPi:
		adapter = captaincode.NewPiAdapter(client)
	case captaincode.HostJido:
		adapter = captaincode.NewJidoAdapter(client)
	case captaincode.HostEditor:
		adapter = captaincode.NewEditorAdapter(client, "", nil)
	default:
		fatal(fmt.Errorf("unknown host %q (pi, jido, editor)", hostKind))
	}
	cert := captaincode.NewCertChecklist(adapter)
	failed := false

	if err := cert.CheckProjectRoot(""); err != nil {
		fmt.Printf("FAIL  project root: %v\n", err)
		failed = true
	} else {
		fmt.Println("PASS  project root")
	}
	root, _ := adapter.DetectProjectRoot("")
	if err := cert.CheckBaseRevision(root); err != nil {
		fmt.Printf("FAIL  base revision: %v\n", err)
		failed = true
	} else {
		fmt.Println("PASS  base revision")
	}
	if err := cert.CheckDelegationLoopRejection("cert-loop"); err != nil {
		fmt.Printf("FAIL  delegation loop: %v\n", err)
		failed = true
	} else {
		fmt.Println("PASS  delegation loop")
	}
	if err := cert.CheckDelegationDepthRejection(); err != nil {
		fmt.Printf("FAIL  delegation depth: %v\n", err)
		failed = true
	} else {
		fmt.Println("PASS  delegation depth")
	}
	if failed {
		os.Exit(1)
	}
	fmt.Println("\ncertification passed")
}
