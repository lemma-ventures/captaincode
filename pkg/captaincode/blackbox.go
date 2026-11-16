package captaincode

import (
	"context"
	"path"
	"sort"
	"strings"
)

func EvaluateBlackbox(ctx context.Context, action BlackboxAction, snapshot BlackboxSnapshot) BlackboxResult {
	result := BlackboxResult{Schema: 1, SnapshotID: snapshot.ID, SnapshotDigest: snapshot.Digest, Status: "complete", Findings: []BlackboxFinding{}, Coverage: []BlackboxCoverage{}}
	if err := ctx.Err(); err != nil {
		result.Status = "error"
		result.Errors = []string{"evaluation_cancelled"}
	}
	if err := validateBlackboxAction(action); err != nil {
		result.Status = "error"
		result.Errors = []string{"invalid_action"}
	}
	incomplete := action.Kind == "unknown" || action.ParseStatus != "parsed" || action.Truncated || len(action.OmittedFields) != 0
	for _, category := range blackboxCategories {
		coverage := BlackboxCoverage{Category: category, Status: "covered"}
		enabled, declared := snapshot.Categories[category]
		if !declared || !enabled {
			coverage.Status = "not_covered"
			coverage.Reason = "category_not_in_snapshot"
		} else if category != "secret_exposure" && blackboxSnapshotRuleCount(snapshot, category) == 0 {
			coverage.Status = "incomplete"
			coverage.Reason = "no_rules"
		} else if incomplete {
			coverage.Status = "incomplete"
			coverage.Reason = "action_input_incomplete"
		}
		if coverage.Status == "incomplete" && result.Status == "complete" {
			// A category the snapshot disables is a signed scope
			// decision, not a degradation: the screening is complete
			// for what the snapshot selected.
			result.Status = "incomplete"
		}
		result.Coverage = append(result.Coverage, coverage)
	}
	if result.Status == "error" {
		return result
	}
	if len(snapshot.compiled) != len(snapshot.Patterns) {
		snapshot.compiled = nil
		for _, rule := range snapshot.Patterns {
			compiled, err := compileBlackboxPattern(rule.Pattern)
			if err != nil {
				result.Status = "error"
				result.Errors = []string{"unsupported_pattern"}
				return result
			}
			snapshot.compiled = append(snapshot.compiled, compiled)
		}
	}
	if snapshot.Categories["dependency"] {
		for _, candidate := range action.Packages {
			for _, rule := range snapshot.Packages {
				if ctx.Err() != nil {
					result.Status = "error"
					result.Errors = []string{"evaluation_cancelled"}
					return result
				}
				if candidate.Ecosystem == rule.Ecosystem && candidate.Name == rule.Name && (rule.Version == "*" || candidate.Version == rule.Version) {
					result.Findings = append(result.Findings, BlackboxFinding{Category: "dependency", RuleID: rule.ID, Kind: rule.Kind, Severity: rule.Severity, Reason: "exact_package_match"})
				}
			}
		}
	}
	if snapshot.Categories["protected_path"] {
		for _, target := range action.Paths {
			cleaned := path.Clean(strings.ReplaceAll(target, "\\", "/"))
			for _, rule := range snapshot.Paths {
				if ctx.Err() != nil {
					result.Status = "error"
					result.Errors = []string{"evaluation_cancelled"}
					return result
				}
				matched := blackboxPathMatch(rule.Path, cleaned)
				if matched {
					result.Findings = append(result.Findings, BlackboxFinding{Category: "protected_path", RuleID: rule.ID, Kind: rule.Kind, Severity: rule.Severity, Reason: "protected_path_match"})
				}
			}
		}
	}
	for _, text := range blackboxActionText(action) {
		if snapshot.Categories["secret_exposure"] {
			for _, pattern := range secretPatterns {
				if ctx.Err() != nil {
					result.Status = "error"
					result.Errors = []string{"evaluation_cancelled"}
					return result
				}
				if pattern.re.FindStringIndex(text) != nil {
					result.Findings = append(result.Findings, BlackboxFinding{Category: "secret_exposure", RuleID: "builtin:" + pattern.kind, Kind: "secret", Severity: "warning", Reason: "secret_exposure"})
				}
			}
		}
		if snapshot.Categories["pattern"] {
			for index, compiled := range snapshot.compiled {
				if compiled.FindStringIndex(text) != nil {
					rule := snapshot.Patterns[index]
					result.Findings = append(result.Findings, BlackboxFinding{Category: rule.Category, RuleID: rule.ID, Kind: rule.Kind, Severity: rule.Severity, Reason: "pattern_match"})
				}
			}
		}
	}
	sort.Slice(result.Findings, func(i, j int) bool {
		left, right := result.Findings[i], result.Findings[j]
		if left.Category != right.Category {
			return left.Category < right.Category
		}
		if left.RuleID != right.RuleID {
			return left.RuleID < right.RuleID
		}
		return left.Kind < right.Kind
	})
	result.Findings = uniqueBlackboxFindings(result.Findings)
	if len(result.Findings) > blackboxMaxRules {
		result.Findings = result.Findings[:blackboxMaxRules]
		result.Status = "incomplete"
		result.Errors = append(result.Errors, "finding_limit")
	}
	return result
}

func blackboxPathMatch(pattern, target string) bool {
	if prefix, found := strings.CutSuffix(pattern, "/**"); found {
		return target == prefix || strings.HasPrefix(target, prefix+"/")
	}
	matched, err := path.Match(pattern, target)
	return err == nil && matched
}

func blackboxSnapshotRuleCount(snapshot BlackboxSnapshot, category string) int {
	switch category {
	case "dependency":
		return len(snapshot.Packages)
	case "protected_path":
		return len(snapshot.Paths)
	case "pattern":
		return len(snapshot.Patterns)
	}
	return 0
}

func blackboxActionText(action BlackboxAction) []string {
	text := make([]string, 0, len(action.URLs)+2)
	if action.Command != "" {
		text = append(text, action.Command)
	}
	if action.Text != "" {
		text = append(text, action.Text)
	}
	text = append(text, action.URLs...)
	return text
}

func uniqueBlackboxFindings(findings []BlackboxFinding) []BlackboxFinding {
	if len(findings) < 2 {
		return findings
	}
	result := findings[:1]
	for _, finding := range findings[1:] {
		if finding != result[len(result)-1] {
			result = append(result, finding)
		}
	}
	return result
}
