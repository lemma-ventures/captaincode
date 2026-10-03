package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every worker writes plainly, the way every worker puts security first:
// on by default, off with CAPTAIN_WORKER_NOSLOP=0, and back on for a turn
// that says /noslop - before or after the command it rides with.
func TestEveryWorkerGetsThePlainWritingContract(t *testing.T) {
	for _, tc := range []struct {
		name, off, text string
		want            bool
	}{
		{"default", "", "write the design doc", true},
		{"turned off", "0", "write the design doc", false},
		{"/noslop first", "0", "/noslop write the design doc", true},
		{"/noslop after a leg", "0", "/grok /noslop write the design doc", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAPTAIN_WORKER_NOSLOP", tc.off)
			b := teamBrain()
			b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
				return captaincode.Assessment{Quality: 9, Verdict: "good"}, nil
			}
			var mu sync.Mutex
			var seen string
			b.runWorkerFn = func(leg captaincode.Leg, prompt string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
				mu.Lock()
				defer mu.Unlock()
				seen = prompt
				return leg, captaincode.Result{Text: "the doc", DurationMs: 5}, nil
			}
			body, _ := json.Marshal(map[string]any{"model": "grok", "stream": false,
				"messages": []map[string]string{{"role": "user", "content": tc.text}}})
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			r.Header.Set(workspaceHeader, t.TempDir())
			b.chatCompletions(rec, r)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.want, bytes.Contains([]byte(seen), []byte("[captain] Plain writing:")), seen)
		})
	}
}
