package captaincode

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeCodexAppServer(t *testing.T, scenario string) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	log := filepath.Join(t.TempDir(), "methods.jsonl")
	t.Setenv("GO_WANT_CODEX_APP_SERVER_HELPER", "1")
	t.Setenv("CAPTAIN_CODEX_HELPER_SCENARIO", scenario)
	t.Setenv("CAPTAIN_CODEX_HELPER_LOG", log)
	fakeBin(t, "codex", fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestCodexAppServerHelper$ -- \"$@\"\n", exe))
	return log
}

func TestCodexAppServerHelper(t *testing.T) {
	if os.Getenv("GO_WANT_CODEX_APP_SERVER_HELPER") != "1" {
		t.Skip("helper process")
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	scenario := os.Getenv("CAPTAIN_CODEX_HELPER_SCENARIO")
	if len(args) >= 3 && args[0] == "app-server" && args[1] == "generate-json-schema" {
		var out string
		for i, arg := range args {
			if arg == "--out" && i+1 < len(args) {
				out = args[i+1]
			}
		}
		if out == "" {
			os.Exit(2)
		}
		writeCodexSchemaFixture(out, scenario == "schema-missing-steer")
		return
	}
	if len(args) >= 2 && args[0] == "exec" {
		fmt.Println(`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"exec fallback"}}`)
		fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":1}}`)
		return
	}
	log, err := os.OpenFile(os.Getenv("CAPTAIN_CODEX_HELPER_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	defer log.Close()
	enc := json.NewEncoder(os.Stdout)
	response := func(id any, result any) {
		_ = enc.Encode(map[string]any{"id": id, "result": result})
	}
	notify := func(method string, params any) {
		_ = enc.Encode(map[string]any{"method": method, "params": params})
	}
	complete := func(status string) {
		notify("thread/tokenUsageUpdated", map[string]any{
			"threadId": "thread-1", "turnId": "turn-1",
			"tokenUsage": map[string]any{"last": map[string]any{
				"inputTokens": 10, "cachedInputTokens": 4, "outputTokens": 2,
				"reasoningOutputTokens": 0, "totalTokens": 12,
			}, "total": map[string]any{}},
		})
		notify("turn/completed", map[string]any{
			"threadId": "thread-1",
			"turn":     map[string]any{"id": "turn-1", "items": []any{}, "status": status},
		})
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			os.Exit(2)
		}
		_ = json.NewEncoder(log).Encode(map[string]any{"method": req.Method, "params": req.Params})
		switch req.Method {
		case "initialize":
			response(req.ID, map[string]any{})
		case "thread/start":
			response(req.ID, map[string]any{"thread": map[string]any{"id": "thread-1"}})
		case "turn/start":
			response(req.ID, map[string]any{"turn": map[string]any{"id": "turn-1", "items": []any{}, "status": "inProgress"}})
			if scenario == "malformed" {
				fmt.Println("not-json")
				continue
			}
			if scenario == "oversize" {
				fmt.Println(strings.Repeat("x", codexAppServerMaxLine+1))
				continue
			}
			if scenario == "normal" || scenario == "parity" {
				notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "item-1", "type": "agentMessage", "text": "app-server answer"}})
				complete("completed")
			}
		case "turn/steer":
			if scenario == "refused-steer" {
				_ = enc.Encode(map[string]any{"id": req.ID, "error": map[string]any{"code": -32602, "message": "turn is not steerable"}})
				notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "item-1", "type": "agentMessage", "text": "app-server answer"}})
			} else {
				response(req.ID, map[string]any{})
				notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "item-1", "type": "agentMessage", "text": "steer accepted"}})
			}
			complete("completed")
		case "turn/interrupt":
			response(req.ID, map[string]any{})
			complete("interrupted")
		}
	}
}

func writeCodexSchemaFixture(out string, omitSteer bool) {
	files := map[string][]string{
		"v1/InitializeParams.json":                    {"clientInfo"},
		"v2/ThreadStartParams.json":                   {"model", "cwd", "approvalPolicy", "sandbox"},
		"v2/ThreadStartResponse.json":                 {"thread"},
		"v2/TurnStartParams.json":                     {"threadId", "input", "effort", "approvalPolicy", "cwd"},
		"v2/TurnStartResponse.json":                   {"turn"},
		"v2/TurnSteerParams.json":                     {"threadId", "expectedTurnId", "input", "additionalContext"},
		"v2/TurnInterruptParams.json":                 {"threadId", "turnId"},
		"v2/ItemStartedNotification.json":             {"item", "threadId", "turnId"},
		"v2/ItemCompletedNotification.json":           {"item", "threadId", "turnId"},
		"v2/TurnCompletedNotification.json":           {"turn", "threadId"},
		"v2/ThreadTokenUsageUpdatedNotification.json": {"turnId", "threadId", "tokenUsage"},
	}
	for name, fields := range files {
		if omitSteer && strings.HasSuffix(name, "TurnSteerParams.json") {
			fields = []string{"threadId", "input"}
		}
		properties := map[string]any{}
		for _, field := range fields {
			properties[field] = map[string]any{}
		}
		raw, _ := json.Marshal(map[string]any{"properties": properties})
		path := filepath.Join(out, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		_ = os.WriteFile(path, raw, 0o600)
	}
	_ = os.WriteFile(filepath.Join(out, "ClientRequest.json"), []byte(`{"methods":["initialize","thread/start","turn/start","turn/steer","turn/interrupt"]}`), 0o600)
}

func runAppServerFixture(t *testing.T, scenario string) (Result, error, *Steer, string) {
	t.Helper()
	log := fakeCodexAppServer(t, scenario)
	t.Setenv("CAPTAIN_CODEX_TRANSPORT", "app-server")
	steer := NewSteer(t.TempDir())
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, nil, "", steer)
		done <- outcome{res, err}
	}()
	if scenario == "steer" || scenario == "refused-steer" {
		require.Eventually(t, func() bool { return len(steer.Attached()) == 1 }, 5*time.Second, 10*time.Millisecond)
		_, err := steer.Send("use the existing helper")
		if scenario == "refused-steer" {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "turn is not steerable")
		} else {
			require.NoError(t, err)
		}
	}
	if scenario == "interrupt" {
		require.Eventually(t, func() bool { return len(steer.Attached()) == 1 }, 5*time.Second, 10*time.Millisecond)
		steer.Abort()
	}
	select {
	case got := <-done:
		return got.res, got.err, steer, log
	case <-time.After(10 * time.Second):
		t.Fatal("app-server run did not finish")
		return Result{}, nil, nil, ""
	}
}

func TestCodexAppServerNormalTurn(t *testing.T) {
	res, err, _, _ := runAppServerFixture(t, "normal")
	require.NoError(t, err)
	assert.Equal(t, "app-server answer", res.Text)
	assert.Equal(t, "thread-1", res.SessionID)
	assert.Equal(t, 12, res.Tokens)
	require.NotNil(t, res.TokenUsage)
	assert.Equal(t, 6, *res.TokenUsage.Input)
	assert.Equal(t, 4, *res.TokenUsage.CacheRead)
	assert.Equal(t, 2, *res.TokenUsage.Output)
}

func TestCodexAppServerSteer(t *testing.T) {
	res, err, steer, log := runAppServerFixture(t, "steer")
	require.NoError(t, err)
	assert.Equal(t, "steer accepted", res.Text)
	notes := steer.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, []Leg{LegCodexCLI}, notes[0].Took)
	raw, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"method":"turn/steer"`)
	assert.Contains(t, string(raw), "expectedTurnId")
	assert.Contains(t, string(raw), "use the existing helper")
}

func TestCodexAppServerRefusedSteerLeavesNoteForNextTurn(t *testing.T) {
	res, err, steer, _ := runAppServerFixture(t, "refused-steer")
	require.NoError(t, err)
	assert.Equal(t, "app-server answer", res.Text)
	notes := steer.Notes()
	require.Len(t, notes, 1)
	assert.Empty(t, notes[0].Took)
}

func TestCodexAppServerInterrupt(t *testing.T) {
	res, err, _, log := runAppServerFixture(t, "interrupt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInterrupted), "got %v", err)
	assert.True(t, res.Partial)
	raw, readErr := os.ReadFile(log)
	require.NoError(t, readErr)
	assert.Contains(t, string(raw), `"method":"turn/interrupt"`)
}

func TestCodexAppServerMalformedLineFailsWithoutHanging(t *testing.T) {
	_, err, _, _ := runAppServerFixture(t, "malformed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
}

func TestCodexAppServerOversizeLineFailsWithoutHanging(t *testing.T) {
	_, err, _, _ := runAppServerFixture(t, "oversize")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event stream")
}

func TestCodexAppServerSchemaGateFallsBackToExec(t *testing.T) {
	fakeCodexAppServer(t, "schema-missing-steer")
	t.Setenv("CAPTAIN_CODEX_TRANSPORT", "app-server")
	var statuses []string
	res, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, func(s string) { statuses = append(statuses, s) }, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "exec fallback", res.Text)
	assert.Contains(t, strings.Join(statuses, "\n"), "expectedTurnId")
	assert.Contains(t, strings.Join(statuses, "\n"), "exec")
}

func TestCodexAppServerSettingsMatchExec(t *testing.T) {
	for _, test := range []struct {
		name    string
		task    string
		sandbox string
		want    string
	}{
		{name: "default bypass", want: "danger-full-access"},
		{name: "workspace", sandbox: "workspace-write", want: "workspace-write"},
		{name: "read only", sandbox: "read-only", want: "read-only"},
		{name: "sent turn", task: SentTurnMarker + "inspect", sandbox: "", want: "workspace-write"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CAPTAIN_CODEX_CLI_SANDBOX", test.sandbox)
			thread, turn := codexAppServerSettings(LegCodexCLI, "/tmp/project", test.task, EffortHigh)
			assert.Equal(t, test.want, thread["sandbox"])
			assert.Equal(t, "never", thread["approvalPolicy"])
			assert.Equal(t, "never", turn["approvalPolicy"])
			assert.Equal(t, codexLegModel(LegCodexCLI, EffortHigh), thread["model"])
			assert.Equal(t, EffortHigh.CodexFlag(), turn["effort"])
			assert.Equal(t, "/tmp/project", thread["cwd"])
		})
	}
}

func TestCodexTransportDefaultsToExec(t *testing.T) {
	t.Setenv("CAPTAIN_CODEX_TRANSPORT", "")
	fakeCodexAppServer(t, "normal")
	res, err := runCodexCLIStream("", "task", 30*time.Second, 0, nil, nil, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "exec fallback", res.Text)
}

func BenchmarkCodexAppServerSettings(b *testing.B) {
	b.Setenv("CAPTAIN_CODEX_CLI_SANDBOX", "workspace-write")
	b.Setenv("CAPTAIN_CODEX_CLI_EFFORT", "")
	b.ReportAllocs()
	for b.Loop() {
		codexAppServerSettings(LegCodexCLI, "/tmp/project", "task", EffortHigh)
	}
}
