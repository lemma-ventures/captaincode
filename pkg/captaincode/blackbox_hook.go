package captaincode

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// The hook path between the opencode plugin and the evaluator (BB2). The
// plugin sends the final restored tool arguments; this file normalizes them,
// evaluates against the configured snapshot, and returns the record the
// caller files. Nothing here can refuse a tool: enforcement is BB5.

// BlackboxKindForTool maps a host tool name to its semantic kind. Known tools
// keep their original name in the action; an unknown tool is an explicit
// coverage gap, never a guess.
func BlackboxKindForTool(tool string) (string, bool) {
	switch strings.ToLower(tool) {
	case "read", "write", "edit", "multiedit", "patch", "apply_patch", "notebookedit", "grep", "glob":
		return "file", true
	case "bash", "shell", "terminal", "run", "execute":
		return "shell", true
	case "webfetch", "fetch", "websearch", "curl":
		return "fetch", true
	}
	return "unknown", false
}

// PrepareBlackboxHookAction normalizes one plugin event. Pre carries the
// restored arguments; post carries the already-masked tool output. The cwd
// becomes a path-free workspace ID on the action.
func PrepareBlackboxHookAction(host, phase, tool string, rawArgs json.RawMessage, output, cwd string) BlackboxAction {
	var action BlackboxAction
	if phase == "post" {
		kind, known := BlackboxKindForTool(tool)
		action = BlackboxAction{Schema: 1, Host: host, Phase: phase, Tool: tool, Kind: kind}
		if !known {
			action.Kind = "unknown"
			action.OmittedFields = []string{"unknown_tool"}
		}
		action.Text = output
		if len(output) > blackboxMaxText {
			action.Text = output[:blackboxMaxText]
			action.Truncated = true
			action.OmittedFields = append(action.OmittedFields, "text_limit")
		}
		action.ParseStatus = "parsed"
	} else {
		var gaps []string
		action, gaps = NormalizeBlackboxAction(host, phase, tool, rawArgs)
		action.OmittedFields = append(action.OmittedFields, gaps...)
	}
	action.WorkspaceID = BlackboxWorkspaceID(cwd)
	return action
}

// EvaluateBlackboxHook loads the snapshot and runs the evaluator with the
// same deadline the offline check uses. A missing or invalid snapshot is a
// degraded result, never a clean one.
func EvaluateBlackboxHook(action BlackboxAction, snapshotPath string) BlackboxResult {
	if snapshotPath == "" {
		return blackboxUnavailableResult("snapshot_unavailable")
	}
	file, err := os.Open(snapshotPath)
	if err != nil {
		return blackboxUnavailableResult("snapshot_unreadable")
	}
	defer file.Close()
	snapshot, err := DecodeBlackboxSnapshot(io.LimitReader(file, BlackboxMaxSnapshotBytes+1))
	if err != nil {
		return blackboxUnavailableResult("snapshot_invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	return EvaluateBlackbox(ctx, action, snapshot)
}

func blackboxUnavailableResult(reason string) BlackboxResult {
	result := BlackboxResult{Schema: 1, Status: "error", Errors: []string{reason}, Findings: []BlackboxFinding{}, Coverage: []BlackboxCoverage{}}
	for _, category := range blackboxCategories {
		result.Coverage = append(result.Coverage, BlackboxCoverage{Category: category, Status: "not_covered", Reason: reason})
	}
	return result
}

// FinishBlackboxHookRecord builds the security record for one hook event,
// joining the session to its task through the gate-session ledger. A session
// with no task stays taskless; the IDs the plugin did not send are named as
// omitted instead of guessed.
func FinishBlackboxHookRecord(action BlackboxAction, result BlackboxResult, mode, session string, started time.Time) BlackboxSecurityRecord {
	at := time.Now()
	record := NewBlackboxSecurityRecord(action, result, mode, at.Sub(started))
	FillBlackboxIDs(&record, session, GateTaskForSession(session, at), action.RunID, action.ToolCallID)
	return record
}
