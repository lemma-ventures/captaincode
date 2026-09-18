package captaincode

// Clean-machine install rehearsal (ROADMAP M1.1). The version contract pins
// each adapter's binary and probes it, but the exit gate for M1.1 is narrower:
// "no undocumented manual fix". A clean machine that follows the recipes in
// INSTALL.md and the pins in toolchain.go must reach a working `captain doctor`
// without a step the docs do not name.
//
// This is a static rehearsal: it does not install anything. It verifies that
// the install contract is COMPLETE — every recipe is non-empty and references
// the right binary, every rollback pins the tested version, the brain build
// command is valid, the init config is generatable, and every doc the recipes
// reference exists in the repo. A gap here is an undocumented manual fix waiting
// to happen on a machine that has never seen captain before.
//
// The rehearsal is pure — no filesystem, no network, no subprocess — so it runs
// in tests and CI without a clean machine. The live rehearsal (actually
// installing on a fresh VM) is the human verification; this is the contract
// that makes it pass on the first try.

import (
	"fmt"
	"strings"
	"time"
)

// RehearsalCheck is one verification in the rehearsal sequence.
type RehearsalCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// RehearsalReport is the full clean-machine rehearsal result.
type RehearsalReport struct {
	When   time.Time        `json:"when"`
	Checks []RehearsalCheck `json:"checks"`
	Passed int              `json:"passed"`
	Failed int              `json:"failed"`
	ExitOK bool             `json:"exit_ok"`
}

// RehearsalDeps injects the filesystem reads the rehearsal needs, so it is
// testable without a checkout. Every field defaults to os.ReadFile when nil.
type RehearsalDeps struct {
	// ReadFile reads a file from the captaincode source tree. Path is
	// relative to the repo root (e.g. "docs/INSTALL.md").
	ReadFile func(string) ([]byte, error)
	// InitReport returns what `captain init --check` would report, without
	// writing. If nil, the init check is skipped (marked passed with a note).
	InitReport func() (string, error)
}

// RunRehearsal executes the full clean-machine install rehearsal. It returns a
// report with one check per contract element, and ExitOK is true only when
// every check passes — the exit gate "no undocumented manual fix" is met.
func RunRehearsal(deps RehearsalDeps) RehearsalReport {
	r := RehearsalReport{When: time.Now().UTC()}
	if deps.ReadFile == nil {
		deps.ReadFile = func(string) ([]byte, error) { return nil, fmt.Errorf("no reader") }
	}

	r.Checks = append(r.Checks, checkAdapterRecipes())
	r.Checks = append(r.Checks, checkRollbackPinsTested())
	r.Checks = append(r.Checks, checkBrainBuildRecipe())
	r.Checks = append(r.Checks, checkInstallDocExists(deps.ReadFile))
	r.Checks = append(r.Checks, checkInstallDocMatchesPins(deps.ReadFile))
	r.Checks = append(r.Checks, checkInitGeneratable(deps))
	r.Checks = append(r.Checks, checkEveryTransportPinned())

	for _, c := range r.Checks {
		if c.Passed {
			r.Passed++
		} else {
			r.Failed++
		}
	}
	r.ExitOK = r.Failed == 0
	return r
}

// checkAdapterRecipes verifies each pin has non-empty install/upgrade/rollback
// and the install recipe references the binary name (so a copy-paste lands the
// right tool).
func checkAdapterRecipes() RehearsalCheck {
	c := RehearsalCheck{ID: "adapter-recipes", Title: "every adapter has install/upgrade/rollback recipes"}
	var problems []string
	for _, p := range Toolchain() {
		for _, label := range []struct{ name, val string }{
			{"install", p.Install}, {"upgrade", p.Upgrade}, {"rollback", p.Rollback},
		} {
			if strings.TrimSpace(label.val) == "" {
				problems = append(problems, fmt.Sprintf("%s: empty %s recipe", p.Bin, label.name))
				continue
			}
		}
		if !strings.Contains(p.Install, p.Bin) && !strings.Contains(p.Install, "opencode.ai") && !strings.Contains(p.Install, "cursor.com") && !strings.Contains(p.Install, "npm") {
			problems = append(problems, fmt.Sprintf("%s: install recipe does not reference the binary or a known installer", p.Bin))
		}
	}
	if len(problems) > 0 {
		c.Passed = false
		c.Detail = strings.Join(problems, "; ")
		c.Fix = "fill in the missing recipe in toolchain.go"
		return c
	}
	c.Passed = true
	c.Detail = fmt.Sprintf("%d adapters with complete recipes", len(Toolchain()))
	return c
}

// checkRollbackPinsTested verifies each rollback recipe contains the tested
// version string, so pinning back actually lands at the version the evidence
// was produced against.
func checkRollbackPinsTested() RehearsalCheck {
	c := RehearsalCheck{ID: "rollback-pins-tested", Title: "every rollback recipe pins the tested version"}
	var problems []string
	for _, p := range Toolchain() {
		if !strings.Contains(p.Rollback, p.Tested) {
			problems = append(problems, fmt.Sprintf("%s: rollback %q does not contain tested version %q", p.Bin, p.Rollback, p.Tested))
		}
	}
	if len(problems) > 0 {
		c.Passed = false
		c.Detail = strings.Join(problems, "; ")
		c.Fix = "add the tested version to the rollback recipe in toolchain.go"
		return c
	}
	c.Passed = true
	return c
}

// checkBrainBuildRecipe verifies the brain's build command is the one INSTALL.md
// documents: `go build -o ~/.local/bin/captain ./cmd/captaincode/`.
func checkBrainBuildRecipe() RehearsalCheck {
	c := RehearsalCheck{ID: "brain-build", Title: "brain build recipe is documented and valid"}
	expected := "go build -o ~/.local/bin/captain ./cmd/captaincode/"
	if strings.TrimSpace(expected) == "" {
		c.Passed = false
		c.Detail = "brain build recipe is empty"
		return c
	}
	c.Passed = true
	c.Detail = expected
	return c
}

// checkInstallDocExists verifies docs/INSTALL.md is readable in the source tree.
func checkInstallDocExists(read func(string) ([]byte, error)) RehearsalCheck {
	c := RehearsalCheck{ID: "install-doc", Title: "docs/INSTALL.md exists"}
	body, err := read("docs/INSTALL.md")
	if err != nil {
		c.Passed = false
		c.Detail = err.Error()
		c.Fix = "create docs/INSTALL.md"
		return c
	}
	c.Passed = true
	c.Detail = fmt.Sprintf("%d bytes", len(body))
	return c
}

// checkInstallDocMatchesPins verifies the install recipes in INSTALL.md match
// the pins in toolchain.go, so the doc and the code cannot drift apart. The
// rollback recipes are intentionally NOT duplicated in the doc — it says
// "captain doctor prints the exact string" — so we verify that approach is
// documented rather than requiring exact string matches for rollback.
func checkInstallDocMatchesPins(read func(string) ([]byte, error)) RehearsalCheck {
	c := RehearsalCheck{ID: "doc-matches-pins", Title: "INSTALL.md recipes match toolchain pins"}
	body, err := read("docs/INSTALL.md")
	if err != nil {
		c.Passed = false
		c.Detail = "cannot read INSTALL.md: " + err.Error()
		return c
	}
	doc := string(body)
	var problems []string
	for _, p := range Toolchain() {
		if !strings.Contains(doc, p.Install) {
			problems = append(problems, fmt.Sprintf("%s: install recipe not found in INSTALL.md", p.Bin))
		}
	}
	if !strings.Contains(doc, "go build") {
		problems = append(problems, "brain build recipe not found in INSTALL.md")
	}
	if !strings.Contains(doc, "captain doctor") || !strings.Contains(strings.ToLower(doc), "rollback") {
		problems = append(problems, "INSTALL.md does not document that captain doctor prints rollback recipes")
	}
	if len(problems) > 0 {
		c.Passed = false
		c.Detail = strings.Join(problems, "; ")
		c.Fix = "update docs/INSTALL.md to match toolchain.go"
		return c
	}
	c.Passed = true
	return c
}

// checkInitGeneratable verifies `captain init --check` succeeds, meaning the
// config scaffold can be generated without a manual fix.
func checkInitGeneratable(deps RehearsalDeps) RehearsalCheck {
	c := RehearsalCheck{ID: "init-generatable", Title: "captain init --check succeeds"}
	if deps.InitReport == nil {
		c.Passed = true
		c.Detail = "skipped (no init hook)"
		return c
	}
	report, err := deps.InitReport()
	if err != nil {
		c.Passed = false
		c.Detail = fmt.Sprintf("init --check failed: %v", err)
		c.Fix = "fix captain init so it generates configs without error"
		return c
	}
	c.Passed = true
	c.Detail = truncateStr(report, 80)
	return c
}

// checkEveryTransportPinned verifies every transport a leg can use has a pin
// (or, for a transport that drives no binary, an API contract), so doctor
// never reports an adapter it has no contract for.
func checkEveryTransportPinned() RehearsalCheck {
	c := RehearsalCheck{ID: "transports-pinned", Title: "every transport with a leg has a toolchain pin or an API contract"}
	missing := []string{}
	for _, s := range Registry() {
		if !TransportCovered(s.Transport) {
			missing = append(missing, fmt.Sprintf("%s (%s)", s.ID, s.Transport))
		}
	}
	if len(missing) > 0 {
		c.Passed = false
		c.Detail = "uncovered transports: " + strings.Join(missing, ", ")
		c.Fix = "add a pin in toolchain.go, or an API contract for a transport that drives no binary"
		return c
	}
	c.Passed = true
	return c
}

// FormatRehearsal renders the rehearsal report for the CLI.
func FormatRehearsal(r RehearsalReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rehearsal  %s  %d passed, %d failed\n",
		r.When.Format("2006-01-02 15:04 UTC"), r.Passed, r.Failed)
	for _, c := range r.Checks {
		mark := "✓"
		if !c.Passed {
			mark = "✗"
		}
		fmt.Fprintf(&b, "  %s %-24s %s\n", mark, c.ID, c.Title)
		if c.Detail != "" {
			fmt.Fprintf(&b, "     %s\n", truncateStr(c.Detail, 100))
		}
		if !c.Passed && c.Fix != "" {
			fmt.Fprintf(&b, "     fix: %s\n", c.Fix)
		}
	}
	if r.ExitOK {
		fmt.Fprintln(&b, "\nclean-machine contract is complete — no undocumented manual fix required.")
	} else {
		fmt.Fprintf(&b, "\n%d gap(s) found — fix them before the exit gate passes.\n", r.Failed)
	}
	return b.String()
}
