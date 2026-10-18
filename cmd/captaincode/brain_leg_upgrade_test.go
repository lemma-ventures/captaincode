package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func geminiFeed() []captaincode.AAModel {
	return []captaincode.AAModel{
		{Slug: "gemini-3-7-flash", Name: "Gemini 3.7 Flash", Creator: "Google", Released: "2026-09-01", IntelligenceIndex: 39, CodingIndex: 40, TokensPerSecond: 100, PriceIn: 0.38, PriceOut: 2},
		{Slug: "gemini-3-8-flash", Name: "Gemini 3.8 Flash", Creator: "Google", Released: "2026-10-01", IntelligenceIndex: 50, CodingIndex: 41, TokensPerSecond: 90, PriceIn: 0.40, PriceOut: 2.2},
	}
}

func TestAutoUpgradeWritesWhenIdleAndSkipsWhenBusy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_AUTO_UPGRADE", "models")
	t.Setenv("CAPTAIN_AUTO_UPGRADE_IN_TEST", "1")
	t.Setenv("CAPTAIN_GEMINI_MODEL", "")
	t.Setenv("CAPTAIN_GEMINI_PROVIDER", "")
	t.Cleanup(func() {
		captaincode.PerfResetToSnapshot()
		_, _ = captaincode.LoadRegistry(filepath.Join(t.TempDir(), "none.json"))
	})
	require.NoError(t, captaincode.SetPerfModels(geminiFeed()))

	b := teamBrain()
	b.roster.catalogue = func() (map[string][]string, error) {
		return map[string][]string{"openrouter": {"google/gemini-3.7-flash", "google/gemini-3.8-flash"}}, nil
	}

	b.inflightRuns.Add(1)
	assert.False(t, b.idleForUpgrade())
	assert.True(t, b.applyReadyPins(), "busy: wait, do not write")
	_, err := os.Stat(filepath.Join(home, ".captaincode", "legs.json"))
	assert.True(t, os.IsNotExist(err), "a busy brain must not write the overlay")
	b.inflightRuns.Add(-1)

	b.repeats = map[string]*repeatThread{"t": {}}
	assert.False(t, b.idleForUpgrade(), "a live repeat is not idle")
	b.repeats = nil
	b.live = &wfLive{}
	assert.False(t, b.idleForUpgrade(), "a live workflow is not idle")
	b.live = nil
	assert.True(t, b.idleForUpgrade())
	assert.False(t, b.applyReadyPins())
	spec, ok := captaincode.Spec(captaincode.LegGemini)
	require.True(t, ok)
	assert.Equal(t, "google/gemini-3.8-flash", spec.Model)
	assert.Equal(t, "gemini-3-8-flash", spec.AA)
	assert.Equal(t, 2.2, spec.PriceOut)
	assert.Contains(t, textsOf(b), "retargeted google/gemini-3.7-flash → google/gemini-3.8-flash while idle")
}

func TestAutoUpgradeDoesNotRunCLIUpdaterWhenBusy(t *testing.T) {
	t.Setenv("CAPTAIN_AUTO_UPGRADE", "cli")
	t.Setenv("CAPTAIN_AUTO_UPGRADE_IN_TEST", "1")
	called := false
	prev := runCLIUpdate
	runCLIUpdate = func(string, []string) (string, error) {
		called = true
		return "", nil
	}
	t.Cleanup(func() { runCLIUpdate = prev })

	b := teamBrain()
	b.inflightRuns.Add(1)
	b.upgradeCLIs()
	assert.False(t, called)
	b.inflightRuns.Add(-1)
}

func textsOf(b *brain) string {
	out := ""
	for _, a := range b.acts {
		out += a.Text + "\n"
	}
	return out
}
