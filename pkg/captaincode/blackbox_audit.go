package captaincode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Bounded security records for the Blackbox audit pilot (BB2). One record per
// screened tool event: correlation IDs, coverage, findings and the final
// disposition. Never the action itself - no commands, paths, URLs, package
// names, prompts, outputs or secret values reach this stream. The run-record
// work (Q8) already keeps the full masked input beside the run; a second copy
// here would widen, not narrow, the sensitive surface.

const (
	// blackboxMaxRecordBytes caps one serialized record; the writer trims
	// findings to fit and refuses anything still larger.
	blackboxMaxRecordBytes = 8 << 10
	// blackboxMaxRecordFindings is how many findings a record carries in
	// full; the count keeps the true total.
	blackboxMaxRecordFindings = 8
)

// blackboxRotateBytes is the size at which security.jsonl is rotated. A
// variable so a test can force rotation without writing a megabyte.
var blackboxRotateBytes int64 = 1 << 20

// BlackboxSecurityFinding is one finding's safe summary.
type BlackboxSecurityFinding struct {
	Category string `json:"category"`
	RuleID   string `json:"rule_id"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
}

// BlackboxSecurityRecord is one screened tool event.
type BlackboxSecurityRecord struct {
	Schema         int                       `json:"schema"`
	EventID        string                    `json:"event_id"`
	Time           string                    `json:"time"`
	WorkspaceID    string                    `json:"workspace_id,omitempty"`
	SessionID      string                    `json:"session_id,omitempty"`
	TaskID         string                    `json:"task_id,omitempty"`
	RunID          string                    `json:"run_id,omitempty"`
	ToolCallID     string                    `json:"tool_call_id,omitempty"`
	Phase          string                    `json:"phase"`
	Tool           string                    `json:"tool"`
	Mode           string                    `json:"mode"`
	Disposition    string                    `json:"disposition"`
	Status         string                    `json:"status"`
	Coverage       []BlackboxCoverage        `json:"coverage,omitempty"`
	Findings       []BlackboxSecurityFinding `json:"findings"`
	FindingCount   int                       `json:"finding_count"`
	SnapshotID     string                    `json:"snapshot_id,omitempty"`
	SnapshotDigest string                    `json:"snapshot_digest,omitempty"`
	DurationUS     int64                     `json:"duration_us"`
	Reasons        []string                  `json:"reasons,omitempty"`
	OmittedIDs     []string                  `json:"omitted_ids,omitempty"`
}

// blackboxIDPattern accepts correlation and event identifiers: short printable
// runs, no control characters, nothing an attacker writes to a terminal.
var blackboxIDPattern = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)

// blackboxEventSeq separates two records stamped within the same nanosecond.
var blackboxEventSeq atomic.Uint64

// BlackboxEventID stamps one record: time, a per-process sequence and the
// identity fields, so two concurrent writers cannot collide.
func BlackboxEventID(at time.Time, workspace, session, tool string) string {
	seq := blackboxEventSeq.Add(1)
	sum := sha256.Sum256([]byte(at.Format(time.RFC3339Nano) + "\x00" + strconv.FormatUint(seq, 10) + "\x00" + workspace + "\x00" + session + "\x00" + tool))
	return "bbx_" + hex.EncodeToString(sum[:8])
}

// BlackboxWorkspaceID is a stable, path-free identity for a working folder:
// the working directory itself never reaches the record.
func BlackboxWorkspaceID(cwd string) string {
	if cwd == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(cwd))
	return "ws_" + hex.EncodeToString(sum[:8])
}

// sanitizeBlackboxID keeps a correlation ID only when it is a short printable
// run. Anything else is reported as omitted, never copied raw.
func sanitizeBlackboxID(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	if !blackboxIDPattern.MatchString(id) {
		return "", true
	}
	return id, false
}

// FillBlackboxIDs sanitizes the correlation IDs in place and names the ones it
// dropped. A missing ID stays missing; a hostile ID is dropped, not recorded.
func FillBlackboxIDs(record *BlackboxSecurityRecord, session, task, run, toolCall string) {
	var omitted []string
	assign := func(field *string, value, name string) {
		if value == "" {
			return
		}
		if clean, dropped := sanitizeBlackboxID(value); !dropped {
			*field = clean
		} else {
			*field = ""
			omitted = append(omitted, name)
		}
	}
	assign(&record.SessionID, session, "session_id")
	assign(&record.TaskID, task, "task_id")
	assign(&record.RunID, run, "run_id")
	assign(&record.ToolCallID, toolCall, "tool_call_id")
	record.OmittedIDs = omitted
}

// NewBlackboxSecurityRecord builds one pre- or post-tool record from an
// evaluation result. severity is the audit threshold a finding must reach to
// be a would_block; everything else stays a recorded observation.
func NewBlackboxSecurityRecord(action BlackboxAction, result BlackboxResult, mode string, duration time.Duration) BlackboxSecurityRecord {
	record := BlackboxSecurityRecord{
		Schema:         1,
		Time:           time.Now().UTC().Format(time.RFC3339Nano),
		WorkspaceID:    action.WorkspaceID,
		SessionID:      action.SessionID,
		TaskID:         action.TaskID,
		RunID:          action.RunID,
		ToolCallID:     action.ToolCallID,
		Phase:          action.Phase,
		Tool:           action.Tool,
		Mode:           mode,
		Status:         result.Status,
		Coverage:       result.Coverage,
		SnapshotID:     result.SnapshotID,
		SnapshotDigest: result.SnapshotDigest,
		DurationUS:     duration.Microseconds(),
		Findings:       make([]BlackboxSecurityFinding, 0, len(result.Findings)),
		FindingCount:   len(result.Findings),
	}
	record.EventID = BlackboxEventID(time.Now(), record.WorkspaceID, record.SessionID, record.Tool)
	threshold := false
	for _, finding := range result.Findings {
		if len(record.Findings) < blackboxMaxRecordFindings {
			record.Findings = append(record.Findings, BlackboxSecurityFinding(finding))
		}
		if finding.Severity == "critical" {
			threshold = true
		}
	}
	switch {
	case action.Phase == "post":
		// A post-tool observation is never prevention, whatever it saw.
		record.Disposition = "observed_after_execution"
	case result.Status == "error":
		record.Disposition = "degraded"
		record.Reasons = append(record.Reasons, result.Errors...)
	case result.Status == "incomplete":
		record.Disposition = "degraded"
		for _, coverage := range result.Coverage {
			if coverage.Status == "incomplete" || coverage.Status == "not_covered" {
				record.Reasons = append(record.Reasons, coverage.Category+":"+coverage.Status)
			}
		}
	case threshold:
		record.Disposition = "would_block"
	default:
		record.Disposition = "evaluated"
	}
	return record
}

// BlackboxSecurityPath is the security record stream. CAPTAIN_BLACKBOX_STATE
// moves it for tests and for an isolated install.
func BlackboxSecurityPath() string {
	if p := os.Getenv("CAPTAIN_BLACKBOX_STATE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "security.jsonl")
}

// AppendBlackboxSecurityRecord writes one bounded line. Concurrent writers are
// separate processes appending one line each with O_APPEND, so a single Write
// of a small buffer stays whole. Rotation keeps the stream bounded: at
// blackboxRotateBytes the file becomes .1 and the oldest generation is dropped.
// A write failure is returned, never hidden: the caller reports the degraded
// screening and the tool still runs - audit mode cannot block.
func AppendBlackboxSecurityRecord(record BlackboxSecurityRecord) error {
	line, err := blackboxRecordLine(record)
	if err != nil {
		return err
	}
	path := BlackboxSecurityPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("security state dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open security stream: %w", err)
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr == nil && info.Size() >= blackboxRotateBytes {
		file.Close()
		blackboxRotate(path)
		file, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("reopen security stream: %w", err)
		}
	}
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("append security record: %w", err)
	}
	return nil
}

// blackboxRotate renames the stream to .1 and drops .2. Best effort: a failed
// rotation keeps appending to the live file, so a record is never lost for a
// rename's sake.
func blackboxRotate(path string) {
	older := path + ".2"
	if _, err := os.Stat(older); err == nil {
		_ = os.Remove(older)
	}
	if err := os.Rename(path+".1", older); err != nil && !os.IsNotExist(err) {
		return
	}
	_ = os.Rename(path, path+".1")
}

// blackboxRecordLine serializes one record under its size cap, dropping
// findings (then coverage reasons) until it fits.
func blackboxRecordLine(record BlackboxSecurityRecord) ([]byte, error) {
	if len(record.Findings) > blackboxMaxRecordFindings {
		record.Findings = record.Findings[:blackboxMaxRecordFindings]
	}
	trim := func() {
		if len(record.Findings) > 0 {
			record.Findings = record.Findings[:len(record.Findings)-1]
		}
	}
	for {
		body, err := json.Marshal(record)
		if err != nil {
			return nil, fmt.Errorf("marshal security record: %w", err)
		}
		line := append(body, '\n')
		if len(line) <= blackboxMaxRecordBytes {
			return line, nil
		}
		if len(record.Findings) > 0 {
			trim()
			continue
		}
		for i := range record.Coverage {
			record.Coverage[i].Reason = ""
		}
		body, err = json.Marshal(record)
		if err != nil {
			return nil, fmt.Errorf("marshal security record: %w", err)
		}
		if len(body)+1 > blackboxMaxRecordBytes {
			return nil, fmt.Errorf("security record exceeds %d bytes", blackboxMaxRecordBytes)
		}
		return append(body, '\n'), nil
	}
}

// ReadBlackboxSecurityRecords reads the stream for tests and reports: partial
// final lines (a crash mid-write) are counted, never guessed at.
func ReadBlackboxSecurityRecords(path string) ([]BlackboxSecurityRecord, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	records := make([]BlackboxSecurityRecord, 0, len(lines))
	partial := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record BlackboxSecurityRecord
		if json.Unmarshal([]byte(line), &record) != nil || record.Schema != 1 || record.EventID == "" {
			partial++
			continue
		}
		records = append(records, record)
	}
	return records, partial, nil
}
