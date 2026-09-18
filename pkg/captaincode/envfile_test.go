package captaincode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCaptainEnvFillsOnlyWhatIsUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, "captain"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "captain", "env"), []byte(
		"# provider keys\nexport OPENROUTER_API_KEY=sk-or-test\nCAPTAIN_TEST_QUOTED=\"a b\"\nCAPTAIN_TEST_PRESET=file\n\nnot a pair\n"), 0o600))
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
