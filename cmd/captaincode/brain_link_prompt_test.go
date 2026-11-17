package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinkPromptAcceptedAsReadOnlySentTurn(t *testing.T) {
	b, signed, dir := signedFixtureText(t, "inspect the repository")
	_, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
	require.NoError(t, err)
	prompt, err := captaincode.NewLinkPrompt("stream-one", "prompt-one", signed)
	require.NoError(t, err)
	require.NoError(t, b.linkPromptAccept(prompt))
	pending := sqList("pending")
	require.Len(t, pending, 1)
	assert.Equal(t, prompt.PromptID, pending[0].PromptID)
	assert.False(t, pending[0].Hold)
	items := b.inbox.take(dir, "")
	require.Len(t, items, 1)
	by, sent, grant := b.inbox.notePresenceTurn(dir, signed.Text)
	assert.True(t, sent)
	assert.Equal(t, "hub (read-only)", by)
	require.NotNil(t, grant)
	assert.True(t, grant.ReadOnly)
}

func TestLinkPromptRunsHeadlessWithoutTypedAuthority(t *testing.T) {
	b, signed, dir := signedFixtureText(t, "inspect the repository")
	_, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
	require.NoError(t, err)
	prompt, err := captaincode.NewLinkPrompt("stream-one", "prompt-one", signed)
	require.NoError(t, err)
	require.NoError(t, b.linkPromptAccept(prompt))
	old := sqHeadlessAfter
	sqHeadlessAfter = 0
	defer func() { sqHeadlessAfter = old }()
	ran := make(chan struct{}, 1)
	b.signedQueueRunFn = func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(raw, &body))
		_, sent, grant := b.inbox.notePresenceTurn(dir, body.Messages[len(body.Messages)-1].Content)
		assert.True(t, sent)
		require.NotNil(t, grant)
		assert.True(t, grant.ReadOnly)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"inspected"}}]}`))
		ran <- struct{}{}
	}
	b.signedQueueStep()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("read-only prompt did not run")
	}
}

func TestLinkPromptQueueModeWaitsForKeyboard(t *testing.T) {
	b, signed, dir := signedFixtureText(t, "inspect the repository")
	_, err := captaincode.LinkAdd(dir, captaincode.LinkModeQueue)
	require.NoError(t, err)
	prompt, err := captaincode.NewLinkPrompt("stream-one", "prompt-one", signed)
	require.NoError(t, err)
	require.NoError(t, b.linkPromptAccept(prompt))
	old := sqHeadlessAfter
	sqHeadlessAfter = 0
	defer func() { sqHeadlessAfter = old }()
	b.signedQueueRunFn = func(http.ResponseWriter, *http.Request) { t.Error("queue-only prompt ran headless") }
	b.signedQueueStep()
	require.Len(t, sqList("pending"), 1)
	assert.True(t, sqList("pending")[0].Hold)
}

func TestLinkPromptExactRetryAndNonceReplay(t *testing.T) {
	b, signed, dir := signedFixtureText(t, "inspect the repository")
	_, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
	require.NoError(t, err)
	prompt, err := captaincode.NewLinkPrompt("stream-one", "prompt-one", signed)
	require.NoError(t, err)
	require.NoError(t, b.linkPromptAccept(prompt))
	require.NoError(t, b.linkPromptAccept(prompt))
	conflict := prompt
	conflict.Text = "different"
	assert.Equal(t, "prompt_conflict", pres.Code(b.linkPromptAccept(conflict)))
	retry := prompt
	retry.PromptID = "prompt-two"
	assert.Equal(t, "replayed_nonce", pres.Code(b.linkPromptAccept(retry)))
	require.Len(t, sqList("pending"), 1)
}

func TestLinkPromptRefusesLoopsAndUnlinkedFolders(t *testing.T) {
	for _, text := range []string{"/repeat 3 inspect", "/repeat finish", "/btw inspect", "/team inspect", "/parallel inspect"} {
		b, signed, dir := signedFixtureText(t, text)
		_, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
		require.NoError(t, err)
		prompt, err := captaincode.NewLinkPrompt("stream-one", "prompt-one", signed)
		require.NoError(t, err)
		assert.Equal(t, "read_only_single_turn_required", pres.Code(b.linkPromptAccept(prompt)))
		assert.Empty(t, sqList("pending"))
	}
	b, signed, _ := signedFixtureText(t, "inspect")
	prompt, err := captaincode.NewLinkPrompt("stream-one", "prompt-one", signed)
	require.NoError(t, err)
	assert.Equal(t, "folder_not_linked", pres.Code(b.linkPromptAccept(prompt)))
	assert.Empty(t, sqList("pending"))
}

func TestLinkPromptWorkerContractRefusesWrites(t *testing.T) {
	b, _, dir := signedInboxFixture(t)
	prompt := sentTurnContract(captaincode.Workspace{SentBy: "hub (read-only)", ReadOnly: true})
	assert.True(t, captaincode.IsSentTurn(prompt))
	assert.True(t, captaincode.IsReadOnlyTurn(prompt))
	sentSessionCache = &sentSessions{}
	b.sessionPromptFn = func(string) (string, error) { return prompt, nil }
	b.sessionDirFn = func(string) (string, error) { return dir, nil }
	body, _ := json.Marshal(map[string]any{"session": "remote-read-only", "tool": "write", "args": map[string]string{"filePath": "notes.md"}, "cwd": dir})
	rec := httptest.NewRecorder()
	b.gateSentHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/gate/sent", bytes.NewReader(body)))
	assert.Contains(t, rec.Body.String(), `"allow":false`)
}
