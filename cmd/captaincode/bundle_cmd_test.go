package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeBundle builds a bundle directory around one fake binary so the tests
// never run go build.
func makeBundle(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	binary := filepath.Join(src, "captain")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\necho fake captain\n"), 0o755))
	dir := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, runBundle([]string{"create", dir, "--binary", binary}))
	return dir
}

func TestBundleCreateAndVerifyRoundTrip(t *testing.T) {
	dir := makeBundle(t)
	var out strings.Builder
	require.NoError(t, bundleVerify(dir, &out))
	assert.Contains(t, out.String(), "ok  captain  28 bytes")
	assert.Contains(t, out.String(), "manifest sha256: ")
}

func TestBundleVerifyRefusesTamperedArtifacts(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"content changed": func(t *testing.T, dir string) {
			p := filepath.Join(dir, "captain")
			require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\necho tampered\n"), 0o755))
		},
		"size changed": func(t *testing.T, dir string) {
			p := filepath.Join(dir, "captain")
			require.NoError(t, os.WriteFile(p, []byte("short"), 0o755))
		},
		"mode changed": func(t *testing.T, dir string) {
			require.NoError(t, os.Chmod(filepath.Join(dir, "captain"), 0o700))
		},
		"missing artifact": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, "captain")))
		},
		"unlisted file": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "extra"), []byte("x"), 0o644))
		},
		"symlink artifact": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, "captain")))
			require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(dir, "captain")))
		},
		"symlink extra": func(t *testing.T, dir string) {
			require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(dir, "linked")))
		},
		"subdirectory": func(t *testing.T, dir string) {
			require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			dir := makeBundle(t)
			tamper(t, dir)
			var out strings.Builder
			err := bundleVerify(dir, &out)
			require.Error(t, err)
			assert.NotContains(t, out.String(), "ok  captain")
		})
	}
}

func TestBundleVerifyRefusesBadManifests(t *testing.T) {
	cases := map[string]func(t *testing.T, body string) string{
		"unknown field": func(t *testing.T, b string) string {
			return strings.Replace(b, `"format": 1`, `"format": 1, "extra": true`, 1)
		},
		"wrong format": func(t *testing.T, b string) string { return strings.Replace(b, `"format": 1`, `"format": 2`, 1) },
		"no artifacts": func(t *testing.T, b string) string { return strings.Replace(b, `"artifacts": [`, `"artifacts": [`, 1) },
		"traversal path": func(t *testing.T, b string) string {
			return strings.Replace(b, `"path": "captain"`, `"path": "../captain"`, 1)
		},
		"nested path": func(t *testing.T, b string) string {
			return strings.Replace(b, `"path": "captain"`, `"path": "bin/captain"`, 1)
		},
		"missing digest":     func(t *testing.T, b string) string { return strings.Replace(b, `"sha256": "`, `"sha256x": "`, 1) },
		"trailing content":   func(t *testing.T, b string) string { return b + "{}" },
		"unparsable created": func(t *testing.T, b string) string { return strings.Replace(b, `"created": "`, `"createdx": "`, 1) },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			dir := makeBundle(t)
			body, err := os.ReadFile(filepath.Join(dir, bundleManifestName))
			require.NoError(t, err)
			edited := edit(t, string(body))
			if name == "no artifacts" {
				edited = edited[:strings.Index(edited, `"artifacts"`)] + `"artifacts": []\n}`
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, bundleManifestName), []byte(edited), 0o644))
			var out strings.Builder
			require.Error(t, bundleVerify(dir, &out), "manifest:\n%s", edited)
		})
	}
	t.Run("absent manifest", func(t *testing.T) {
		dir := makeBundle(t)
		require.NoError(t, os.Remove(filepath.Join(dir, bundleManifestName)))
		require.Error(t, bundleVerify(dir, &strings.Builder{}))
	})
}

func TestBundleInstallVerifiesFirstAndDeploysAtomically(t *testing.T) {
	dir := makeBundle(t)
	dest := filepath.Join(t.TempDir(), "bin", "captain")
	require.NoError(t, runBundle([]string{"install", dir, "--bin", dest}))
	body, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho fake captain\n", string(body))
	info, err := os.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	matches, err := filepath.Glob(dest + ".bundle-new")
	require.NoError(t, err)
	assert.Empty(t, matches, "no staged file is left behind")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "captain"), []byte("tampered"), 0o755))
	err = runBundle([]string{"install", dir, "--bin", dest})
	require.Error(t, err)
	body, err = os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho fake captain\n", string(body), "a tampered bundle does not touch the installed binary")
}

func TestBundleInstallRefusesSentTurns(t *testing.T) {
	t.Setenv(captaincode.SentTurnEnv, "1")
	err := runBundle([]string{"install", makeBundle(t), "--bin", filepath.Join(t.TempDir(), "c")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal only")
}
