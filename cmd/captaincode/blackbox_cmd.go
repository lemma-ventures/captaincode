package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
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
		return fmt.Errorf("usage: captain blackbox check|export|keygen|hook|pilot|status ...")
	}
	switch args[0] {
	case "check":
		return runBlackboxCheck(args[1:], stdin, stdout)
	case "keygen":
		return runBlackboxKeygen(args[1:], stdout)
	case "export":
		return runBlackboxExport(args[1:], stdout)
	case "hook":
		return runBlackboxHook(args[1:], stdin, stdout)
	case "status":
		return runBlackboxStatus(args[1:], stdout)
	case "pilot":
		return runBlackboxPilot(args[1:], stdout)
	default:
		return fmt.Errorf("usage: captain blackbox check|export|keygen|hook|pilot|status ...")
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
	Refuse      bool   `json:"refuse,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// runBlackboxHook screens one tool event and files a private security record.
// Audit mode never refuses: every screened event exits 0, so an audit problem
// can never break a tool call. Enforce mode (BB5) additionally refuses the
// action for an eligible trusted rule match or an unavailable required check:
// the refusal is a decision, not an error, and it is reported in the JSON
// output for the host to act on before any side effect.
func runBlackboxHook(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("blackbox hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshotPath := flags.String("snapshot", "", "synthetic rule snapshot")
	modeFlag := flags.String("mode", "", "blackbox mode (audit, enforce)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: captain blackbox hook [--snapshot <file>] [--mode audit|enforce] < event.json")
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
	case "audit", "enforce":
	default:
		return fmt.Errorf("unknown blackbox mode %q", mode)
	}
	mode = strings.ToLower(mode)
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
	result := evaluateBlackboxHookAction(action, *snapshotPath)
	record := captaincode.FinishBlackboxHookRecord(action, result, mode, input.Session, started)

	var decision captaincode.BlackboxEnforceDecision
	if mode == "enforce" {
		now := time.Now()
		overrides, overrideErr := captaincode.LoadBlackboxOverrides(captaincode.BlackboxOverridesPath(), now)
		if overrideErr != nil {
			// A file that cannot be trusted grants nothing: every
			// would-block refusal stands.
			fmt.Fprintf(os.Stderr, "captain: blackbox overrides: %v\n", overrideErr)
			overrides = nil
		}
		decision = captaincode.DecideBlackboxEnforcement(phase, result, overrides, action.WorkspaceID, now)
		captaincode.ApplyBlackboxEnforcement(&record, decision)
	}

	writeErr := captaincode.AppendBlackboxSecurityRecord(record)
	status := blackboxHookOutput{Status: record.Status, Disposition: record.Disposition, EventID: record.EventID, Findings: record.FindingCount, Refuse: decision.Refuse, Reason: decision.Reason}
	if writeErr != nil {
		// The screening happened; its record did not. Say so, never fail.
		fmt.Fprintf(os.Stderr, "captain: blackbox security record: %v\n", writeErr)
		status.Status = "degraded"
		status.Disposition = "degraded"
		status.EventID = ""
		// A refusal stands even when its record could not be written:
		// losing the record must not lose the refusal.
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

// evaluateBlackboxHookAction resolves the rule source for one hook event.
// An explicit snapshot flag or CAPTAIN_BLACKBOX_SNAPSHOT file wins. Without
// one, a configured authenticated store supplies the accepted snapshot; a
// store that is configured but cannot be opened degrades the screening
// instead of silently falling back to the unverified file. With no store
// configured at all, the default file path keeps the previous behavior.
func evaluateBlackboxHookAction(action captaincode.BlackboxAction, flagPath string) captaincode.BlackboxResult {
	if flagPath != "" || os.Getenv("CAPTAIN_BLACKBOX_SNAPSHOT") != "" {
		return captaincode.EvaluateBlackboxHook(action, blackboxSnapshotPath(flagPath))
	}
	store, configured, err := blackboxStoreFromEnv()
	if !configured {
		return captaincode.EvaluateBlackboxHook(action, blackboxSnapshotPath(flagPath))
	}
	if err != nil {
		return captaincode.BlackboxUnavailableResult("snapshot_unavailable")
	}
	return captaincode.EvaluateBlackboxHookStore(action, store, time.Now())
}

// blackboxStoreFromEnv opens the authenticated snapshot store when the
// operator configured any of its trust pins. configured is true when at least
// one pin is set, so a half-configured store is reported, not ignored.
func blackboxStoreFromEnv() (store *captaincode.BlackboxStore, configured bool, err error) {
	values := []string{
		os.Getenv("CAPTAIN_BLACKBOX_STORE"), os.Getenv("CAPTAIN_BLACKBOX_KEY_ID"),
		os.Getenv("CAPTAIN_BLACKBOX_PUBLIC_KEY"), os.Getenv("CAPTAIN_BLACKBOX_TRUST_ROOT"),
		os.Getenv("CAPTAIN_BLACKBOX_GRAPH"), os.Getenv("CAPTAIN_BLACKBOX_NETWORK"),
	}
	complete := true
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			complete = false
		} else {
			configured = true
		}
	}
	if !configured || !complete {
		return nil, configured, nil
	}
	publicKey, err := base64.StdEncoding.DecodeString(values[2])
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, true, fmt.Errorf("blackbox store public key pin is invalid")
	}
	store, err = captaincode.OpenBlackboxStore(values[0], captaincode.BlackboxTrustPins{
		KeyID: values[1], PublicKey: publicKey, TrustRoot: values[3], Graph: values[4], Network: values[5],
	})
	return store, true, err
}

func writeBlackboxHookOutput(stdout io.Writer, output blackboxHookOutput) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(output)
}
