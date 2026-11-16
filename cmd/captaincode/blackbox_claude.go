package main

// BB4: Claude hook parity. Claude Code runs `captain redact --hook` at its
// PreToolUse boundary, and that hook is the single Captain composition path
// for Claude. When CAPTAIN_BLACKBOX is audit or enforce, it screens the exact
// input that will execute - after restoration, after the sent-turn jail wrap -
// and files one private security record. Audit mode never refuses; enforce
// mode (BB5) answers Claude Code with a deny when the shared decision
// refuses, so the action never executes. An audit problem must never break a
// tool call.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// claudeBlackboxMode is the composed Claude screening mode: "audit" or
// "enforce" when the switch selects one, "" when it is off. It reads the same
// switch as the plugin and `captain blackbox hook`, so one environment value
// covers both hosts.
func claudeBlackboxMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CAPTAIN_BLACKBOX"))) {
	case "audit":
		return "audit"
	case "enforce":
		return "enforce"
	}
	return ""
}

// screenClaudeToolInput screens `input` (the tool input Claude will execute)
// and files one bounded record. It returns the normalized action, which stays
// in process memory: tests use it to prove the screened arguments are the
// executed ones, and it is never written anywhere. In enforce mode it also
// returns the enforce decision, whose Refuse the caller must turn into a
// Claude denial before the tool runs.
func screenClaudeToolInput(tool, session, cwd string, input map[string]any) (captaincode.BlackboxAction, captaincode.BlackboxSecurityRecord, captaincode.BlackboxEnforceDecision, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return captaincode.BlackboxAction{}, captaincode.BlackboxSecurityRecord{}, captaincode.BlackboxEnforceDecision{}, err
	}
	started := time.Now()
	action := captaincode.PrepareBlackboxHookAction("claude", "pre", strings.ToLower(tool), raw, "", cwd)
	result := captaincode.EvaluateBlackboxHook(action, blackboxSnapshotPath(""))
	mode := claudeBlackboxMode()
	record := captaincode.FinishBlackboxHookRecord(action, result, mode, session, started)
	var decision captaincode.BlackboxEnforceDecision
	if mode == "enforce" {
		now := time.Now()
		overrides, overrideErr := captaincode.LoadBlackboxOverrides(captaincode.BlackboxOverridesPath(), now)
		if overrideErr != nil {
			// A file that cannot be trusted grants nothing.
			fmt.Fprintf(os.Stderr, "captain: blackbox overrides: %v\n", overrideErr)
			overrides = nil
		}
		decision = captaincode.DecideBlackboxEnforcement("pre", result, overrides, action.WorkspaceID, now)
		captaincode.ApplyBlackboxEnforcement(&record, decision)
	}
	return action, record, decision, captaincode.AppendBlackboxSecurityRecord(record)
}

// claudeDenyOutput is the Claude Code denial for one enforce decision. The
// reason names the rule or the unavailable check; it never carries action
// text, paths or secrets.
func claudeDenyOutput(decision captaincode.BlackboxEnforceDecision) map[string]any {
	reason := decision.Reason
	switch {
	case strings.HasPrefix(reason, "blackbox_rule_match:"):
		reason = "captain blackbox refused this action: a security rule matched (" + strings.TrimPrefix(reason, "blackbox_rule_match:") + ")"
	case reason == "security_check_unavailable":
		reason = "captain blackbox refused this action: a required security check was unavailable"
	default:
		reason = "captain blackbox refused this action"
	}
	return map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny",
		"permissionDecisionReason": reason,
	}}
}

// auditClaudeHook runs the composed screening. It returns the screened
// action, whether anything was screened at all - tests use them to prove the
// screened input is the executed one - and a Claude denial map when enforce
// mode refuses the action. It reports a record failure on stderr instead of
// returning it: the tool call proceeds or is denied either way.
func auditClaudeHook(tool, session, cwd string, input map[string]any) (captaincode.BlackboxAction, bool, map[string]any) {
	if claudeBlackboxMode() == "" {
		return captaincode.BlackboxAction{}, false, nil
	}
	action, _, decision, err := screenClaudeToolInput(tool, session, cwd, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain: blackbox security record: %v\n", err)
	}
	if decision.Refuse {
		return action, true, claudeDenyOutput(decision)
	}
	return action, true, nil
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
