package captaincode

import (
	"errors"
	"strings"
	"testing"
)

func TestParseToolVersionRealOutputs(t *testing.T) {
	for _, c := range []struct{ out, want string }{
		{"2.1.270 (Claude Code)", "2.1.270"},
		{"codex-cli 0.153.4", "0.153.4"},
		{"1.18.30", "1.18.30"},
		{"2026.09.10-fd3934a", "2026.09.10"},
		{"cursor-agent 2026.09.10\nreleased 2020.01.01", "2026.09.10"}, // second line must not win
		{"no version here", ""},
		{"", ""},
	} {
		if got := ParseToolVersion(c.out); got != c.want {
			t.Errorf("ParseToolVersion(%q) = %q, want %q", c.out, got, c.want)
		}
	}
}

func TestCompareToolVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.18.30", "1.10.0", 1},
		{"1.9.99", "1.10.0", -1}, // numeric, not lexical
		{"2.1.270", "2.1.270", 0},
		{"2.1", "2.1.0", 0}, // shorter padded with zeros
		{"2026.09.10", "2026.01.01", 1},
		{"0.153.4-rc1", "0.153.4", 0}, // suffixes ignored
	} {
		if got := CompareToolVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareToolVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// probe builds a stub machine: found maps bin -> --version output.
func probe(t *testing.T, bin string, found map[string]string, runErr error) ToolStatus {
	t.Helper()
	p, ok := ToolPinFor(pinTransport(t, bin))
	if !ok {
		t.Fatalf("no pin for %s", bin)
	}
	return ProbeTool(p,
		func(name string) (string, error) {
			if _, ok := found[name]; !ok {
				return "", errors.New("not found")
			}
			return "/usr/local/bin/" + name, nil
		},
		func(path string, _ ...string) ([]byte, error) {
			return []byte(found[strings.TrimPrefix(path, "/usr/local/bin/")]), runErr
		})
}

func pinTransport(t *testing.T, bin string) Transport {
	t.Helper()
	for _, p := range Toolchain() {
		if p.Bin == bin {
			return p.Transport
		}
	}
	t.Fatalf("unknown bin %s", bin)
	return ""
}

func TestProbeToolStates(t *testing.T) {
	if s := probe(t, "claude", map[string]string{}, nil); s.State != ToolMissing || !s.Blocked() {
		t.Errorf("absent claude: %+v", s)
	}
	if s := probe(t, "claude", map[string]string{"claude": "2.1.280 (Claude Code)"}, nil); s.State != ToolOK || s.Version != "2.1.280" {
		t.Errorf("tested claude: %+v", s)
	}
	if s := probe(t, "claude", map[string]string{"claude": "1.9.0 (Claude Code)"}, nil); s.State != ToolOld || !s.Blocked() {
		t.Errorf("below minimum: %+v", s)
	}
	// Newer than tested is usable but named, so an evidence run cannot quote
	// the manifest's version while running another.
	s := probe(t, "claude", map[string]string{"claude": "9.9.9 (Claude Code)"}, nil)
	if s.State != ToolNewer || s.Blocked() || !strings.Contains(s.Detail, "2.1.280") {
		t.Errorf("newer than tested: %+v", s)
	}
	// A binary called codex that is not codex is blocked, not trusted.
	if s := probe(t, "codex", map[string]string{"codex": "1.2.3 (some other tool)"}, nil); s.State != ToolMismatch || !s.Blocked() {
		t.Errorf("impostor: %+v", s)
	}
	// A tool that prints no version is reported unknown, never as satisfying
	// the pin - and is not blocked for it.
	if s := probe(t, "opencode", map[string]string{"opencode": "dev build"}, nil); s.State != ToolUnknown || s.Blocked() {
		t.Errorf("unversioned: %+v", s)
	}
	if s := probe(t, "opencode", map[string]string{"opencode": ""}, errors.New("exit 1")); s.State != ToolUnknown || s.Blocked() {
		t.Errorf("failed probe: %+v", s)
	}
}

func TestToolchainPinsAreSelfConsistent(t *testing.T) {
	seen := map[Transport]bool{}
	for _, p := range Toolchain() {
		if seen[p.Transport] {
			t.Errorf("%s: duplicate transport %s", p.Bin, p.Transport)
		}
		seen[p.Transport] = true
		if CompareToolVersions(p.Tested, p.Min) < 0 {
			t.Errorf("%s: tested %s is below its own minimum %s", p.Bin, p.Tested, p.Min)
		}
		if !strings.Contains(p.Rollback, p.Tested) {
			t.Errorf("%s: rollback recipe does not pin the tested version: %s", p.Bin, p.Rollback)
		}
		for _, f := range []string{p.Install, p.Upgrade, p.Rollback} {
			if strings.TrimSpace(f) == "" {
				t.Errorf("%s: empty recipe", p.Bin)
			}
		}
	}
	// Every transport a leg can be driven through must have a pin - or, when
	// it drives no binary, an API contract - or doctor would report an
	// adapter it has no contract for.
	for _, s := range Registry() {
		if !TransportCovered(s.Transport) {
			t.Errorf("leg %s: transport %s has neither a pinned adapter nor an API contract", s.ID, s.Transport)
		}
	}
	for _, c := range APIContracts() {
		if seen[c.Transport] {
			t.Errorf("%s: both pinned and under an API contract", c.Transport)
		}
		if c.Endpoint == "" || c.Versioned == "" {
			t.Errorf("%s: an API contract names its endpoint and how the served version is learned", c.Transport)
		}
	}
	if TransportCovered("nope") {
		t.Error("an unknown transport is not covered")
	}
}
