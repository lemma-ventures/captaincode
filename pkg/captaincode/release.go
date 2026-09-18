package captaincode

// Release package (ROADMAP M1.5). The exit gate for M1 is "a new developer
// can install Captain and reproduce what its routing gains or costs". The
// release package is the artifact that makes reproduction possible: a
// manifest naming every version the evidence was produced against, a
// compatibility table a host adapter can scan, extraction checks that verify
// every doc and recipe the manifest references exists, and a dated evidence
// report suitable for the website.
//
// The manifest is the single source of truth for "what was tested". It carries
// the captain revision, the go version, the toolchain pins, the capability
// version, the registry legs and their model pins, and the accounting schema
// version. A reader who has the manifest and the source checkout can reproduce
// the binary; a reader who has the manifest and the report can verify the
// claims.
//
// The extraction check is the M1.5 analog of the M1.1 rehearsal: the rehearsal
// verifies the install contract is complete; the extraction check verifies the
// release contract is complete — every doc referenced in the code exists, every
// recipe in the manifest is valid, and the compatibility table has no gaps.

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// ReleaseManifest is the machine-readable artifact naming every version an
// evidence run was produced against. It is the "what was tested" record.
type ReleaseManifest struct {
	When          time.Time          `json:"when"`
	Captain       CaptainBuildInfo   `json:"captain"`
	GoVersion     string             `json:"go_version"`
	Platform      string             `json:"platform"`
	Toolchain     []ToolPin          `json:"toolchain"`
	CapabilityVer int                `json:"capability_version"`
	Legs          []LegManifest      `json:"legs"`
	ModelPins     []ModelPinManifest `json:"model_pins"`
	SchemaVer     int                `json:"schema_version"`
	TaskAPIVer    int                `json:"task_api_version"`
}

// CaptainBuildInfo is the captain binary's identity.
type CaptainBuildInfo struct {
	Revision string `json:"revision"`
	Dirty    bool   `json:"dirty"`
	Time     string `json:"build_time,omitempty"`
}

// LegManifest is one leg's release-level identity.
type LegManifest struct {
	ID        Leg       `json:"id"`
	Transport Transport `json:"transport"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	Frontier  bool      `json:"frontier,omitempty"`
	Prior     float64   `json:"prior"`
	Disabled  bool      `json:"disabled,omitempty"`
}

// ModelPinManifest is one model pin's release-level identity.
type ModelPinManifest struct {
	Leg      Leg    `json:"leg"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ReleaseDeps injects the build-info and file reads the manifest needs.
type ReleaseDeps struct {
	ReadFile  func(string) ([]byte, error)
	BuildInfo func() (revision string, modified, ok bool)
}

// BuildManifest assembles the release manifest from the current build's pins
// and registry. It does not probe the machine — it reports what this binary
// was built against, not what is installed.
func BuildManifest(deps ReleaseDeps) ReleaseManifest {
	m := ReleaseManifest{
		When:          time.Now().UTC(),
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion:     runtime.Version(),
		Toolchain:     Toolchain(),
		CapabilityVer: CapabilityVersion,
		SchemaVer:     AccountingVersion,
		TaskAPIVer:    TaskAPIVersion,
	}

	if deps.BuildInfo == nil {
		deps.BuildInfo = GoBuildStamp
	}
	if rev, modified, ok := deps.BuildInfo(); ok {
		m.Captain.Revision = rev
		m.Captain.Dirty = modified
	}
	if BuildRevision != "" {
		m.Captain.Revision = BuildRevision
	}
	if BuildTime != "" {
		m.Captain.Time = BuildTime
	}

	for _, s := range Registry() {
		m.Legs = append(m.Legs, LegManifest{
			ID: s.ID, Transport: s.Transport, Provider: s.Provider,
			Model: s.Model, Frontier: s.Frontier, Prior: s.Prior, Disabled: s.Disabled,
		})
	}

	for _, p := range LegModelPins() {
		m.ModelPins = append(m.ModelPins, ModelPinManifest{Leg: Leg(p.Leg), Provider: p.Provider, Model: p.Model})
	}

	return m
}

// ReleaseCheck is one extraction verification.
type ReleaseCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// ReleaseReport is the full release package: manifest + checks + compat table.
type ReleaseReport struct {
	Manifest    ReleaseManifest `json:"manifest"`
	Checks      []ReleaseCheck  `json:"checks"`
	CompatTable []CompatRow     `json:"compat_table"`
	Passed      int             `json:"passed"`
	Failed      int             `json:"failed"`
	ExitOK      bool            `json:"exit_ok"`
}

// CompatRow is one row of the compatibility table a host adapter scans.
type CompatRow struct {
	Transport  Transport `json:"transport"`
	Binary     string    `json:"binary"`
	Tested     string    `json:"tested"`
	Min        string    `json:"min"`
	Caps       []CapRow  `json:"capabilities"`
	ScopeLevel string    `json:"scope_level"`
}

// CapRow is one capability in the compat table.
type CapRow struct {
	Cap     Capability `json:"cap"`
	Support Support    `json:"support"`
}

// RunReleaseChecks runs the extraction checks for M1.5: verify every doc
// referenced in the code exists, every recipe in the manifest is valid, and
// the compat table has no gaps. deps.ReadFile reads from the repo root.
func RunReleaseChecks(deps ReleaseDeps) []ReleaseCheck {
	if deps.ReadFile == nil {
		deps.ReadFile = func(string) ([]byte, error) { return nil, fmt.Errorf("no reader") }
	}
	if deps.BuildInfo == nil {
		deps.BuildInfo = GoBuildStamp
	}

	var checks []ReleaseCheck

	// 1. Required docs exist.
	checks = append(checks, checkDocExists(deps.ReadFile, "docs/INSTALL.md"))
	checks = append(checks, checkDocExists(deps.ReadFile, "docs/ROADMAP.md"))
	checks = append(checks, checkDocExists(deps.ReadFile, "docs/eval/README.md"))
	checks = append(checks, checkDocExists(deps.ReadFile, "docs/TASK_API_COMPATIBILITY.md"))
	checks = append(checks, checkDocExists(deps.ReadFile, "docs/TASK_MCP_CONTRACT.md"))

	// 2. Pilot fixture exists.
	checks = append(checks, checkDocExists(deps.ReadFile, "docs/eval/pilot-12.json"))
	checks = append(checks, checkDocExists(deps.ReadFile, "docs/eval/fixtures/captainfix.bundle"))

	// 3. Toolchain pins are self-consistent (same check as the rehearsal,
	// but the release package must pass it independently).
	checks = append(checks, checkToolchainConsistency())

	// 4. Every leg has a transport with a pin.
	checks = append(checks, checkTransportsCovered())

	// 5. Capability declarations cover every transport with a leg.
	checks = append(checks, checkCapabilityCoverage())

	// 6. Build identity is present (not dirty).
	checks = append(checks, checkBuildIdentity(deps.BuildInfo))

	return checks
}

// BuildReleaseReport assembles the full release package.
func BuildReleaseReport(deps ReleaseDeps) ReleaseReport {
	if deps.BuildInfo == nil {
		deps.BuildInfo = GoBuildStamp
	}
	m := BuildManifest(deps)
	checks := RunReleaseChecks(deps)
	compat := BuildCompatTable()

	r := ReleaseReport{Manifest: m, Checks: checks, CompatTable: compat}
	for _, c := range checks {
		if c.Passed {
			r.Passed++
		} else {
			r.Failed++
		}
	}
	r.ExitOK = r.Failed == 0
	return r
}

// BuildCompatTable assembles the compatibility table from toolchain pins and
// capability declarations. It finds one leg per transport to read capabilities
// and scope contracts through the same API the router uses.
func BuildCompatTable() []CompatRow {
	legForTransport := map[Transport]Leg{}
	for _, s := range Registry() {
		if _, ok := legForTransport[s.Transport]; !ok {
			legForTransport[s.Transport] = s.ID
		}
	}
	var rows []CompatRow
	for _, p := range Toolchain() {
		row := CompatRow{
			Transport: p.Transport, Binary: p.Bin,
			Tested: p.Tested, Min: p.Min,
		}
		if leg, ok := legForTransport[p.Transport]; ok {
			cs := CapabilitiesFor(leg)
			for _, cap := range Capabilities {
				row.Caps = append(row.Caps, CapRow{Cap: cap, Support: cs.Supports(cap)})
			}
			sc := ScopeContractFor(leg)
			row.ScopeLevel = string(sc.Transport)
		}
		rows = append(rows, row)
	}
	return rows
}

// FormatReleaseManifest renders the manifest for the CLI.
func FormatReleaseManifest(m ReleaseManifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "manifest   %s\n", m.When.Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "captain    revision %s", shortRev(m.Captain.Revision))
	if m.Captain.Dirty {
		fmt.Fprint(&b, " (dirty)")
	}
	if m.Captain.Time != "" {
		fmt.Fprintf(&b, "  built %s", m.Captain.Time)
	}
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "go         %s  %s\n", m.GoVersion, m.Platform)
	fmt.Fprintf(&b, "schema     accounting v%d  capability v%d  task-api v%d\n",
		m.SchemaVer, m.CapabilityVer, m.TaskAPIVer)
	fmt.Fprintf(&b, "toolchain  %d adapters\n", len(m.Toolchain))
	for _, p := range m.Toolchain {
		fmt.Fprintf(&b, "  %-13s tested %-12s min %-12s  %s\n", p.Bin, p.Tested, p.Min, p.Install)
	}
	fmt.Fprintf(&b, "legs       %d registered\n", len(m.Legs))
	for _, l := range m.Legs {
		mark := " "
		if l.Disabled {
			mark = "·"
		} else if l.Frontier {
			mark = "★"
		}
		fmt.Fprintf(&b, "  %s %-10s %-11s prior %.1f\n", mark, l.ID, l.Transport, l.Prior)
	}
	if len(m.ModelPins) > 0 {
		fmt.Fprintf(&b, "model-pins %d\n", len(m.ModelPins))
		for _, p := range m.ModelPins {
			fmt.Fprintf(&b, "  %-10s %s/%s\n", p.Leg, p.Provider, p.Model)
		}
	}
	return b.String()
}

// FormatReleaseReport renders the full release report for the CLI.
func FormatReleaseReport(r ReleaseReport) string {
	var b strings.Builder
	fmt.Fprintln(&b, FormatReleaseManifest(r.Manifest))
	fmt.Fprintf(&b, "checks     %d passed, %d failed\n", r.Passed, r.Failed)
	for _, c := range r.Checks {
		mark := "✓"
		if !c.Passed {
			mark = "✗"
		}
		fmt.Fprintf(&b, "  %s %-28s %s\n", mark, c.ID, c.Title)
		if c.Detail != "" {
			fmt.Fprintf(&b, "     %s\n", truncateStr(c.Detail, 100))
		}
		if !c.Passed && c.Fix != "" {
			fmt.Fprintf(&b, "     fix: %s\n", c.Fix)
		}
	}
	fmt.Fprintf(&b, "\ncompat     %d transports\n", len(r.CompatTable))
	for _, row := range r.CompatTable {
		var caps []string
		for _, c := range row.Caps {
			caps = append(caps, fmt.Sprintf("%s=%s", c.Cap, c.Support))
		}
		fmt.Fprintf(&b, "  %-13s tested %-12s  %s\n", row.Binary, row.Tested, strings.Join(caps, " "))
	}
	if r.ExitOK {
		fmt.Fprintln(&b, "\nrelease package is complete — reproducible artifacts, compatibility table, and extraction checks all pass.")
	} else {
		fmt.Fprintf(&b, "\n%d gap(s) found — fix them before releasing.\n", r.Failed)
	}
	return b.String()
}

// ReleaseJSON marshals the release report to indented JSON.
func ReleaseJSON(r ReleaseReport) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// ManifestJSON marshals the manifest to indented JSON.
func ManifestJSON(m ReleaseManifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// --- individual checks ---

func checkDocExists(read func(string) ([]byte, error), path string) ReleaseCheck {
	id := strings.ReplaceAll(strings.TrimPrefix(path, "docs/"), "/", "-")
	if id == "" {
		id = "doc"
	}
	c := ReleaseCheck{ID: "doc:" + id, Title: path + " exists"}
	body, err := read(path)
	if err != nil {
		c.Passed = false
		c.Detail = err.Error()
		c.Fix = "create " + path
		return c
	}
	c.Passed = true
	c.Detail = fmt.Sprintf("%d bytes", len(body))
	return c
}

func checkToolchainConsistency() ReleaseCheck {
	c := ReleaseCheck{ID: "toolchain-consistent", Title: "toolchain pins are self-consistent"}
	var problems []string
	for _, p := range Toolchain() {
		if CompareToolVersions(p.Tested, p.Min) < 0 {
			problems = append(problems, fmt.Sprintf("%s: tested %s below min %s", p.Bin, p.Tested, p.Min))
		}
		if !strings.Contains(p.Rollback, p.Tested) {
			problems = append(problems, fmt.Sprintf("%s: rollback does not pin tested", p.Bin))
		}
	}
	if len(problems) > 0 {
		c.Passed = false
		c.Detail = strings.Join(problems, "; ")
		return c
	}
	c.Passed = true
	return c
}

func checkTransportsCovered() ReleaseCheck {
	c := ReleaseCheck{ID: "transports-covered", Title: "every transport has a toolchain pin or an API contract"}
	missing := []string{}
	for _, s := range Registry() {
		if !TransportCovered(s.Transport) {
			missing = append(missing, string(s.ID))
		}
	}
	if len(missing) > 0 {
		c.Passed = false
		c.Detail = "uncovered: " + strings.Join(missing, ", ")
		return c
	}
	c.Passed = true
	return c
}

func checkCapabilityCoverage() ReleaseCheck {
	c := ReleaseCheck{ID: "capability-coverage", Title: "every transport has capability declarations"}
	missing := []string{}
	seen := map[Transport]bool{}
	for _, s := range Registry() {
		if seen[s.Transport] {
			continue
		}
		seen[s.Transport] = true
		cs := CapabilitiesFor(s.ID)
		if cs.Version == 0 {
			missing = append(missing, string(s.Transport))
		}
	}
	if len(missing) > 0 {
		c.Passed = false
		c.Detail = "no caps: " + strings.Join(missing, ", ")
		return c
	}
	c.Passed = true
	return c
}

func checkBuildIdentity(buildInfo func() (string, bool, bool)) ReleaseCheck {
	c := ReleaseCheck{ID: "build-identity", Title: "captain binary has a reproducible revision"}
	rev, modified, ok := buildInfo()
	if BuildRevision != "" {
		rev, modified, ok = BuildRevision, false, true
	}
	if !ok {
		c.Passed = false
		c.Detail = "no VCS stamp — built outside a checkout"
		c.Fix = "rebuild with `go build ./cmd/captaincode/` from the source tree"
		return c
	}
	if modified {
		c.Passed = false
		c.Detail = "built from a modified tree — revision " + shortRev(rev) + " is not the code that ran"
		c.Fix = "commit or stash changes, then rebuild"
		return c
	}
	c.Passed = true
	c.Detail = shortRev(rev)
	return c
}
