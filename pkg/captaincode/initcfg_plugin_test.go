package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Captain Code stopped forking opencode: routing and the panels are two
// plugins stock opencode loads from `plugin:`. init owns that config, so init
// is what registers them - otherwise every machine needs a hand-edited file
// and a fresh checkout silently runs an unrouted opencode.

func pluginSrc(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "plugin", "captain-ui"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "plugin", "captain.ts"), []byte("export default {}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "plugin", "captain-ui", "index.tsx"), []byte("export default {}"), 0o644))
	return src
}

func pluginList(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(StripJSONC(body), &cfg))
	raw, _ := cfg["plugin"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestEnsurePluginsPutsEachHalfInTheConfigThatReadsIt(t *testing.T) {
	// The panels never appeared the first time because both entries went into
	// opencode.jsonc. The TUI reads its OWN config (tui.json); a UI plugin
	// listed in opencode.jsonc is silently never loaded.
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	src := pluginSrc(t)
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(path, []byte(`{"$schema":"https://opencode.ai/config.json"}`), 0o644))

	changed, err := EnsureCaptainPlugins(path, src)
	require.NoError(t, err)
	assert.True(t, changed)

	server := pluginList(t, path)
	require.Len(t, server, 1, "opencode's config carries the router only")
	assert.Contains(t, server[0], "plugin/captain.ts")
	assert.True(t, strings.HasPrefix(server[0], "file:///"),
		"a GUI-launched opencode inherits neither PATH nor cwd: %s", server[0])

	ui := pluginList(t, TuiConfigPath())
	require.Len(t, ui, 1, "the TUI config carries the panels")
	assert.Contains(t, ui[0], "plugin/captain-ui/index.tsx")
	assert.True(t, strings.HasPrefix(ui[0], "file:///"), ui[0])
}

func TestEnsurePluginsCreatesTheTuiConfigWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	src := pluginSrc(t)
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o644))

	// A machine that has never customised the TUI has no tui.json at all.
	require.NoFileExists(t, TuiConfigPath())
	_, err := EnsureCaptainPlugins(path, src)
	require.NoError(t, err)
	assert.FileExists(t, TuiConfigPath(), "init writes it rather than skipping the panels")
}

func TestEnsurePluginsIsIdempotentAndKeepsForeignEntries(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := pluginSrc(t)
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(path, []byte(`{"plugin":["some-other-plugin"]}`), 0o644))

	_, err := EnsureCaptainPlugins(path, src)
	require.NoError(t, err)
	changed, err := EnsureCaptainPlugins(path, src)
	require.NoError(t, err)
	assert.False(t, changed, "a second run must not rewrite the file")

	got := pluginList(t, path)
	assert.Contains(t, got, "some-other-plugin", "another plugin the user installed is never dropped")
	assert.Len(t, got, 2, "the router joins it here; the panels live in the TUI config")
}

func TestEnsurePluginsSkipsWhatIsNotOnDisk(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o644))

	// An install without the plugin sources (binary only) must not register
	// paths that do not exist: opencode logs a load failure for each one.
	changed, err := EnsureCaptainPlugins(path, filepath.Join(t.TempDir(), "nowhere"))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Empty(t, pluginList(t, path))
}
