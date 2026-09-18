package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The TUI's Context panel reads the usage block of every completion; the
// brain answered zeros, so it sat at "0 tokens · $0.00" (2026-09-18). Real
// tokens and the cost the brain accounts for, streamed and not.
func TestCompletionsReportTheRunsUsage(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if onDelta != nil {
			onDelta("the answer")
		}
		return leg, captaincode.Result{Text: "the answer", Tokens: 4000, CostUSD: 0.012, DurationMs: 10, Streamed: onDelta != nil}, nil
	}
	for _, stream := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"model": "glm", "stream": stream, // distinct prompts: identical ones attach to the solo dedupe cache
			"messages": []map[string]string{{"role": "user", "content": fmt.Sprintf("how many tokens is this (stream=%v)", stream)}}})
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		out := rec.Body.String()
		var usageLine string
		if stream {
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, `"usage"`) {
					usageLine = l
				}
			}
			require.NotEmpty(t, usageLine, "the finishing chunk carries the usage: %s", out)
			assert.Contains(t, usageLine, `"finish_reason":"stop"`)
		} else {
			usageLine = out
		}
		assert.Contains(t, usageLine, `"total_tokens":4000`, "stream=%v", stream)
		assert.Contains(t, usageLine, `"prompt_tokens":3000`)
		assert.Contains(t, usageLine, `"cost":0.012`, "the measured cost")
	}
}

// A team turn reports the sum of its workers.
func TestTeamCompletionReportsTheSumOfItsWorkers(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{Class: captaincode.ClassHigh, Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: task}, {Leg: captaincode.LegGLM, Brief: task}}}, nil
	}
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 7, Verdict: "good"}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "done by " + string(leg), Tokens: 1000, DurationMs: 10}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/team do two things"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"total_tokens":2000`, "both workers' tokens: %s", rec.Body.String())
}
