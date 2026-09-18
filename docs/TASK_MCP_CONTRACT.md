# Task MCP contract fixtures

**M4.2 — Execution MCP.** This document defines the contract a host adapter
(Pi, Jido, or an editor) verifies against the `captain task mcp` server. The
fixtures are executable: each is a sequence of JSON-RPC messages over stdio
that a host can send and assert on.

The MCP server speaks the 2025-11-25 protocol version with the experimental
Tasks capability. A client that does not declare `tasks` in its initialize
capabilities gets the 2025-06-18 protocol (tools only) and the existing
seven tools work synchronously as before.

## Protocol version negotiation

The server supports two protocol versions:

| Client requests | Server responds | Tasks capability |
|---|---|---|
| `2025-11-25` | `2025-11-25` | Advertised (`tasks.list`, `tasks.cancel`, `tasks.requests.tools.call`) |
| `2025-06-18` | `2025-06-18` | Not advertised |
| Other | `2025-06-18` | Not advertised (fallback) |

### Fixture 1: Initialize with Tasks

```json
→ {"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"tasks":{"list":{},"cancel":{}}},"clientInfo":{"name":"host","version":"1.0"}}}
← {"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{},"tasks":{"list":{},"cancel":{},"requests":{"tools":{"call":{}}}}},"serverInfo":{"name":"captain-task","version":"0.1.0"}}}
```

### Fixture 2: Initialize without Tasks (fallback)

```json
→ {"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"host","version":"1.0"}}}
← {"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"captain-task","version":"0.1.0"}}}
```

## Tool-level task support

`task_submit` declares `execution.taskSupport: "optional"` — a client MAY
augment it with a `task` field. Other tools do not declare task support
(default: forbidden).

### Fixture 3: Tools list shows task support

```json
→ {"jsonrpc":"2.0","id":2,"method":"tools/list"}
← {"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"task_submit",...,"execution":{"taskSupport":"optional"}},...]}}
```

## Task lifecycle

### Fixture 4: Create a task (augmented tools/call)

```json
→ {"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"task_submit","arguments":{"prompt":"fix the bug"},"task":{"ttl":60000}}}
← {"jsonrpc":"2.0","id":3,"result":{"task":{"taskId":"<mcp-task-id>","status":"working","createdAt":"...","lastUpdatedAt":"...","ttl":60000,"pollInterval":5000}}}
```

The response is a `CreateTaskResult` containing only task metadata. The
actual tool result is not included — it is retrieved via `tasks/result`
after the task completes.

### Fixture 5: Poll task status (tasks/get)

```json
→ {"jsonrpc":"2.0","id":4,"method":"tasks/get","params":{"taskId":"<mcp-task-id>"}}
← {"jsonrpc":"2.0","id":4,"result":{"taskId":"<mcp-task-id>","status":"working","createdAt":"...","lastUpdatedAt":"...","ttl":60000,"pollInterval":5000}}
```

The client SHOULD respect `pollInterval` and continue polling until a
terminal status (`completed`, `failed`, `cancelled`) or `input_required`.

### Fixture 6: Retrieve result (tasks/result)

```json
→ {"jsonrpc":"2.0","id":5,"method":"tasks/result","params":{"taskId":"<mcp-task-id>"}}
← {"jsonrpc":"2.0","id":5,"result":{"content":[{"type":"text","text":"task <id> — succeeded\n..."}],"_meta":{"io.modelcontextprotocol/related-task":{"taskId":"<mcp-task-id>"}}}}
```

`tasks/result` blocks until the task reaches a terminal status (up to 120s).
If the task is not terminal after the timeout, the server returns a `-32603`
error suggesting the client poll with `tasks/get`.

### Fixture 7: Cancel a task

```json
→ {"jsonrpc":"2.0","id":6,"method":"tasks/cancel","params":{"taskId":"<mcp-task-id>"}}
← {"jsonrpc":"2.0","id":6,"result":{"taskId":"<mcp-task-id>","status":"cancelled","statusMessage":"The task was cancelled by request.","createdAt":"...","lastUpdatedAt":"...","ttl":60000,"pollInterval":5000}}
```

Cancelling a task already in a terminal status returns `-32602` (Invalid
params):

```json
← {"jsonrpc":"2.0","id":6,"error":{"code":-32602,"message":"cannot cancel task: already in terminal status 'completed'"}}
```

### Fixture 8: List tasks

```json
→ {"jsonrpc":"2.0","id":7,"method":"tasks/list","params":{}}
← {"jsonrpc":"2.0","id":7,"result":{"tasks":[{"taskId":"...","status":"working",...},{"taskId":"...","status":"completed",...}]}}
```

Supports cursor-based pagination via `cursor` in params and `nextCursor`
in the response.

## Error handling

| Error case | JSON-RPC code |
|---|---|
| Unknown `taskId` in `tasks/get`, `tasks/result`, `tasks/cancel` | `-32602` |
| Cancel a terminal task | `-32602` |
| Task not terminal after `tasks/result` timeout | `-32603` |
| Task augmentation on a tool that doesn't support it | `-32600` |
| Brain unreachable | `-32603` |

## Non-augmented calls

A client that does not include the `task` field in `tools/call` params gets
the existing synchronous behavior — the tool runs and returns its result
directly. This is backward-compatible with the 2025-06-18 protocol.

## Status mapping

Captain lifecycle states map to MCP task statuses:

| Captain state | MCP status |
|---|---|
| `admitted` | `working` |
| `running` | `working` |
| `waiting_for_input` | `input_required` |
| `succeeded` | `completed` |
| `cancelled` | `cancelled` |
| `failed` | `failed` |
| `exhausted` | `failed` |
| `interrupted` | `failed` |

## Reference implementation

The contract tests in `cmd/captaincode/task_mcp_test.go` serve as the
executable fixtures. Run them with:

```sh
go test ./cmd/captaincode/ -run TestTaskMCP -v
```

A host adapter should verify it can complete the full lifecycle:
initialize → tools/list → task-augmented tools/call → tasks/get →
tasks/cancel (or tasks/result) → tasks/list.
