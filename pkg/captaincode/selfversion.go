package captaincode

// Captain's own half of the version contract (ROADMAP M1.1). The adapter pins
// in toolchain.go answer "which CLIs drove the legs?"; they say nothing about
// the two pieces of software that did the routing. A report that names
// `claude 2.1.270` and `codex 0.153.4` but not the brain that chose between
// them is still not reproducible: the routing policy, the prices and the
// accounting schema all live in the binary.
//
// Captain is built from source rather than installed from a release, so there
// is no version to pin it *to* - the honest identity is the revision it was
// built from, and whether that revision was the whole truth. Go stamps both
// into the binary (`vcs.revision`, `vcs.modified`) whenever it builds inside a
// git checkout, so this needs no ldflags and cannot be forgotten at build
// time; a release process that wants to override them may still set
// BuildRevision/BuildTime.
//
// Three rows, and the distinctions are again the point:
//
//   - A binary built from a MODIFIED tree reports `dirty`. It is perfectly
//     usable - it is what a developer runs all day - but its revision names a
//     commit whose code is not the code that ran, so an evidence run may not
//     quote it. Dirty does not block; unreproducible is not broken.
//   - The `captain` resolved on PATH being a DIFFERENT file from the running
//     executable is a mismatch, and it blocks: the user's next invocation runs
//     a binary this report never probed. That is the self-inflicted version of
//     the `codex`-is-somebody-else's-script failure.
//   - The terminal fork is a source checkout, not a binary, so its version is
//     its HEAD and its cleanliness. Brain-only installs have no fork and are
//     not penalised for it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// Build identity overrides, for a release build that prefers to stamp its own
// (`-ldflags "-X github.com/lemma-ventures/captaincode/pkg/captaincode.BuildRevision=..."`).
// Left empty, the revision comes from Go's own VCS stamp.
var (
	BuildRevision string
	BuildTime     string
)

// ToolDirty is a component built from a modified working tree: usable, named,
// never quoted as a revision.
const ToolDirty ToolState = "dirty"

// SelfStatus is what the probe found for one piece of captain itself.
type SelfStatus struct {
	Component string    `json:"component"`
	State     ToolState `json:"state"`
	Version   string    `json:"version,omitempty"`
	Path      string    `json:"path,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

// Blocked reports whether this state stops captain from being trusted to run.
// Only an identity failure does: a revision captain cannot read, or a tree it
// cannot vouch for, is a reason to distrust the report rather than the build.
func (s SelfStatus) Blocked() bool { return s.State == ToolMismatch }

// Reproducible reports whether this component can be named in an evidence run.
// A dirty or unknown revision cannot: neither identifies the code that ran.
func (s SelfStatus) Reproducible() bool { return s.State == ToolOK }

// SelfProbe injects everything that touches the machine, so the contract is
// testable without a build stamp, a PATH or a checkout.
type SelfProbe struct {
	Executable func() (string, error)                      // the running binary
	LookPath   func(string) (string, error)                // `captain` on PATH
	SourceDir  string                                      // the checkout the terminal fork is built from
	BuildInfo  func() (revision string, modified, ok bool) // Go's VCS stamp
	Git        func(dir string, args ...string) ([]byte, error)
	ReadFile   func(string) ([]byte, error)
}

// ProbeSelf resolves the brain binary and the terminal fork, in report order.
func ProbeSelf(p SelfProbe) []SelfStatus {
	if p.Executable == nil {
		p.Executable = os.Executable
	}
	if p.LookPath == nil {
		p.LookPath = exec.LookPath
	}
	if p.BuildInfo == nil {
		p.BuildInfo = GoBuildStamp
	}
	if p.Git == nil {
		p.Git = runGit
	}
	if p.ReadFile == nil {
		p.ReadFile = os.ReadFile
	}
	return []SelfStatus{probeBrainBinary(p), probeTerminalFork(p)}
}

// GoBuildStamp reads the revision Go embedded at build time. ok is false when
// the binary was built outside a checkout (`go run`, a tarball, a test binary),
// which is a real state and not an error.
func GoBuildStamp() (string, bool, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, false
	}
	var rev string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if rev == "" {
		return "", false, false
	}
	return rev, modified, true
}

// probeBrainBinary identifies the routing engine that produced this report.
func probeBrainBinary(p SelfProbe) SelfStatus {
	s := SelfStatus{Component: "captain"}
	self, err := p.Executable()
	if err == nil {
		s.Path = resolvePath(self)
	}

	rev, modified, ok := p.BuildInfo()
	if BuildRevision != "" {
		rev, ok = BuildRevision, true
	}
	switch {
	case !ok:
		s.State = ToolUnknown
		s.Detail = "no VCS stamp - built outside a checkout; rebuild with `go build ./cmd/captaincode/` from the source tree"
	case modified:
		s.State, s.Version = ToolDirty, shortRev(rev)
		s.Detail = "built from a modified tree - usable, but this revision does not name the code that ran"
	default:
		s.State, s.Version = ToolOK, shortRev(rev)
	}

	// Identity outranks version: a report about a binary the user's next
	// command will not run is worse than one whose revision is merely dirty,
	// so the mismatch replaces the state - but never the revision, which is
	// still the truth about the binary that produced the report.
	if onPath, err := p.LookPath("captain"); err == nil && s.Path != "" {
		if resolved := resolvePath(onPath); resolved != s.Path {
			s.State = ToolMismatch
			s.Detail = "this report describes the running binary; `captain` on PATH is " + resolved + ", which the next command will run instead"
		}
	}

	if BuildTime != "" {
		s.Detail = strings.TrimSpace("built " + BuildTime + " " + s.Detail)
	}
	return s
}

// probeTerminalFork identifies the opencode fork the full-terminal shape runs.
// Its absence is the brain-only install, which is a supported shape.
func probeTerminalFork(p SelfProbe) SelfStatus {
	s := SelfStatus{Component: "terminal"}
	if p.SourceDir == "" {
		s.State, s.Detail = ToolMissing, "brain-only install - no terminal fork to pin"
		return s
	}
	s.Path = p.SourceDir
	if _, err := p.ReadFile(filepath.Join(p.SourceDir, "package.json")); err != nil {
		s.State, s.Detail = ToolMissing, "no checkout at "+p.SourceDir+" (set CAPTAIN_SRC) - brain-only install"
		return s
	}
	rev, err := p.Git(p.SourceDir, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(rev)) == "" {
		s.State, s.Detail = ToolUnknown, "checkout at "+p.SourceDir+" has no readable revision"
		return s
	}
	s.Version = shortRev(strings.TrimSpace(string(rev)))
	dirty, err := p.Git(p.SourceDir, "status", "--porcelain")
	switch {
	case err != nil:
		s.State, s.Detail = ToolUnknown, "cannot tell whether "+p.SourceDir+" is modified"
	case strings.TrimSpace(string(dirty)) != "":
		s.State = ToolDirty
		s.Detail = "checkout has uncommitted changes - the terminal running is not this revision"
	default:
		s.State = ToolOK
	}
	return s
}

// BunPin builds the runtime pin for the terminal fork out of the checkout's
// own `packageManager` field, so package.json stays the single authority and
// doctor cannot quote a bun version the fork does not actually install with.
func BunPin(sourceDir string, readFile func(string) ([]byte, error)) (ToolPin, bool) {
	if sourceDir == "" {
		return ToolPin{}, false
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	body, err := readFile(filepath.Join(sourceDir, "package.json"))
	if err != nil {
		return ToolPin{}, false
	}
	_, rest, found := strings.Cut(string(body), `"packageManager"`)
	if !found {
		return ToolPin{}, false
	}
	_, rest, found = strings.Cut(rest, `"bun@`)
	if !found {
		return ToolPin{}, false
	}
	v, _, _ := strings.Cut(rest, `"`)
	if v = ParseToolVersion(v); v == "" {
		return ToolPin{}, false
	}
	return ToolPin{
		Bin: "bun", VersionArgs: []string{"--version"},
		Tested: v, Min: v,
		Install:  "curl -fsSL https://bun.sh/install | bash",
		Upgrade:  "bun upgrade",
		Rollback: "bun upgrade --to " + v,
	}, true
}

func runGit(dir string, args ...string) ([]byte, error) {
	return exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
}

// resolvePath follows symlinks so the two sides of the identity check compare
// files rather than the names that happen to point at them.
func resolvePath(p string) string {
	if abs, err := filepath.EvalSymlinks(p); err == nil {
		return abs
	}
	return p
}

func shortRev(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}
