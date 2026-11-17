package main

// `captain blackbox status` - the workspace security status: which hosts are
// covered, how old the active rule snapshot is, and what the private security
// stream says. It is the read half of the Blackbox integration (BB4): a
// bounded, read-only report over records that never carry action text, so it
// prints counts and path-free workspace ids, never commands, paths or rules.

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// blackboxStatusMaxRecords is how many of the newest records the report
// reads. The stream rotates at 1 MiB, so this bounds the work.
const blackboxStatusMaxRecords = 500

// blackboxDispositions are the states the workspace view distinguishes.
// `not_covered` counts records that carry a not_covered coverage entry; it is
// the only one the writer cannot produce as a record disposition itself.
// `blocked`, `security_check_unavailable` and `overridden` are enforce-mode
// dispositions (BB5); audit mode keeps would_block.
var blackboxDispositions = []string{
	"blocked", "security_check_unavailable", "overridden",
	"would_block", "observed_after_execution", "evaluated", "degraded", "not_covered",
}

// blackboxUncoveredTransports are the Captain workers with no pre-execution
// hook today. Naming them is the coverage contract: nothing else is claimed.
var blackboxUncoveredTransports = []string{"codex-cli", "grok-cli", "cursor-agent"}

type blackboxAuthorityStatus struct {
	Configured bool
	OK         bool
	Expired    bool
	ID         string
	VerifiedAt time.Time
	ExpiresAt  time.Time
}

// blackboxStatus is the report's inputs, gathered once and printed once.
type blackboxStatus struct {
	Mode              string
	ModeValid         bool
	ClaudeHook        bool
	SnapshotID        string
	SnapshotAge       time.Duration
	SnapshotPath      string
	SnapshotOK        bool
	SnapshotFromStore bool
	StoreClosed       bool
	Authority         blackboxAuthorityStatus
	Records           []captaincode.BlackboxSecurityRecord
	Total             int
	Partial           int
}

func runBlackboxStatus(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: captain blackbox status")
	}
	status := collectBlackboxStatus()
	_, err := io.WriteString(stdout, status.String())
	return err
}

func collectBlackboxStatus() blackboxStatus {
	status := blackboxStatus{Mode: blackboxEnvMode(), Authority: collectBlackboxAuthorityStatus(time.Now().UTC())}
	switch status.Mode {
	case "audit", "enforce", "off", "shadow":
		status.ModeValid = true
	}
	status.ClaudeHook = claudeBlackboxMode() != "" && captaincode.ClaudeHookInstalled("captain redact --hook")

	// Match evaluateBlackboxHookAction. An explicit snapshot file wins. With
	// none, a configured store is the only source: a closed store fails closed
	// and the unused legacy file is not a degraded screening.
	status.SnapshotPath = blackboxSnapshotPath("")
	if os.Getenv("CAPTAIN_BLACKBOX_SNAPSHOT") == "" && status.Authority.Configured {
		status.SnapshotFromStore = true
		status.SnapshotPath = "authenticated store"
		if status.Authority.ID != "" {
			status.SnapshotID = status.Authority.ID
			status.SnapshotOK = status.Authority.OK
			if !status.Authority.VerifiedAt.IsZero() {
				status.SnapshotAge = time.Since(status.Authority.VerifiedAt)
			}
		} else {
			status.StoreClosed = true
		}
	} else if file, err := os.Open(status.SnapshotPath); err == nil {
		if snapshot, decodeErr := captaincode.DecodeBlackboxSnapshot(io.LimitReader(file, captaincode.BlackboxMaxSnapshotBytes+1)); decodeErr == nil {
			status.SnapshotOK = true
			status.SnapshotID = safeField(snapshot.ID + " " + snapshot.Source)
		}
		if info, statErr := file.Stat(); statErr == nil {
			status.SnapshotAge = time.Since(info.ModTime())
		}
		file.Close()
	}

	path := captaincode.BlackboxSecurityPath()
	records, partial, err := captaincode.ReadBlackboxSecurityRecords(path)
	if err != nil {
		records, partial = nil, 0
	}
	status.Partial = partial
	status.Total = len(records)
	if status.Total > blackboxStatusMaxRecords {
		records = records[status.Total-blackboxStatusMaxRecords:]
	}
	status.Records = records
	return status
}

func collectBlackboxAuthorityStatus(now time.Time) blackboxAuthorityStatus {
	values := []string{
		os.Getenv("CAPTAIN_BLACKBOX_STORE"), os.Getenv("CAPTAIN_BLACKBOX_KEY_ID"),
		os.Getenv("CAPTAIN_BLACKBOX_PUBLIC_KEY"), os.Getenv("CAPTAIN_BLACKBOX_TRUST_ROOT"),
		os.Getenv("CAPTAIN_BLACKBOX_GRAPH"), os.Getenv("CAPTAIN_BLACKBOX_NETWORK"),
	}
	configured := false
	complete := true
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			complete = false
		} else {
			configured = true
		}
	}
	report := blackboxAuthorityStatus{Configured: configured}
	if !configured || !complete {
		return report
	}
	publicKey, err := base64.StdEncoding.DecodeString(values[2])
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return report
	}
	store, err := captaincode.OpenBlackboxStore(values[0], captaincode.BlackboxTrustPins{
		KeyID: values[1], PublicKey: publicKey, TrustRoot: values[3], Graph: values[4], Network: values[5],
	})
	if err != nil || !store.HasCurrent() {
		return report
	}
	report.ID = safeField(store.Snapshot().ID + " " + store.Snapshot().Source)
	report.VerifiedAt = store.VerifiedAt().UTC()
	report.ExpiresAt = store.ExpiresAt().UTC()
	report.Expired = store.Expired(now)
	report.OK = !report.Expired
	return report
}

// blackboxEnvMode is CAPTAIN_BLACKBOX as the operator set it: lower case,
// trimmed, "off" when unset.
func blackboxEnvMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CAPTAIN_BLACKBOX")))
	if mode == "" {
		return "off"
	}
	return mode
}

func (s blackboxStatus) String() string {
	var b strings.Builder
	switch {
	case !s.ModeValid:
		fmt.Fprintf(&b, "blackbox   mode %s - unknown; `captain blackbox hook` refuses it, use audit or off\n", s.Mode)
	case s.Mode == "audit":
		fmt.Fprintln(&b, "blackbox   mode audit - tool events are recorded, nothing is refused (BB5)")
	case s.Mode == "enforce":
		fmt.Fprintln(&b, "blackbox   mode enforce - eligible critical matches and unavailable required checks are refused before the tool runs (BB5)")
	default:
		fmt.Fprintln(&b, "blackbox   mode off - CAPTAIN_BLACKBOX=audit starts recording tool events (nothing is ever refused)")
	}
	if os.Getenv("CAPTAIN_JAILED") == "1" {
		fmt.Fprintln(&b, "settings   hidden - a sent-turn shell never receives the mode, the overrides or the snapshot; run this outside a jail")
	}
	switch {
	case s.ClaudeHook && s.Mode == "enforce":
		fmt.Fprintln(&b, "claude     covered - the composed path in `captain redact --hook` applies the same refusals")
	case s.ClaudeHook:
		fmt.Fprintln(&b, "claude     covered - the audit composes into `captain redact --hook`")
	case s.Mode == "audit" && captaincode.RedactMode() == "off":
		fmt.Fprintln(&b, "claude     not covered - CAPTAIN_REDACT=off keeps the composed hook uninstalled")
	case s.Mode == "audit":
		fmt.Fprintln(&b, "claude     not covered - `captain redact --hook` is not installed (run `captain init`)")
	case s.ModeValid:
		fmt.Fprintln(&b, "claude     off - CAPTAIN_BLACKBOX=audit turns the composed audit on")
	default:
		fmt.Fprintf(&b, "claude     unknown - mode %s here\n", s.Mode)
	}
	switch {
	case s.SnapshotFromStore && s.StoreClosed:
		fmt.Fprintln(&b, "snapshot   authenticated store unavailable - screenings fail closed; the legacy file is not used")
	case s.SnapshotFromStore && s.Authority.Expired:
		fmt.Fprintf(&b, "snapshot   %s, expired at %s - authenticated store; screenings fail closed until renewal\n", s.SnapshotID, s.Authority.ExpiresAt.Format(time.RFC3339))
	case s.SnapshotFromStore && s.SnapshotOK:
		fmt.Fprintf(&b, "snapshot   %s, verified age %s - authenticated store\n", s.SnapshotID, ageText(s.SnapshotAge))
	case s.SnapshotOK:
		fmt.Fprintf(&b, "snapshot   %s, age %s - %s\n", s.SnapshotID, ageText(s.SnapshotAge), s.SnapshotPath)
	default:
		fmt.Fprintln(&b, "snapshot   unreadable - every screening stays degraded, never clean - "+safeField(s.SnapshotPath))
	}
	switch {
	case !s.Authority.Configured:
		fmt.Fprintln(&b, "authority  not configured - the file snapshot remains unverified")
	case s.Authority.OK:
		fmt.Fprintf(&b, "authority  %s, verified at %s, expires at %s - authenticated\n", s.Authority.ID, s.Authority.VerifiedAt.Format(time.RFC3339), s.Authority.ExpiresAt.Format(time.RFC3339))
	case s.Authority.Expired:
		fmt.Fprintf(&b, "authority  %s, expired at %s - authenticated but not fresh\n", s.Authority.ID, s.Authority.ExpiresAt.Format(time.RFC3339))
	default:
		if s.SnapshotFromStore {
			fmt.Fprintln(&b, "authority  unavailable - check the store and trust pins; screenings fail closed")
		} else {
			fmt.Fprintln(&b, "authority  unavailable - check the store and trust pins; the file snapshot remains unverified")
		}
	}

	byDisposition := map[string]int{}
	byWorkspace := map[string]int{}
	byHost := map[string]int{}
	for _, record := range s.Records {
		byWorkspace[record.WorkspaceID]++
		host := record.Host
		if host == "" {
			host = "unknown"
		}
		byHost[host]++
		if hasNotCovered(record) {
			byDisposition["not_covered"]++
		}
		if record.Disposition != "" {
			byDisposition[record.Disposition]++
		}
	}
	if s.Total == 0 {
		fmt.Fprintln(&b, "records    0 - no tool event has been screened on this host yet")
	} else {
		line := fmt.Sprintf("records    %d", s.Total)
		if s.Total > len(s.Records) {
			line += fmt.Sprintf(" (the newest %d counted)", len(s.Records))
		}
		if s.Partial > 0 {
			line += fmt.Sprintf(", %d unreadable line(s)", s.Partial)
		}
		fmt.Fprintln(&b, line)
	}
	fmt.Fprint(&b, "dispositions  ")
	for i, name := range blackboxDispositions {
		if i > 0 {
			b.WriteString(" · ")
		}
		fmt.Fprintf(&b, "%s %d", name, byDisposition[name])
	}
	b.WriteString("\n")

	if len(byHost) > 0 {
		fmt.Fprintln(&b, "hosts      "+joinCounts(byHost))
	}
	if len(byWorkspace) > 0 {
		fmt.Fprintln(&b, "workspaces "+joinCounts(byWorkspace))
	}
	fmt.Fprintln(&b, "uncovered  "+strings.Join(blackboxUncoveredTransports, ", ")+" (no pre-execution hook)")
	return b.String()
}

func hasNotCovered(record captaincode.BlackboxSecurityRecord) bool {
	for _, coverage := range record.Coverage {
		if coverage.Status == "not_covered" {
			return true
		}
	}
	return false
}

// joinCounts prints "key value · key value", largest first, names sorted on a
// tie. Keys are already sanitized ids from the record writer.
func joinCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
	}
	return strings.Join(parts, " · ")
}

// ageText gives a snapshot age in whole units - "42 m", "3 h", "12 d" - so a
// stale rule set is one look away.
func ageText(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return "<1 m"
	case age < time.Hour:
		return fmt.Sprintf("%d m", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%d h", int(age.Hours()))
	default:
		return fmt.Sprintf("%d d", int(age.Hours()/24))
	}
}

// safeField keeps one printable ASCII run and bounds it: a snapshot id or
// revision is external data and must not reach a terminal with control bytes
// in it (same rule as `captain secrets list`).
func safeField(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r < 0x20 || r > 0x7e || b.Len() >= 64 {
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}
