import { expect, test } from "bun:test"
import { securityClient, securitySummary, type SecurityStatus } from "./security"

const status: SecurityStatus = {
  workspace: "ws_0123456789abcdef",
  mode: "audit",
  mode_valid: true,
  snapshot_ok: true,
  total: 3,
  dispositions: { would_block: 1, observed_after_execution: 1, evaluated: 1, degraded: 0, not_covered: 1 },
  hosts: { claude: 2, opencode: 1 },
  uncovered: ["codex-cli", "grok-cli", "cursor-agent"],
  recent: [],
}

test("the security client keeps the workspace-scoped URL and reads the bounded summary", async () => {
  const url = "http://localhost:14097/v1/blackbox/status?cwd=%2Fworkspace%2Fproject-a"
  const client = securityClient(url, (async (input, init) => {
    expect(String(input)).toBe(url)
    expect(init?.method).toBe("GET")
    return Response.json(status)
  }) as typeof fetch)
  expect(await client.read()).toEqual(status)
})

test("failed and malformed responses do not become a healthy status", async () => {
  const failed = securityClient("http://localhost/v1/blackbox/status", (async () => Response.json({ error: "unavailable" }, { status: 500 })) as typeof fetch)
  await expect(failed.read()).rejects.toThrow("unavailable")
  const malformed = securityClient("http://localhost/v1/blackbox/status", (async () => Response.json({ mode: "audit" })) as typeof fetch)
  await expect(malformed.read()).rejects.toThrow("Invalid security status")
})

test("the sidebar states audit-only coverage and never presents would-block as prevention", () => {
  const text = securitySummary(status)
  expect(text).toContain("audit only")
  expect(text).toContain("3 events")
  expect(text).toContain("1 would block")
  expect(text).toContain("1 not covered")
  expect(text).toContain("codex-cli, grok-cli, cursor-agent uncovered")
  expect(text).not.toContain("blocked")
})

test("empty, off and degraded states stay explicit", () => {
  expect(securitySummary({ ...status, total: 0, dispositions: {}, hosts: {}, uncovered: [] })).toContain("no events in this folder")
  expect(securitySummary({ ...status, mode: "off", total: 0 })).toContain("off")
  expect(securitySummary({ ...status, snapshot_ok: false })).toContain("snapshot unreadable")
})

test("an enforcing host reports refusals as refusals, not as audit-only coverage", () => {
  const enforcing: SecurityStatus = {
    ...status,
    mode: "enforce",
    total: 4,
    dispositions: { blocked: 2, overridden: 1, security_check_unavailable: 1, degraded: 0, evaluated: 0 },
  }
  const text = securitySummary(enforcing)
  expect(text).toContain("enforcing")
  expect(text).toContain("4 events")
  expect(text).toContain("2 blocked")
  expect(text).toContain("1 overridden")
  expect(text).toContain("1 check unavailable")
  expect(text).not.toContain("audit only")
  expect(securitySummary({ ...enforcing, total: 0, dispositions: {} })).toContain("enforcing · no events in this folder")
})
