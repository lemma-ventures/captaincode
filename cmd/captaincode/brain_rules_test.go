package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rule the user keeps in .captaincode/rules reaches the worker prompt, the
// way the plain-writing and private-names lines do; CAPTAIN_WORKER_RULES=0
// drops it.
func TestEveryWorkerGetsTheUsersRules(t *testing.T) {
	for _, tc := range []struct {
		name, off string
		want      bool
	}{
		{"default", "", true},
		{"turned off", "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CAPTAIN_WORKER_RULES", tc.off)
			dir := filepath.Join(home, ".captaincode", "rules")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "no-self-promotion.md"), []byte("Commits name the operator only."), 0o644))

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
				return leg, captaincode.Result{Text: "committed", DurationMs: 5}, nil
			}
			body, _ := json.Marshal(map[string]any{"model": "grok", "stream": false,
				"messages": []map[string]string{{"role": "user", "content": "commit the fix"}}})
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			r.Header.Set(workspaceHeader, t.TempDir())
			b.chatCompletions(rec, r)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.want, bytes.Contains([]byte(seen), []byte("--- rule no-self-promotion (~/.captaincode/rules/no-self-promotion.md) ---\nCommits name the operator only.")), seen)
		})
	}
}
