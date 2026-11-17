package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cleanHomeTemplate = "CAPTAIN_AUTO_UPGRADE=all\n" +
	"CAPTAIN_REDACT=on\n" +
	"CAPTAIN_PROXY_ADDR=127.0.0.1:14098\n" +
	"CAPTAIN_PROXY_CLAUDE=1\n" +
	"CAPTAIN_PROXY_CODEX=1\n"

// buildTestCaptain builds the real captain binary once for the clean-home
// run. It reuses the module build cache, so a warmed checkout costs seconds.
func buildTestCaptain(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "captain-src")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", out)
	return binary
}

// cleanHomeEnv is the environment a fresh account starts with: its own home
// and the system path, nothing else. Every captain setting, credential, XDG
// override and worker guard from this test process is dropped, so anything the
// installed binary reports comes from the bundle or the empty home.
func cleanHomeEnv(home string) []string {
	return []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + home,
		"LC_ALL=C",
	}
}

// runInstalled runs the installed captain binary under the clean account
// environment and returns its stdout, stderr and wall time.
func runInstalled(t *testing.T, bin, home string, args ...string) (string, string, time.Duration, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = cleanHomeEnv(home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	return stdout.String(), stderr.String(), time.Since(start), err
}

// TestBundleCleanHomeEndToEnd installs a pinned bundle into an empty home and
// runs the installed binary there, which is the local half of machine-fleet
// plan step 5's clean-account run. The real account gate still needs a fresh
// login and a reboot; this proves the pieces first: verification and install
// work offline, the binary answers from the empty home, no host configuration
// or credential is read, and the home gains only the installed binary.
func TestBundleCleanHomeEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real captain binary")
	}

	hostHome, err := os.UserHomeDir()
	require.NoError(t, err)

	binary := buildTestCaptain(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(captaincode.SentTurnEnv, "")
	t.Setenv(captaincode.GuardEnv, "")
	t.Setenv("CAPTAIN_JAILED", "")

	dir := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, bundleCreate(dir, binary))

	var verifyOut strings.Builder
	start := time.Now()
	require.NoError(t, bundleVerify(dir, &verifyOut))
	verifyCost := time.Since(start)
	assert.Contains(t, verifyOut.String(), "ok  captain  ")
	assert.Contains(t, verifyOut.String(), "manifest sha256: ")

	dest := filepath.Join(home, ".local", "bin", "captain")
	start = time.Now()
	require.NoError(t, runBundle([]string{"install", dir}))
	installCost := time.Since(start)
	info, err := os.Stat(dest)
	require.NoError(t, err, "the default install path is inside the clean home")
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	bin := dest
	steps := []string{"secrets template", "secrets list", "link status", "bundle verify " + dir}
	var costs []time.Duration
	for _, step := range steps {
		args := strings.Fields(step)
		stdout, stderr, cost, runErr := runInstalled(t, bin, home, args...)
		costs = append(costs, cost)
		require.NoError(t, runErr, "captain %s: %s%s", step, stdout, stderr)
		for _, text := range []string{stdout, stderr} {
			require.NotContains(t, text, hostHome, "captain %s reported the host home", step)
			require.NotContains(t, text, "OPENROUTER_API_KEY", "captain %s reported a host credential name", step)
		}
		switch args[0] {
		case "template":
			assert.Equal(t, cleanHomeTemplate, stdout, "the template is the fixed five settings")
		case "list":
			assert.Contains(t, stdout, filepath.Join(home, ".config", "captain")+" (mode")
			assert.Contains(t, stdout, "(absent)")
			assert.NotContains(t, stdout, "[secret]")
		case "link":
			assert.Contains(t, stdout, "link key: not created")
			assert.Contains(t, stdout, "machine id: unconfigured")
		case "bundle":
			assert.Contains(t, stdout, "ok  captain  ")
		}
	}

	// The account gains the binary and, from the local-state readers, two
	// empty lock files. It gains no configuration, no credential, no link key
	// and no presence record: the commands answered from absence.
	configHome := filepath.Join(home, ".config", "captain")
	for _, absent := range []string{"env", "secrets.env", "link.key", "link.json", "presence.json"} {
		_, err := os.Lstat(filepath.Join(configHome, absent))
		assert.True(t, os.IsNotExist(err), "%s must not exist in a clean account", absent)
	}

	var gained []string
	require.NoError(t, filepath.Walk(home, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == home {
			return nil
		}
		rel, err := filepath.Rel(home, p)
		if err != nil {
			return err
		}
		gained = append(gained, rel)
		return nil
	}))
	// The ledger directory appears because main loads the ledger before it
	// dispatches a command. It stays empty and private at 0700.
	stateDir := filepath.Join(home, ".captaincode")
	stateInfo, err := os.Stat(stateDir)
	require.NoError(t, err, "main opened the ledger state directory")
	assert.Equal(t, os.FileMode(0o700), stateInfo.Mode().Perm())
	entries, err := os.ReadDir(stateDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a read-only command writes no state")

	allowed := map[string]bool{
		filepath.Join(".local", "bin", "captain"): true,
		".local":                             true,
		".local/bin":                         true,
		".config":                            true,
		".config/captain":                    true,
		".config/captain/link.json.lock":     true,
		".config/captain/presence.json.lock": true,
		".captaincode":                       true,
	}
	for _, rel := range gained {
		if allowed[rel] {
			if strings.HasSuffix(rel, ".lock") {
				lockInfo, err := os.Stat(filepath.Join(home, rel))
				require.NoError(t, err)
				assert.Equal(t, int64(0), lockInfo.Size(), "%s must be an empty lock file", rel)
			}
			continue
		}
		t.Errorf("the clean account gained an unexpected entry: %s", rel)
	}
	assert.Contains(t, gained, filepath.Join(".local", "bin", "captain"))

	t.Logf("verify %.3f ms, install %.3f ms, then secrets template %.3f ms, secrets list %.3f ms, link status %.3f ms, bundle verify %.3f ms",
		float64(verifyCost.Microseconds())/1000, float64(installCost.Microseconds())/1000,
		float64(costs[0].Microseconds())/1000, float64(costs[1].Microseconds())/1000,
		float64(costs[2].Microseconds())/1000, float64(costs[3].Microseconds())/1000)
}

// TestBundleCleanHomeRefusesGuardedInstall keeps the terminal-only rule true
// on a clean account: a worker or a jail running the installed binary cannot
// replace it.
func TestBundleCleanHomeRefusesGuardedInstall(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real captain binary")
	}
	home := t.TempDir()
	binary := buildTestCaptain(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, bundleCreate(dir, binary))

	bin := filepath.Join(home, ".local", "bin", "captain")
	t.Setenv(captaincode.GuardEnv, "1")
	err := runBundle([]string{"install", dir, "--bin", bin})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal only")
	_, statErr := os.Stat(bin)
	assert.True(t, os.IsNotExist(statErr), "a guarded run installs nothing")
}

// BenchmarkBundleVerify measures the offline check a clean account runs before
// it executes anything from a bundle: it re-hashes every artifact.
func BenchmarkBundleVerify(b *testing.B) {
	if testing.Short() {
		b.Skip("builds the real captain binary")
	}
	var out bytes.Buffer
	bundleBinary := bundleBenchBinary(b)
	dir := filepath.Join(b.TempDir(), "bundle")
	requireNoError(b, bundleCreate(dir, bundleBinary))
	info, err := os.Stat(filepath.Join(dir, bundleBinaryArtifact))
	requireNoError(b, err)
	b.SetBytes(info.Size())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		if err := bundleVerify(dir, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func bundleBenchBinary(b *testing.B) string {
	b.Helper()
	if runtime.GOOS == "" {
		b.Fatal("no platform")
	}
	var out bytes.Buffer
	binary := filepath.Join(b.TempDir(), "captain-src")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		b.Fatalf("go build failed: %s", out.String())
	}
	return binary
}

func requireNoError(b *testing.B, err error) {
	b.Helper()
	if err != nil {
		b.Fatal(err)
	}
}
