import assert from "node:assert/strict"
import { chmod, mkdir, mkdtemp, readdir, readFile, realpath, rm } from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { setTimeout as sleep } from "node:timers/promises"

// Combined check: a stock opencode release with the captain plugin, a real
// Go brain and real worker child processes. The worker is a fake `claude` that speaks the stream-json
// protocol and records what it receives; its one-shot mode answers the
// director's plan and synthesis calls. Both servers run under a macOS
// sandbox that allows outbound connections only to the ports this check
// assigned, so neither can reach the operator's brain, opencode serve,
// egress proxy or the internet. The brain gets a temporary home.

type Message = { info: { id: string; role: string; error?: unknown }; parts: { type: string; text?: string }[] }
type Record = { pid: number; event: string; text: string }

const FAKE_CLAUDE = `
const fs = require("node:fs")
const log = process.env.FAKE_LOG
const write = (event, text) => fs.appendFileSync(log, JSON.stringify({ pid: process.pid, event, text }) + "\\n")
const out = (value) => process.stdout.write(JSON.stringify(value) + "\\n")
if (process.argv.includes("--version")) {
  console.log("2.1.0 (Claude Code)")
  process.exit(0)
}
if (process.argv[2] === "auth") {
  console.log(JSON.stringify({ loggedIn: true }))
  process.exit(0)
}
// A director, synthesis or review call: one prompt on stdin, one JSON object out.
if (!process.argv.includes("--input-format")) {
  const prompt = await new Response(Bun.stdin.stream()).text()
  const answer = (kind, value) => {
    write("oneshot", kind)
    out({ result: typeof value === "string" ? value : JSON.stringify(value), is_error: false, total_cost_usd: 0, usage: {} })
    process.exit(0)
  }
  if (prompt.includes("router manager (director)"))
    answer("plan", {
      class: "medium",
      workers: [
        { leg: "claude", brief: "MEMBER_ONE draft the first approach" },
        { leg: "claude", brief: "MEMBER_TWO draft the second approach" },
      ],
      rationale: "two takes",
    })
  if (prompt.includes("Parallel workers each handled part"))
    answer("synthesis", {
      synthesis: "SYNTH_OK merged answer",
      scores: [
        { worker: "w1-claude", quality: 8, verdict: "good", notes: "" },
        { worker: "w2-claude", quality: 7, verdict: "good", notes: "" },
      ],
    })
  answer("other", "{}")
}
let task
let holding
for await (const line of console) {
  if (!line.trim()) continue
  const content = JSON.parse(line).message?.content
  const text = typeof content === "string" ? content : JSON.stringify(content)
  if (task !== undefined) {
    write("note", text)
    continue
  }
  task = text
  write("start", text)
  out({ type: "system", subtype: "init", model: "fake-claude" })
  out({ type: "stream_event", event: { type: "content_block_delta", delta: { type: "text_delta", text: "WORKER_OK " } } })
  // HOLD_: a worker still busy when its brain dies keeps working on its own.
  const holds = fs.readFileSync(log, "utf8").split("\\n").filter((item) => item.includes('"start"') && item.includes("HOLD_CRASH")).length
  if (text.includes("HOLD_CRASH") && holds === 1) {
    holding = setInterval(() => write("tick", "HOLD_CRASH"), 200)
    write("waiting", "HOLD_CRASH")
    continue
  }
  const marker = text.match(/WAIT_[A-Z]+/)?.[0]
  const seen = marker && fs.readFileSync(log, "utf8").split("\\n").filter((item) => item.includes('"start"') && item.includes(marker)).length
  if (marker && seen === 1) {
    write("waiting", marker)
    continue
  }
  if (text.includes("GUARD_COMMIT")) {
    const commit = Bun.spawnSync(["git", "commit", "--allow-empty", "-m", "fixture\\n\\nCo-authored-by: Claude <noreply@anthropic.com>"], {
      cwd: process.cwd(),
      env: process.env,
    })
    write("guard", JSON.stringify({ code: commit.exitCode, stderr: commit.stderr.toString() }))
  }
  if (text.includes("EUCLID_WRITE"))
    out({ type: "assistant", message: { model: "fake-claude", content: [{ type: "tool_use", id: "tool_1", name: "Edit", input: { file_path: "notes.md" } }] } })
  out({ type: "assistant", message: { model: "fake-claude", content: [{ type: "text", text: "WORKER_OK done" }] } })
  out({ type: "result", subtype: "success", result: "WORKER_OK done", usage: { input_tokens: 12, output_tokens: 3 }, total_cost_usd: 0 })
  process.exit(0)
}
if (!holding) process.exit(0)
`

export async function runCombined(brainBinary: string, opencodeBinary: string) {
  const plugin = path.resolve(import.meta.dir, "../captain.ts")
  const scratch = await realpath(await mkdtemp(path.join(os.tmpdir(), "captain-combined-")))
  const directory = path.join(scratch, "project")
  const secondWorkspace = path.join(scratch, "project-two")
  const home = path.join(scratch, "home")
  const bin = path.join(scratch, "bin")
  const log = path.join(scratch, "worker.jsonl")
  const securityLog = path.join(scratch, "security.jsonl")
  for (const item of [directory, secondWorkspace, home, bin]) await mkdir(item)
  await Bun.write(log, "")
  await Bun.write(path.join(scratch, "fake-claude.mjs"), FAKE_CLAUDE)
  await Bun.write(
    path.join(bin, "claude"),
    `#!/bin/sh\nFAKE_LOG="${log}" exec "${process.execPath}" "${path.join(scratch, "fake-claude.mjs")}" "$@"\n`,
  )
  await chmod(path.join(bin, "claude"), 0o755)
  await Bun.spawn(["/usr/bin/git", "init", "-q", directory]).exited
  // Without the guard this commit would succeed, so a refusal is the guard's.
  await Bun.spawn(["/usr/bin/git", "-C", directory, "config", "user.email", "fixture@example.invalid"]).exited
  await Bun.spawn(["/usr/bin/git", "-C", directory, "config", "user.name", "Fixture"]).exited
  // opencode installs @opencode-ai/plugin, pinned to its own version, into
  // its config folder before it loads a plugin. The sandbox has no network,
  // so the install happens here, before the server starts.
  const version = Bun.spawnSync([opencodeBinary, "--version"]).stdout.toString().trim()
  const configDir = path.join(scratch, "config", "opencode")
  await mkdir(configDir, { recursive: true })
  await Bun.write(path.join(configDir, "package.json"), JSON.stringify({ dependencies: { "@opencode-ai/plugin": version } }))
  const install = Bun.spawnSync([process.execPath, "install", "--silent"], { cwd: configDir })
  assert.equal(install.exitCode, 0, `install @opencode-ai/plugin@${version}: ${install.stderr.toString()}`)
  // opencode skips its own install only when an npm lockfile lists the package.
  await Bun.write(
    path.join(configDir, "package-lock.json"),
    JSON.stringify({ lockfileVersion: 3, packages: { "": { dependencies: { "@opencode-ai/plugin": version } } } }),
  )
  // An existing main brain is all a run needs to be journaled into it.
  await mkdir(path.join(home, ".euclid"))
  // BB2's synthetic snapshot: the host fixture plus the absolute form of the
  // protected directory, so the rule holds whichever form opencode hands the
  // hook (relative path from the model, resolved path from the host).
  const snapshotPath = path.join(scratch, "bb2-snapshot.json")
  const snapshotRules = JSON.parse(
    await readFile(path.join(import.meta.dir, "../../pkg/captaincode/testdata/blackbox/snapshot.json"), "utf8"),
  ) as { paths: { id: string; path: string; kind: string; severity: string }[] }
  for (const [index, root] of [directory, secondWorkspace].entries()) {
  snapshotRules.paths.push({
    id: `path-absolute-${index + 1}`,
    path: `${root}/config/private/**`,
    kind: "protected_path",
    severity: "critical",
  })
}
  await Bun.write(snapshotPath, JSON.stringify(snapshotRules))

  const free = async () => {
    const probe = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: () => new Response() })
    const port = probe.port
    await probe.stop(true)
    return port
  }
  // The fixture model: a local OpenAI-compatible endpoint that answers one
  // tool call and then the final text. It is what makes a real opencode tool
  // call possible in this check without a provider or any network path. The
  // user text names the file to write, so each workspace gets its own target.
  const fixtureModel = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    async fetch(request) {
      const url = new URL(request.url)
      if (request.method !== "POST" || !url.pathname.endsWith("/chat/completions")) {
        return new Response("fixture model: unknown route", { status: 404 })
      }
      const body = JSON.parse(await request.text()) as {
        model?: string
        stream?: boolean
        tools?: unknown[]
        messages?: { role: string; content?: unknown }[]
      }
      const target = /WRITE (\S+)/.exec(String(body.messages?.find((m) => m.role === "user")?.content ?? ""))?.[1]
      const callAgain = !body.tools || body.tools.length === 0 || (body.messages ?? []).every((m) => m.role !== "tool")
      const chunks =
        target && callAgain
          ? [
              {
                choices: [
                  {
                    index: 0,
                    delta: {
                      role: "assistant",
                      tool_calls: [
                        {
                          index: 0,
                          id: "call_bb2",
                          type: "function",
                          function: { name: "write", arguments: JSON.stringify({ filePath: target, content: "bb2\n" }) },
                        },
                      ],
                    },
                    finish_reason: null,
                  },
                ],
              },
              { choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }] },
            ]
          : [
              { choices: [{ index: 0, delta: { content: "FIXTURE_HOOK_DONE" }, finish_reason: null }] },
              { choices: [{ index: 0, delta: {}, finish_reason: "stop" }] },
            ]
      const id = `chatcmpl-fixture-${Date.now()}`
      const created = Math.floor(Date.now() / 1000)
      if (!body.stream) {
        const first = chunks[0]!.choices[0]!.delta
        const last = chunks[chunks.length - 1]!.choices[0]!
        return Response.json({
          id,
          object: "chat.completion",
          created,
          model: body.model ?? "fixture",
          choices: [
            {
              index: 0,
              message: { role: "assistant", content: first.content ?? null, tool_calls: first.tool_calls ?? null },
              finish_reason: last.finish_reason,
            },
          ],
          usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
        })
      }
      const sse = chunks
        .map((chunk) => `data: ${JSON.stringify({ id, object: "chat.completion.chunk", created, model: body.model ?? "fixture", ...chunk })}\n\n`)
        .join("")
      return new Response(`${sse}data: [DONE]\n\n`, { headers: { "content-type": "text/event-stream" } })
    },
  })
  const ports = { brain: await free(), proxy: await free(), frontend: await free(), bb2a: await free(), bb2b: await free(), model: fixtureModel.port }
  const profile = path.join(scratch, "loopback.sb")
  await Bun.write(
    profile,
    [
      "(version 1)",
      "(allow default)",
      "(deny network-outbound)",
      "(allow network-outbound (remote unix-socket))",
      ...Object.values(ports).map((port) => `(allow network-outbound (remote ip "localhost:${port}"))`),
    ].join("\n"),
  )
  const brainURL = `http://127.0.0.1:${ports.brain}`
  const processes: ReturnType<typeof Bun.spawn>[] = []
  const outputs: { name: string; text: string }[] = []
  const readers: Promise<void>[] = []
  const spawn = (name: string, cmd: string[], cwd: string, env: Record<string, string>, ready: string) => {
    const child = Bun.spawn(["/usr/bin/sandbox-exec", "-f", profile, ...cmd], {
      cwd,
      env,
      stdout: "pipe",
      stderr: "pipe",
    })
    processes.push(child)
    const output = { name, text: "" }
    outputs.push(output)
    const read = async (stream: ReadableStream<Uint8Array>) => {
      const reader = stream.getReader()
      try {
        while (true) {
          const next = await reader.read()
          if (next.done) return
          output.text = (output.text + new TextDecoder().decode(next.value)).slice(-32000)
        }
      } finally {
        reader.releaseLock()
      }
    }
    readers.push(read(child.stdout), read(child.stderr))
    return until(() => {
      assert.equal(child.exitCode, null, `${name} exited:\n${output.text}`)
      return output.text.includes(ready)
    }, 30000).then(() => ({ child, output }))
  }

  const startBrain = () =>
    spawn(
      "brain",
      [brainBinary, "brain", "--addr", `127.0.0.1:${ports.brain}`],
      scratch,
      {
        HOME: home,
        PATH: `${bin}:/usr/bin:/bin`,
        TMPDIR: scratch,
        CAPTAIN_LEGS: "claude",
        CAPTAIN_TRIAGE: "0",
        CAPTAIN_TRIAGE_JEV: "0",
        CAPTAIN_OPENCODE_SPAWN: "0",
        CAPTAIN_PROXY_ADDR: `127.0.0.1:${ports.proxy}`,
        CAPTAIN_EUCLID_AUTODISTILL: "0",
        CAPTAIN_EUCLID_AUTOINDEX: "0",
        CAPTAIN_PRIVATE_CURATE: "0",
        CAPTAIN_SOLO_VERIFY: "0",
      },
      `listening on ${brainURL}`,
    )

  const config = {
    model: "captain/auto",
    enabled_providers: ["captain", "fixture"],
    autoupdate: false,
    formatter: false,
    lsp: false,
    // File writes and shell are allowed so the Blackbox case below can run a
    // real tool call; every other tool keeps the fixture's deny-by-default.
    permission: { "*": "deny", edit: "allow", bash: "allow" },
    agent: { title: { disable: true }, summary: { disable: true } },
    plugin: [`file://${plugin}`],
    provider: {
      captain: {
        name: "Captain",
        npm: "@ai-sdk/openai-compatible",
        models: Object.fromEntries(
          ["auto", "claude", "team", "frontier"].map((id) => [id, { name: id, limit: { context: 100000, output: 4096 } }]),
        ),
        options: { apiKey: "combined-key", baseURL: `${brainURL}/v1`, headers: { "X-Captain-Cwd": "{env:CAPTAIN_CWD}" } },
      },
      fixture: {
        name: "Fixture",
        npm: "@ai-sdk/openai-compatible",
        models: { toolcall: { name: "toolcall", limit: { context: 100000, output: 4096 } } },
        options: { apiKey: "fixture-key", baseURL: `http://127.0.0.1:${ports.model}/v1` },
      },
    },
  }
  const auth = `Basic ${Buffer.from("opencode:combined-password").toString("base64")}`
  const frontendEnv = (workspace: string) => ({
        PATH: `${path.dirname(process.execPath)}:/usr/bin:/bin`,
        HOME: path.join(scratch, "frontend-home"),
        TMPDIR: scratch,
        XDG_CONFIG_HOME: path.join(scratch, "config"),
        XDG_CACHE_HOME: path.join(scratch, "cache"),
        XDG_DATA_HOME: path.join(scratch, "data"),
        XDG_STATE_HOME: path.join(scratch, "state"),
        OPENCODE_CONFIG_CONTENT: JSON.stringify(config),
        OPENCODE_DISABLE_MODELS_FETCH: "1",
        OPENCODE_DISABLE_AUTOUPDATE: "1",
        OPENCODE_DISABLE_PROJECT_CONFIG: "1",
        OPENCODE_DISABLE_DEFAULT_PLUGINS: "1",
        OPENCODE_DISABLE_EXTERNAL_SKILLS: "1",
        OPENCODE_EXPERIMENTAL_DISABLE_FILEWATCHER: "1",
        OPENCODE_SERVER_USERNAME: "opencode",
        OPENCODE_SERVER_PASSWORD: "combined-password",
        CAPTAIN_ROUTE_PLUGIN: "1",
        CAPTAIN_BRAIN_URL: brainURL,
        CAPTAIN_CWD: workspace,
        CAPTAIN_LOG: path.join(scratch, "route.log"),
        // The Blackbox audit hook (BB2): these records are what the fixture
        // below checks. The engine is the same binary as the brain.
        CAPTAIN_BLACKBOX: "audit",
        CAPTAIN_BLACKBOX_SNAPSHOT: snapshotPath,
        CAPTAIN_BLACKBOX_STATE: securityLog,
        CAPTAIN_BIN: brainBinary,
        CAPTAIN_ACTION_GATE: "off",
  })
  const startFrontend = (workspace: string, listen: number, name = "frontend", extra: Record<string, string> = {}) =>
    spawn(
      name,
      [opencodeBinary, "--print-logs", "--log-level", "INFO", "serve", "--hostname", "127.0.0.1", "--port", String(listen)],
      workspace,
      { ...frontendEnv(workspace), ...extra },
      `server listening on http://127.0.0.1:${listen}`,
    )

  const call = async (route: string, body?: unknown) => {
    const response = await fetch(`http://127.0.0.1:${ports.frontend}${route}`, {
      method: body === undefined ? "GET" : "POST",
      headers: { authorization: auth, "content-type": "application/json", "x-opencode-directory": directory },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(60000),
    })
    assert.ok(response.ok, `${route}: ${response.status} ${await response.clone().text()}`)
    return response
  }
  const session = async () =>
    ((await (await call("/session", { title: "combined" })).json()) as { id: string }).id
  const send = async (id: string, text: string) =>
    (await (await call(`/session/${id}/message`, { parts: [{ type: "text", text }] })).json()) as Message
  // The same three calls against any frontend, so the Blackbox case below can
  // address its own workspace and port.
  const callIn = async (workspace: string, listen: number, route: string, body?: unknown) => {
    const response = await fetch(`http://127.0.0.1:${listen}${route}`, {
      method: body === undefined ? "GET" : "POST",
      headers: { authorization: auth, "content-type": "application/json", "x-opencode-directory": workspace },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(60000),
    })
    assert.ok(response.ok, `${route}: ${response.status} ${await response.clone().text()}`)
    return response
  }
  const sessionIn = async (workspace: string, listen: number) =>
    ((await (await callIn(workspace, listen, "/session", { title: "blackbox" })).json()) as { id: string }).id
  // An unprefixed turn on the fixture model: CAPTAIN_AUTO_MODEL="" on these
  // frontends leaves the model alone, so the local fixture provider answers,
  // no provider key and no network path are involved.
  const sendFixture = async (workspace: string, listen: number, id: string, text: string) =>
    ((
      await (
        await callIn(workspace, listen, `/session/${id}/message`, {
          parts: [{ type: "text", text }],
          model: { providerID: "fixture", modelID: "toolcall" },
        })
      ).json()
    ) as Message)
  // The plugin answers /btw and /interrupt out of band, then refuses the
  // typed message so the TUI queues no copy of it: the server reports the
  // refusal as an error.
  const control = async (id: string, text: string) => {
    const response = await fetch(`http://127.0.0.1:${ports.frontend}/session/${id}/message`, {
      method: "POST",
      headers: { authorization: auth, "content-type": "application/json", "x-opencode-directory": directory },
      body: JSON.stringify({ parts: [{ type: "text", text }] }),
      signal: AbortSignal.timeout(60000),
    })
    assert.ok(!response.ok, `${text} was queued as a turn: ${await response.text()}`)
  }
  const records = async () =>
    (await readFile(log, "utf8"))
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line) as Record)
  const alive = (pid: number) => {
    try {
      process.kill(pid, 0)
      return true
    } catch {
      return false
    }
  }
  const waiting = async (marker: string) => {
    await until(async () => (await records()).some((item) => item.event === "waiting" && item.text === marker), 30000)
    return (await records()).find((item) => item.event === "waiting" && item.text === marker)!.pid
  }

  const reports: string[] = []
  try {
    let brain = await startBrain()
    await startFrontend(directory, ports.frontend)

    const first = await session()
    const done = await send(first, "/claude COMBINED_PLAIN task")
    assert.ok(
      done.parts.some((part) => part.text?.includes("WORKER_OK")),
      JSON.stringify(done),
    )
    const starts = (await records()).filter((item) => item.event === "start" && item.text.includes("COMBINED_PLAIN"))
    assert.ok(starts.length >= 1, "the Go brain started the worker process")
    // The brain writes its ledger about a second after a run, not before the reply.
    const ledger = path.join(home, ".captaincode", "state.json")
    const ledgerRuns = async () => {
      const text = await readFile(ledger, "utf8").catch(() => "")
      if (!text) return []
      const state = JSON.parse(text) as {
        events?: { task: string; leg: string; outcome: string; duration_ms: number }[]
      }
      return state.events ?? []
    }
    await until(async () => (await ledgerRuns()).some((item) => item.task.includes("COMBINED_PLAIN")), 10000)
    const events = await ledgerRuns()
    const state = { events }
    const runs = events.filter((item) => item.task.includes("COMBINED_PLAIN") && item.leg === "claude")
    assert.equal(runs.length, 1, `one ledger run for one turn: ${JSON.stringify(state.events)}`)
    assert.equal(runs[0].outcome, "ok")
    assert.ok(runs[0].duration_ms > 0, "the brain measured the run duration")
    reports.push("PASS: a forced turn runs one real worker process and the brain records one measured run")

    const second = await session()
    const runningA = send(first, "/claude WAIT_ALPHA task")
    const pidA = await waiting("WAIT_ALPHA")
    const runningB = send(second, "/claude WAIT_BRAVO task")
    const pidB = await waiting("WAIT_BRAVO")
    assert.ok(alive(pidA) && alive(pidB))

    const beforeNote = (await records()).filter((item) => item.event === "start").length
    await control(first, "/btw COMBINED_NOTE use the smaller fixture")
    await until(async () =>
      (await records()).some(
        (item) => item.pid === pidA && item.event === "note" && item.text.includes("COMBINED_NOTE"),
      ),
    )
    reports.push("PASS: /btw reaches only its session worker through the Go brain")

    await control(first, "/interrupt COMBINED_HANDOFF")
    await until(async () =>
      (await records()).some(
        (item) => item.pid === pidA && item.event === "note" && item.text.includes("COMBINED_HANDOFF"),
      ),
    )
    assert.ok(alive(pidB), "handoff must leave the other session running")

    await call(`/session/${first}/abort`, {})
    const abortedA = await runningA
    await until(() => !alive(pidA), 15000)
    await sleep(1000)
    assert.ok(alive(pidB), "stopping one session must not stop another session's worker in the same folder")
    const savedA = ((await (await call(`/session/${first}/message`)).json()) as Message[])
    assert.ok(
      savedA.some(
        (item) => item.info.id === abortedA.info.id && item.parts.some((part) => part.text?.includes("WORKER_OK")),
      ),
      "partial output is kept",
    )
    reports.push(
      "PASS: abort ends its own worker process, keeps partial output and leaves another session's worker running",
    )

    await call(`/session/${second}/abort`, {})
    await runningB
    await until(() => !alive(pidB), 15000)
    await sleep(500)
    assert.equal(
      (await records()).some(
        (item) => item.pid === pidB && item.event === "note" && item.text.includes("COMBINED_HANDOFF"),
      ),
      false,
      "handoff must not reach another session",
    )
    const afterNote = (await records()).filter((item) => item.event === "start" && item.text.includes("COMBINED_NOTE"))
    assert.equal(afterNote.length, 0, "a delivered note never starts a worker")
    assert.equal(
      (await records()).some(
        (item) => item.pid === pidB && item.event === "note" && item.text.includes("COMBINED_NOTE"),
      ),
      false,
      "a note must not reach another session in the same workspace",
    )
    assert.equal(
      (await records()).filter((item) => item.event === "start" && /WAIT_|COMBINED_PLAIN/.test(item.text)).length,
      beforeNote,
      "no worker was replayed",
    )
    reports.push("PASS: the second session stops on its own abort; the delivered note never starts a worker")

    brain.child.kill("SIGTERM")
    await brain.child.exited
    const beforeRestart = (await records()).length
    brain = await startBrain()
    await sleep(500)
    assert.equal((await records()).length, beforeRestart, "a brain restart must not replay worker runs")
    const resumed = await send(first, "/claude COMBINED_AFTER_RESTART task")
    assert.ok(resumed.parts.some((part) => part.text?.includes("WORKER_OK")))
    const history = ((await (await call(`/session/${first}/message`)).json()) as Message[])
    assert.ok(history.some((item) => item.parts.some((part) => part.text?.includes("COMBINED_PLAIN"))))
    reports.push("PASS: after a brain restart the session keeps its history, nothing replays and a follow-up runs")

    const replyText = (item: Message) => item.parts.map((part) => part.text ?? "").join("")
    const startsFor = async (marker: string) =>
      (await records()).filter((item) => item.event === "start" && item.text.includes(marker))
    const oneshots = async () => (await records()).filter((item) => item.event === "oneshot").map((item) => item.text)

    // A program: a group chained to a second turn runs both turns, in order,
    // and the second reads the first one's answer.
    const program = await send(await session(), "(/claude PROG_ONE list the files) > /claude PROG_TWO summarise them")
    const steps = (await records()).filter((item) => item.event === "start" && /PROG_ONE|PROG_TWO/.test(item.text))
    assert.equal(steps.length, 2, `two program steps ran: ${JSON.stringify(steps)}`)
    assert.match(steps[0].text, /step 1\/2/)
    assert.match(steps[1].text, /step 2\/2/)
    assert.match(steps[1].text, /WORKER_OK/, "step 2 reads step 1's answer")
    assert.match(replyText(program), /finished/, replyText(program))
    reports.push("PASS: a program runs its steps in order through the Go brain, and step 2 reads step 1's answer")

    // A team: the director plans two members, both run, and the reply is the
    // director's synthesis of their answers.
    const before = (await oneshots()).length
    const team = await send(await session(), "/team /claude TEAM_TASK compare two approaches")
    const calls = (await oneshots()).slice(before)
    assert.ok(
      calls.includes("plan"),
      `the director planned the team: ${JSON.stringify(calls)} reply=${replyText(team)} starts=${JSON.stringify((await startsFor("TEAM_TASK")).map((item) => item.text.slice(-300)))}`,
    )
    assert.equal((await startsFor("MEMBER_ONE")).length, 1, "the first member ran")
    assert.equal((await startsFor("MEMBER_TWO")).length, 1, "the second member ran")
    assert.ok(calls.includes("synthesis"), `the director synthesised the answers: ${JSON.stringify(calls)}`)
    assert.match(replyText(team), /SYNTH_OK/, replyText(team))
    reports.push("PASS: /team plans two members, runs both and answers with the director's synthesis")

    // Euclid: a run is journaled into the existing main brain, with the files it touched.
    await send(await session(), "/claude EUCLID_WRITE update the notes")
    const journal = path.join(home, ".euclid", "journal")
    const entries = async () => {
      const files = await readdir(journal).catch(() => [] as string[])
      const lines = await Promise.all(
        files.filter((file) => file.endsWith(".jsonl")).map((file) => readFile(path.join(journal, file), "utf8")),
      )
      return lines
        .flatMap((text) => text.split("\n"))
        .filter(Boolean)
        .map((line) => JSON.parse(line) as { task?: string; leg?: string; outcome?: string; files?: string[] })
    }
    await until(async () => (await entries()).some((item) => item.task?.includes("EUCLID_WRITE")), 10000)
    const entry = (await entries()).find((item) => item.task?.includes("EUCLID_WRITE"))!
    assert.equal(entry.leg, "claude")
    assert.equal(entry.outcome, "ok")
    assert.deepEqual(entry.files, ["notes.md"])
    reports.push("PASS: a worker run is journaled into the Euclid brain with its leg, outcome and files")

    // Guard: a worker's commit that credits an AI agent is refused before git runs.
    await send(await session(), "/claude GUARD_COMMIT commit the fixture")
    const guard = (await records()).find((item) => item.event === "guard")
    assert.ok(guard, "the worker tried the commit")
    const refused = JSON.parse(guard.text) as { code: number; stderr: string }
    assert.equal(refused.code, 126, refused.stderr)
    assert.match(refused.stderr, /credits an AI agent/)
    const head = Bun.spawnSync(["/usr/bin/git", "-C", directory, "rev-parse", "--verify", "HEAD"])
    assert.notEqual(head.exitCode, 0, "no commit was made")
    const conduct = await readFile(path.join(home, ".captaincode", "conduct.jsonl"), "utf8")
    assert.match(conduct, /refused/)
    reports.push("PASS: the worker guard refuses a commit crediting an AI agent, and records the refusal")

    // Crash: the brain dies mid-run. Stock opencode retries the request
    // until a brain answers; the restarted brain ends the turn instead of
    // running it again, the orphaned worker does not keep working, and an
    // explicit resend runs.
    const crashed = await session()
    const holding = send(crashed, "/claude HOLD_CRASH long task")
    const pidC = await waiting("HOLD_CRASH")
    brain.child.kill("SIGKILL")
    await brain.child.exited
    await sleep(3000)
    const startsBefore = (await records()).filter((item) => item.event === "start").length
    brain = await startBrain()
    const ended = await Promise.race([holding, sleep(60000).then(() => undefined)])
    assert.ok(ended, "the retried turn ends once a brain is back")
    assert.match(replyText(ended), /not run a second time/, replyText(ended))
    assert.equal(
      (await records()).filter((item) => item.event === "start").length,
      startsBefore,
      "a restarted brain replays nothing",
    )
    await until(() => !alive(pidC), 15000).catch(() =>
      assert.fail(`the worker of a crashed brain is still running (pid ${pidC})`),
    )
    const again = await send(crashed, "/claude HOLD_RESENT task")
    assert.match(replyText(again), /WORKER_OK/)
    reports.push("PASS: a brain crash ends the turn and its worker, the retry runs nothing, and a resend runs")

    // BB2: a live opencode tool call reaches the Blackbox audit hook, in two
    // workspaces at the same time, and the hook never refuses the call.
    type SecurityRow = {
      mode: string
      phase: string
      tool: string
      disposition: string
      status: string
      snapshot_id: string
      workspace_id: string
      session_id: string
      finding_count: number
      findings: { rule_id: string; severity: string }[]
      coverage?: { category: string; status: string }[]
      reasons?: string[]
      omitted_ids?: string[]
    }
    const security = async () =>
      (await readFile(securityLog, "utf8").catch(() => ""))
        .split("\n")
        .filter(Boolean)
        .map((line) => JSON.parse(line) as SecurityRow)
    const isolatedHome = (suffix: string) => ({
      HOME: path.join(scratch, `blackbox-home-${suffix}`),
      XDG_STATE_HOME: path.join(scratch, `blackbox-state-${suffix}`),
      XDG_DATA_HOME: path.join(scratch, `blackbox-data-${suffix}`),
      XDG_CACHE_HOME: path.join(scratch, `blackbox-cache-${suffix}`),
      // With no default leg the plugin leaves the message's own model alone,
      // so the local fixture provider answers and no provider key or network
      // path is involved. Forced leg prefixes still route as before.
      CAPTAIN_AUTO_MODEL: "",
    })
    for (const suffix of ["a", "b"]) await mkdir(path.join(scratch, `blackbox-home-${suffix}`), { recursive: true })
    await startFrontend(directory, ports.bb2a, "blackbox-a", isolatedHome("a"))
    await startFrontend(secondWorkspace, ports.bb2b, "blackbox-b", isolatedHome("b"))
    const target = "config/private/leak.txt"
    const sessionA = await sessionIn(directory, ports.bb2a)
    const sessionB = await sessionIn(secondWorkspace, ports.bb2b)
    const [workspaceA, workspaceB] = await Promise.all([
      sendFixture(directory, ports.bb2a, sessionA, `WRITE ${target}`),
      sendFixture(secondWorkspace, ports.bb2b, sessionB, `WRITE ${target}`),
    ])
    assert.match(replyText(workspaceA), /FIXTURE_HOOK_DONE/, replyText(workspaceA))
    assert.match(replyText(workspaceB), /FIXTURE_HOOK_DONE/, replyText(workspaceB))
    for (const root of [directory, secondWorkspace]) {
      const file = path.join(root, target)
      assert.equal(await readFile(file, "utf8").catch(() => ""), "bb2\n", `the audited tool call still ran in ${root}`)
    }
    await until(async () => (await security()).filter((row) => row.phase === "pre").length >= 2, 30000)
    const rows = await security()
    const describe = () => JSON.stringify(rows, null, 2)
    const pre = rows.filter((row) => row.phase === "pre" && row.tool === "write")
    const post = rows.filter((row) => row.phase === "post" && row.tool === "write")
    assert.equal(pre.length, 2, `one pre-tool record per workspace: ${describe()}`)
    assert.equal(post.length, 2, `one post-tool record per workspace: ${describe()}`)
    const forSession = (session: string) => {
      const before = pre.find((row) => row.session_id === session)
      const after = post.find((row) => row.session_id === session)
      assert.ok(before && after, `both records joined to session ${session}: ${describe()}`)
      assert.equal(before.mode, "audit", describe())
      assert.equal(before.status, "complete", describe())
      assert.equal(before.snapshot_id, "fixture", describe())
      assert.equal(before.disposition, "would_block", `a critical protected-path finding is a would_block: ${describe()}`)
      assert.ok(before.finding_count >= 1, describe())
      assert.ok(
        before.findings.some((finding) => finding.severity === "critical"),
        describe(),
      )
      assert.equal(after.disposition, "observed_after_execution", describe())
      assert.equal(after.workspace_id, before.workspace_id, `a workspace's records stay together: ${describe()}`)
      assert.equal(after.tool, "write", describe())
      assert.ok(before.workspace_id.length > 0, describe())
      assert.ok(
        !JSON.stringify(before).includes("config/private") && !JSON.stringify(before).includes("leak"),
        `the stream carries no path: ${describe()}`,
      )
      assert.ok(!JSON.stringify(after).includes("leak"), `the stream carries no output text: ${describe()}`)
      return before.workspace_id
    }
    const [idA, idB] = [forSession(sessionA), forSession(sessionB)]
    assert.notEqual(idA, idB, `two workspaces must not share a workspace id: ${describe()}`)
    reports.push(
      "PASS: two simultaneous workspaces run a live tool call, the Blackbox hook files would_block then observed_after_execution records, and the call is never refused",
    )
    return reports
  } catch (error) {
    const route = await readFile(path.join(scratch, "route.log"), "utf8").catch(() => "")
    const tails = outputs.map((item) => `--- ${item.name}\n${item.text.slice(-30000)}`).join("\n")
    throw new Error(`${String(error)}\n${reports.join("\n")}\n--- route log\n${route.slice(-3000)}\n${tails}`)
  } finally {
    for (const child of processes) if (child.exitCode === null) child.kill("SIGKILL")
    await Promise.all(processes.map((child) => child.exited))
    for (const item of await records().catch(() => [])) if (alive(item.pid)) process.kill(item.pid, "SIGKILL")
    await Promise.all(readers)
    await fixtureModel.stop(true)
    await rm(scratch, { recursive: true, force: true })
  }
}

async function until(check: () => boolean | Promise<boolean>, timeout = 10000) {
  const end = performance.now() + timeout
  while (performance.now() < end) {
    if (await check()) return
    await sleep(50)
  }
  assert.fail("combined check timed out")
}

if (import.meta.main) {
  const brain = process.env["CAPTAIN_BRAIN_BIN"]
  const opencode = process.env["OPENCODE_BIN"]
  if (!brain || !opencode)
    throw new Error("set CAPTAIN_BRAIN_BIN to a captain built from this checkout and OPENCODE_BIN to a stock opencode")
  for (const report of await runCombined(brain, opencode)) console.log(report)
}
