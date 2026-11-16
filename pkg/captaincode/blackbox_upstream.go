package captaincode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const BlackboxUpstreamMaxCacheBytes = 8 << 20

func BlackboxImportEcosystems() []string {
	return []string{"npm", "pypi", "cargo", "rubygems", "homebrew"}
}

type BlackboxImportOptions struct {
	SnapshotID string
	Revision   string
	Ecosystems []string
	MaxRules   int
	// LocalPaths is the operator's local protected-path policy. Upstream has
	// no path rules, so this input is what enables the protected_path
	// category. It is validated with the snapshot's own path rule checks.
	LocalPaths []BlackboxPathRule
}

type BlackboxUpstreamImport struct {
	Snapshot           BlackboxSnapshot
	CompleteCategories []string
	Omitted            []string
	Revocations        []string
	Graph              string
	SyncedAt           time.Time
}

type blackboxUpstreamCache struct {
	SyncedAt             float64                                   `json:"synced_at"`
	RefreshDueAt         float64                                   `json:"refresh_due_at"`
	ContextGraphID       string                                    `json:"context_graph_id"`
	Injection            []blackboxUpstreamInjectionRule           `json:"injection"`
	Escalation           []json.RawMessage                         `json:"escalation"`
	Dependency           map[string]blackboxUpstreamDependencyRule `json:"dependency"`
	Fileaccess           []json.RawMessage                         `json:"fileaccess"`
	Skill                []json.RawMessage                         `json:"skill"`
	IOC                  map[string]json.RawMessage                `json:"ioc"`
	GraphThreats         []json.RawMessage                         `json:"graph_threats"`
	Community            map[string]json.RawMessage                `json:"community"`
	CommunityPaused      bool                                      `json:"community_paused"`
	CommunityFingerprint string                                    `json:"community_fingerprint"`
	CommunityEmptySince  float64                                   `json:"community_empty_since"`
	KillList             map[string]json.RawMessage                `json:"kill_list"`
	KillListRefused      json.RawMessage                           `json:"kill_list_refused"`
	CuratorManifestState string                                    `json:"curator_manifest_state"`
	VerifiedScope        json.RawMessage                           `json:"verified_scope"`
	CuratorRevoked       []string                                  `json:"curator_revoked"`
}

type blackboxUpstreamDependencyRule struct {
	Identifier     string  `json:"identifier"`
	Subject        string  `json:"subject"`
	Description    string  `json:"description"`
	Severity       string  `json:"severity"`
	Name           string  `json:"name"`
	Source         string  `json:"source"`
	Ecosystem      string  `json:"ecosystem"`
	PackageName    string  `json:"packageName"`
	PackageVersion string  `json:"packageVersion"`
	AdvisoryID     *string `json:"advisoryId"`
	Kind           *string `json:"kind"`
	AssetTier      string  `json:"asset_tier"`
}

type blackboxUpstreamInjectionRule struct {
	Identifier  string `json:"identifier"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	PatternSrc  string `json:"pattern_src"`
}

type blackboxUpstreamVerifiedScope struct {
	ContextGraphID     string                     `json:"context_graph_id"`
	StoreURL           string                     `json:"store_url"`
	AssertionGraphs    []string                   `json:"assertion_graphs"`
	SuppressedSubjects []string                   `json:"suppressed_subjects"`
	RevokedIdentifiers []string                   `json:"revoked_identifiers"`
	PackageAliases     map[string][]string        `json:"package_aliases"`
	VerifiedCounts     map[string]int             `json:"verified_counts"`
	Fallback           string                     `json:"fallback"`
	AssetFacts         map[string]json.RawMessage `json:"asset_facts"`
	LegacyRefused      string                     `json:"legacy_refused"`
	BuiltAt            float64                    `json:"built_at"`
}

func ImportBlackboxUpstreamCache(reader io.Reader, options BlackboxImportOptions) (BlackboxUpstreamImport, error) {
	snapshotID := strings.TrimSpace(options.SnapshotID)
	revision := strings.TrimSpace(options.Revision)
	if snapshotID == "" || revision == "" {
		return BlackboxUpstreamImport{}, fmt.Errorf("import requires a snapshot id and an upstream revision")
	}
	body, err := io.ReadAll(io.LimitReader(reader, BlackboxUpstreamMaxCacheBytes+1))
	if err != nil {
		return BlackboxUpstreamImport{}, fmt.Errorf("read upstream cache: %w", err)
	}
	if len(body) > BlackboxUpstreamMaxCacheBytes {
		return BlackboxUpstreamImport{}, fmt.Errorf("upstream cache exceeds %d bytes", BlackboxUpstreamMaxCacheBytes)
	}
	var cache blackboxUpstreamCache
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cache); err != nil {
		return BlackboxUpstreamImport{}, fmt.Errorf("decode upstream cache: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return BlackboxUpstreamImport{}, fmt.Errorf("decode upstream cache: trailing data")
	}
	graph := strings.TrimSpace(cache.ContextGraphID)
	if graph == "" {
		return BlackboxUpstreamImport{}, fmt.Errorf("upstream cache carries no context graph id")
	}
	scope, err := decodeBlackboxUpstreamScope(cache.VerifiedScope)
	if err != nil {
		return BlackboxUpstreamImport{}, err
	}
	maxRules := options.MaxRules
	if maxRules <= 0 {
		maxRules = blackboxMaxRules
	}
	ecosystems := map[string]bool{}
	selected := options.Ecosystems
	if len(selected) == 0 {
		selected = BlackboxImportEcosystems()
	}
	for _, ecosystem := range selected {
		ecosystems[strings.ToLower(strings.TrimSpace(ecosystem))] = true
	}
	revoked := map[string]bool{}
	for _, id := range append(append([]string{}, cache.CuratorRevoked...), scope.RevokedIdentifiers...) {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			revoked[trimmed] = true
		}
	}
	omittedSet := map[string]bool{}
	omitted := []string{}
	record := func(entry string) {
		if !omittedSet[entry] {
			omittedSet[entry] = true
			omitted = append(omitted, entry)
		}
	}
	for _, source := range []struct {
		name    string
		omitted bool
	}{
		{name: "ioc", omitted: len(cache.IOC) > 0},
		{name: "fileaccess", omitted: len(cache.Fileaccess) > 0},
		{name: "skill", omitted: len(cache.Skill) > 0},
		{name: "escalation", omitted: len(cache.Escalation) > 0},
		{name: "community", omitted: len(cache.Community) > 0},
		{name: "graph_threats", omitted: len(cache.GraphThreats) > 0},
		{name: "kill_list", omitted: len(cache.KillList) > 0},
	} {
		if source.omitted {
			record(source.name)
		}
	}
	packages := []BlackboxPackageRule{}
	seen := map[string]bool{}
	keys := make([]string, 0, len(cache.Dependency))
	for key := range cache.Dependency {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		row := cache.Dependency[key]
		identifier := strings.TrimSpace(row.Identifier)
		if identifier == "" {
			identifier = "dep:" + key
		}
		if revoked[identifier] {
			continue
		}
		if seen[identifier] {
			record(blackboxOmittedIdentifier("dependency:", identifier))
			continue
		}
		if !blackboxImportedDependencyReady(row, identifier, ecosystems) {
			record(blackboxOmittedIdentifier("dependency:", identifier))
			continue
		}
		seen[identifier] = true
		packages = append(packages, BlackboxPackageRule{
			ID:        identifier,
			Ecosystem: strings.ToLower(row.Ecosystem),
			Name:      row.PackageName,
			Version:   row.PackageVersion,
			Kind:      strings.TrimSpace(*row.Kind),
			Severity:  strings.TrimSpace(row.Severity),
		})
	}
	patterns := []BlackboxPatternRule{}
	for _, row := range cache.Injection {
		identifier := strings.TrimSpace(row.Identifier)
		if identifier == "" {
			identifier = strings.TrimSpace(row.Name)
		}
		if identifier == "" {
			identifier = "unnamed-injection"
		}
		if revoked[identifier] {
			continue
		}
		_, patternErr := compileBlackboxPattern(row.PatternSrc)
		ready := row.Source == "public" &&
			strings.HasPrefix(identifier, "injection:") &&
			blackboxRuleID.MatchString(identifier) &&
			blackboxFindingLabel.MatchString(strings.TrimSpace(row.Severity)) &&
			patternErr == nil
		if seen[identifier] {
			ready = false
		}
		if !ready {
			record(blackboxOmittedIdentifier("pattern:", identifier))
			continue
		}
		seen[identifier] = true
		patterns = append(patterns, BlackboxPatternRule{
			ID:       identifier,
			Category: "pattern",
			Pattern:  row.PatternSrc,
			Kind:     "heuristic",
			Severity: strings.TrimSpace(row.Severity),
		})
	}
	if len(packages)+len(patterns)+len(options.LocalPaths) > maxRules {
		return BlackboxUpstreamImport{}, fmt.Errorf("import would carry more than %d rules; narrow the import scope", maxRules)
	}
	// The local policy is the only source of path rules: with no rules the
	// category stays disabled (scope, not an omission), and with rules it is
	// selected for every screening.
	paths := append([]BlackboxPathRule{}, options.LocalPaths...)
	categories := map[string]bool{
		"dependency":      len(packages) > 0,
		"protected_path":  len(paths) > 0,
		"secret_exposure": true,
		"pattern":         len(patterns) > 0,
	}
	snapshot := BlackboxSnapshot{
		Schema:     1,
		ID:         snapshotID,
		Source:     BlackboxExportSource,
		Revision:   revision,
		Categories: categories,
		Packages:   packages,
		Paths:      paths,
		Patterns:   patterns,
	}
	if err := validateBlackboxSnapshot(&snapshot, BlackboxExportSource); err != nil {
		return BlackboxUpstreamImport{}, fmt.Errorf("imported rules are invalid: %w", err)
	}
	complete := []string{}
	if categories["dependency"] && !blackboxOmitsPrefix(omitted, "dependency:") {
		complete = append(complete, "dependency")
	}
	if categories["pattern"] && !blackboxOmitsPrefix(omitted, "pattern:") {
		complete = append(complete, "pattern")
	}
	// The local policy is complete only when nothing path-shaped was dropped:
	// an omitted `fileaccess` source has no schema 1 mapping, so the claim is
	// withheld for that refresh instead of overstating coverage.
	if categories["protected_path"] && !blackboxOmissionDropsPathMaterial(omitted) {
		complete = append(complete, "protected_path")
	}
	complete = append(complete, "secret_exposure")
	revocations := make([]string, 0, len(revoked))
	for id := range revoked {
		revocations = append(revocations, id)
	}
	sort.Strings(revocations)
	sort.Strings(omitted)
	return BlackboxUpstreamImport{
		Snapshot:           snapshot,
		CompleteCategories: complete,
		Omitted:            omitted,
		Revocations:        revocations,
		Graph:              graph,
		SyncedAt:           blackboxEpochSeconds(cache.SyncedAt),
	}, nil
}

func blackboxImportedDependencyReady(row blackboxUpstreamDependencyRule, identifier string, ecosystems map[string]bool) bool {
	if row.Kind == nil || strings.TrimSpace(*row.Kind) == "" {
		return false
	}
	if row.Source != "public" {
		return false
	}
	if !ecosystems[strings.ToLower(row.Ecosystem)] {
		return false
	}
	if !blackboxRuleID.MatchString(identifier) ||
		!blackboxFindingLabel.MatchString(strings.TrimSpace(*row.Kind)) ||
		!blackboxFindingLabel.MatchString(strings.TrimSpace(row.Severity)) {
		return false
	}
	return validBlackboxPackage(row.Ecosystem, row.PackageName, row.PackageVersion, false)
}

func blackboxOmittedIdentifier(prefix, identifier string) string {
	if blackboxRuleID.MatchString(identifier) {
		return prefix + identifier
	}
	return prefix + "unsafe-id"
}

func blackboxOmitsPrefix(omitted []string, prefix string) bool {
	for _, entry := range omitted {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func decodeBlackboxUpstreamScope(raw json.RawMessage) (blackboxUpstreamVerifiedScope, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return blackboxUpstreamVerifiedScope{}, nil
	}
	var scope blackboxUpstreamVerifiedScope
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scope); err != nil {
		return blackboxUpstreamVerifiedScope{}, fmt.Errorf("decode upstream verified scope: %w", err)
	}
	return scope, nil
}

func blackboxEpochSeconds(seconds float64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	fraction := seconds - float64(int64(seconds))
	return time.Unix(int64(seconds), int64(fraction*1e9)).UTC()
}
