package captaincode

// The adapter version contract (ROADMAP M1.1). A leg is a model *and* the CLI
// that drives it, and the CLI moves under us: `claude -p` grew a different
// JSON envelope, `codex exec` renamed its flags, opencode's server changed the
// session route. `captain doctor` used to answer only "is there a binary named
// codex on PATH?", which is the wrong question twice over - a binary can be
// present and too old to honour the contract captain compiles against, and a
// name on PATH is not proof of identity (a `codex` that is somebody else's
// script fails in a way no amount of retrying fixes).
//
// So each transport pins two versions and, where the tool says its own name,
// one identity marker:
//
//   - Tested is the version this build's behaviour was actually observed
//     against. Newer is allowed and reported, because forbidding it would
//     make every upstream release a captain outage; but an evidence run that
//     quotes a different version than the one in the manifest is not the same
//     run, which is why the probe records what it found rather than a yes/no.
//   - Min is the oldest version whose contract captain still relies on. Below
//     it the adapter is expected to misbehave, so doctor blocks the leg with
//     the upgrade command rather than letting a worker fail mid-task.
//
// Nothing here reads a version out of a lockfile: the only authority is the
// binary that will actually run, asked for its version the same way a user
// would. Probing is pure - the caller supplies lookPath and run - so the
// contract is testable without installing four CLIs.

import (
	"regexp"
	"strconv"
	"strings"
)

// ToolPin is the version contract for one adapter binary.
type ToolPin struct {
	Bin         string    `json:"bin"`
	Transport   Transport `json:"transport"`
	VersionArgs []string  `json:"version_args"`
	Expect      string    `json:"expect,omitempty"` // lowercase marker the tool prints with its version, when it prints one
	Tested      string    `json:"tested"`
	Min         string    `json:"min"`
	Install     string    `json:"install"`
	Upgrade     string    `json:"upgrade"`
	Rollback    string    `json:"rollback"` // pins the tested version back
}

// ToolState is the outcome of probing one pin.
type ToolState string

const (
	ToolOK       ToolState = "ok"       // present, identified, at or above Min and at Tested
	ToolNewer    ToolState = "newer"    // usable, but not the version the evidence was produced against
	ToolOld      ToolState = "old"      // below Min: the contract captain compiles against is not there
	ToolUnknown  ToolState = "unknown"  // present but it would not say a version; never claimed as satisfied
	ToolMismatch ToolState = "mismatch" // that name on PATH is some other program
	ToolMissing  ToolState = "missing"  // nothing on PATH under that name
)

// ToolStatus is what the probe found for one pin.
type ToolStatus struct {
	Pin     ToolPin   `json:"pin"`
	State   ToolState `json:"state"`
	Path    string    `json:"path,omitempty"`
	Version string    `json:"version,omitempty"`
	Detail  string    `json:"detail,omitempty"` // the fix, or why the tool could not be identified
}

// Blocked reports whether this state stops captain from using the adapter.
// "newer" and "unknown" do not: they are reported, not enforced, because a
// version captain cannot read is a reason to distrust the report, not a reason
// to refuse a tool the user has installed.
func (s ToolStatus) Blocked() bool {
	return s.State == ToolMissing || s.State == ToolOld || s.State == ToolMismatch
}

// Toolchain is the pinned adapter set, in the order doctor reports it.
// Tested versions are the ones M1's baseline runs were produced against.
func Toolchain() []ToolPin {
	return []ToolPin{
		{
			Bin: "opencode", Transport: TransportOpencode, VersionArgs: []string{"--version"},
			Tested: "1.18.30", Min: "1.10.0",
			Install:  "curl -fsSL https://opencode.ai/install | bash",
			Upgrade:  "opencode upgrade",
			Rollback: "curl -fsSL https://opencode.ai/install | VERSION=1.18.30 bash",
		},
		{
			Bin: "claude", Transport: TransportClaudeCLI, VersionArgs: []string{"--version"}, Expect: "claude",
			Tested: "2.1.270", Min: "2.0.0",
			Install:  "npm i -g @anthropic-ai/claude-code",
			Upgrade:  "npm i -g @anthropic-ai/claude-code@latest",
			Rollback: "npm i -g @anthropic-ai/claude-code@2.1.270",
		},
		{
			Bin: "codex", Transport: TransportCodexCLI, VersionArgs: []string{"--version"}, Expect: "codex",
			Tested: "0.153.4", Min: "0.100.0",
			Install:  "npm i -g @openai/codex",
			Upgrade:  "npm i -g @openai/codex@latest",
			Rollback: "npm i -g @openai/codex@0.153.4",
		},
		{
			Bin: "cursor-agent", Transport: TransportCursorCLI, VersionArgs: []string{"--version"},
			Tested: "2026.09.10", Min: "2026.01.01",
			Install:  "curl https://cursor.com/install -fsS | bash",
			Upgrade:  "cursor-agent upgrade",
			Rollback: "curl https://cursor.com/install -fsS | bash -s -- --version 2026.09.10",
		},
	}
}

// APIContract is the version contract of a transport that drives no binary:
// a versioned HTTP API. Where a pin names the adapter version an evidence run
// quotes, the contract names the endpoint and what to quote instead.
type APIContract struct {
	Transport Transport `json:"transport"`
	Endpoint  string    `json:"endpoint"`
	Versioned string    `json:"versioned"` // how the served version is learned and frozen
}

// APIContracts are the transports covered by contract rather than by pin.
func APIContracts() []APIContract {
	return []APIContract{{
		Transport: TransportSystemOne,
		Endpoint:  "https://api.typesafe.ai/v1/systemone",
		Versioned: "every response names the release that served it (model: jev-1.13.0 behind the jev-latest alias); CAPTAIN_JEV_MODEL pins one",
	}}
}

// TransportCovered reports whether a transport is under a version contract:
// pinned in Toolchain(), or an API transport with no binary to pin. It is
// what doctor and the release checks require of every transport a leg runs on.
func TransportCovered(t Transport) bool {
	if _, ok := ToolPinFor(t); ok {
		return true
	}
	for _, c := range APIContracts() {
		if c.Transport == t {
			return true
		}
	}
	return false
}

// ToolPinFor returns the pin driving a transport.
func ToolPinFor(t Transport) (ToolPin, bool) {
	for _, p := range Toolchain() {
		if p.Transport == t {
			return p, true
		}
	}
	return ToolPin{}, false
}

// ProbeTool resolves one pin against the machine. lookPath and run are
// injected so the contract can be tested without the CLIs; run receives the
// resolved path and the pin's version arguments and returns combined output,
// because several of these tools print their version on stderr.
func ProbeTool(p ToolPin, lookPath func(string) (string, error), run func(string, ...string) ([]byte, error)) ToolStatus {
	s := ToolStatus{Pin: p}
	path, err := lookPath(p.Bin)
	if err != nil {
		s.State, s.Detail = ToolMissing, p.Install
		return s
	}
	s.Path = path
	out, err := run(path, p.VersionArgs...)
	text := strings.ToLower(strings.TrimSpace(string(out)))
	if text == "" {
		why := "printed nothing"
		if err != nil {
			why = "failed: " + err.Error()
		}
		s.State, s.Detail = ToolUnknown, "`"+p.Bin+" "+strings.Join(p.VersionArgs, " ")+"` "+why
		return s
	}
	if p.Expect != "" && !strings.Contains(text, p.Expect) {
		s.State, s.Detail = ToolMismatch, path+" does not identify itself as "+p.Bin+" - remove it from PATH or "+p.Install
		return s
	}
	s.Version = ParseToolVersion(text)
	switch {
	case s.Version == "":
		s.State, s.Detail = ToolUnknown, "no version in `"+p.Bin+" "+strings.Join(p.VersionArgs, " ")+"` output"
	case CompareToolVersions(s.Version, p.Min) < 0:
		s.State, s.Detail = ToolOld, "below pinned minimum "+p.Min+" - "+p.Upgrade
	case CompareToolVersions(s.Version, p.Tested) > 0:
		s.State, s.Detail = ToolNewer, "newer than tested "+p.Tested+" - pin it back with `"+p.Rollback+"`"
	default:
		s.State = ToolOK
	}
	return s
}

// ProbeToolchain probes every pin, in Toolchain order.
func ProbeToolchain(lookPath func(string) (string, error), run func(string, ...string) ([]byte, error)) []ToolStatus {
	pins := Toolchain()
	out := make([]ToolStatus, 0, len(pins))
	for _, p := range pins {
		out = append(out, ProbeTool(p, lookPath, run))
	}
	return out
}

// versionRe is the first dotted numeric run in the output. It deliberately
// tolerates prefixes and suffixes: these four tools print "2.1.270 (Claude
// Code)", "codex-cli 0.153.4", "1.18.30" and "2026.09.10-fd3934a".
var versionRe = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)+`)

// ParseToolVersion pulls the version out of a --version line, or "" if the
// tool printed none. The first line only: a tool that follows its version with
// a changelog must not have a date mistaken for it.
func ParseToolVersion(out string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return versionRe.FindString(line)
}

// CompareToolVersions orders two dotted numeric versions, component by
// component, shorter padded with zeros. Non-numeric suffixes are ignored -
// captain pins release lines, not pre-release ordering, and guessing whether
// "-rc1" precedes its release would block a tool the user chose to install.
func CompareToolVersions(a, b string) int {
	x, y := versionFields(a), versionFields(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var xi, yi int
		if i < len(x) {
			xi = x[i]
		}
		if i < len(y) {
			yi = y[i]
		}
		if xi != yi {
			if xi < yi {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionFields(v string) []int {
	v = versionRe.FindString(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
