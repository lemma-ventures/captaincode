package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionLess(t *testing.T) {
	assert.True(t, versionLess("0.153.4", "0.154.0"))
	assert.True(t, versionLess("2.1.9", "2.1.10"))
	assert.False(t, versionLess("2.1.270", "2.1.270"))
	assert.False(t, versionLess("1.18.30", "1.18.3"))
}

func TestRosterRanksFrontierFirstAndFlagsUpgrades(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.rosterHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/roster", nil))
	require.Equal(t, 200, rec.Code)
	var out struct {
		PerfSource string `json:"perf_source"`
		Legs       []rosterLeg
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "snapshot", out.PerfSource, "no key in tests: the compiled snapshot")
	require.NotEmpty(t, out.Legs)
	assert.Equal(t, "claude", out.Legs[0].Leg, "ranked by perf: the Opus 5.5 row leads the compiled snapshot of 2026-09-22")
	assert.True(t, out.Legs[0].Frontier)
	byLeg := map[string]rosterLeg{}
	for _, l := range out.Legs {
		byLeg[l.Leg] = l
		assert.NotEqual(t, "frontier", l.Leg, "the pseudo-leg is a mode, not a roster row")
	}
	assert.True(t, byLeg["grok-max"].Frontier)
	// model × route; effort is per request, never in the name (2026-09-13)
	assert.Equal(t, "codex-cli", byLeg["codex-cli"].Label, "the CLI leg")
	assert.Equal(t, "codex-openai", byLeg["codex"].Label, "the model pin through opencode's openai provider")
	assert.Equal(t, "claude-cli", byLeg["claude"].Label)
	assert.Equal(t, "cursor-cli", byLeg["cursor"].Label)
	assert.Equal(t, "grok-max-xai", byLeg["grok-max"].Label)
	assert.Equal(t, "glm-orouter", byLeg["glm"].Label)
	assert.Equal(t, "kimi-nim", byLeg["kimi"].Label)
	assert.Equal(t, "free-zen", byLeg["free"].Label)
	assert.False(t, byLeg["glm"].Frontier, "glm ranks near grok-max but stays in Models")
	assert.True(t, byLeg["glm"].OpenWeight)
	require.NotNil(t, byLeg["gemini"].Upgrade, "gemini-3-7-flash has 3-8-flash above it")
	assert.Equal(t, "gemini-3-8-flash", byLeg["gemini"].Upgrade.Slug)
}

// ── the feed refresh: cadence and what it says ─────────────────────────────

func TestPerfRefreshRetriesHourlyWithoutAKeyAndSaysSoOnce(t *testing.T) {
	b := teamBrain()
	b.roster.key = func() string { return "" }
	b.roster.fetch = func(string) ([]captaincode.AAModel, error) {
		t.Fatal("no key: the feed must not be read")
		return nil, nil
	}
	assert.Equal(t, perfRetryEvery, b.refreshPerf(), "checks for a key hourly, not daily")
	assert.True(t, b.roster.keyWarned)
	assert.Equal(t, perfRetryEvery, b.refreshPerf())
	assert.Empty(t, b.acts, "a missing key is a log line, not a sidebar item")
}

func TestPerfRefreshRetriesHourlyAfterAFailedFetch(t *testing.T) {
	b := teamBrain()
	b.roster.key = func() string { return "k" }
	b.roster.fetch = func(string) ([]captaincode.AAModel, error) { return nil, errors.New("HTTP 503") }
	assert.Equal(t, perfRetryEvery, b.refreshPerf())
	_, src, _ := captaincode.PerfModels()
	assert.Equal(t, "snapshot", src, "a failed fetch keeps the ranking it had")
}

func TestPerfRefreshAnnouncesWhatTheFeedChanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the fetch is cached under HOME
	t.Setenv("CAPTAIN_PERF_REFRESH", "")
	t.Cleanup(captaincode.PerfResetToSnapshot)
	prev, _, _ := captaincode.PerfModels()
	next := append([]captaincode.AAModel{}, prev...)
	next = append(next, captaincode.AAModel{Slug: "grok-4-8", Name: "Grok 4.8", Creator: "SpaceXAI", Released: "2026-10-01", IntelligenceIndex: 49})
	b := teamBrain()
	b.roster.key = func() string { return "k" }
	b.roster.fetch = func(string) ([]captaincode.AAModel, error) { return next, nil }

	assert.Equal(t, perfRefreshEvery, b.refreshPerf(), "six hours between reads once the feed answers")
	_, src, _ := captaincode.PerfModels()
	assert.Equal(t, "live", src)
	require.NotEmpty(t, b.acts, "the news reaches every open TUI's Last Runs")
	for _, a := range b.acts {
		assert.Equal(t, "feed", a.Kind)
		assert.Equal(t, "", a.Dir, "machine-wide: the ranking is not one workspace's")
	}
	texts := ""
	for _, a := range b.acts {
		texts += a.Text + "\n"
	}
	assert.Contains(t, texts, "new on the ranking: grok-4-8 (49)")
	assert.Contains(t, texts, "⇡ grok-max: grok-4-8 (49) outscores grok-4-7 (46)", "a newer family member outscoring the pin is a ⇡ with what to do about it")
	assert.Contains(t, texts, "captain upgrade --models")

	b.acts = nil
	assert.Equal(t, perfRefreshEvery, b.refreshPerf())
	assert.Empty(t, b.acts, "the same feed again is no news")
}

func TestPerfRefreshIntervalIsConfigurable(t *testing.T) {
	t.Setenv("CAPTAIN_PERF_REFRESH", "12h")
	assert.Equal(t, 12*time.Hour, perfRefreshInterval())
	t.Setenv("CAPTAIN_PERF_REFRESH", "5s")
	assert.Equal(t, perfRefreshEvery, perfRefreshInterval(), "under a minute is a typo, not a cadence")
	t.Setenv("CAPTAIN_PERF_REFRESH", "")
	assert.Equal(t, perfRefreshEvery, perfRefreshInterval())
}

func TestPerfNewsLinesCapTheArrivalsAndNameTheMoves(t *testing.T) {
	row := func(slug string, perf float64) captaincode.PerfRow {
		return captaincode.PerfRow{Slug: slug, Perf: perf}
	}
	lines := perfNewsLines(captaincode.PerfNews{
		Arrivals: []captaincode.PerfRow{row("a", 60), row("b", 50), row("c", 40), row("d", 30), row("e", 20), row("f", 10)},
		Moved:    []captaincode.PerfMove{{Leg: captaincode.LegGrokMax, From: row("grok-4-6", 44.3), To: row("grok-4-7", 46.4)}, {Leg: captaincode.LegCursor, To: row("cursor-composer", 41)}},
	})
	require.Len(t, lines, 3)
	assert.Equal(t, "new on the ranking: a (60), b (50), c (40), d (30) +2 more", lines[0])
	assert.Equal(t, "grok-max now reads grok-4-7 (46), was grok-4-6 (44)", lines[1])
	assert.Equal(t, "cursor is now listed: cursor-composer (41)", lines[2])
	assert.Empty(t, perfNewsLines(captaincode.PerfNews{}))
}

func TestRosterReportsWhetherTheRankingCanRefresh(t *testing.T) {
	b := teamBrain()
	b.roster.key = func() string { return "" }
	rec := httptest.NewRecorder()
	b.rosterHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/roster", nil))
	var out struct {
		PerfKey    bool `json:"perf_key"`
		PerfModels int  `json:"perf_models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.False(t, out.PerfKey, "no key: the sidebar says the ranking cannot get fresher")
	assert.Greater(t, out.PerfModels, 600)
}
