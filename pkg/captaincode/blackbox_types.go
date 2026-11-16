package captaincode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

const (
	BlackboxMaxActionBytes   = 1 << 20
	BlackboxMaxSnapshotBytes = 4 << 20
	blackboxMaxRules         = 1024
	blackboxMaxTargets       = 256
	blackboxMaxText          = 256 << 10
)

type BlackboxPackage struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
}

type BlackboxAction struct {
	Schema        int               `json:"schema"`
	ID            string            `json:"action_id,omitempty"`
	Host          string            `json:"host"`
	Phase         string            `json:"phase"`
	Tool          string            `json:"tool"`
	Kind          string            `json:"kind"`
	WorkspaceID   string            `json:"workspace_id,omitempty"`
	SessionID     string            `json:"session_id,omitempty"`
	TaskID        string            `json:"task_id,omitempty"`
	RunID         string            `json:"run_id,omitempty"`
	ToolCallID    string            `json:"tool_call_id,omitempty"`
	Command       string            `json:"command,omitempty"`
	Paths         []string          `json:"paths,omitempty"`
	URLs          []string          `json:"urls,omitempty"`
	Packages      []BlackboxPackage `json:"packages,omitempty"`
	Text          string            `json:"text,omitempty"`
	Origin        string            `json:"origin,omitempty"`
	ParseStatus   string            `json:"parse_status"`
	OmittedFields []string          `json:"omitted_fields,omitempty"`
	Truncated     bool              `json:"truncated,omitempty"`
}

type BlackboxPackageRule struct {
	ID        string `json:"id"`
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Severity  string `json:"severity"`
}

type BlackboxPathRule struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
}

type BlackboxPatternRule struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
}

type BlackboxSnapshot struct {
	Schema     int                   `json:"schema"`
	ID         string                `json:"id"`
	Source     string                `json:"source"`
	Revision   string                `json:"revision"`
	Categories map[string]bool       `json:"categories"`
	Packages   []BlackboxPackageRule `json:"packages,omitempty"`
	Paths      []BlackboxPathRule    `json:"paths,omitempty"`
	Patterns   []BlackboxPatternRule `json:"patterns,omitempty"`
	Digest     string                `json:"-"`
	compiled   []*regexp.Regexp
}

type BlackboxFinding struct {
	Category string `json:"category"`
	RuleID   string `json:"rule_id"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
}

type BlackboxCoverage struct {
	Category string `json:"category"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

type BlackboxResult struct {
	Schema         int                `json:"schema"`
	SnapshotID     string             `json:"snapshot_id"`
	SnapshotDigest string             `json:"snapshot_digest"`
	Status         string             `json:"status"`
	Findings       []BlackboxFinding  `json:"findings"`
	Coverage       []BlackboxCoverage `json:"coverage"`
	Errors         []string           `json:"errors,omitempty"`
}

var blackboxCategories = []string{"dependency", "protected_path", "secret_exposure", "pattern"}
var blackboxRuleID = regexp.MustCompile(`^[A-Za-z0-9_./@:-]{1,128}$`)
var blackboxFindingLabel = regexp.MustCompile(`^[a-z0-9_.:-]{1,64}$`)
var blackboxPackageName = regexp.MustCompile(`^[A-Za-z0-9@_./+:-]{1,256}$`)
var blackboxPackageVersion = regexp.MustCompile(`^(\*|[0-9A-Za-z.+_-]{1,128})$`)

func decodeBlackboxJSON(reader io.Reader, limit int, target any) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read input")
	}
	if len(body) > limit {
		return nil, fmt.Errorf("input exceeds %d bytes", limit)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing JSON")
	}
	return body, nil
}

func DecodeBlackboxAction(reader io.Reader) (BlackboxAction, error) {
	var action BlackboxAction
	_, err := decodeBlackboxJSON(reader, BlackboxMaxActionBytes, &action)
	if err != nil {
		return action, err
	}
	return action, validateBlackboxAction(action)
}

func DecodeBlackboxSnapshot(reader io.Reader) (BlackboxSnapshot, error) {
	var snapshot BlackboxSnapshot
	body, err := decodeBlackboxJSON(reader, BlackboxMaxSnapshotBytes, &snapshot)
	if err != nil {
		return snapshot, err
	}
	if err := validateBlackboxSnapshot(&snapshot, "synthetic"); err != nil {
		return snapshot, err
	}
	digest := sha256.Sum256(body)
	snapshot.Digest = hex.EncodeToString(digest[:])
	return snapshot, nil
}

func validateBlackboxAction(action BlackboxAction) error {
	if action.Schema != 1 || action.Tool == "" || action.Kind == "" {
		return fmt.Errorf("action schema, tool and kind are required")
	}
	switch action.Kind {
	case "file", "shell", "fetch", "unknown":
	default:
		return fmt.Errorf("action kind is unsupported")
	}
	if action.Phase != "pre" && action.Phase != "post" {
		return fmt.Errorf("action phase must be pre or post")
	}
	if len(action.Command) > blackboxMaxText || len(action.Text) > blackboxMaxText {
		return fmt.Errorf("action text exceeds its limit")
	}
	if len(action.Paths) > blackboxMaxTargets || len(action.URLs) > blackboxMaxTargets || len(action.Packages) > blackboxMaxTargets {
		return fmt.Errorf("action target count exceeds its limit")
	}
	for _, candidate := range action.Packages {
		if !validBlackboxPackage(candidate.Ecosystem, candidate.Name, candidate.Version, true) {
			return fmt.Errorf("action package candidate is invalid")
		}
	}
	return nil
}

func validateBlackboxSnapshot(snapshot *BlackboxSnapshot, allowedSources ...string) error {
	if snapshot.Schema != 1 || !blackboxRuleID.MatchString(snapshot.ID) || snapshot.Revision == "" {
		return fmt.Errorf("snapshot schema, source, id and revision are required")
	}
	if !blackboxSourceAllowed(snapshot.Source, allowedSources) {
		return fmt.Errorf("snapshot source %q is not accepted here", snapshot.Source)
	}
	if len(snapshot.Categories) != len(blackboxCategories) {
		return fmt.Errorf("snapshot must declare every coverage category")
	}
	for _, category := range blackboxCategories {
		if _, ok := snapshot.Categories[category]; !ok {
			return fmt.Errorf("snapshot has an unknown coverage category")
		}
	}
	if len(snapshot.Packages)+len(snapshot.Paths)+len(snapshot.Patterns) > blackboxMaxRules {
		return fmt.Errorf("snapshot rule count exceeds its limit")
	}
	seen := map[string]bool{}
	for _, rule := range snapshot.Packages {
		if !validBlackboxRule(rule.ID, rule.Kind, rule.Severity, seen) || !validBlackboxPackage(rule.Ecosystem, rule.Name, rule.Version, false) {
			return fmt.Errorf("package rule is invalid")
		}
	}
	for _, rule := range snapshot.Paths {
		if !validBlackboxRule(rule.ID, rule.Kind, rule.Severity, seen) || len(rule.Path) == 0 || len(rule.Path) > 4096 || strings.ContainsRune(rule.Path, '\x00') {
			return fmt.Errorf("path rule is invalid")
		}
		if _, err := path.Match(rule.Path, "fixture"); err != nil {
			return fmt.Errorf("path rule pattern is invalid")
		}
	}
	snapshot.compiled = make([]*regexp.Regexp, 0, len(snapshot.Patterns))
	for _, rule := range snapshot.Patterns {
		if !validBlackboxRule(rule.ID, rule.Kind, rule.Severity, seen) || rule.Category != "pattern" || len(rule.Pattern) == 0 || len(rule.Pattern) > 1024 {
			return fmt.Errorf("pattern rule is invalid")
		}
		compiled, err := compileBlackboxPattern(rule.Pattern)
		if err != nil {
			return fmt.Errorf("unsupported pattern for rule %s", rule.ID)
		}
		snapshot.compiled = append(snapshot.compiled, compiled)
	}
	return nil
}

func blackboxSourceAllowed(source string, allowedSources []string) bool {
	for _, allowed := range allowedSources {
		if source == allowed {
			return true
		}
	}
	return false
}

func compileBlackboxPattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(pattern)
}

func validBlackboxRule(id, kind, severity string, seen map[string]bool) bool {
	if !blackboxRuleID.MatchString(id) || seen[id] || !blackboxFindingLabel.MatchString(kind) || !blackboxFindingLabel.MatchString(severity) {
		return false
	}
	seen[id] = true
	return true
}

func validBlackboxPackage(ecosystem, name, version string, optionalVersion bool) bool {
	switch ecosystem {
	case "npm", "pypi", "cargo", "rubygems", "homebrew":
	default:
		return false
	}
	if !blackboxPackageName.MatchString(name) || strings.Contains(name, "..") {
		return false
	}
	if optionalVersion && version == "" {
		return true
	}
	return blackboxPackageVersion.MatchString(version)
}
