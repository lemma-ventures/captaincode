// Captain Code as a STOCK-opencode plugin, no fork.
//
// This replaces the fork's patch to packages/opencode/src/session/prompt.ts.
// Stock opencode loads it out of `plugin:` in opencode.json.
//
// What it replicates:
//   - forced leg prefixes (`/claude …`, `/team …`, `/frontier …`); the stored
//     message keeps the directive, the brain strips it for the worker
//   - the leg roster fetched from the brain (GET /v1/models, 60s cache) with a
//     static fallback, so a new leg needs no plugin change
//   - everything else goes to captain/auto, so the brain routes inside the turn
//     and the prompt never waits on a director call to render
//   - the route log the fork writes, so `tail -f /tmp/captain-route.log` still
//     shows what happened
//
// What no plugin slot can reach - the wordmark, the subagent footer labels -
// is listed in plugin/README.md beside what it replaces.

import { appendFileSync } from "node:fs"
import { spawnSync } from "node:child_process"

const BRAIN = process.env["CAPTAIN_BRAIN_URL"] ?? "http://127.0.0.1:14097"
const LOG = process.env["CAPTAIN_LOG"] ?? "/tmp/captain-route.log"
// Everything the user did not route by hand goes to captain/auto: the brain
// triages, asks the director only when it must, and dispatches INSIDE the turn.
// Deciding here instead would freeze the UI until the director answered, which
// is the whole reason routing used to live in a forked prompt loop.
const AUTO_MODEL = process.env["CAPTAIN_AUTO_MODEL"] ?? "auto"

// ONLY the user's terminal routes. The brain runs workers on a shared
// `opencode serve`, and that process loads plugins from the same config: if
// this hook acted there, every worker turn would be re-routed back into the
// brain, which would dispatch another worker. The launcher sets this variable
// for the TUI process alone (never in ~/.config/captain/env, which the brain
// and its serve inherit).
const ENABLED = process.env["CAPTAIN_ROUTE_PLUGIN"] === "1"

// ── secrets at the tool boundary ─────────────────────────────────────────────
// Unlike routing, this runs in EVERY process that loads the plugin - the
// workers' `opencode serve` most of all, since that is where tools run. A
// tool's output is masked before it enters the context (a .env keeps its
// names, loses its values; the operator's home becomes /Users/captain), a
// tool's arguments are restored on the way back (a file the model writes
// with [[secret:…]] in it lands with the real value), and files that are
// only secrets (private keys, credential stores) are refused. The engine is
// `captain redact` (one process per tool call, ~10ms); CAPTAIN_REDACT=off
// turns the whole layer off. See pkg/captaincode/redact.go.
const REDACT = (process.env["CAPTAIN_REDACT"] ?? "on").toLowerCase() !== "off"
const CAPTAIN_BIN = process.env["CAPTAIN_BIN"] ?? "captain"

function captainRedact(args: string[], input: string): string | null {
  try {
    const r = spawnSync(CAPTAIN_BIN, ["redact", ...args], { input, encoding: "utf8", maxBuffer: 64 * 1024 * 1024, timeout: 10_000 })
    if (r.status !== 0 || typeof r.stdout !== "string") return null
    return r.stdout
  } catch {
    return null // no captain on PATH: the wire proxy still stands
  }
}

function secretFileRefusal(path: string): string | null {
  try {
    const r = spawnSync(CAPTAIN_BIN, ["redact", "--check", path], { encoding: "utf8", timeout: 5_000 })
    return r.status === 3 ? String(r.stdout).trim() : null
  } catch {
    return null
  }
}

// restoreArgs puts real values back into every string of a tool's arguments.
function restoreArgs(v: any): any {
  if (typeof v === "string") {
    if (!v.includes("[[secret:") && !v.includes("/Users/captain") && !v.includes("/home/captain") && !v.includes("captain-user") && !v.includes("captain@example.invalid") && !v.includes("Captain Operator")) return v
    return captainRedact(["--restore"], v) ?? v
  }
  if (Array.isArray(v)) return v.map(restoreArgs)
  if (v && typeof v === "object") {
    for (const k of Object.keys(v)) v[k] = restoreArgs(v[k])
  }
  return v
}

const STATIC_LEGS = [
  "claude", "codex-cli", "cursor", "kimi", "glm", "gemini", "deepseek",
  "ds-flash", "minimax", "grok", "codex", "qwen", "free", "team", "frontier",
]

function log(line: string) {
  try {
    appendFileSync(LOG, `${new Date().toISOString().slice(11, 19)} [captain-plugin] ${line}\n`)
  } catch {
    /* logging must never break a prompt */
  }
}

let legs: string[] | null = null
let legsAt = 0

// legPattern builds the forced-prefix matcher from the legs the brain actually
// serves, longest first so `/codex-cli` is never read as `/codex`.
function legPattern(): RegExp {
  const ids = legs ?? STATIC_LEGS
  const sorted = [...ids].sort((a, b) => b.length - a.length).map((x) => x.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
  return new RegExp("^\\/(" + sorted.join("|") + ")\\b[\\s:]*", "i")
}

async function refreshLegs(): Promise<void> {
  if (Date.now() - legsAt < 60_000) return
  legsAt = Date.now()
  try {
    const r = await fetch(`${BRAIN}/v1/models`, { signal: AbortSignal.timeout(2000) })
    if (!r.ok) return
    const body = (await r.json()) as { data?: { id?: string }[] }
    const ids = (body.data ?? []).map((m) => m.id).filter((id): id is string => typeof id === "string")
    if (ids.length) legs = ids
  } catch {
    /* brain down: the static roster still routes forced prefixes */
  }
}

// A /repeat control word typed while a watch streams. The TUI queues the
// turn behind the stream ("QUEUED /repeat show", live 2026-09-13), but this
// hook fires the moment the message is submitted - so the word goes to the
// brain out of band now: the answer lands in the watching turn (the brain
// prints it there) and in a toast. The queued copy still runs when the
// watch ends; a status is idempotent, so that is a repeat, not a harm.
const REPEAT_CTL = /^\s*\/repeat\s+(show|status|finish|wrapup|stop|abort)\b\s*(.*)$/i

// /btw <note>: a note for the worker that is ALREADY running. Same timing
// problem, same answer: sent to the brain the moment it is typed, and the
// brain hands it to the running worker mid-turn (claude -p takes it on
// stdin, an opencode session as a merged message). The queued copy is then
// acknowledged by the brain instead of run - unless nobody could take it
// (codex, cursor, no worker), when it runs as the follow-up turn it is.
const BTW = /^\s*\/btw\b[\s:]*([\s\S]*)$/i

// /interrupt [reason]: stop the running worker WITHOUT losing its work. Sent
// the moment it is typed: claude and the opencode legs are asked to write a
// handoff (done / left / resume) and end; codex and cursor are stopped with
// what they streamed. The queued copy prints the outcome.
const INTERRUPT = /^\s*\/interrupt\b[\s:]*([\s\S]*)$/i

export const server = async (input?: { client?: any; directory?: string }) => ({
  "chat.message": async (_input: unknown, output: any) => {
    if (!ENABLED) return
    if (output?.message?.role !== "user") return
    const started = Date.now()
    await refreshLegs()

    const textPart = (output.parts ?? []).find((p: any) => p.type === "text" && typeof p.text === "string")
    // `opencode run` hands the prompt through wrapped in quotes; the TUI does
    // not. Unwrap so a forced prefix is found on both paths.
    let text: string = textPart?.text ?? ""
    const wrapped = text.length > 1 && text.startsWith('"') && text.endsWith('"')
    if (wrapped) text = text.slice(1, -1)

    // Control words answered OUT OF BAND are never queued as turns: the
    // brain acts the moment the word is typed, the toast says what it did,
    // and the message is then REFUSED here so the TUI does not also queue a
    // copy behind the running turn (live 2026-09-17: "/btw Cerebras sorry"
    // sat QUEUED for the length of a 15-minute grok run). The refusal
    // surfaces as the prompt's error, worded as the confirmation it is. A
    // word the brain could not act on (nothing running, brain down) goes
    // through as an ordinary turn, so nothing is ever lost.
    const cwd = process.env["CAPTAIN_CWD"] ?? input?.directory ?? ""
    const outOfBand = async (title: string, path: string, body: unknown, acted: (j: any) => boolean): Promise<string | null> => {
      try {
        const r = await fetch(`${BRAIN}${path}?cwd=${encodeURIComponent(cwd)}`, {
          method: "POST",
          body: JSON.stringify(body),
          signal: AbortSignal.timeout(10_000),
        })
        const j = (await r.json()) as { result?: string; error?: string }
        const msg = (j.result ?? j.error ?? `${r.status}`).replace(/[*_`#]/g, "").trim()
        const done = r.ok && acted(j)
        log(`${title} out of band → ${msg.slice(0, 80)} (acted=${done})`)
        await input?.client?.tui?.showToast?.({
          body: { title, message: msg.slice(0, 400), variant: done ? "success" : "info", duration: 8000 },
        })
        return done ? msg : null
      } catch (e) {
        log(`${title} out of band failed: ${String(e).slice(0, 80)}`)
        return null
      }
    }

    const ctl = text.match(REPEAT_CTL)
    if (ctl) {
      const msg = await outOfBand(`/repeat ${ctl[1].toLowerCase()}`, "/v1/repeat/ctl", { word: ctl[1].toLowerCase(), arg: ctl[2] ?? "" }, () => true)
      if (msg !== null) throw new Error(`captain: /repeat ${ctl[1].toLowerCase()} done - nothing queued. ${msg.slice(0, 200)}`)
    }

    const btw = text.match(BTW)
    if (btw && btw[1].trim()) {
      const msg = await outOfBand("/btw", "/v1/btw", { text: btw[1].trim() }, (j) => !!j.delivered)
      if (msg !== null) throw new Error(`captain: ${msg.slice(0, 240)} - nothing queued`)
    }

    const intr = text.match(INTERRUPT)
    if (intr) {
      const msg = await outOfBand("/interrupt", "/v1/interrupt", { reason: (intr[1] ?? "").trim() }, (j) => (j.asked?.length || j.stopped?.length || j.withdrawn) > 0)
      if (msg !== null) throw new Error(`captain: ${msg.slice(0, 240)} - nothing queued`)
    }

    const forced = text.match(legPattern())
    let leg: string | undefined
    if (forced) {
      leg = forced[1].toLowerCase()
      // The stored message is NOT rewritten: it is the user's transcript, and
      // the fork never touched it either. The brain strips `/grok`, `/team`,
      // `/quality` from every user turn when it builds the worker prompt
      // (stripCaptainDirectives), so the worker still sees only the task.
      // Rewriting it here made the transcript show the text WITHOUT the
      // directive the user typed (live 2026-09-11).
      log(`forced=${leg} via prefix  «${text.slice(forced[0].length, forced[0].length + 48)}»`)
    } else {
      leg = AUTO_MODEL
    }

    if (!leg) return
    const before = output.message.model
    output.message.model = { providerID: "captain", modelID: leg }
    log(`routed → ${leg} in ${Date.now() - started}ms (was ${before?.providerID}/${before?.modelID})`)
  },

  "tool.execute.before": async (input: { tool: string }, output: { args: any }) => {
    if (!REDACT || !output?.args) return
    const path = typeof output.args.filePath === "string" ? output.args.filePath : typeof output.args.path === "string" ? output.args.path : ""
    if (path && (input.tool === "read" || input.tool === "grep" || input.tool === "glob")) {
      const why = secretFileRefusal(path)
      if (why) {
        log(`refused ${input.tool} ${path}`)
        throw new Error(why)
      }
    }
    output.args = restoreArgs(output.args)
  },

  "tool.execute.after": async (input: { tool: string }, output: { output: string }) => {
    if (!REDACT || typeof output?.output !== "string" || output.output.length === 0) return
    const masked = captainRedact(["--source", `tool:${input.tool}`], output.output)
    if (masked !== null && masked !== output.output) output.output = masked
  },
})

// A path-loaded plugin module must carry an id, and opencode refuses a module
// that exports both server() and tui() ("not both"), so the sidebar half lives
// in its own entry (captain-tui.tsx).
export default { id: "captain", server }
