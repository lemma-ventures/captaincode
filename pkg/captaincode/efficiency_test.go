package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTokenBucketsNoDoubleCounting(t *testing.T) {
	c := ClaudeTokenUsage(10, 5, 100, 20)
	require.Equal(t, 135, *c.Input+*c.Output+*c.CacheRead+*c.CacheWrite)
	cached := 100
	d := InclusiveTokenUsage(130, 5, &cached, "codex")
	require.Equal(t, 30, *d.Input)
	require.Nil(t, d.CacheWrite)
	require.Equal(t, 135, *d.Input+*d.Output+*d.CacheRead)
	require.Nil(t, InclusiveTokenUsage(130, 5, nil, "codex").Input)
	bad := 131
	require.Nil(t, InclusiveTokenUsage(130, 5, &bad, "codex").CacheRead)
	require.Nil(t, OptionalInclusiveTokenUsage(nil, nil, tokenCount(0), "codex").Input)
	require.Nil(t, OptionalInclusiveTokenUsage(tokenCount(10), nil, tokenCount(0), "codex").Output)
	require.Nil(t, MergeTokenUsage(nil, d))
	require.Nil(t, MergeTokenUsage(c, d))
	require.Equal(t, 60, *MergeTokenUsage(d, d).Input)
}
func TestPromptReuseAndTelemetryPrivacy(t *testing.T) {
	t.Setenv("CAPTAIN_EFFICIENCY_LOG", "")
	var tr PromptReuseTracker
	a := strings.Repeat("private fixture ", 100)
	first := tr.Observe("private workspace", a)
	require.False(t, first.Comparable)
	second := tr.Observe("private workspace", a+"next turn")
	require.True(t, second.Comparable)
	require.Greater(t, second.CommonPrefixBytes, 1000)
	changed := tr.Observe("private workspace", "CHANGED"+a)
	require.Zero(t, changed.CommonPrefixBytes)
	t.Setenv("HOME", t.TempDir())
	LogEfficiency(second)
	raw, e := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".captaincode", "efficiency.jsonl"))
	require.NoError(t, e)
	require.NotContains(t, string(raw), "private")
	require.True(t, json.Valid(raw))
}
func TestEuclidLeanSchemaBudget(t *testing.T) {
	t.Setenv("CAPTAIN_EUCLID_TOOL_PROFILE", "full")
	full := EuclidToolSchemaUsage()
	require.Greater(t, full.Count, 2)
	t.Setenv("CAPTAIN_EUCLID_TOOL_PROFILE", "lean")
	lean := EuclidToolSchemaUsage()
	require.Equal(t, 2, lean.Count)
	require.Less(t, lean.EstimatedTokens, full.EstimatedTokens)
	for _, tool := range EuclidTools() {
		require.Contains(t, []string{"search", "code_search"}, tool["name"])
	}
}

func TestCheaperCapableTieRespectsExplicitQuality(t *testing.T) {
	normal := BuildPickPrompt("fix a typo", ClassTrivial, "", nil, LegClaude)
	require.Contains(t, normal, "cheap-capable-v1")
	require.Contains(t, normal, "Uncertainty about safety or capability is not a tie")
	quality := BuildPickPrompt("audit security", ClassHigh, "quality", nil, LegClaude)
	require.Contains(t, quality, "strongest row whatever it costs")
	require.NotContains(t, quality, "Tie-break policy cheap-capable-v1")
	l := &Ledger{}
	l.RecordDecision(Decision{Path: PathPick, Chosen: LegClaude, TiePolicy: "cheap-capable-v1"})
	d, ok := l.LastDecision()
	require.True(t, ok)
	require.Equal(t, "cheap-capable-v1", d.TiePolicy)
	require.NotNil(t, d.ChosenFrontier)
}

func TestClaudeUsagePresence(t *testing.T) {
	var event claudeStreamLine
	require.NoError(t, json.Unmarshal([]byte(`{"type":"result","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}`), &event))
	require.Equal(t, 135, event.Usage.Total())
	require.Equal(t, 20, *event.Usage.Detail().CacheWrite)
	event = claudeStreamLine{}
	require.NoError(t, json.Unmarshal([]byte(`{"type":"result","usage":{"input_tokens":10,"output_tokens":5}}`), &event))
	require.Nil(t, event.Usage.Detail().CacheRead)
	event = claudeStreamLine{}
	require.Nil(t, event.Usage.Detail())
	require.Zero(t, event.Usage.Total())
	require.NoError(t, json.Unmarshal([]byte(`{"type":"result","usage":{"input_tokens":-1,"output_tokens":5}}`), &event))
	require.Nil(t, event.Usage.Detail().Input)
	require.Equal(t, 5, event.Usage.Total())
}

func TestTokenUsageJournalRoundTrip(t *testing.T) {
	l, path := journalLedger(t)
	ms := int64(7)
	l.Record(Event{TaskID: "task-1", Leg: LegClaude, Tokens: 135, TokenUsage: ClaudeTokenUsage(10, 5, 100, 20), TTFTMs: &ms, Outcome: "ok"})
	rows := ReadRoutingLog(path, 0)
	require.Len(t, rows, 1)
	require.Equal(t, 100, *rows[0].Event.TokenUsage.CacheRead)
	require.Equal(t, int64(7), *rows[0].Event.TTFTMs)
}

func TestTokenStreamKeepsUsageOnFailure(t *testing.T) {
	// No here-document: this fixture needs no shell temporary file.
	script := "#!/bin/sh\ncat >/dev/null &\nprintf '%s\\n' '" +
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}}` + "' '" +
		`{"type":"result","is_error":true,"result":"failed","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":20,"cache_creation_input_tokens":0}}` + "'\n"
	fakeBin(t, "claude", script)
	res, err := runClaudeStream(t.TempDir(), "fixture", time.Minute, nil, nil)
	require.Error(t, err)
	require.NotNil(t, res.TTFTMs)
	require.NotNil(t, res.TokenUsage)
	require.Equal(t, 20, *res.TokenUsage.CacheRead)
	require.Equal(t, 35, res.Tokens)
}
