package captaincode

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SCORING.md Phase 0: the ledger recorded the model captain configured
// (claude's `opus` alias), never the one that ran, so Opus 5 and 5.5 shared
// one score. The transports report what they ran.
func TestClaudeStreamReportsTheModelItRan(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null &\n" +
		`echo '{"type":"system","subtype":"init","model":"claude-opus-5-5"}'` + "\n" +
		`echo '{"type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"done"}]}}'` + "\n" +
		`echo '{"type":"result","result":"done","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}'` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := runClaudeStream(t.TempDir(), "say done", time.Minute, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "done", res.Text)
	assert.Equal(t, "claude-opus-5-5", res.Model, "the init event's model, not the `opus` alias")
}

func TestOpencodeReportsTheModelItRan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.Write([]byte(`{"info":{"modelID":"z-ai/glm-5.3","providerID":"openrouter","tokens":{"total":12}},"parts":[{"type":"text","text":"PONG"}]}`))
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()
	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	res, err := d.Run(LegGLM, "reply with: PONG")
	require.NoError(t, err)
	assert.Equal(t, "openrouter/z-ai/glm-5.3", res.Model)
}

func TestRecordedEventsSayHowTheirModelIsKnown(t *testing.T) {
	l := &Ledger{}
	l.Record(Event{Leg: LegCodexCLI, Outcome: "ok"})
	l.Record(Event{Leg: LegGLM, Outcome: "ok"})
	l.Record(Event{Leg: LegClaude, Outcome: "ok", Model: "claude-opus-5-5", ModelResolved: "observed"})
	assert.Equal(t, "pinned", l.Events[0].ModelResolved, "codex exec gets -m: its model cannot be substituted")
	assert.Equal(t, "projected", l.Events[1].ModelResolved)
	assert.Equal(t, "observed", l.Events[2].ModelResolved)
	assert.Equal(t, "claude-opus-5-5", l.Events[2].Model)
	assert.NotEmpty(t, l.Events[1].Route)
}

func TestChargesCarryTheirModel(t *testing.T) {
	l := &Ledger{}
	l.RecordCharge(Charge{ID: "a-1", TaskID: "t-1", Kind: KindAttempt, Leg: LegClaude})
	l.RecordCharge(Charge{Parent: "a-1", TaskID: "t-1", Kind: KindCall, Leg: LegClaude})
	require.Len(t, l.Charges, 2)
	assert.Equal(t, ModelID(LegClaude), l.Charges[1].Model, "configured model by default")
	l.SetChargeModel("a-1", "claude-opus-5-5")
	assert.Equal(t, "claude-opus-5-5", l.Charges[1].Model, "the worker's report replaces it")
	assert.Empty(t, l.Charges[0].Model, "attempt nodes are not calls")
}
