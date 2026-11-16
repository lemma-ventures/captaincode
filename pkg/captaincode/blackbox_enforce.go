package captaincode

// BB5 step 1: selective enforcement. This file turns one evaluation result
// into an enforce-mode decision following the policy table in
// AGENT_BLACKBOX_INTEGRATION.md. Only exact package rules and explicit local
// path rules can block, only at the configured threshold, and only when the
// snapshot selected that category. Pattern and secret findings stay advisory.
// A required check that cannot run refuses with security_check_unavailable,
// never a claim of malicious intent. An operator override is narrow,
// time-limited and local, and a file that cannot be trusted grants nothing.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"
)

// blackboxEnforceableCategories are the categories enforcement can refuse for.
// Everything else is recorded but never blocks in this release.
var blackboxEnforceableCategories = []string{"dependency", "protected_path"}

// blackboxBlockSeverities are the finding severities eligible to refuse an
// action in enforce mode.
var blackboxBlockSeverities = map[string]bool{"critical": true}

// BlackboxEnforceDecision is the enforce-mode outcome for one screened event.
// Refuse true means the host must stop the action before any side effect.
type BlackboxEnforceDecision struct {
	Refuse         bool
	Reason         string
	Disposition    string
	OverrideRule   string
	OverrideReason string
}

// BlackboxOverride is one operator override entry. It suppresses one rule for
// one workspace until it expires; it never widens any other control.
type BlackboxOverride struct {
	RuleID      string `json:"rule_id"`
	WorkspaceID string `json:"workspace_id"`
	Expires     string `json:"expires"`
	Reason      string `json:"reason"`
}

const (
	blackboxMaxOverrideFile  = 64 * 1024
	blackboxMaxOverrides     = 32
	blackboxMaxOverrideLife  = 24 * time.Hour
	blackboxMaxOverrideCause = 256
)

var blackboxOverrideWorkspace = regexp.MustCompile(`^ws_[a-f0-9]{16}$`)

// BlackboxOverridesPath is the operator override file. A worker with the same
// user identity can edit it; that limit is documented in the plan, and the
// file still cannot grant anything beyond suppressing one recorded rule.
func BlackboxOverridesPath() string {
	if p := os.Getenv("CAPTAIN_BLACKBOX_OVERRIDES"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return home + "/.config/captain/blackbox-overrides.json"
}

// LoadBlackboxOverrides reads the override file with a guarded, no-follow,
// permission-checked read. Any violation returns no overrides and an error:
// the caller then keeps every would-block refusal, because a file that cannot
// be trusted grants nothing. Expired entries simply never match.
func LoadBlackboxOverrides(path string, now time.Time) ([]BlackboxOverride, error) {
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() == false {
		return nil, fmt.Errorf("override file is not a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("override file mode is %o, want 0600", info.Mode().Perm())
	}
	if info.Size() > blackboxMaxOverrideFile {
		return nil, fmt.Errorf("override file exceeds %d bytes", blackboxMaxOverrideFile)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var raw []BlackboxOverride
	decoder := json.NewDecoder(io.LimitReader(file, blackboxMaxOverrideFile+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("override file: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("override file: trailing JSON")
	}
	if len(raw) > blackboxMaxOverrides {
		return nil, fmt.Errorf("override file has %d entries, want at most %d", len(raw), blackboxMaxOverrides)
	}
	overrides := make([]BlackboxOverride, 0, len(raw))
	for _, entry := range raw {
		if !blackboxRuleID.MatchString(entry.RuleID) {
			return nil, fmt.Errorf("override rule id is invalid")
		}
		if !blackboxOverrideWorkspace.MatchString(entry.WorkspaceID) {
			return nil, fmt.Errorf("override workspace id is invalid")
		}
		if len(entry.Reason) > blackboxMaxOverrideCause || len(entry.Reason) == 0 {
			return nil, fmt.Errorf("override reason is missing or too long")
		}
		expires, err := time.Parse(time.RFC3339, entry.Expires)
		if err != nil {
			return nil, fmt.Errorf("override expiry: %w", err)
		}
		if expires.After(now.Add(blackboxMaxOverrideLife)) {
			return nil, fmt.Errorf("override expiry is more than %s away", blackboxMaxOverrideLife)
		}
		overrides = append(overrides, entry)
	}
	return overrides, nil
}

// blackboxOverrideActive matches one finding against the loaded overrides.
// The workspace must match exactly and the entry must not have expired.
func blackboxOverrideActive(overrides []BlackboxOverride, ruleID, workspaceID string, now time.Time) (BlackboxOverride, bool) {
	for _, entry := range overrides {
		if entry.RuleID != ruleID || entry.WorkspaceID != workspaceID {
			continue
		}
		expires, err := time.Parse(time.RFC3339, entry.Expires)
		if err != nil || !expires.After(now) {
			continue
		}
		return entry, true
	}
	return BlackboxOverride{}, false
}

// DecideBlackboxEnforcement applies the enforce-mode policy table to one
// evaluation. The decision is a pure function of the action phase, the result
// and the overrides: the same event screened twice refuses twice, so a worker
// cannot retry its way past a refusal.
func DecideBlackboxEnforcement(phase string, result BlackboxResult, overrides []BlackboxOverride, workspaceID string, now time.Time) BlackboxEnforceDecision {
	decision := BlackboxEnforceDecision{}
	if phase != "pre" {
		// What already ran cannot be un-run; a post-tool observation is
		// never prevention.
		return decision
	}
	enforceable := map[string]bool{}
	for _, category := range blackboxEnforceableCategories {
		enforceable[category] = true
	}
	// A required category is one the snapshot selected for enforcement. An
	// evaluation error means the required check cannot run: refuse as
	// unavailable, fail closed, without claiming malicious intent.
	required := map[string]bool{}
	for _, coverage := range result.Coverage {
		if !enforceable[coverage.Category] {
			continue
		}
		if coverage.Status == "covered" || coverage.Status == "incomplete" {
			required[coverage.Category] = true
		}
	}
	if result.Status == "error" {
		// The snapshot could not be read or the evaluator failed. The
		// operator selected enforce mode, so the check is required and
		// unavailable.
		decision.Refuse = true
		decision.Reason = "security_check_unavailable"
		decision.Disposition = "security_check_unavailable"
		return decision
	}
	for _, coverage := range result.Coverage {
		if required[coverage.Category] && coverage.Status == "incomplete" {
			decision.Refuse = true
			decision.Reason = "security_check_unavailable"
			decision.Disposition = "security_check_unavailable"
			return decision
		}
	}
	// Eligible matches: critical findings from the enforceable categories.
	// Advisory findings are recorded only and cannot authorize a block.
	for _, finding := range result.Findings {
		if !enforceable[finding.Category] || !blackboxBlockSeverities[finding.Severity] {
			continue
		}
		if !required[finding.Category] {
			continue
		}
		if entry, active := blackboxOverrideActive(overrides, finding.RuleID, workspaceID, now); active {
			decision.Disposition = "overridden"
			decision.OverrideRule = entry.RuleID
			decision.OverrideReason = entry.Reason
			continue
		}
		decision.Refuse = true
		decision.Reason = "blackbox_rule_match:" + finding.RuleID
		decision.Disposition = "blocked"
		return decision
	}
	return decision
}

// ApplyBlackboxEnforcement rewrites one record's disposition with the
// enforce-mode decision. Audit records keep their own dispositions
// (would_block, evaluated, degraded); enforce records say what happened:
// blocked, security_check_unavailable, overridden, or the audit value when
// the decision changed nothing.
func ApplyBlackboxEnforcement(record *BlackboxSecurityRecord, decision BlackboxEnforceDecision) {
	if decision.Disposition != "" {
		record.Disposition = decision.Disposition
	}
	if decision.Reason != "" {
		record.Reasons = append(record.Reasons, decision.Reason)
	}
	if decision.OverrideRule != "" {
		record.Reasons = append(record.Reasons, "override:"+decision.OverrideRule)
	}
}
