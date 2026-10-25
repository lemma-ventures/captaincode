package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The kimi leg moved to Kimi K3.1 Pro, but the TUI's model picker kept
// "Kimi K2.7-code (captain · opencode)": init refreshed limits and prices
// only (2026-10-25). A name captain generated - it carries "(captain ·" - now
// follows the registry; a name the user wrote is kept.
func TestRefreshCaptainModelEntriesRenamesGeneratedNames(t *testing.T) {
	want := LegDisplayName(LegKimi)
	cfg := map[string]any{"provider": map[string]any{"captain": map[string]any{"models": map[string]any{
		"kimi":   map[string]any{"name": "Kimi K2.7-code (captain · opencode)"},
		"claude": map[string]any{"name": "My Claude"},
	}}}}
	assert.True(t, refreshCaptainModelEntries(cfg))
	models := cfg["provider"].(map[string]any)["captain"].(map[string]any)["models"].(map[string]any)
	assert.Equal(t, want, models["kimi"].(map[string]any)["name"])
	assert.Equal(t, "My Claude", models["claude"].(map[string]any)["name"], "a name the user wrote is kept")
	assert.False(t, refreshCaptainModelEntries(cfg), "nothing left to change")
}
