package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskMCPServerToolsList(t *testing.T) {
	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		replies = append(replies, m)
	}
	require.Len(t, replies, 2)
	assert.Equal(t, mcpProtocolVersion, replies[0]["result"].(map[string]any)["protocolVersion"])
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	names := []string{}
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	assert.ElementsMatch(t, []string{
		"task_plan", "task_submit", "task_inspect", "task_events",
		"task_artifacts", "task_cancel", "task_resume",
	}, names)
}

func TestTaskMCPServerUnknownMethod(t *testing.T) {
	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var reply map[string]any
	sc := bufio.NewScanner(&out)
	require.True(t, sc.Scan())
	require.NoError(t, json.Unmarshal(sc.Bytes(), &reply))
	assert.NotNil(t, reply["error"])
}

func TestTaskMCPServerUnknownTool(t *testing.T) {
	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var reply map[string]any
	sc := bufio.NewScanner(&out)
	require.True(t, sc.Scan())
	require.NoError(t, json.Unmarshal(sc.Bytes(), &reply))
	assert.Equal(t, true, reply["result"].(map[string]any)["isError"])
}

func TestTaskMCPToolCallPlan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req captaincode.TaskRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, captaincode.OpPlan, req.Op)
		assert.Equal(t, captaincode.TaskAPIVersion, req.Version)
		var body captaincode.PlanRequest
		json.Unmarshal(req.Body, &body)
		assert.Equal(t, "fix the bug", body.Prompt)

		plan := captaincode.PlanResponse{Leg: "claude", Class: "edit", Rationale: "frontier model for complex edit"}
		bodyJSON, _ := json.Marshal(plan)
		json.NewEncoder(w).Encode(captaincode.TaskResponse{
			Version: captaincode.TaskAPIVersion, Status: captaincode.ResponseOK, Op: captaincode.OpPlan,
			Body: bodyJSON,
		})
	}))
	defer srv.Close()

	original := os.Getenv("CAPTAIN_BRAIN_URL")
	os.Setenv("CAPTAIN_BRAIN_URL", srv.URL)
	defer os.Setenv("CAPTAIN_BRAIN_URL", original)

	res, err := taskMCPToolCall("task_plan", map[string]any{"prompt": "fix the bug"})
	require.NoError(t, err)
	text := res["content"].([]map[string]any)[0]["text"].(string)
	assert.Contains(t, text, "leg: claude")
	assert.Contains(t, text, "class: edit")
	assert.Contains(t, text, "frontier model for complex edit")
}

func TestTaskMCPToolCallSubmit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req captaincode.TaskRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, captaincode.OpSubmit, req.Op)
		var body captaincode.SubmitRequest
		json.Unmarshal(req.Body, &body)
		assert.Equal(t, "write a test", body.Prompt)

		sub := captaincode.SubmitResponse{TaskID: "task-abc", State: captaincode.StateAdmitted}
		bodyJSON, _ := json.Marshal(sub)
		json.NewEncoder(w).Encode(captaincode.TaskResponse{
			Version: captaincode.TaskAPIVersion, Status: captaincode.ResponseOK, Op: captaincode.OpSubmit,
			TaskID: "task-abc", Body: bodyJSON,
		})
	}))
	defer srv.Close()

	original := os.Getenv("CAPTAIN_BRAIN_URL")
	os.Setenv("CAPTAIN_BRAIN_URL", srv.URL)
	defer os.Setenv("CAPTAIN_BRAIN_URL", original)

	res, err := taskMCPToolCall("task_submit", map[string]any{"prompt": "write a test"})
	require.NoError(t, err)
	text := res["content"].([]map[string]any)[0]["text"].(string)
	assert.Contains(t, text, "task_id: task-abc")
	assert.Contains(t, text, "admitted")
}

func TestTaskMCPToolCallCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req captaincode.TaskRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, captaincode.OpCancel, req.Op)
		assert.Equal(t, "task-xyz", req.TaskID)

		can := captaincode.CancelResponse{TaskID: "task-xyz", State: captaincode.StateCancelled, Cancelled: []string{"worker-1"}}
		bodyJSON, _ := json.Marshal(can)
		json.NewEncoder(w).Encode(captaincode.TaskResponse{
			Version: captaincode.TaskAPIVersion, Status: captaincode.ResponseOK, Op: captaincode.OpCancel,
			TaskID: "task-xyz", Body: bodyJSON,
		})
	}))
	defer srv.Close()

	original := os.Getenv("CAPTAIN_BRAIN_URL")
	os.Setenv("CAPTAIN_BRAIN_URL", srv.URL)
	defer os.Setenv("CAPTAIN_BRAIN_URL", original)

	res, err := taskMCPToolCall("task_cancel", map[string]any{"task_id": "task-xyz"})
	require.NoError(t, err)
	text := res["content"].([]map[string]any)[0]["text"].(string)
	assert.Contains(t, text, "task-xyz")
	assert.Contains(t, text, "cancelled")
	assert.Contains(t, text, "worker-1")
}

func TestTaskMCPToolCallBrainUnreachable(t *testing.T) {
	original := os.Getenv("CAPTAIN_BRAIN_URL")
	os.Setenv("CAPTAIN_BRAIN_URL", "http://127.0.0.1:1")
	defer os.Setenv("CAPTAIN_BRAIN_URL", original)

	res, err := taskMCPToolCall("task_plan", map[string]any{"prompt": "test"})
	require.NoError(t, err)
	text := res["content"].([]map[string]any)[0]["text"].(string)
	assert.Contains(t, text, "error:")
	assert.Contains(t, text, "unavailable")
}

func TestTaskMCPToolCallMissingPrompt(t *testing.T) {
	_, err := taskMCPToolCall("task_plan", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt is required")
}

func TestTaskMCPToolCallMissingTaskID(t *testing.T) {
	_, err := taskMCPToolCall("task_inspect", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task_id is required")
}

func TestTaskMCPToolCallResumeRequiresAttemptID(t *testing.T) {
	_, err := taskMCPToolCall("task_resume", map[string]any{"task_id": "t1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "attempt_id is required")
}

func TestTaskMCPFullFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req captaincode.TaskRequest
		json.NewDecoder(r.Body).Decode(&req)
		var bodyJSON []byte
		op := req.Op
		switch op {
		case captaincode.OpSubmit:
			bodyJSON, _ = json.Marshal(captaincode.SubmitResponse{TaskID: "task-flow", State: captaincode.StateAdmitted})
		case captaincode.OpInspect:
			ts := &captaincode.TaskState{TaskID: "task-flow", State: captaincode.StateRunning}
			bodyJSON, _ = json.Marshal(captaincode.InspectResponse{Task: ts})
		case captaincode.OpCancel:
			bodyJSON, _ = json.Marshal(captaincode.CancelResponse{TaskID: "task-flow", State: captaincode.StateCancelled})
		}
		json.NewEncoder(w).Encode(captaincode.TaskResponse{
			Version: captaincode.TaskAPIVersion, Status: captaincode.ResponseOK, Op: op,
			TaskID: "task-flow", Body: bodyJSON,
		})
	}))
	defer srv.Close()

	original := os.Getenv("CAPTAIN_BRAIN_URL")
	os.Setenv("CAPTAIN_BRAIN_URL", srv.URL)
	defer os.Setenv("CAPTAIN_BRAIN_URL", original)

	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"task_submit","arguments":{"prompt":"do work"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"task_inspect","arguments":{"task_id":"task-flow"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"task_cancel","arguments":{"task_id":"task-flow"}}}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		replies = append(replies, m)
	}
	require.Len(t, replies, 5)

	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	assert.Len(t, tools, 7)

	submitText := replies[2]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, submitText, "task-flow")

	inspectText := replies[3]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, inspectText, "running")

	cancelText := replies[4]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, cancelText, "cancelled")
}

// ── MCP Tasks capability negotiation tests ──

func TestTaskMCPProtocolVersionNegotiation(t *testing.T) {
	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"tasks":{"list":{},"cancel":{}}},"clientInfo":{"name":"t","version":"0"}}}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var reply map[string]any
	sc := bufio.NewScanner(&out)
	require.True(t, sc.Scan())
	require.NoError(t, json.Unmarshal(sc.Bytes(), &reply))
	result := reply["result"].(map[string]any)
	assert.Equal(t, "2025-11-25", result["protocolVersion"])
	caps := result["capabilities"].(map[string]any)
	assert.Contains(t, caps, "tasks")
	tasks := caps["tasks"].(map[string]any)
	assert.Contains(t, tasks, "list")
	assert.Contains(t, tasks, "cancel")
}

func TestTaskMCPProtocolVersionFallback(t *testing.T) {
	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var reply map[string]any
	sc := bufio.NewScanner(&out)
	require.True(t, sc.Scan())
	require.NoError(t, json.Unmarshal(sc.Bytes(), &reply))
	result := reply["result"].(map[string]any)
	assert.Equal(t, "2025-06-18", result["protocolVersion"])
	caps := result["capabilities"].(map[string]any)
	assert.NotContains(t, caps, "tasks")
}

func TestTaskMCPToolsListHasTaskSupport(t *testing.T) {
	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"tasks":{}},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		replies = append(replies, m)
	}
	require.Len(t, replies, 2)
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	for _, tl := range tools {
		tm := tl.(map[string]any)
		if tm["name"] == "task_submit" {
			exec, ok := tm["execution"].(map[string]any)
			require.True(t, ok, "task_submit should have execution field")
			assert.Equal(t, "optional", exec["taskSupport"])
			return
		}
	}
	t.Fatal("task_submit tool not found")
}

func TestTaskMCPTaskAugmentedCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req captaincode.TaskRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Op == captaincode.OpSubmit {
			sub := captaincode.SubmitResponse{TaskID: "cap-task-1", State: captaincode.StateAdmitted}
			bodyJSON, _ := json.Marshal(sub)
			json.NewEncoder(w).Encode(captaincode.TaskResponse{
				Version: captaincode.TaskAPIVersion, Status: captaincode.ResponseOK, Op: captaincode.OpSubmit,
				TaskID: "cap-task-1", Body: bodyJSON,
			})
			return
		}
		if req.Op == captaincode.OpInspect {
			ts := &captaincode.TaskState{TaskID: "cap-task-1", State: captaincode.StateRunning}
			bodyJSON, _ := json.Marshal(captaincode.InspectResponse{Task: ts})
			json.NewEncoder(w).Encode(captaincode.TaskResponse{
				Version: captaincode.TaskAPIVersion, Status: captaincode.ResponseOK, Op: captaincode.OpInspect,
				TaskID: "cap-task-1", Body: bodyJSON,
			})
		}
	}))
	defer srv.Close()

	original := os.Getenv("CAPTAIN_BRAIN_URL")
	os.Setenv("CAPTAIN_BRAIN_URL", srv.URL)
	defer os.Setenv("CAPTAIN_BRAIN_URL", original)

	sess := newTaskMCPSession()

	createReqs := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"tasks":{"list":{},"cancel":{}}},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"task_submit","arguments":{"prompt":"do work"},"task":{"ttl":60000}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	serveTaskMCPWithSession(strings.NewReader(createReqs), &out, sess)

	sc := bufio.NewScanner(&out)
	var createReply map[string]any
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		if m["id"] != nil && fmt.Sprintf("%v", m["id"]) == "2" {
			createReply = m
		}
	}
	require.NotNil(t, createReply, "create reply not found")
	createResult := createReply["result"].(map[string]any)
	task := createResult["task"].(map[string]any)
	assert.Equal(t, "working", task["status"])
	taskID := task["taskId"].(string)
	assert.NotEmpty(t, taskID)

	getReq := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":{"taskId":%q}}`, taskID)
	var out2 bytes.Buffer
	serveTaskMCPWithSession(strings.NewReader(getReq+"\n"), &out2, sess)

	sc2 := bufio.NewScanner(&out2)
	require.True(t, sc2.Scan())
	var getReply map[string]any
	require.NoError(t, json.Unmarshal(sc2.Bytes(), &getReply))
	getResult := getReply["result"].(map[string]any)
	assert.Equal(t, taskID, getResult["taskId"].(string))
	assert.Equal(t, "working", getResult["status"])
}

func TestTaskMCPTasksGetNotFound(t *testing.T) {
	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"tasks":{}},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"taskId":"nonexistent"}}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		replies = append(replies, m)
	}
	require.Len(t, replies, 2)
	errReply := replies[1]["error"].(map[string]any)
	assert.Equal(t, float64(-32602), errReply["code"])
	assert.Contains(t, errReply["message"].(string), "not found")
}

func TestTaskMCPTasksCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req captaincode.TaskRequest
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Op {
		case captaincode.OpSubmit:
			sub := captaincode.SubmitResponse{TaskID: "cap-cancel-1", State: captaincode.StateAdmitted}
			bodyJSON, _ := json.Marshal(sub)
			json.NewEncoder(w).Encode(captaincode.TaskResponse{
				Version: captaincode.TaskAPIVersion, Status: captaincode.ResponseOK, Op: captaincode.OpSubmit,
				TaskID: "cap-cancel-1", Body: bodyJSON,
			})
		case captaincode.OpCancel:
			can := captaincode.CancelResponse{TaskID: "cap-cancel-1", State: captaincode.StateCancelled, Cancelled: []string{"worker-1"}}
			bodyJSON, _ := json.Marshal(can)
			json.NewEncoder(w).Encode(captaincode.TaskResponse{
				Version: captaincode.TaskAPIVersion, Status: captaincode.ResponseOK, Op: captaincode.OpCancel,
				TaskID: "cap-cancel-1", Body: bodyJSON,
			})
		}
	}))
	defer srv.Close()

	original := os.Getenv("CAPTAIN_BRAIN_URL")
	os.Setenv("CAPTAIN_BRAIN_URL", srv.URL)
	defer os.Setenv("CAPTAIN_BRAIN_URL", original)

	sess := newTaskMCPSession()
	createReqs := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"tasks":{"cancel":{}}},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"task_submit","arguments":{"prompt":"do work"},"task":{"ttl":60000}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	serveTaskMCPWithSession(strings.NewReader(createReqs), &out, sess)

	sc := bufio.NewScanner(&out)
	var taskID string
	for sc.Scan() {
		var m map[string]any
		json.Unmarshal(sc.Bytes(), &m)
		if m["id"] != nil && fmt.Sprintf("%v", m["id"]) == "2" {
			taskID = m["result"].(map[string]any)["task"].(map[string]any)["taskId"].(string)
		}
	}
	require.NotEmpty(t, taskID)

	cancelReq := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tasks/cancel","params":{"taskId":%q}}`, taskID)
	var out2 bytes.Buffer
	serveTaskMCPWithSession(strings.NewReader(cancelReq+"\n"), &out2, sess)

	sc2 := bufio.NewScanner(&out2)
	require.True(t, sc2.Scan())
	var cancelReply map[string]any
	require.NoError(t, json.Unmarshal(sc2.Bytes(), &cancelReply))
	cancelResult := cancelReply["result"].(map[string]any)
	assert.Equal(t, "cancelled", cancelResult["status"])
}

func TestTaskMCPTasksCancelTerminalRejected(t *testing.T) {
	sess := newTaskMCPSession()
	now := time.Now()
	sess.putTask(&mcpTask{
		ID:          "completed-task",
		CaptainID:   "cap-1",
		Status:      "completed",
		CreatedAt:   now,
		LastUpdated: now,
		TTL:         mcpTaskDefaultTTL,
	})

	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tasks/cancel", Params: []byte(`{"taskId":"completed-task"}`)}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	reply := func(id json.RawMessage, result any, err *rpcError) {
		_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: err})
	}
	handleTasksCancel(req, sess, reply)

	var resp map[string]any
	json.Unmarshal(out.Bytes(), &resp)
	assert.NotNil(t, resp["error"])
	errReply := resp["error"].(map[string]any)
	assert.Equal(t, float64(-32602), errReply["code"])
	assert.Contains(t, errReply["message"].(string), "terminal")
}

func TestTaskMCPTasksList(t *testing.T) {
	sess := newTaskMCPSession()
	now := time.Now()
	for i := 0; i < 3; i++ {
		sess.putTask(&mcpTask{
			ID:          fmt.Sprintf("task-%d", i),
			CaptainID:   fmt.Sprintf("cap-%d", i),
			Status:      "working",
			CreatedAt:   now,
			LastUpdated: now,
			TTL:         mcpTaskDefaultTTL,
		})
	}

	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tasks/list", Params: []byte(`{}`)}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	reply := func(id json.RawMessage, result any, err *rpcError) {
		_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: err})
	}
	handleTasksList(req, sess, reply)

	var resp map[string]any
	json.Unmarshal(out.Bytes(), &resp)
	result := resp["result"].(map[string]any)
	tasks := result["tasks"].([]any)
	assert.Len(t, tasks, 3)
}

func TestTaskMCPLifecycleMapping(t *testing.T) {
	assert.Equal(t, "working", lifecycleToMCPStatus(captaincode.StateAdmitted))
	assert.Equal(t, "working", lifecycleToMCPStatus(captaincode.StateRunning))
	assert.Equal(t, "input_required", lifecycleToMCPStatus(captaincode.StateWaitingForInput))
	assert.Equal(t, "completed", lifecycleToMCPStatus(captaincode.StateSucceeded))
	assert.Equal(t, "cancelled", lifecycleToMCPStatus(captaincode.StateCancelled))
	assert.Equal(t, "failed", lifecycleToMCPStatus(captaincode.StateFailed))
	assert.Equal(t, "failed", lifecycleToMCPStatus(captaincode.StateExhausted))
	assert.Equal(t, "failed", lifecycleToMCPStatus(captaincode.StateInterrupted))
}

func TestTaskMCPNonTaskAugmentedCallStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub := captaincode.SubmitResponse{TaskID: "cap-plain", State: captaincode.StateAdmitted}
		bodyJSON, _ := json.Marshal(sub)
		json.NewEncoder(w).Encode(captaincode.TaskResponse{
			Version: captaincode.TaskAPIVersion, Status: captaincode.ResponseOK, Op: captaincode.OpSubmit,
			TaskID: "cap-plain", Body: bodyJSON,
		})
	}))
	defer srv.Close()

	original := os.Getenv("CAPTAIN_BRAIN_URL")
	os.Setenv("CAPTAIN_BRAIN_URL", srv.URL)
	defer os.Setenv("CAPTAIN_BRAIN_URL", original)

	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"tasks":{}},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"task_submit","arguments":{"prompt":"plain call"}}}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		replies = append(replies, m)
	}
	require.Len(t, replies, 2)
	// Non-augmented call returns content directly (no "task" key)
	result := replies[1]["result"].(map[string]any)
	assert.Contains(t, result, "content")
	assert.NotContains(t, result, "task")
}

func TestTaskMCPClientWithoutTasksGetsToolsOnly(t *testing.T) {
	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"taskId":"any"}}`,
	}
	var out bytes.Buffer
	serveTaskMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		replies = append(replies, m)
	}
	require.Len(t, replies, 2)
	// Server should still handle tasks/get even without client caps — the
	// server's capability is what matters. But the task won't exist.
	errReply := replies[1]["error"].(map[string]any)
	assert.Equal(t, float64(-32602), errReply["code"])
}
