package captaincode

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Captain's own version contract (ROADMAP M1.1): the brain binary and the
// terminal fork must appear in the same report as the adapters, and the
// report must distinguish a revision an evidence run may quote from one it
// may not.

func stamp(rev string, modified, ok bool) func() (string, bool, bool) {
	return func() (string, bool, bool) { return rev, modified, ok }
}

func forkAt(t *testing.T, packageManager string) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"name":"captaincode"`
	if packageManager != "" {
		body += `,"packageManager":"` + packageManager + `"`
	}
	body += "}"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644))
	return dir
}

func TestCleanBuildIsQuotableAndDirtyBuildIsNot(t *testing.T) {
	base := SelfProbe{
		Executable: func() (string, error) { return "/opt/bin/captain", nil },
		LookPath:   func(string) (string, error) { return "/opt/bin/captain", nil },
		Git:        func(string, ...string) ([]byte, error) { return nil, errors.New("no checkout") },
		ReadFile:   func(string) ([]byte, error) { return nil, errors.New("no checkout") },
	}

	base.BuildInfo = stamp("abcdef1234567890", false, true)
	clean := ProbeSelf(base)[0]
	assert.Equal(t, ToolOK, clean.State)
	assert.Equal(t, "abcdef1", clean.Version, "the revision is reported short")
	assert.True(t, clean.Reproducible())

	base.BuildInfo = stamp("abcdef1234567890", true, true)
	dirty := ProbeSelf(base)[0]
	assert.Equal(t, ToolDirty, dirty.State)
	assert.Equal(t, "abcdef1", dirty.Version, "a dirty build still names what it was built from")
	assert.False(t, dirty.Reproducible(), "a modified tree cannot be quoted by an evidence run")
	assert.False(t, dirty.Blocked(), "unreproducible is not broken - the developer build still runs")

	base.BuildInfo = stamp("", false, false)
	unstamped := ProbeSelf(base)[0]
	assert.Equal(t, ToolUnknown, unstamped.State)
	assert.False(t, unstamped.Reproducible())
	assert.False(t, unstamped.Blocked())
}

func TestAnotherCaptainOnPathIsAMismatchButKeepsTheRevision(t *testing.T) {
	s := ProbeSelf(SelfProbe{
		Executable: func() (string, error) { return "/tmp/go-build/captain", nil },
		LookPath:   func(string) (string, error) { return "/opt/bin/captain", nil },
		BuildInfo:  stamp("abcdef1234567890", false, true),
		Git:        func(string, ...string) ([]byte, error) { return nil, errors.New("no checkout") },
		ReadFile:   func(string) ([]byte, error) { return nil, errors.New("no checkout") },
	})[0]

	assert.Equal(t, ToolMismatch, s.State)
	assert.True(t, s.Blocked(), "the next command would run a binary this report never probed")
	assert.Equal(t, "abcdef1", s.Version, "the running binary's revision is still the truth about this report")
	assert.Contains(t, s.Detail, "/opt/bin/captain")
}

func TestTerminalForkReportsItsCheckoutAndItsAbsence(t *testing.T) {
	dir := forkAt(t, "bun@1.3.14")
	git := func(clean bool) func(string, ...string) ([]byte, error) {
		return func(_ string, args ...string) ([]byte, error) {
			if args[0] == "rev-parse" {
				return []byte("0d48e2affffffff\n"), nil
			}
			if clean {
				return nil, nil
			}
			return []byte(" M cmd/captaincode/main.go\n"), nil
		}
	}

	s := ProbeSelf(SelfProbe{SourceDir: dir, BuildInfo: stamp("", false, false), Git: git(true)})[1]
	assert.Equal(t, ToolOK, s.State)
	assert.Equal(t, "0d48e2a", s.Version)

	s = ProbeSelf(SelfProbe{SourceDir: dir, BuildInfo: stamp("", false, false), Git: git(false)})[1]
	assert.Equal(t, ToolDirty, s.State)
	assert.False(t, s.Reproducible())

	// Brain-only: no fork is a supported shape, not a failure.
	s = ProbeSelf(SelfProbe{SourceDir: filepath.Join(dir, "absent"), BuildInfo: stamp("", false, false), Git: git(true)})[1]
	assert.Equal(t, ToolMissing, s.State)
	assert.False(t, s.Blocked())
	assert.Contains(t, s.Detail, "brain-only")
}

func TestBunPinComesFromTheForksOwnPackageJSON(t *testing.T) {
	pin, ok := BunPin(forkAt(t, "bun@1.3.14"), nil)
	require.True(t, ok)
	assert.Equal(t, "1.3.14", pin.Tested)
	assert.Equal(t, pin.Tested, pin.Min, "an exact packageManager pin is both floor and ceiling")
	assert.Contains(t, pin.Rollback, "1.3.14")

	_, ok = BunPin(forkAt(t, ""), nil)
	assert.False(t, ok, "no packageManager field means no pin to quote")

	_, ok = BunPin(forkAt(t, "pnpm@9.0.0"), nil)
	assert.False(t, ok, "a fork that does not install with bun has no bun pin")

	_, ok = BunPin("", nil)
	assert.False(t, ok)
}

func TestBunPinProbesThroughTheSameContractAsTheAdapters(t *testing.T) {
	pin, ok := BunPin(forkAt(t, "bun@1.3.14"), nil)
	require.True(t, ok)
	look := func(string) (string, error) { return "/opt/bin/bun", nil }

	at := ProbeTool(pin, look, func(string, ...string) ([]byte, error) { return []byte("1.3.14\n"), nil })
	assert.Equal(t, ToolOK, at.State)

	above := ProbeTool(pin, look, func(string, ...string) ([]byte, error) { return []byte("1.4.0\n"), nil })
	assert.Equal(t, ToolNewer, above.State)

	below := ProbeTool(pin, look, func(string, ...string) ([]byte, error) { return []byte("1.2.0\n"), nil })
	assert.Equal(t, ToolOld, below.State, "below the fork's own pin the terminal is not the one that was tested")
}
