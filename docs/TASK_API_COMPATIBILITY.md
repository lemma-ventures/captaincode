# Task API compatibility policy

**M4.1 — Versioned task API.** This document defines what a protocol version
bump means, when it is required, and how a host adapter can detect and refuse
a contract it does not understand.

## Protocol version

The task API carries `TaskAPIVersion` (current: 1) on every request and
response envelope. `TaskAPIMinVersion` (current: 1) is the oldest version the
server accepts.

### When a version bump is required

A **major bump** (`TaskAPIVersion + 1`, `TaskAPIMinVersion` raised) is
required when:

- A field is removed or renamed from any request or response body.
- A field's type changes in a way that breaks JSON unmarshalling.
- An operation is removed.
- The meaning of an existing field changes (e.g. `max_attempts` starts
  counting differently).
- The error taxonomy changes a code's meaning or removes a code.

A **minor addition** does NOT require a bump:

- Adding a new optional field to a request or response body.
- Adding a new operation (the server advertises it; old clients ignore it).
- Adding a new error code (old clients fall through to their default handler).

### Client behavior

A client that receives a response with a `version` higher than it supports
MUST refuse the body. A server that receives a request with a version outside
`[TaskAPIMinVersion, TaskAPIVersion]` rejects it with
`ErrUnsupportedVersion`.

## Authentication

When `CAPTAIN_TASK_TOKEN` is set, every request to `/v1/task` MUST carry an
`Authorization: Bearer <token>` header. Without the header or with a wrong
token, the server responds with `ErrUnauthorized` (HTTP 401).

When no token is set, the endpoint is loopback-only. Non-loopback connections
are rejected with `ErrUnauthorized`. This is the default and is safe for
local development, the TUI, and MCP stdio.

## Error taxonomy

Error codes are stable tokens. A client switches on the code; the message is
human context. The current taxonomy:

| Code | HTTP status | Meaning |
|---|---|---|
| `unsupported_version` | 400 | Protocol version outside supported range |
| `bad_request` | 400 | Malformed request or missing required field |
| `not_found` | 404 | Task or attempt does not exist |
| `conflict` | 409 | State mismatch (e.g. attempt is not interrupted) |
| `method_not_allowed` | 405 | Non-POST request |
| `exhausted` | 409 | Budget exhausted |
| `already_terminal` | 409 | Task is terminal and cannot be modified |
| `unauthorized` | 401 | Missing or invalid token, or non-loopback without token |
| `forbidden` | 403 | Delegation depth or recursion violation |
| `internal` | 500 | Internal server error |
| `unavailable` | 503 | Service temporarily unavailable |

## Contract fixtures

Host adapters can verify their implementation against the task API using
the contract tests in `pkg/captaincode/taskapi_test.go`. These tests cover
compatibility checking, error taxonomy, delegation lineage, idempotency,
cursor round-trips, and JSON envelope serialization.

For live-brain verification, run the standalone fixture program:

```
captain task fixture              # run all checks against a live brain
captain task fixture --json       # machine-readable JSON output
captain task fixture --prompt X   # override the test prompt
```

The fixture exercises the full contract wire: protocol version
compatibility, plan (no execution), submit (durable task ID), inspect
(state read), events (cursor), artifacts, cancel (terminal state),
error handling (not_found), and idempotent submit. Exit 0 on pass, 1 on
any failure. The 3 tests in `task_fixture_test.go` serve as the
executable fixtures — run with `go test ./cmd/captaincode/ -run TestTaskFixture`.

A host adapter running against a live brain should:

1. Call `plan` with a prompt and verify the response carries a protocol
   version it supports.
2. Call `submit` and verify the response carries a durable task ID.
3. Call `inspect` with the task ID and verify the lifecycle state.
4. Call `cancel` and verify the terminal state.

All four should succeed without error. Any `unsupported_version` response
means the host and brain are on different protocol versions.