package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func initTestBrain() *brain {
	return &brain{
		ledger:  &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		allowed: map[captaincode.Leg]bool{},
	}
}

func postChat(t *testing.T, b *brain, text string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(oaiChatReq{
		Model:    "free",
		Messages: []oaiMessage{{Role: "user", Content: json.RawMessage(`"` + text + `"`)}},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, req)
	return rec
}

func TestChatCompletions_InitGeneratesConfigs(t *testing.T) {
	// TestMain pins HOME to a temp dir; pin XDG too so config paths land there.
	cfgHome := filepath.Join(os.Getenv("HOME"), "xdg-init-test")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("CAPTAIN_CWD", t.TempDir()) // keep the overlay scan off real repos

	rec := postChat(t, initTestBrain(), "/init")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "captain init")

	_, err := os.Stat(filepath.Join(cfgHome, "opencode", "opencode.jsonc"))
	require.NoError(t, err, "/init must create the opencode config")
	_, err = os.Stat(filepath.Join(cfgHome, "captain", "env"))
	require.NoError(t, err, "/init must scaffold the captain env")
}

func TestChatCompletions_InitCheckWritesNothing(t *testing.T) {
	cfgHome := filepath.Join(os.Getenv("HOME"), "xdg-init-check-test")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("CAPTAIN_CWD", t.TempDir()) // keep the overlay scan off real repos

	rec := postChat(t, initTestBrain(), "/init check")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "check mode")

	_, err := os.Stat(filepath.Join(cfgHome, "opencode", "opencode.jsonc"))
	assert.True(t, os.IsNotExist(err), "check mode must not write")
}
