package captaincode

// Capability probes (ROADMAP M2.2 remaining). The capability registry declares
// what each transport supports, but a CLI that dropped a flag between releases
// makes the declaration a lie. ProbeTool exercises the version contract;
// ProbeCapabilities exercises the capability contract: run the binary's --help
// and check for the flag or subcommand that corresponds to each declared `yes`.
//
// Not every capability is CLI-probable. CapCancel is about process behaviour
// (kill -TERM), and opencode's CapUsage/CapCancel are server-API facts that
// --help cannot reveal. Those are marked `skip` rather than reported as
// verified, so a reader does not confuse "we did not check" with "it works."

import (
	"strings"
)

// CapProbeState is the outcome of probing one capability against a binary.
type CapProbeState string

const (
	CapProbeVerified CapProbeState = "verified"  // the flag/subcommand is present
	CapProbeMissing  CapProbeState = "missing"   // declared yes but the flag is absent
	CapProbeSkip     CapProbeState = "skip"      // not CLI-probable (process or server-level)
	CapProbeNoBinary CapProbeState = "no-binary" // the binary is not on PATH
)

// CapProbe defines how to verify one declared capability against an installed binary.
type CapProbe struct {
	Cap    Capability
	Args   []string // args to run (without the binary name)
	Expect string   // lowercase substring to find in combined output
}

// CapProbeResult is what the probe found for one transport+capability.
type CapProbeResult struct {
	Transport Transport
	Cap       Capability
	State     CapProbeState
	Detail    string
}

// capProbesFor returns the probes that verify capabilities declared as `yes`
// for a transport. Capabilities that are not CLI-probable (process behaviour,
// server API) are returned as skip entries so the report names what it did not
// check rather than silently omitting it.
func capProbesFor(t Transport) []CapProbe {
	switch t {
	case TransportClaudeCLI:
		return []CapProbe{
			{Cap: CapTools, Args: []string{"--help"}, Expect: "--print"},
			{Cap: CapPermissions, Args: []string{"--help"}, Expect: "--permission-mode"},
			{Cap: CapUsage, Args: []string{"--help"}, Expect: "--output-format"},
			{Cap: CapCost, Args: []string{"--help"}, Expect: "--output-format"},
		}
	case TransportCodexCLI:
		return []CapProbe{
			{Cap: CapTools, Args: []string{"--help"}, Expect: "exec"},
			{Cap: CapPermissions, Args: []string{"--help"}, Expect: "--sandbox"},
			{Cap: CapUsage, Args: []string{"--help"}, Expect: "json"},
		}
	case TransportCursorCLI:
		return []CapProbe{
			{Cap: CapTools, Args: []string{"--help"}, Expect: "-p"},
			{Cap: CapPermissions, Args: []string{"--help"}, Expect: "--trust"},
		}
	case TransportOpencode:
		return []CapProbe{
			{Cap: CapTools, Args: []string{"--help"}, Expect: "serve"},
			{Cap: CapSessionReuse, Args: []string{"--help"}, Expect: "serve"},
		}
	}
	return nil
}

// skipCaps are capabilities declared yes but not verifiable through --help.
var skipCaps = map[Transport][]Capability{
	TransportClaudeCLI: {CapCancel, CapSessionReuse},
	TransportCodexCLI:  {CapCancel, CapSessionReuse, CapCost},
	TransportCursorCLI: {CapCancel, CapSessionReuse, CapUsage, CapCost},
	TransportOpencode:  {CapCancel, CapUsage, CapCost, CapPermissions},
}

// ProbeCapabilities runs CLI probes for every transport's declared `yes`
// capabilities. lookPath and run are injected for testability. The result
// includes skip entries for capabilities that cannot be checked from --help,
// so the report distinguishes "verified" from "not checked".
func ProbeCapabilities(lookPath func(string) (string, error), run func(string, ...string) ([]byte, error)) []CapProbeResult {
	var out []CapProbeResult
	for _, pin := range Toolchain() {
		probes := capProbesFor(pin.Transport)
		path, err := lookPath(pin.Bin)
		if err != nil {
			for _, p := range probes {
				out = append(out, CapProbeResult{Transport: pin.Transport, Cap: p.Cap, State: CapProbeNoBinary, Detail: pin.Bin + " not on PATH"})
			}
			for _, c := range skipCaps[pin.Transport] {
				out = append(out, CapProbeResult{Transport: pin.Transport, Cap: c, State: CapProbeSkip, Detail: "not CLI-probable"})
			}
			continue
		}
		for _, p := range probes {
			raw, _ := run(path, p.Args...)
			text := strings.ToLower(string(raw))
			if strings.Contains(text, p.Expect) {
				out = append(out, CapProbeResult{Transport: pin.Transport, Cap: p.Cap, State: CapProbeVerified})
			} else {
				out = append(out, CapProbeResult{Transport: pin.Transport, Cap: p.Cap, State: CapProbeMissing,
					Detail: "`" + pin.Bin + " " + strings.Join(p.Args, " ") + "` does not mention " + p.Expect})
			}
		}
		for _, c := range skipCaps[pin.Transport] {
			out = append(out, CapProbeResult{Transport: pin.Transport, Cap: c, State: CapProbeSkip, Detail: "not CLI-probable"})
		}
	}
	return out
}

// CapProbeMark is the glyph for one probe result.
func CapProbeMark(s CapProbeState) string {
	switch s {
	case CapProbeVerified:
		return "✓"
	case CapProbeMissing:
		return "✗"
	case CapProbeNoBinary:
		return "·"
	}
	return "~"
}
