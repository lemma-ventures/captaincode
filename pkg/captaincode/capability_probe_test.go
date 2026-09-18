package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProbeCapabilities_VerifiesClaudeFlags(t *testing.T) {
	lookPath := func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte("Usage: claude [options]\n  --print, -p       Run in headless mode\n  --permission-mode  Set permission mode\n  --output-format    Output format (text, stream-json)\n"), nil
	}
	results := ProbeCapabilities(lookPath, run)
	for _, r := range results {
		if r.Transport != TransportClaudeCLI {
			continue
		}
		switch r.Cap {
		case CapTools, CapPermissions, CapUsage, CapCost:
			assert.Equal(t, CapProbeVerified, r.State, "%s: flag should be found in --help", r.Cap)
		case CapCancel, CapSessionReuse:
			assert.Equal(t, CapProbeSkip, r.State, "%s: not CLI-probable", r.Cap)
		}
	}
}

func TestProbeCapabilities_ReportsMissingFlag(t *testing.T) {
	lookPath := func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte("Usage: claude [options]\n  --print, -p       Run in headless mode\n"), nil
	}
	results := ProbeCapabilities(lookPath, run)
	var perms CapProbeResult
	for _, r := range results {
		if r.Transport == TransportClaudeCLI && r.Cap == CapPermissions {
			perms = r
		}
	}
	assert.Equal(t, CapProbeMissing, perms.State, "--permission-mode absent → missing")
	assert.Contains(t, perms.Detail, "--permission-mode")
}

func TestProbeCapabilities_NoBinarySkipsAll(t *testing.T) {
	lookPath := func(bin string) (string, error) { return "", errNotFound }
	run := func(_ string, _ ...string) ([]byte, error) { return nil, nil }
	results := ProbeCapabilities(lookPath, run)
	for _, r := range results {
		if r.Transport == TransportCursorCLI && r.Cap == CapTools {
			assert.Equal(t, CapProbeNoBinary, r.State)
			return
		}
	}
	t.Fatal("cursor-agent CapTools not found in results")
}

func TestProbeCapabilities_VerifiesCodexFlags(t *testing.T) {
	lookPath := func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte("Usage: codex [command]\n  exec              Execute a prompt\n  --sandbox         Set sandbox mode\n  --json            Output as JSON\n"), nil
	}
	results := ProbeCapabilities(lookPath, run)
	for _, r := range results {
		if r.Transport != TransportCodexCLI {
			continue
		}
		switch r.Cap {
		case CapTools, CapPermissions, CapUsage:
			assert.Equal(t, CapProbeVerified, r.State, "%s should be verified", r.Cap)
		case CapCancel, CapSessionReuse, CapCost:
			assert.Equal(t, CapProbeSkip, r.State, "%s should be skipped", r.Cap)
		}
	}
}

func TestProbeCapabilities_VerifiesCursorFlags(t *testing.T) {
	lookPath := func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte("Usage: cursor-agent [options]\n  -p, --prompt      Run headless\n  --trust           Auto-trust workspace\n  --output-format   Output format\n"), nil
	}
	results := ProbeCapabilities(lookPath, run)
	for _, r := range results {
		if r.Transport != TransportCursorCLI {
			continue
		}
		switch r.Cap {
		case CapTools, CapPermissions:
			assert.Equal(t, CapProbeVerified, r.State, "%s should be verified", r.Cap)
		case CapCancel, CapSessionReuse, CapUsage, CapCost:
			assert.Equal(t, CapProbeSkip, r.State, "%s should be skipped", r.Cap)
		}
	}
}

func TestProbeCapabilities_VerifiesOpencodeServe(t *testing.T) {
	lookPath := func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte("Usage: opencode [command]\n  serve             Start the server\n  auth              Manage authentication\n"), nil
	}
	results := ProbeCapabilities(lookPath, run)
	for _, r := range results {
		if r.Transport != TransportOpencode {
			continue
		}
		switch r.Cap {
		case CapTools, CapSessionReuse:
			assert.Equal(t, CapProbeVerified, r.State, "%s should find serve subcommand", r.Cap)
		case CapCancel, CapUsage, CapCost, CapPermissions:
			assert.Equal(t, CapProbeSkip, r.State, "%s should be skipped (server-level)", r.Cap)
		}
	}
}

func TestCapProbeMark(t *testing.T) {
	assert.Equal(t, "✓", CapProbeMark(CapProbeVerified))
	assert.Equal(t, "✗", CapProbeMark(CapProbeMissing))
	assert.Equal(t, "·", CapProbeMark(CapProbeNoBinary))
	assert.Equal(t, "~", CapProbeMark(CapProbeSkip))
}

var errNotFound = notFoundErr{}

type notFoundErr struct{}

func (notFoundErr) Error() string { return "not found" }
