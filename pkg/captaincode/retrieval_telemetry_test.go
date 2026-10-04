package captaincode

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetrievalTelemetryLoggingAndStats(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "retrieval.jsonl")
	t.Setenv("CAPTAIN_RETRIEVAL_LOG", logPath)

	now := time.Now()
	events := []RetrievalEvent{
		{
			At:             now.Add(-2 * time.Hour),
			Tool:           "code_search",
			Query:          "runOpenShell",
			Lane:           "code",
			DurationMS:     2.5,
			HitsCount:      5,
			TokensReturned: 120,
			TokensAvoided:  4500,
			Safe:           true,
			CodeIntelUsed:  true,
		},
		{
			At:             now.Add(-1 * time.Hour),
			Tool:           "search",
			Query:          "architecture",
			Lane:           "doc",
			DurationMS:     4.2,
			HitsCount:      3,
			TokensReturned: 200,
			TokensAvoided:  3000,
			Safe:           true,
			CodeIntelUsed:  false,
		},
		{
			At:             now,
			Tool:           "code_search",
			Query:          "AutoRoutes",
			Lane:           "code",
			DurationMS:     1.8,
			HitsCount:      2,
			TokensReturned: 90,
			TokensAvoided:  2500,
			Safe:           true,
			CodeIntelUsed:  true,
		},
	}

	for _, e := range events {
		LogRetrievalEvent(e)
	}

	read, err := ReadRetrievalEvents(logPath, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Len(t, read, 3)

	stats := ComputeRetrievalStats(read, 7)
	assert.Equal(t, 3, stats.TotalQueries)
	assert.Equal(t, 2, stats.CodeQueries)
	assert.Equal(t, 1, stats.DocQueries)
	assert.InDelta(t, 2.5, stats.MedianLatencyMS, 0.01)
	assert.Equal(t, 100.0, stats.SafetyRate)
	assert.Equal(t, 100.0, stats.HitRate)
	assert.InDelta(t, 66.6, stats.CodeIntelShare, 0.5)
	assert.Equal(t, 410, stats.TotalTokensReturned)
	assert.Equal(t, 10000, stats.TotalTokensAvoided)
	assert.True(t, stats.TokenSavingsPercent > 95.0)

	rendered := RenderRetrievalStats(stats)
	assert.Contains(t, rendered, "Euclid & CodeIntel Retrieval Efficiency")
	assert.Contains(t, rendered, "3 total (2 code, 1 doc)")
	assert.Contains(t, rendered, "safe containment")
}
