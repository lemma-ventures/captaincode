package captaincode

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live 2026-07-30: a paper audit ran claude and codex past the 8m cap and the
// turn delivered NOTHING - minutes of real review discarded because the last
// token never arrived. Output produced before the cap must survive it.
func TestClaudeTimeoutKeepsWhatItProduced(t *testing.T) {
	// A run is cut only when QUIET past its base cap (the progress contract):
	// the idle window is what ends a silent one.
	t.Setenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT", "200ms")
	fakeBin(t, "claude", `#!/bin/sh
cat <<'EOF2'
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Finding 1: the threat model omits X."}}}
EOF2
sleep 30
`)
	start := time.Now()
	// 2s, not 400ms: under a loaded full-suite run the shell takes longer than
	// that to print its first line, and the test then measures spawn latency
	// instead of salvage. The 20s bound below still proves the cap holds.
	res, err := runClaudeStreamOpts("", "audit the paper", 2*time.Second, 0, nil, nil, false, "", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerTimeout), "timeouts are their own class, not generic failures")
	assert.True(t, res.Partial, "salvaged output is marked partial")
	assert.Contains(t, res.Text, "Finding 1", "the work done before the cap is returned")
	assert.Less(t, time.Since(start), 20*time.Second, "the cap still bounds the run")
}

func TestClaudeTimeoutWithNoOutputStaysAnError(t *testing.T) {
	// A run is cut only when QUIET past its base cap (the progress contract):
	// the idle window is what ends a silent one.
	t.Setenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT", "200ms")
	fakeBin(t, "claude", "#!/bin/sh\nsleep 30\n")
	res, err := runClaudeStreamOpts("", "audit", 300*time.Millisecond, 0, nil, nil, false, "", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerTimeout))
	assert.Empty(t, strings.TrimSpace(res.Text))
	assert.False(t, res.Partial)
}

func TestWorkerTimeoutDefaultCoversDeepWork(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_TIMEOUT", "")
	assert.Equal(t, 15*time.Minute, workerTimeout(), "8m was too short for a paper audit")
	t.Setenv("CAPTAIN_WORKER_TIMEOUT", "25m")
	assert.Equal(t, 25*time.Minute, workerTimeout())
}
