package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdBlackbox(args []string) {
	if err := runBlackbox(args, os.Stdin, os.Stdout); err != nil {
		fatal(err)
	}
}

func runBlackbox(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: captain blackbox check|hook ...")
	}
	switch args[0] {
	case "check":
		return runBlackboxCheck(args[1:], stdin, stdout)
	case "hook":
		return runBlackboxHook(args[1:], stdin, stdout)
	default:
		return fmt.Errorf("usage: captain blackbox check|hook ...")
	}
}

func runBlackboxCheck(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("blackbox check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshotPath := flags.String("snapshot", "", "synthetic rule snapshot")
	if err := flags.Parse(args); err != nil || *snapshotPath == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: captain blackbox check --snapshot <file> < action.json")
	}
	file, err := os.Open(*snapshotPath)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	snapshot, decodeErr := captaincode.DecodeBlackboxSnapshot(io.LimitReader(file, captaincode.BlackboxMaxSnapshotBytes+1))
	closeErr := file.Close()
	if decodeErr != nil {
		return fmt.Errorf("snapshot: %w", decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close snapshot: %w", closeErr)
	}
	action, err := captaincode.DecodeBlackboxAction(io.LimitReader(stdin, captaincode.BlackboxMaxActionBytes+1))
	if err != nil {
		return fmt.Errorf("action: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	result := captaincode.EvaluateBlackbox(ctx, action, snapshot)
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

// blackboxHookInput is what the opencode plugin sends per tool call. Args is
// the FINAL RESTORED argument object (secret placeholders already turned back
// into real values by the redact layer), so the detector sees the command that
// will actually run. Output is the post-tool result text, already masked.
type blackboxHookInput struct {
	Tool    string          `json:"tool"`
	Cwd     string          `json:"cwd"`
	Session string          `json:"session"`
	Phase   string          `json:"phase"`
	Args    json.RawMessage `json:"args"`
	Output  string          `json:"output"`
}

type blackboxHookOutput struct {
	Status      string `json:"status"`
	Disposition string `json:"disposition"`
	EventID     string `json:"event_id,omitempty"`
	Findings    int    `json:"finding_count"`
}

// runBlackboxHook screens one tool event in audit mode and files a private
// security record. It never refuses: enforcement is BB5 and is not built. It
// exits 0 for every screened event, including degraded ones, so an audit
// problem can never break a tool call.
func runBlackboxHook(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("blackbox hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshotPath := flags.String("snapshot", "", "synthetic rule snapshot")
	modeFlag := flags.String("mode", "", "audit mode (audit)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: captain blackbox hook [--snapshot <file>] [--mode audit] < event.json")
	}
	mode := *modeFlag
	if mode == "" {
		mode = os.Getenv("CAPTAIN_BLACKBOX")
	}
	if mode == "" {
		mode = "off"
	}
	switch strings.ToLower(mode) {
	case "off":
		return writeBlackboxHookOutput(stdout, blackboxHookOutput{Status: "disabled"})
	case "audit":
	case "enforce":
		return fmt.Errorf("enforcement is not implemented (BB5); use audit")
	default:
		return fmt.Errorf("unknown blackbox mode %q", mode)
	}
	var input blackboxHookInput
	decoder := json.NewDecoder(io.LimitReader(stdin, captaincode.BlackboxMaxActionBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("hook input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("hook input: trailing JSON")
	}
	if input.Tool == "" {
		return fmt.Errorf("hook input: tool is required")
	}
	phase := input.Phase
	if phase == "" {
		phase = "pre"
	}
	if phase != "pre" && phase != "post" {
		return fmt.Errorf("hook input: phase must be pre or post")
	}

	started := time.Now()
	action := captaincode.PrepareBlackboxHookAction("opencode", phase, input.Tool, input.Args, input.Output, input.Cwd)
	result := captaincode.EvaluateBlackboxHook(action, blackboxSnapshotPath(*snapshotPath))
	record := captaincode.FinishBlackboxHookRecord(action, result, "audit", input.Session, started)

	writeErr := captaincode.AppendBlackboxSecurityRecord(record)
	status := blackboxHookOutput{Status: record.Status, Disposition: record.Disposition, EventID: record.EventID, Findings: record.FindingCount}
	if writeErr != nil {
		// The screening happened; its record did not. Say so, never fail.
		fmt.Fprintf(os.Stderr, "captain: blackbox security record: %v\n", writeErr)
		status.Status = "degraded"
		status.Disposition = "degraded"
		status.EventID = ""
	}
	return writeBlackboxHookOutput(stdout, status)
}

func blackboxSnapshotPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if p := os.Getenv("CAPTAIN_BLACKBOX_SNAPSHOT"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + "/.config/captain/blackbox-snapshot.json"
}

func writeBlackboxHookOutput(stdout io.Writer, output blackboxHookOutput) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(output)
}
