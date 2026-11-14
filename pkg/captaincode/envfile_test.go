package captaincode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeEnv(t *testing.T, home, name, content string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "captain"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "captain", name), []byte(content), mode))
}

func TestLoadCaptainEnvFillsOnlyWhatIsUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeEnv(t, home, "env", "# provider keys\nexport OPENROUTER_API_KEY=sk-or-test\nCAPTAIN_TEST_QUOTED=\"a b\"\nCAPTAIN_TEST_PRESET=file\n\nnot a pair\n", 0o600)
	t.Setenv("CAPTAIN_TEST_PRESET", "shell")
	os.Unsetenv("OPENROUTER_API_KEY")
	os.Unsetenv("CAPTAIN_TEST_QUOTED")
	t.Cleanup(func() { os.Unsetenv("OPENROUTER_API_KEY"); os.Unsetenv("CAPTAIN_TEST_QUOTED") })

	set := LoadCaptainEnv()
	assert.ElementsMatch(t, []string{"OPENROUTER_API_KEY", "CAPTAIN_TEST_QUOTED"}, set)
	assert.Equal(t, "sk-or-test", os.Getenv("OPENROUTER_API_KEY"), "export prefix stripped")
	assert.Equal(t, "a b", os.Getenv("CAPTAIN_TEST_QUOTED"), "quotes stripped")
	assert.Equal(t, "shell", os.Getenv("CAPTAIN_TEST_PRESET"), "the shell's value wins")
}

func TestLoadCaptainEnvSecretsFileOverridesEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeEnv(t, home, "env", "CAPTAIN_TEST_SAME=from-env\nCAPTAIN_TEST_ONLY_ENV=x\n", 0o600)
	writeEnv(t, home, "secrets.env", "CAPTAIN_TEST_SAME=from-secrets\nCAPTAIN_TEST_ONLY_SECRETS=y\n", 0o600)
	os.Unsetenv("CAPTAIN_TEST_SAME")
	os.Unsetenv("CAPTAIN_TEST_ONLY_ENV")
	os.Unsetenv("CAPTAIN_TEST_ONLY_SECRETS")
	t.Cleanup(func() {
		os.Unsetenv("CAPTAIN_TEST_SAME")
		os.Unsetenv("CAPTAIN_TEST_ONLY_ENV")
		os.Unsetenv("CAPTAIN_TEST_ONLY_SECRETS")
	})

	set := LoadCaptainEnv()
	assert.ElementsMatch(t, []string{"CAPTAIN_TEST_SAME", "CAPTAIN_TEST_SAME", "CAPTAIN_TEST_ONLY_ENV", "CAPTAIN_TEST_ONLY_SECRETS"}, set)
	assert.Equal(t, "from-secrets", os.Getenv("CAPTAIN_TEST_SAME"), "the secrets file wins over the env file")
	assert.Equal(t, "x", os.Getenv("CAPTAIN_TEST_ONLY_ENV"))
	assert.Equal(t, "y", os.Getenv("CAPTAIN_TEST_ONLY_SECRETS"))
}

func TestLoadCaptainEnvRefusesLooseSecretsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeEnv(t, home, "env", "CAPTAIN_TEST_PLAIN=1\n", 0o600)
	writeEnv(t, home, "secrets.env", "CAPTAIN_TEST_LEAKED=2\n", 0o644)
	os.Unsetenv("CAPTAIN_TEST_PLAIN")
	os.Unsetenv("CAPTAIN_TEST_LEAKED")
	t.Cleanup(func() {
		os.Unsetenv("CAPTAIN_TEST_PLAIN")
		os.Unsetenv("CAPTAIN_TEST_LEAKED")
	})

	set := LoadCaptainEnv()
	assert.ElementsMatch(t, []string{"CAPTAIN_TEST_PLAIN"}, set)
	assert.Empty(t, os.Getenv("CAPTAIN_TEST_LEAKED"), "a group-readable secrets file is not loaded")
}

func TestIsSecretEnvName(t *testing.T) {
	for _, k := range []string{"OPENROUTER_API_KEY", "ANTHROPIC_AUTH_TOKEN", "GITHUB_SECRET", "X_PASSWORD", "DB_PASSWD", "MY_CREDENTIAL"} {
		assert.True(t, IsSecretEnvName(k), k)
	}
	for _, k := range []string{"CAPTAIN_AUTO_UPGRADE", "PATH", "MONOKER", "WORKER"} {
		assert.False(t, IsSecretEnvName(k), k)
	}
}
