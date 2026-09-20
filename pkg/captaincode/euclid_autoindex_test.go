package captaincode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A burst of journal writes rebuilds the index once, and a write during a
// rebuild schedules exactly one more - never one per write, never none.
func TestJournalWritesRebuildTheIndexOncePerBurst(t *testing.T) {
	t.Setenv("CAPTAIN_EUCLID_AUTOINDEX", "1")
	repo := t.TempDir()
	root := filepath.Join(repo, ".euclid")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "BRAIN.md"), []byte("# BRAIN\n"), 0o644))
	// A vendored "engine" that only counts its runs.
	counter := filepath.Join(repo, "runs")
	script := "import os\nopen(os.environ['COUNTER'], 'a').write('x')\n"
	for _, n := range []string{"build-catalog.py", "build-dashboard.py"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "bin", n), []byte(script), 0o644))
	}
	t.Setenv("COUNTER", counter)

	old := autoIndexDelay
	autoIndexDelay = 50 * time.Millisecond
	defer func() { autoIndexDelay = old }()

	dev := EuclidBrain{Root: filepath.Join(root, "developers", "me"), Kind: "developer"}
	assert.Equal(t, root, indexRootOf(dev), "a developer subtree is indexed with its repo brain")

	for i := 0; i < 6; i++ {
		ScheduleReindex(dev) // six workers of one team turn
	}
	runs := func() int {
		b, _ := os.ReadFile(counter)
		return len(strings.TrimSpace(string(b)))
	}
	require.Eventually(t, func() bool { return runs() >= 2 }, 5*time.Second, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 2, runs(), "one rebuild (two scripts) for the burst")

	ScheduleReindex(EuclidBrain{Root: root, Kind: "repo"})
	require.Eventually(t, func() bool { return runs() >= 4 }, 5*time.Second, 20*time.Millisecond)
}

func TestAutoIndexIsOffByEnvAndOffBrainRoots(t *testing.T) {
	t.Setenv("CAPTAIN_EUCLID_AUTOINDEX", "0")
	ScheduleReindex(EuclidBrain{Root: t.TempDir(), Kind: "repo"}) // must not panic or schedule
	t.Setenv("CAPTAIN_EUCLID_AUTOINDEX", "1")
	ScheduleReindex(EuclidBrain{Root: t.TempDir(), Kind: "repo"}) // not a .euclid: ignored
	autoIndex.mu.Lock()
	defer autoIndex.mu.Unlock()
	assert.Empty(t, autoIndex.pending)
}
