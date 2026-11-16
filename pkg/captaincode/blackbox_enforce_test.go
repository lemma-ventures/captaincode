package captaincode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func enforceResult(status string, coverage []BlackboxCoverage, findings ...BlackboxFinding) BlackboxResult {
	if coverage == nil {
		coverage = []BlackboxCoverage{
			{Category: "dependency", Status: "covered"},
			{Category: "protected_path", Status: "covered"},
			{Category: "secret_exposure", Status: "covered"},
			{Category: "pattern", Status: "covered"},
		}
	}
	return BlackboxResult{Schema: 1, Status: status, Findings: findings, Coverage: coverage}
}

func TestDecideBlackboxEnforcementTable(t *testing.T) {
	now := time.Now()
	criticalPackage := BlackboxFinding{Category: "dependency", RuleID: "pkg-1", Kind: "known_malware", Severity: "critical", Reason: "exact_package_match"}
	cases := []struct {
		name       string
		phase      string
		result     BlackboxResult
		wantRefuse bool
		wantReason string
		wantDisp   string
	}{
		{"eligible critical match refuses", "pre", enforceResult("complete", nil, criticalPackage), true, "blackbox_rule_match:pkg-1", "blocked"},
		{"post never refuses", "post", enforceResult("complete", nil, criticalPackage), false, "", ""},
		{"warning severity records only", "pre", enforceResult("complete", nil, BlackboxFinding{Category: "dependency", RuleID: "pkg-2", Severity: "warning"}), false, "", ""},
		{"pattern findings never refuse", "pre", enforceResult("complete", nil, BlackboxFinding{Category: "pattern", RuleID: "pat-1", Severity: "critical"}), false, "", ""},
		{"secret findings never refuse", "pre", enforceResult("complete", nil, BlackboxFinding{Category: "secret_exposure", RuleID: "sec-1", Severity: "critical"}), false, "", ""},
		{"error result is unavailable", "pre", enforceResult("error", nil), true, "security_check_unavailable", "security_check_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := DecideBlackboxEnforcement(tc.phase, tc.result, nil, "workspace", now)
			if decision.Refuse != tc.wantRefuse || decision.Reason != tc.wantReason || decision.Disposition != tc.wantDisp {
				t.Fatalf("decision = %+v, want refuse=%v reason=%q disposition=%q", decision, tc.wantRefuse, tc.wantReason, tc.wantDisp)
			}
		})
	}
}

func TestDecideBlackboxEnforcementIncompleteRequiredCategory(t *testing.T) {
	now := time.Now()
	// The snapshot selected dependency but its evaluation was incomplete:
	// the required check did not finish, so the action refuses as
	// unavailable. An incomplete advisory category alone does not refuse.
	coverage := []BlackboxCoverage{
		{Category: "dependency", Status: "incomplete", Reason: "action_input_incomplete"},
		{Category: "protected_path", Status: "covered"},
		{Category: "secret_exposure", Status: "incomplete", Reason: "action_input_incomplete"},
		{Category: "pattern", Status: "covered"},
	}
	decision := DecideBlackboxEnforcement("pre", enforceResult("incomplete", coverage), nil, "workspace", now)
	if !decision.Refuse || decision.Reason != "security_check_unavailable" {
		t.Fatalf("decision = %+v", decision)
	}
	advisoryOnly := []BlackboxCoverage{
		{Category: "dependency", Status: "not_covered", Reason: "category_not_in_snapshot"},
		{Category: "protected_path", Status: "not_covered", Reason: "category_not_in_snapshot"},
		{Category: "secret_exposure", Status: "incomplete", Reason: "action_input_incomplete"},
		{Category: "pattern", Status: "covered"},
	}
	decision = DecideBlackboxEnforcement("pre", enforceResult("incomplete", advisoryOnly), nil, "workspace", now)
	if decision.Refuse {
		t.Fatalf("decision = %+v - no required category means no refusal", decision)
	}
}

func TestBlackboxEnforcementShellGapScopesRefusalToDependency(t *testing.T) {
	now := time.Now()
	action, _ := NormalizeBlackboxAction("claude", "pre", "bash", json.RawMessage(`{"command":"echo \"hi\" | grep foo"}`))
	if action.ParseStatus != "parsed" || len(action.OmittedFields) != 1 {
		t.Fatalf("action = %+v", action)
	}
	// A pilot-shaped snapshot disables the enforceable categories: the
	// quoted command screens clean instead of degrading every check.
	pilot := blackboxFixtureSnapshot()
	pilot.Categories["dependency"] = false
	pilot.Categories["protected_path"] = false
	pilot.Packages, pilot.Paths = nil, nil
	decision := DecideBlackboxEnforcement("pre", EvaluateBlackbox(context.Background(), action, pilot), nil, "workspace", now)
	if decision.Refuse {
		t.Fatalf("decision = %+v - no required category lost its input", decision)
	}
	// With dependency selected, the same command cannot run the required
	// exact-package check, so it refuses as unavailable: fail closed.
	enabled := blackboxFixtureSnapshot()
	result := EvaluateBlackbox(context.Background(), action, enabled)
	if result.Status != "incomplete" {
		t.Fatalf("status = %s", result.Status)
	}
	decision = DecideBlackboxEnforcement("pre", result, nil, "workspace", now)
	if !decision.Refuse || decision.Reason != "security_check_unavailable" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestDecideBlackboxEnforcementOverrideRules(t *testing.T) {
	now := time.Now()
	finding := BlackboxFinding{Category: "dependency", RuleID: "pkg-1", Severity: "critical"}
	active := []BlackboxOverride{{RuleID: "pkg-1", WorkspaceID: "workspace", Expires: now.Add(time.Hour).UTC().Format(time.RFC3339), Reason: "reviewed"}}
	decision := DecideBlackboxEnforcement("pre", enforceResult("complete", nil, finding), active, "workspace", now)
	if decision.Refuse || decision.Disposition != "overridden" || decision.OverrideRule != "pkg-1" {
		t.Fatalf("decision = %+v - an active override suppresses the refusal", decision)
	}
	expired := []BlackboxOverride{{RuleID: "pkg-1", WorkspaceID: "workspace", Expires: now.Add(-time.Minute).UTC().Format(time.RFC3339), Reason: "reviewed"}}
	decision = DecideBlackboxEnforcement("pre", enforceResult("complete", nil, finding), expired, "workspace", now)
	if !decision.Refuse || decision.Disposition != "blocked" {
		t.Fatalf("decision = %+v - an expired override must not suppress", decision)
	}
	// A different finding after an override continues scanning, and a
	// second eligible finding still refuses.
	two := []BlackboxFinding{finding, {Category: "protected_path", RuleID: "path-1", Severity: "critical"}}
	decision = DecideBlackboxEnforcement("pre", enforceResult("complete", nil, two...), active, "workspace", now)
	if !decision.Refuse || decision.Reason != "blackbox_rule_match:path-1" {
		t.Fatalf("decision = %+v - an override on one rule must not cover another", decision)
	}
}

func TestLoadBlackboxOverridesGuarded(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	valid := func(body string, mode os.FileMode) string {
		p := filepath.Join(dir, "overrides.json")
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		// WriteFile keeps an existing file's mode: set it explicitly.
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	expires := now.Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := LoadBlackboxOverrides(valid(`[{"rule_id":"pkg-1","workspace_id":"`+BlackboxWorkspaceID("/x")+`","expires":"`+expires+`","reason":"ok"}]`, 0o600), now); err != nil {
		t.Fatalf("valid file: %v", err)
	}
	if _, err := LoadBlackboxOverrides(filepath.Join(dir, "absent.json"), now); err != nil {
		t.Fatalf("absent file: %v", err)
	}
	for name, tc := range map[string]struct {
		body string
		mode os.FileMode
	}{
		"wrong mode":  {`[]`, 0o644},
		"bad json":    {`{`, 0o600},
		"trailing":    {`[] extra`, 0o600},
		"unknown key": {`[{"rule_id":"a","workspace_id":"` + BlackboxWorkspaceID("/x") + `","expires":"` + expires + `","reason":"r","extra":1}]`, 0o600},
		"far expiry":  {`[{"rule_id":"a","workspace_id":"` + BlackboxWorkspaceID("/x") + `","expires":"` + now.Add(48*time.Hour).UTC().Format(time.RFC3339) + `","reason":"r"}]`, 0o600},
		"no reason":   {`[{"rule_id":"a","workspace_id":"` + BlackboxWorkspaceID("/x") + `","expires":"` + expires + `","reason":""}]`, 0o600},
		"bad rule id": {`[{"rule_id":"has space","workspace_id":"` + BlackboxWorkspaceID("/x") + `","expires":"` + expires + `","reason":"r"}]`, 0o600},
	} {
		if _, err := LoadBlackboxOverrides(valid(tc.body, tc.mode), now); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	// A symlink never grants: it is rejected, not followed.
	target := filepath.Join(dir, "target.json")
	os.WriteFile(target, []byte(`[]`), 0o600)
	link := filepath.Join(dir, "link.json")
	os.Symlink(target, link)
	if _, err := LoadBlackboxOverrides(link, now); err == nil {
		t.Fatal("a symlinked override file must be rejected")
	}
}

func BenchmarkDecideBlackboxEnforcement(b *testing.B) {
	now := time.Now()
	overrides := []BlackboxOverride{{RuleID: "other", WorkspaceID: "workspace", Expires: now.Add(time.Hour).UTC().Format(time.RFC3339), Reason: "r"}}
	findings := make([]BlackboxFinding, 0, 8)
	for i := 0; i < 8; i++ {
		findings = append(findings, BlackboxFinding{Category: "pattern", RuleID: "pat", Severity: "warning"})
	}
	result := enforceResult("complete", nil, findings...)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if decision := DecideBlackboxEnforcement("pre", result, overrides, "workspace", now); decision.Refuse {
			b.Fatal("unexpected refusal")
		}
	}
}
