package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInvalidGateRefusesTurnBeforeDispatch(t *testing.T) {
	for _, prompt := range []string{
		"/codex fix it gate: definitely-missing-gate-checker --all",
		"/team fix it gate: definitely-missing-gate-checker --all",
		"/quality fix it gate: definitely-missing-gate-checker --all",
		"/codex fix it gate: definitely-missing-gate-checker --all > /claude review",
	} {
		t.Run(prompt, func(t *testing.T) {
			b := teamBrain()
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, wfReq(false, prompt))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "gate cannot run")
			b.rmu.Lock()
			defer b.rmu.Unlock()
			assert.Empty(t, b.repeatState())
		})
	}
}
