package main

// BB4: Claude hook parity. Claude Code runs `captain redact --hook` at its
// PreToolUse boundary, and that hook is the single Captain composition path
// for Claude. When CAPTAIN_BLACKBOX=audit is set, it screens the exact input
// that will execute - after restoration, after the sent-turn jail wrap - and
// files one private security record. The screening never refuses: BB5 owns
// enforcement, and an audit problem must never break a tool call.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// claudeBlackboxMode is "audit" when the composed Claude screening is on and
// "" when it is off. It reads the same switch as the plugin and
// `captain blackbox hook`, so one environment value covers both hosts.
func claudeBlackboxMode() string {
	if strings.ToLower(strings.TrimSpace(os.Getenv("CAPTAIN_BLACKBOX"))) == "audit" {
		return "audit"
	}
	return ""
}

// screenClaudeToolInput screens `input` (the tool input Claude will execute)
// and files one bounded record in audit mode. It returns the normalized
// action, which stays in process memory: tests use it to prove the screened
// arguments are the executed ones, and it is never written anywhere.
func screenClaudeToolInput(tool, session, cwd string, input map[string]any) (captaincode.BlackboxAction, captaincode.BlackboxSecurityRecord, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return captaincode.BlackboxAction{}, captaincode.BlackboxSecurityRecord{}, err
	}
	started := time.Now()
	action := captaincode.PrepareBlackboxHookAction("claude", "pre", strings.ToLower(tool), raw, "", cwd)
	result := captaincode.EvaluateBlackboxHook(action, blackboxSnapshotPath(""))
	record := captaincode.FinishBlackboxHookRecord(action, result, "audit", session, started)
	return action, record, captaincode.AppendBlackboxSecurityRecord(record)
}

// auditClaudeHook runs the composed screening. It returns the screened action
// and whether anything was screened at all - tests use them to prove the
// screened input is the executed one. It reports a record failure on stderr
// instead of returning it: the tool call proceeds either way.
func auditClaudeHook(tool, session, cwd string, input map[string]any) (captaincode.BlackboxAction, bool) {
	if claudeBlackboxMode() != "audit" {
		return captaincode.BlackboxAction{}, false
	}
	action, _, err := screenClaudeToolInput(tool, session, cwd, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain: blackbox security record: %v\n", err)
	}
	return action, true
}

// hookUpdatedInput reads the input a Claude hook output will execute, or nil
// when the hook only denies (nothing then runs, so nothing is screened).
func hookUpdatedInput(output map[string]any) map[string]any {
	if output == nil {
		return nil
	}
	hookSpecific, _ := output["hookSpecificOutput"].(map[string]any)
	input, _ := hookSpecific["updatedInput"].(map[string]any)
	return input
}
