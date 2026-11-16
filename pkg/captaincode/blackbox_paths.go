package captaincode

// Local protected-path policy. The snapshot contract leaves `protected_path`
// to local configuration: upstream's cache has no path rules, and the
// integration plan requires an explicit operator choice before a path rule may
// refuse an action. `DecodeBlackboxPathPolicy` reads one such operator file
// (a JSON array of path rules) with the same bounds and rejection rules the
// snapshot receiver applies, so an unusable policy never reaches a signed
// envelope.

import (
	"fmt"
	"io"
)

const (
	BlackboxPathPolicyMaxBytes = 256 << 10
	BlackboxPathPolicyMaxRules = 256
)

// DecodeBlackboxPathPolicy reads one bounded policy file. The expected input
// is one JSON array of BlackboxPathRule objects. Empty, oversized, unknown
// field, trailing data, duplicate id, unsafe id, invalid glob and null-byte
// inputs are all rejected here, and anything rejected stays out of an export.
func DecodeBlackboxPathPolicy(reader io.Reader) ([]BlackboxPathRule, error) {
	var rules []BlackboxPathRule
	if _, err := decodeBlackboxJSON(reader, BlackboxPathPolicyMaxBytes, &rules); err != nil {
		return nil, fmt.Errorf("path policy: %w", err)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("path policy: no rules")
	}
	if len(rules) > BlackboxPathPolicyMaxRules {
		return nil, fmt.Errorf("path policy: %d rules, want at most %d", len(rules), BlackboxPathPolicyMaxRules)
	}
	seen := map[string]bool{}
	for _, rule := range rules {
		if err := validateBlackboxPathRule(rule, seen); err != nil {
			return nil, fmt.Errorf("path policy: %w", err)
		}
	}
	return rules, nil
}

// blackboxOmissionDropsPathMaterial reports whether a recorded omission holds
// source material the protected-path category should have carried. The
// upstream `fileaccess` source describes tool file access and has no schema 1
// path mapping, so while it is omitted the export does not claim
// `protected_path` complete.
func blackboxOmissionDropsPathMaterial(omitted []string) bool {
	for _, entry := range omitted {
		category := blackboxOmissionCategory(entry)
		if category == "protected_path" || category == "fileaccess" {
			return true
		}
	}
	return false
}
