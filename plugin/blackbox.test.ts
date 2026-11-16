import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import { chmod, mkdtemp, writeFile } from "node:fs/promises"
import { readFileSync } from "node:fs"
import os from "node:os"
import path from "node:path"

// BB2: the Blackbox audit hook. These tests drive the plugin's exported
// server handlers with a fake `captain` binary on CAPTAIN_BIN, proving the
// hook is invoked per tool call, on the restored arguments, before the gate,
// for both phases - and that a failing hook never breaks a tool.

const savedEnv = { ...process.env }

let scratch: string
let fakeBin: string
let hookLog: string

const FAKE_CAPTAIN = `#!/bin/sh
# records what the plugin sent, answers like the real hook
cat >> "$CAPTAIN_HOOK_LOG" 
echo '{"status":"complete","disposition":"evaluated","event_id":"bbx_test","finding_count":0}'
`

const REFUSING_CAPTAIN = `#!/bin/sh
# records what the plugin sent, refuses like an enforce-mode rule match
cat >> "$CAPTAIN_HOOK_LOG"
printf '\\n' >> "$CAPTAIN_HOOK_LOG"
echo '{"status":"complete","disposition":"blocked","finding_count":1,"refuse":true,"reason":"blackbox_rule_match:pkg-exact"}'
`

async function loadServer() {
  // A query string per load: the plugin reads its env at import time, so each
  // test needs a fresh module with its own snapshot.
  const mod = await import(`./captain?bbx=${Math.random()}`)
  const server = await (mod.server as (input?: { directory?: string }) => Promise<Record<string, unknown>>)({ directory: scratch })
  return server as {
    "tool.execute.before": (call: { tool: string; sessionID?: string }, output: { args: any }) => Promise<void>
    "tool.execute.after": (input: { tool: string; sessionID?: string }, output: { output: string }) => Promise<void>
  }
}

const OVERWRITE = [
  "CAPTAIN_BIN",
  "CAPTAIN_BLACKBOX",
  "CAPTAIN_REDACT",
  "CAPTAIN_ACTION_GATE",
  "CAPTAIN_BRAIN_URL",
  "CAPTAIN_HOOK_LOG",
  "CAPTAIN_CWD",
  "CAPTAIN_LOG",
]

beforeEach(async () => {
  scratch = await mkdtemp(path.join(os.tmpdir(), "bbx-plugin-"))
  fakeBin = path.join(scratch, "captain")
  hookLog = path.join(scratch, "hook.log")
  await writeFile(fakeBin, FAKE_CAPTAIN, { mode: 0o700 })
  process.env["CAPTAIN_BIN"] = fakeBin
  process.env["CAPTAIN_BLACKBOX"] = "audit"
  process.env["CAPTAIN_REDACT"] = "off"
  process.env["CAPTAIN_ACTION_GATE"] = "off"
  process.env["CAPTAIN_BRAIN_URL"] = "http://127.0.0.1:1" // dead port: the sent-turn policy returns nothing
  process.env["CAPTAIN_HOOK_LOG"] = hookLog
  process.env["CAPTAIN_CWD"] = path.join(scratch, "repo")
  process.env["CAPTAIN_LOG"] = path.join(scratch, "route.log")
})

afterEach(() => {
  for (const key of OVERWRITE) delete process.env[key]
  Object.assign(process.env, savedEnv)
})

function readHookEvents(): any[] {
  try {
    return readFileSync(hookLog, "utf8")
      .split("\n")
      .filter((line) => line.trim().length > 0)
      .map((line) => JSON.parse(line))
  } catch {
    return []
  }
}

describe("the blackbox audit hook (BB2)", () => {
  test("a pre-tool call sends the final arguments to captain blackbox hook", async () => {
    const server = await loadServer()
    const args = { command: "npm install example-package@1.2.3" }
    await server["tool.execute.before"]({ tool: "bash", sessionID: "ses-1" }, { args })
    const events = readHookEvents()
    expect(events.length).toBe(1)
    expect(events[0].tool).toBe("bash")
    expect(events[0].args).toEqual(args)
    expect(events[0].phase).toBe("pre")
    expect(events[0].session).toBe("ses-1")
    // the session's directory is the cwd the record joins on
    expect(events[0].cwd).toBe(scratch)
  })

  test("a post-tool call records an observation of the masked output", async () => {
    const server = await loadServer()
    await server["tool.execute.after"]({ tool: "bash", sessionID: "ses-1" }, { output: "tool output text" })
    const events = readHookEvents()
    expect(events.length).toBe(1)
    expect(events[0].phase).toBe("post")
    expect(events[0].output).toBe("tool output text")
    expect(events[0].tool).toBe("bash")
  })

  test("post output is bounded to 16 KiB before it leaves the plugin", async () => {
    const server = await loadServer()
    await server["tool.execute.after"]({ tool: "bash" }, { output: "x".repeat(20 * 1024) })
    const events = readHookEvents()
    expect(events[0].output.length).toBe(16 * 1024)
  })

  test("the hook does not run when CAPTAIN_BLACKBOX is off", async () => {
    process.env["CAPTAIN_BLACKBOX"] = "off"
    const server = await loadServer()
    await server["tool.execute.before"]({ tool: "bash", sessionID: "ses-1" }, { args: { command: "ls" } })
    expect(readHookEvents().length).toBe(0)
  })

  test("a broken captain binary never breaks the tool call", async () => {
    await chmod(fakeBin, 0o000)
    const server = await loadServer()
    await expect(
      server["tool.execute.before"]({ tool: "bash", sessionID: "ses-1" }, { args: { command: "ls" } }),
    ).resolves.toBeUndefined()
    await expect(server["tool.execute.after"]({ tool: "bash" }, { output: "text" })).resolves.toBeUndefined()
  })

  test("the hook runs without the routing plugin enabled", async () => {
    // CAPTAIN_ROUTE_PLUGIN is not set: routing is off everywhere, the
    // tool-boundary layer - including this hook - still runs.
    delete process.env["CAPTAIN_ROUTE_PLUGIN"]
    const server = await loadServer()
    await server["tool.execute.before"]({ tool: "bash", sessionID: "ses-1" }, { args: { command: "ls" } })
    expect(readHookEvents().length).toBe(1)
  })
})

describe("the blackbox enforce refusal (BB5)", () => {
  test("an enforce refusal throws before the tool runs, and a retry meets the same refusal", async () => {
    // The fake captain refuses every pre screening: the same event must
    // refuse twice, so a worker cannot retry its way past it.
    await writeFile(fakeBin, REFUSING_CAPTAIN, { mode: 0o700 })
    process.env["CAPTAIN_BLACKBOX"] = "enforce"
    const server = await loadServer()
    for (let attempt = 0; attempt < 2; attempt++) {
      await expect(
        server["tool.execute.before"]({ tool: "bash", sessionID: "ses-1" }, { args: { command: "npm install example-package@1.2.3" } }),
      ).rejects.toThrow("blackbox_rule_match:pkg-exact")
    }
    const events = readHookEvents()
    expect(events.length).toBe(2)
    expect(events[0].args.command).toBe("npm install example-package@1.2.3")
  })

  test("a post-tool observation never refuses, whatever the hook says", async () => {
    await writeFile(fakeBin, REFUSING_CAPTAIN, { mode: 0o700 })
    process.env["CAPTAIN_BLACKBOX"] = "enforce"
    const server = await loadServer()
    await expect(server["tool.execute.after"]({ tool: "bash" }, { output: "text" })).resolves.toBeUndefined()
  })

  test("a refusal is honored in enforce mode only", async () => {
    await writeFile(fakeBin, REFUSING_CAPTAIN, { mode: 0o700 })
    process.env["CAPTAIN_BLACKBOX"] = "audit"
    const server = await loadServer()
    await expect(
      server["tool.execute.before"]({ tool: "bash", sessionID: "ses-1" }, { args: { command: "ls" } }),
    ).resolves.toBeUndefined()
  })

  test("a hook that crashes or says nothing decides nothing", async () => {
    await writeFile(fakeBin, "#!/bin/sh\nexit 3\n", { mode: 0o700 })
    process.env["CAPTAIN_BLACKBOX"] = "enforce"
    const server = await loadServer()
    await expect(
      server["tool.execute.before"]({ tool: "bash", sessionID: "ses-1" }, { args: { command: "ls" } }),
    ).resolves.toBeUndefined()
  })
})
