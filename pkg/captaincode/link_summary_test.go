package captaincode

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLinkTurnSummaryBoundsAndMasksResult(t *testing.T) {
	now := time.Unix(1800000000, 0)
	summary := NewLinkTurnSummary("prompt-one", "Inspected the repository. OPENROUTER_API_KEY="+"sk-live-fixture-secret", now)
	require.NoError(t, LinkTurnSummaryValid(summary))
	assert.Equal(t, "completed", summary.Status)
	assert.Equal(t, "unknown", summary.CostStatus)
	assert.NotContains(t, summary.Done, "sk-live-fixture-secret")
	assert.Contains(t, summary.Done, "[secret]")
	assert.Equal(t, now.Unix(), summary.CompletedAt)

	long := NewLinkTurnSummary("prompt-one", strings.Repeat("é", linkSummaryDoneLimit), now)
	require.NoError(t, LinkTurnSummaryValid(long))
	assert.LessOrEqual(t, len(long.Done), linkSummaryDoneLimit)
	empty := NewLinkTurnSummary("prompt-one", "", now)
	require.NoError(t, LinkTurnSummaryValid(empty))

	failed := NewLinkTurnSummary("prompt-one", "failed: provider unavailable", now)
	assert.Equal(t, "failed", failed.Status)
	assert.Equal(t, "The read-only turn did not complete.", failed.Done)
	assert.Equal(t, "provider unavailable", failed.Limitations)
}

func TestLinkTurnSummaryValidation(t *testing.T) {
	valid := LinkTurnSummary{PromptID: "prompt-one", Status: "completed", Done: "Inspected the repository.", CostStatus: "unknown", CompletedAt: 1800000000}
	require.NoError(t, LinkTurnSummaryValid(valid))
	for name, mutate := range map[string]func(*LinkTurnSummary){
		"prompt id":        func(s *LinkTurnSummary) { s.PromptID = "../path" },
		"status":           func(s *LinkTurnSummary) { s.Status = "running" },
		"empty result":     func(s *LinkTurnSummary) { s.Done = "" },
		"large result":     func(s *LinkTurnSummary) { s.Done = strings.Repeat("x", linkSummaryDoneLimit+1) },
		"large limitation": func(s *LinkTurnSummary) { s.Limitations = strings.Repeat("x", linkSummaryFieldLimit+1) },
		"decision id":      func(s *LinkTurnSummary) { s.Decisions = []string{"not-a-decision"} },
		"cost status":      func(s *LinkTurnSummary) { s.CostStatus = "free" },
		"timestamp":        func(s *LinkTurnSummary) { s.CompletedAt = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			assert.Error(t, LinkTurnSummaryValid(candidate))
		})
	}
}

func TestLinkStreamPostSummaryChecksAcknowledgement(t *testing.T) {
	pub, brainKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, hubKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	summary := LinkTurnSummary{PromptID: "prompt-one", Status: "completed", Done: "Inspected the repository.", CostStatus: "unknown", CompletedAt: time.Now().Unix()}
	var received LinkTurnSummary
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope LinkEnvelope
		require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
		require.NoError(t, LinkVerify(pub, envelope, linkBrainDirection, time.Now()))
		require.Equal(t, "turn.summary", envelope.Type)
		require.NoError(t, LinkDecodePayload(envelope, &received))
		reply, err := LinkSign(hubKey, envelope.MachineID, linkHubDirection, "turn.accepted", map[string]string{"message_id": envelope.ID, "prompt_id": summary.PromptID}, time.Now())
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(w).Encode(reply))
	}))
	defer srv.Close()
	opts := LinkStreamOptions{Hub: LinkHub{URL: srv.URL, PublicKey: base64.RawURLEncoding.EncodeToString(hubKey.Public().(ed25519.PublicKey))}, MachineID: "machine-one", Key: brainKey, Client: srv.Client()}
	require.NoError(t, linkStreamPostSummary(context.Background(), opts.Client, opts, hubKey.Public().(ed25519.PublicKey), summary))
	assert.Equal(t, summary, received)
}

func TestLinkStreamDeliversQueuedSummaryOnce(t *testing.T) {
	fx := newStreamFixture(t)
	opts := fx.options()
	summary := LinkTurnSummary{PromptID: "prompt-one", Status: "completed", Done: "Inspected the repository.", CostStatus: "unknown", CompletedAt: time.Now().Unix()}
	opts.Summaries = func() ([]LinkTurnSummary, error) { return []LinkTurnSummary{summary}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan LinkStreamReport, 1)
	go func() {
		report, _ := LinkStreamLoop(ctx, opts)
		result <- report
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		fx.hub.mu.Lock()
		count := len(fx.hub.summaries)
		fx.hub.mu.Unlock()
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn summary was not delivered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(2 * fx.interval)
	fx.hub.mu.Lock()
	require.Len(t, fx.hub.summaries, 1)
	assert.Equal(t, summary, fx.hub.summaries[0])
	fx.hub.mu.Unlock()
	cancel()
	report := <-result
	assert.Equal(t, 1, report.Summaries)
}

func BenchmarkNewLinkTurnSummary(b *testing.B) {
	now := time.Unix(1800000000, 0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = NewLinkTurnSummary("prompt-one", "Inspected the repository and reported three remaining items.", now)
	}
}
