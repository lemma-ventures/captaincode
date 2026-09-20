package main

// `captain gate` - the action gate at the tool boundary (pkg gate.go).
//
// Captain's workers run at full permission because a headless fleet has
// nobody to answer an approval prompt. This is the screening that replaces
// the prompt: three nouls on the decision leg, a few hundred milliseconds,
// asked about the action a worker is one instant from taking.
//
//	captain gate --hook                Claude Code PreToolUse hook: hook JSON in, hook JSON out
//	captain gate --tool <name> [--cwd d]
//	                                   the opencode plugin's call: the tool's arguments as JSON on
//	                                   stdin; exit 0 allows, exit 3 refuses with the reason on stdout
//	captain gate --check "<command>"   screen one command by hand and print the verdict
//	captain gate --status              the mode, the bar, and whether a decision leg is configured
//	captain gate --report [--target 0.9] [--min 20]
//	                                   the screenings read as a calibration (`captain jev shadow`'s
//	                                   gate points), so a bar is chosen from the record, not guessed
//
// Every screening is appended to ~/.captaincode/gate.log. The brain does not
// write it: a screening happens in whichever process runs the tool, and two
// of those would race the ledger.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const gateUsage = "usage: captain gate --hook | captain gate --tool <name> [--cwd dir] | captain gate --check \"<command>\" | captain gate --status | captain gate --report [--target 0.9] [--min 20]"

// gateTimeout bounds a screening. A tool call waits on this, so it is short:
// past it the action is allowed and the timeout is on the record. A gate that
// can hang a worker is a worse failure than a gate that misses one action.
var gateTimeout = 3 * time.Second

func cmdGate(args []string) {
	fs := flag.NewFlagSet("gate", flag.ExitOnError)
	hook := fs.Bool("hook", false, "Claude Code PreToolUse hook: hook JSON on stdin, hook JSON on stdout")
	tool := fs.String("tool", "", "screen this tool's call; its arguments are JSON on stdin")
	cwd := fs.String("cwd", "", "the worker's working directory (default: this process's)")
	check := fs.String("check", "", "screen one shell command by hand")
	status := fs.Bool("status", false, "print the gate's mode and whether a decision leg is configured")
	report := fs.Bool("report", false, "read the screenings as a calibration")
	target := fs.Float64("target", 0.9, "agreement a confidence floor must reach to be suggested as the bar")
	minN := fs.Int("min", 20, "comparisons a floor needs before it can be suggested")
	_ = fs.Parse(args)

	switch {
	case *status:
		gateStatus()
	case *report:
		gateReport(*target, *minN)
	case *hook:
		gateHook()
	case *check != "":
		a := gateActionFromEnv(captaincode.GateAction{Tool: "bash", Command: *check, Cwd: gateCwd(*cwd)})
		v := screenAndLog(a)
		fmt.Println(captaincode.FormatGateVerdict(a, v))
		if !v.Allow {
			os.Exit(3)
		}
	case *tool != "":
		gateTool(*tool, gateCwd(*cwd))
	default:
		fatal(fmt.Errorf("%s", gateUsage))
	}
}

func gateCwd(v string) string {
	if v != "" {
		return v
	}
	d, _ := os.Getwd()
	return d
}

// gateActionFromEnv fills in what the worker's environment carries: which leg
// is running, what it was asked to do, and the task the screening joins to
// (pkg gate.go GateWorkerEnv). Absent, the screening judges on the working
// directory alone, which is what the opencode workers' shared serve can offer.
func gateActionFromEnv(a captaincode.GateAction) captaincode.GateAction {
	if a.Leg == "" {
		a.Leg = captaincode.Leg(os.Getenv(captaincode.GateLegEnv))
	}
	if a.Task == "" {
		a.Task = os.Getenv(captaincode.GateTaskEnv)
	}
	if a.TaskID == "" {
		a.TaskID = os.Getenv(captaincode.GateTaskIDEnv)
	}
	return a
}

// screenAndLog is the whole gate for one action: screen it on the decision
// leg and file the answer. With no leg configured this returns an allow
// without a call, so a captain with no key never pays for the gate and never
// waits on it.
func screenAndLog(a captaincode.GateAction) captaincode.GateVerdict {
	c := captaincode.SystemOneFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), gateTimeout)
	defer cancel()
	v := captaincode.ScreenAction(ctx, c, a)
	if r, ok := captaincode.GateRecord(a, v); ok {
		captaincode.AppendGateLog(r)
	}
	return v
}

// gateTool is the opencode plugin's call: the tool's arguments as JSON on
// stdin, exit 3 and a sentence on stdout to refuse. It mirrors
// `captain redact --check`, which the plugin already speaks.
func gateTool(tool, cwd string) {
	var raw map[string]any
	if err := json.NewDecoder(os.Stdin).Decode(&raw); err != nil {
		return // not our shape: allow silently, the way the redaction hook does
	}
	a := gateActionFromEnv(captaincode.GateAction{
		Tool:    tool,
		Command: firstString(raw, "command", "cmd", "url", "script"),
		Path:    firstString(raw, "filePath", "file_path", "path"),
		Cwd:     cwd,
	})
	v := screenAndLog(a)
	if v.Allow {
		return
	}
	fmt.Println(v.Reason)
	os.Exit(3)
}

// gateHook is the Claude Code PreToolUse hook body: {"tool_name":…,
// "tool_input":{…}} in, a deny decision out when the gate refuses. Silence
// allows, so a gate that is off, unconfigured or slow changes nothing.
func gateHook() {
	var in struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
		Cwd       string         `json:"cwd"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return
	}
	a := gateActionFromEnv(captaincode.GateAction{
		Tool:    strings.ToLower(in.ToolName),
		Command: firstString(in.ToolInput, "command", "url", "prompt"),
		Path:    firstString(in.ToolInput, "file_path", "filePath", "path", "notebook_path"),
		Cwd:     gateCwd(in.Cwd),
	})
	v := screenAndLog(a)
	if v.Allow {
		return
	}
	writeStdoutJSON(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny",
		"permissionDecisionReason": v.Reason,
	}})
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// gateStatus says what the gate would do right now and why - including the
// case that matters most, which is that without a decision leg it does
// nothing at all.
func gateStatus() {
	mode := captaincode.GateModeFromEnv()
	c := captaincode.SystemOneFromEnv()
	fmt.Printf("mode: %s (%s=off|shadow|enforce)\n", mode, captaincode.GateModeEnv)
	if c == nil {
		fmt.Printf("decision leg: none - no %s and no %s, so nothing is screened, no call is made and no tool call waits on one\n",
			captaincode.SystemOneKeyEnv, captaincode.SystemOneURLEnv)
		return
	}
	key := "key from " + c.KeySource
	if c.Keyless() {
		key = "keyless, from " + captaincode.SystemOneURLEnv
	}
	fmt.Printf("decision leg: %s (%s), model %s, %s\n", c.Backend(), key, c.Model, c.BaseURL)
	switch mode {
	case captaincode.GateOff:
		fmt.Println("nothing is screened")
	case captaincode.GateShadow:
		fmt.Printf("screening and recording to %s; every action is allowed\n", captaincode.GateLogPath())
		fmt.Println("read the record with `captain gate --report` before enforcing")
	case captaincode.GateEnforce:
		fmt.Printf("REFUSING actions whose risk reaches %.2f; a failed or slow call still allows\n", captaincode.GateBar())
		fmt.Println("the bar is only meaningful if `captain gate --report` shows it holds on this backend")
	}
}

// gateReport reads the screenings as a calibration. The gate's nouls are
// predictions, not a second opinion on a choice captain made at the same
// moment, so nothing captain decided beside a screening can settle it. What
// settles one is the task's own acceptance: a task the user accepted with
// no correction and no regression contains no action that destroyed
// anything, wandered off, or shipped the machine's contents off it, so
// every screening on it settles as `false` (settle.go).
//
// That sample is one-sided by construction, and the report says so: it
// bounds how often the gate fires on an action that turned out to be fine,
// and says nothing about what it misses. That is the bar `enforce` needs -
// the cost of turning it on too early is a refused worker - but it is not a
// detection rate and must never be read as one.
func gateReport(target float64, minN int) {
	rows := captaincode.ReadGateLog()
	if len(rows) == 0 {
		fmt.Printf("no screenings recorded in %s\n", captaincode.GateLogPath())
		fmt.Println("the gate records once a decision leg is configured (TYPESAFE_API_KEY, or a keyless CAPTAIN_SYSTEMONE_URL) and a worker runs a tool that can change the machine")
		return
	}
	var outcomes []captaincode.OutcomeEvidence
	if l, err := captaincode.LoadLedger(); err == nil {
		outcomes = l.Outcomes
	}
	fmt.Printf("%d screening(s) in %s\n\n", len(rows), captaincode.GateLogPath())
	fmt.Print(captaincode.FormatGateRisks(rows))
	fmt.Println()

	settled, withTask := captaincode.SettleGateRows(rows, outcomes), 0
	for _, r := range rows {
		if r.TaskID != "" {
			withTask++
		}
	}
	fmt.Printf("settled: %d of %d screening(s) belong to a task the user accepted cleanly\n", settled, len(rows))
	if withTask < len(rows) {
		fmt.Printf("  %d screening(s) carry no task identity and can never be settled: the opencode\n", len(rows)-withTask)
		fmt.Printf("  workers share one `opencode serve`, so %s is not in their environment\n", captaincode.GateTaskIDEnv)
	}
	if settled == 0 && withTask > 0 {
		fmt.Println("  the tasks these belong to are still pending or were not cleanly accepted;")
		fmt.Println("  `captain outcomes` shows what each is waiting on")
	}
	fmt.Println()

	cal := captaincode.ShadowCalibration(nil, rows, outcomes)
	fmt.Print(captaincode.FormatShadowCalibration(cal, target, minN))
	fmt.Println("\nRead the bar as a FALSE-POSITIVE bound, not a detection rate. A settled row is")
	fmt.Println("an action that turned out to be fine, because that is the only half a task's")
	fmt.Println("acceptance can settle: a rejected task says the work was bad, not which of its")
	fmt.Println("actions was the dangerous one. So this says how often a noul at or above a floor")
	fmt.Println("fired on something harmless - which is what enforcing too early would cost - and")
	fmt.Println("says nothing about what the gate lets through.")
}
