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

func secretsFixture(t *testing.T) (envPath, secretsPath string) {
	t.Helper()
	t.Setenv(captaincode.SentTurnEnv, "")
	t.Setenv(captaincode.GuardEnv, "")
	t.Setenv("CAPTAIN_JAILED", "")
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, "captain"), 0o755))
	envPath = captaincode.CaptainEnvPath()
	secretsPath = captaincode.CaptainSecretsPath()
	require.Equal(t, filepath.Join(home, "captain", "secrets.env"), secretsPath)
	return envPath, secretsPath
}

func TestSecretsSplitMovesSecretLines(t *testing.T) {
	envPath, secretsPath := secretsFixture(t)
	require.NoError(t, os.WriteFile(envPath, []byte("# config\nCAPTAIN_AUTO_UPGRADE=all\nOPENROUTER_API_KEY=sk-or-1\nexport GITHUB_TOKEN=ghp_1\nCAPTAIN_LEGS=claude,codex\n"), 0o600))

	require.NoError(t, runSecrets([]string{"split"}))

	envRaw, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Equal(t, "# config\nCAPTAIN_AUTO_UPGRADE=all\nCAPTAIN_LEGS=claude,codex\n", string(envRaw))

	secretsRaw, err := os.ReadFile(secretsPath)
	require.NoError(t, err)
	assert.Contains(t, string(secretsRaw), "OPENROUTER_API_KEY=sk-or-1")
	assert.Contains(t, string(secretsRaw), "export GITHUB_TOKEN=ghp_1")

	info, err := os.Stat(secretsPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSecretsSplitDryRunWritesNothing(t *testing.T) {
	envPath, secretsPath := secretsFixture(t)
	require.NoError(t, os.WriteFile(envPath, []byte("OPENROUTER_API_KEY=sk-or-1\nCAPTAIN_LEGS=claude\n"), 0o600))

	require.NoError(t, runSecrets([]string{"split", "--dry-run"}))

	_, err := os.Stat(secretsPath)
	assert.True(t, os.IsNotExist(err), "dry run creates no secrets file")
	envRaw, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Contains(t, string(envRaw), "OPENROUTER_API_KEY=sk-or-1")
}

func TestSecretsSplitTerminalOnly(t *testing.T) {
	for _, name := range []string{captaincode.SentTurnEnv, captaincode.GuardEnv, "CAPTAIN_JAILED"} {
		t.Run(name, func(t *testing.T) {
			envPath, _ := secretsFixture(t)
			require.NoError(t, os.WriteFile(envPath, []byte("OPENROUTER_API_KEY=sk-or-1\n"), 0o600))
			t.Setenv(name, "1")
			err := runSecrets([]string{"split"})
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), "terminal only"))
		})
	}
}

func TestSecretsListShowsNamesOnly(t *testing.T) {
	envPath, _ := secretsFixture(t)
	require.NoError(t, os.WriteFile(envPath, []byte("OPENROUTER_API_KEY=sk-or-secret-value\nCAPTAIN_LEGS=claude\n"), 0o600))
	require.NoError(t, runSecrets([]string{"list"}))
	envRaw, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Contains(t, string(envRaw), "sk-or-secret-value", "list must not rewrite files")
}
