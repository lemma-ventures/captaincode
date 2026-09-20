/** @jsxImportSource @opentui/solid */
// Captain sidebar panel - the UI plugin that reshapes opencode's interface for
// captain-code. It renders captain's director, persistent worker legs with live
// status (idle/busy/cooling), hierarchical team scorecards, and a LIVE activity
// feed - all by polling the brain (`captain brain`, default http://127.0.0.1:14097).
import type { TuiPlugin, TuiPluginApi, TuiPluginModule } from "@opencode-ai/plugin/tui"
import { RGBA, TextAttributes } from "@opentui/core"
import { createSignal, createMemo, onCleanup, For, Show } from "solid-js"
import { existsSync } from "node:fs"
import { homedir, platform } from "node:os"
import { join } from "node:path"

const id = "captain:sidebar"

const BRAIN = (globalThis as any).process?.env?.CAPTAIN_BRAIN_URL ?? "http://127.0.0.1:14097"
// The folder this TUI is open in. One brain serves every captain-code TUI on
// the machine, so each call names its workspace (?cwd=) or the panel shows
// another project's workers and memory (DLM showed "memory: arc", 2026-09-12).
// The launcher exports CAPTAIN_CWD to the TUI; a bare `opencode` run falls
// back to its cwd.
const CWD: string = (globalThis as any).process?.env?.CAPTAIN_CWD || (globalThis as any).process?.cwd?.() || ""
const brainURL = (path: string) => `${BRAIN}${path}${CWD ? (path.includes("?") ? "&" : "?") + "cwd=" + encodeURIComponent(CWD) : ""}`

const SPIN = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"]

// Chrome profile: restyles the TUI to match the website demo without swapping
// live brain data. "demo" installs/selects the captain-demo theme and uses the
// demo cooling glyph; "default" restores the previous OpenCode theme.
type Chrome = "default" | "demo"
const DEMO_THEME = "captain-demo"
const DEMO_THEME_PATH = "themes/captain-demo.json"
const KV_CHROME = "captain.ui.chrome"
const KV_THEME_BEFORE = "captain.ui.theme.before_demo"
const envChrome = (): Chrome | undefined => {
  const v = String((globalThis as any).process?.env?.CAPTAIN_UI_CHROME ?? "").trim().toLowerCase()
  if (v === "demo" || v === "default") return v
  return undefined
}
const [chrome, setChrome] = createSignal<Chrome>("default")
const [chromeTick, setChromeTick] = createSignal(0)

type LegStat = { N: number; Scored: number; AvgQuality: number; AvgDurationMs: number; AvgTokens: number }
type TeamStat = { N: number; Scored: number; AvgQuality: number }
type WorkerStatus = "idle" | "busy" | "cooling"
type WorkerInfo = {
  leg: string
  status: WorkerStatus
  sessionID?: string
  title: string
  task?: string
  coolingUntil?: number
  runs: number
  elapsed_ms?: number
}
type Assignment = { leg: string; brief: string; team?: Assignment[] }
type LastRoute = {
  task: string
  leg: string
  model: string
  rationale: string
  at: string
  team?: string
  workers?: Assignment[]
}
type Stats = {
  director: string
  legs: Record<string, LegStat>
  teams?: Record<string, TeamStat>
  last: LastRoute | null
  workers?: WorkerInfo[]
}
type Activity = { at: string; kind: "route" | "run" | "done"; leg: string; model: string; text: string; ms: number; effort?: string }
// The roster (GET /v1/roster): each leg ranked by its perf index, its section
// (Frontier / Models), and a newer family member when one outscores it - plus
// the local agent CLIs with installed vs latest versions.
type RosterLeg = {
  leg: string
  label?: string
  model: string
  frontier: boolean
  perf?: number
  coding?: number
  slug?: string
  upgrade?: { slug: string; name: string; perf: number }
  subscription?: boolean
  open_weights?: boolean
}
type CliStatus = { name: string; installed: string; latest?: string; outdated: boolean; legs: string }
type Roster = { perf_source: string; perf_as_of: string; legs: RosterLeg[]; cli: CliStatus[] }
// The shield (GET /v1/proxy/stats): what never left the machine - secrets
// masked on the wire by the egress proxy and at the tool boundary.
type Shield = { mode: string; requests: number; secrets: number; identity: number; boundary?: { secrets: number; today: number } }

function View(props: { api: TuiPluginApi }) {
  const theme = () => {
    chromeTick()
    return props.api.theme.current
  }
  const [stats, setStats] = createSignal<Stats | null>(null)
  const [acts, setActs] = createSignal<Activity[]>([])
  const [workers, setWorkers] = createSignal<WorkerInfo[]>([])
  const [roster, setRoster] = createSignal<Roster | null>(null)
  const [shield, setShield] = createSignal<Shield | null>(null)
  // The last retarget's outcome, shown under Models for a few seconds.
  const [upgradeNote, setUpgradeNote] = createSignal("")
  const retarget = async (leg: string) => {
    setUpgradeNote(`${leg}: retargeting…`)
    try {
      const r = await fetch(brainURL("/v1/roster/upgrade"), { method: "POST", body: JSON.stringify({ leg }), signal: AbortSignal.timeout(30_000) })
      // A 404 is a brain older than this sidebar (loaded from source): it
      // never had the endpoint. Say what to do, not "404".
      if (r.status === 404) {
        setUpgradeNote(`✗ the running brain predates this control - captaincode.sh restart`)
      } else {
        const j = (await r.json()) as { result?: string; error?: string }
        setUpgradeNote(r.ok ? `✓ ${j.result ?? leg}` : `✗ ${j.error ?? r.status}`)
      }
      void pollRoster()
    } catch (e) {
      setUpgradeNote(`✗ ${leg}: ${String(e).slice(0, 60)}`)
    }
    setTimeout(() => setUpgradeNote(""), 12_000)
  }
  const [offline, setOffline] = createSignal(false)
  const [frame, setFrame] = createSignal(0)

  const poll = async () => {
    try {
      const [rs, ra, rw] = await Promise.all([
        fetch(brainURL("/v1/stats")),
        fetch(brainURL("/v1/activity")),
        fetch(brainURL("/v1/workers")),
      ])
      if (!rs.ok) throw new Error(String(rs.status))
      const s = (await rs.json()) as Stats
      // Only touch a signal when its data changed: every set re-renders the
      // panel, and a panel re-rendering 8 times a second while the user scrolls
      // the sidebar is the one mechanism that could plausibly blank it.
      const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)
      if (!same(s, stats())) setStats(s)
      if (ra.ok) {
        const a = ((await ra.json()) as { activity: Activity[] }).activity ?? []
        if (!same(a, acts())) setActs(a)
      }
      let w: WorkerInfo[] | undefined
      if (rw.ok) w = ((await rw.json()) as { workers: WorkerInfo[] }).workers ?? []
      else if (s.workers) w = s.workers
      if (w && !same(w, workers())) setWorkers(w)
      if (offline()) setOffline(false)
    } catch {
      setOffline(true)
    }
  }
  void poll()
  const timer = setInterval(() => void poll(), 1000)
  // The inbox: prompts handed to this folder's TUI from outside it (`captain
  // send`, a watcher that saw the GPU come up). Submitted into the open
  // session as if typed - a user turn, the forced leg honoured, the answer
  // streaming - by the same SDK client the prompt box uses. Only a TUI
  // sitting in a session takes them; on the home screen they wait.
  const inboxTaken = new Set<string>()
  const pollInbox = async () => {
    try {
      const cur = props.api.route.current as { name: string; params?: { sessionID?: string } }
      if (cur?.name !== "session" || !cur.params?.sessionID) return
      const r = await fetch(brainURL("/v1/inbox"), { signal: AbortSignal.timeout(3000) })
      if (!r.ok) return
      const j = (await r.json()) as { items?: { id: string; text: string; leg?: string; from?: string }[] }
      for (const it of j.items ?? []) {
        if (!it?.text || inboxTaken.has(it.id)) continue
        inboxTaken.add(it.id)
        const body: any = { parts: [{ type: "text", text: it.text }] }
        if (it.leg) body.model = { providerID: "captain", modelID: it.leg }
        await (props.api.client as any).session.promptAsync({ path: { id: cur.params.sessionID }, body })
        props.api.ui.toast({ title: "captain inbox", message: `${it.from ? it.from + ": " : ""}${it.text.slice(0, 120)}`, variant: "info", duration: 8000 } as any)
      }
    } catch {}
  }
  const inboxTimer = setInterval(() => void pollInbox(), 2000)
  // The roster changes daily (a feed refresh, a CLI update), not per turn.
  const pollRoster = async () => {
    try {
      const r = await fetch(brainURL("/v1/roster"), { signal: AbortSignal.timeout(3000) })
      if (!r.ok) return
      const ro = (await r.json()) as Roster
      if (JSON.stringify(ro) !== JSON.stringify(roster())) setRoster(ro)
    } catch {}
    try {
      const r = await fetch(brainURL("/v1/proxy/stats"), { signal: AbortSignal.timeout(3000) })
      if (!r.ok) return
      const sh = (await r.json()) as Shield
      if (JSON.stringify(sh) !== JSON.stringify(shield())) setShield(sh)
    } catch {}
  }
  void pollRoster()
  const rosterTimer = setInterval(() => void pollRoster(), 60_000)
  // The spinner animates only while a leg is busy; an idle panel must not
  // repaint itself 8 times a second for nothing.
  const spin = setInterval(() => {
    if (workers().some((w) => w.status === "busy")) setFrame((f) => (f + 1) % SPIN.length)
  }, 120)
  onCleanup(() => {
    clearInterval(timer)
    clearInterval(inboxTimer)
    clearInterval(rosterTimer)
    clearInterval(spin)
  })

  const feed = createMemo(() => acts().slice(0, 7))
  const running = createMemo(() => {
    const busy = workers().find((w) => w.status === "busy")
    if (busy) return busy.leg
    const a = acts()[0]
    return a && a.kind === "run" ? a.leg : null
  })

  // One row per leg: the roster's ranking (perf index, highest first) merged
  // with live state (busy / cooling / idle) and the scorecard. Two sections:
  // Frontier - the legs /frontier addresses, in its failover order - and
  // Models, everything else. The pseudo-legs (frontier, team, workflow) are
  // modes, not models: they never get a row (2026-09-13). Without a roster
  // (older brain) every leg lands in Models, most used first.
  type LegRow = { leg: string; w?: WorkerInfo; st?: LegStat; ro?: RosterLeg }
  const MODES = new Set(["frontier", "team", "workflow", "auto"])
  const allRows = createMemo<LegRow[]>(() => {
    const st = stats()?.legs ?? {}
    const live = new Map(workers().map((w) => [w.leg, w] as const))
    const ro = new Map((roster()?.legs ?? []).map((r) => [r.leg, r] as const))
    const ids = new Set([...ro.keys(), ...Object.keys(st), ...live.keys()])
    const pos = new Map((roster()?.legs ?? []).map((r, i) => [r.leg, i] as const))
    return [...ids]
      .filter((leg) => !MODES.has(leg))
      .map((leg) => ({ leg, w: live.get(leg), st: st[leg], ro: ro.get(leg) }))
      .sort((a, b) => (pos.get(a.leg) ?? 999) - (pos.get(b.leg) ?? 999) || (b.st?.N ?? 0) - (a.st?.N ?? 0) || a.leg.localeCompare(b.leg))
  })
  // One list, ranked by perf, each row a model × route (codex-cli,
  // codex-openai, glm-orouter): the frontier-class legs are rows like the
  // others, not a section of their own (2026-09-13: two sections read as
  // two different things; they are one ladder). Effort is per run, on the
  // Last Runs line.
  const modelRows = allRows
  // Local agent CLIs with an update available; hidden when all are current.
  const staleCLIs = createMemo(() => (roster()?.cli ?? []).filter((c) => c.outdated))
  const upgrades = createMemo(() => allRows().filter((r) => r.ro?.upgrade).length)
  const fmtElapsed = (ms: number) => {
    const s = Math.round(ms / 1000)
    return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s`
  }

  const teams = createMemo(() => {
    const s = stats()
    if (!s?.teams) return [] as [string, TeamStat][]
    return Object.entries(s.teams).sort((a, b) => b[1].N - a[1].N)
  })

  const lastTeam = createMemo(() => stats()?.last?.workers)

  const kindColor = (k: Activity["kind"]) =>
    k === "route" ? theme().accent : k === "done" ? theme().success : theme().text

  const statusColor = (status: WorkerStatus) => {
    if (status === "busy") return theme().accent
    if (status === "cooling") return theme().warning
    return theme().success
  }

  const statusGlyph = (w: WorkerInfo) =>
    w.status === "busy" ? SPIN[frame()] : w.status === "cooling" ? (chrome() === "demo" ? "❄" : "~") : "·"

  const statusLabel = (w: WorkerInfo) => {
    if (w.status === "busy") return `${SPIN[frame()]} busy`
    if (w.status === "cooling") {
      const left = w.coolingUntil ? Math.max(0, Math.ceil((w.coolingUntil - Date.now()) / 1000)) : 0
      return left > 0 ? `cooling ${left}s` : "cooling"
    }
    return "idle"
  }

  // One leg row: status glyph, name, perf index, then live state or scorecard.
  // A ⇡ after the name means a newer model of the same family outscores the
  // pinned one (its slug shows in place of the scorecard when the leg is idle).
  const LegLine = (p: { r: LegRow }) => {
    const r = p.r
    const perf = () => (r.ro?.perf ? r.ro.perf.toFixed(0) : "—")
    const detail = () => {
      if (r.w?.status === "busy") return `${fmtElapsed(r.w.elapsed_ms ?? 0)}${r.w.task ? " · " + r.w.task.slice(0, 26) : ""}`
      if (r.w?.status === "cooling") return statusLabel(r.w)
      const tail = ""
      if (r.ro?.upgrade) return `⇡ ${r.ro.upgrade.slug} (${r.ro.upgrade.perf.toFixed(0)})${tail}`
      if ((r.st?.N ?? 0) === 0) return `ready${tail}`
      return `n=${r.st!.N}${(r.st?.Scored ?? 0) > 0 ? ` q${r.st!.AvgQuality.toFixed(1)}` : ""}${(r.st?.AvgTokens ?? 0) > 0 ? ` ~${Math.round(r.st!.AvgTokens / 1000)}k` : ""}${tail}`
    }
    const upgradable = () => !!r.ro?.upgrade && r.w?.status !== "busy"
    // The director wears the helm (☸) and the accent colour instead of a
    // "· director" tag at the end of its row.
    const isDirector = () => r.leg === stats()?.director
    return (
      <box flexDirection="row" gap={1}>
        <text fg={r.w ? statusColor(r.w.status) : theme().textMuted}>{r.w ? statusGlyph(r.w) : "·"}</text>
        <text fg={isDirector() ? theme().accent : theme().text}>{(isDirector() ? "☸ " : "") + (r.ro?.label ?? r.leg)}</text>
        <text fg={r.ro?.open_weights ? theme().success : theme().textMuted}>{perf()}</text>
        <Show when={upgradable()} fallback={<text fg={theme().textMuted}>{detail()}</text>}>
          {/* Click = retarget the pin to the newer model (POST /v1/roster/upgrade); never automatic. */}
          <text fg={theme().warning} attributes={TextAttributes.UNDERLINE} onMouseUp={() => void retarget(r.leg)}>
            {detail()}
          </text>
        </Show>
      </box>
    )
  }

  return (
    <box>
      <Dashboards api={props.api} />
      <box flexDirection="row" gap={1}>
        <text fg={theme().text}>
          <b>Captain</b>
        </text>
        <Show when={stats()}>
          <text fg={theme().textMuted}> director: {stats()!.director}</text>
        </Show>
        <Show when={running()}>
          <text fg={theme().accent}>
            {" "}
            {SPIN[frame()]} {running()}…
          </text>
        </Show>
      </box>

      <Show when={offline()}>
        <text fg={theme().textMuted}>
          <i>brain offline - run `captain brain`</i>
        </text>
      </Show>

      {/* Models: every leg, ranked by perf. ☸ = director, ⇡ = a newer model in the family. */}
      <Show when={modelRows().length && !offline()}>
        <box>
          <text> </text>
          <text fg={theme().text}>
            <b>Models</b>
            <span style={{ fg: theme().textMuted }}> perf{roster()?.perf_source === "snapshot" ? " (snapshot)" : ""}</span>
            <Show when={upgrades() > 0}>
              <span style={{ fg: theme().warning }}> ⇡{upgrades()} newer</span>
            </Show>
          </text>
          <For each={modelRows()}>{(r) => <LegLine r={r} />}</For>
          <Show when={upgradeNote()}>
            <text fg={theme().textMuted} wrapMode="word">
              {upgradeNote()}
            </text>
          </Show>
          <Show when={upgrades() > 0 && !upgradeNote()}>
            <text fg={theme().textMuted}>
              <i>click a ⇡ to retarget that leg · `captain upgrade --models`</i>
            </text>
          </Show>
        </box>
      </Show>

      {/* The shield: secrets that never left the machine. */}
      <Show when={shield() && !offline()}>
        <text> </text>
        <box flexDirection="row" gap={1}>
          <text fg={shield()!.mode === "off" ? theme().warning : theme().success}>{shield()!.mode === "off" ? "○" : "●"}</text>
          <text fg={theme().text}>shield</text>
          <text fg={theme().textMuted}>
            {shield()!.mode === "off"
              ? "off (CAPTAIN_REDACT=off)"
              : `${shield()!.secrets + (shield()!.boundary?.secrets ?? 0)} secrets masked · ${shield()!.identity} identity · wire ${shield()!.requests}`}
          </text>
        </box>
      </Show>

      {/* Local agent CLIs behind on their published version. */}
      <Show when={staleCLIs().length && !offline()}>
        <box>
          <For each={staleCLIs()}>
            {(c) => (
              <box flexDirection="row" gap={1}>
                <text fg={theme().warning}>⇡</text>
                <text fg={theme().text}>{c.name}</text>
                <text fg={theme().textMuted}>
                  {c.installed} → {c.latest} · `captain upgrade`
                </text>
              </box>
            )}
          </For>
        </box>
      </Show>

      <Show when={showQuit}>
        <QuitLink api={props.api} />
      </Show>

      {/* Hierarchical team last assigned by the director. */}
      <Show when={lastTeam()?.length}>
        <box>
          <text> </text>
          <text fg={theme().text}>
            <b>Team</b>
            <Show when={stats()?.last?.team}>
              <span style={{ fg: theme().textMuted }}> {stats()!.last!.team}</span>
            </Show>
          </text>
          <For each={lastTeam()!}>
            {(node) => (
              <TeamNode
                node={node}
                depth={0}
                text={theme().text}
                muted={theme().textMuted}
                accent={theme().accent}
              />
            )}
          </For>
        </box>
      </Show>

      {/* Last Runs - routing picks + each worker start/finish. */}
      <Show
        when={feed().length}
        fallback={
          <Show when={stats() && !offline()}>
            <text fg={theme().textMuted}>
              <i>routing on - send a prompt</i>
            </text>
          </Show>
        }
      >
        <text> </text>
        <text fg={theme().text}>
          <b>Last Runs</b>
        </text>
        <For each={feed()}>
          {(a, i) => (
            <box>
              <box flexDirection="row" gap={1}>
                <text fg={theme().textMuted}>{a.at}</text>
                <text fg={kindColor(a.kind)}>
                  {a.kind === "run" && i() === 0 && running()
                    ? SPIN[frame()]
                    : a.kind === "route"
                      ? "→"
                      : a.kind === "done"
                        ? "✓"
                        : "▶"}{" "}
                  {a.leg}
                </text>
                <Show when={a.effort}>
                  <text fg={theme().textMuted}>{a.effort}</text>
                </Show>
                <Show when={a.ms > 0}>
                  <text fg={theme().textMuted}>{a.ms >= 1000 ? `${(a.ms / 1000).toFixed(1)}s` : `${a.ms}ms`}</text>
                </Show>
              </box>
              <Show when={a.text}>
                <text fg={theme().textMuted} wrapMode="word">
                  {a.text}
                </text>
              </Show>
            </box>
          )}
        </For>
      </Show>

      {/* Ensemble scorecards. */}
      <Show when={teams().length}>
        <For each={teams()}>
          {([team, st]) => (
            <box flexDirection="row" gap={1}>
              <text fg={theme().accent}>{team}</text>
              <text fg={theme().textMuted}>
                n={st.N} {st.Scored > 0 ? `q${st.AvgQuality.toFixed(1)}` : "-"}
              </text>
            </box>
          )}
        </For>
      </Show>

    </box>
  )
}

function TeamNode(props: { node: Assignment; depth: number; text: RGBA; muted: RGBA; accent: RGBA }) {
  const pad = "  ".repeat(props.depth)
  return (
    <box>
      <box flexDirection="row" gap={1}>
        <text fg={props.text}>
          {pad}
          {props.depth > 0 ? "↳ " : "• "}
          {props.node.leg}
        </text>
        <Show when={props.node.team?.length}>
          <text fg={props.accent}>team/{props.node.team!.length}</text>
        </Show>
      </box>
      <Show when={props.node.brief}>
        <text fg={props.muted} wrapMode="word">
          {pad}  {props.node.brief.slice(0, 48)}
        </text>
      </Show>
      <Show when={props.node.team?.length}>
        <For each={props.node.team!}>
          {(child) => (
            <TeamNode
              node={child}
              depth={props.depth + 1}
              text={props.text}
              muted={props.muted}
              accent={props.accent}
            />
          )}
        </For>
      </Show>
    </box>
  )
}



// ── clickable dashboards ─────────────────────────────────────────────────────
// Two things worth a click from inside a session: the captain dashboard
// (~/.captaincode/dashboard, rebuilt every 5 min by the LaunchAgent) and the
// Euclid dashboard of the brain this project reads - shown only when the
// euclid MCP is actually registered in this TUI and a built dashboard exists.
// Click = onMouseUp, the same mechanism as the TUI's own Link component; the
// URL is opened with the platform opener rather than an npm dependency.
function openInBrowser(url: string) {
  // CAPTAIN_UI_OPEN_LOG: record instead of opening (tests drive the TUI on a
  // pty and must not pop browser windows on the machine running them).
  const logTo = (globalThis as any).process?.env?.CAPTAIN_UI_OPEN_LOG
  if (logTo) {
    try {
      require("node:fs").appendFileSync(logTo, `${new Date().toISOString()} open ${url}\n`)
    } catch {}
    return
  }
  const cmd = platform() === "darwin" ? ["open", url] : platform() === "win32" ? ["cmd", "/c", "start", "", url] : ["xdg-open", url]
  try {
    ;(globalThis as any).Bun?.spawn(cmd, { stdout: "ignore", stderr: "ignore" })
  } catch {
    /* a link that cannot open is not worth a crash */
  }
}

type BuildPlan = { host: string; steps: string[][]; env: Record<string, string>; staticFrom?: string }
type DashLink = { label: string; url: string; build?: BuildPlan }

// euclidEngine locates a Euclid checkout that can build a dashboard for ANY
// brain: CAPTAIN_EUCLID_ENGINE, else ~/Gits/euclid, else
// $CAPTAIN_WORKSPACE_ROOT/euclid. A vendored copy inside a brain's bin/ is
// preferred when present, because it matches that brain's format.
function euclidEngine(): string | undefined {
  const env = (globalThis as any).process?.env ?? {}
  const candidates = [env.CAPTAIN_EUCLID_ENGINE, join(homedir(), "Gits", "euclid"), env.CAPTAIN_WORKSPACE_ROOT && join(env.CAPTAIN_WORKSPACE_ROOT, "euclid")].filter(Boolean) as string[]
  return candidates.find((c) => existsSync(join(c, "dashboard", "build-dashboard.py")) && existsSync(join(c, "engine", "build-catalog.py")))
}

// buildPlan says how to build the dashboard of a brain rooted at <host>/.euclid.
// The dashboard needs the relation index first (build-catalog.py writes
// .euclid/index/graph.json; without it build-dashboard.py dies on an empty
// JSON read - found on a copy of the main brain, 2026-09-11), then the data,
// then the static page copied beside the data when the brain has none.
function captainBin(): string {
  const env = (globalThis as any).process?.env ?? {}
  if (env.CAPTAIN_BIN) return env.CAPTAIN_BIN
  const local = join(homedir(), ".local", "bin", "captain")
  return existsSync(local) ? local : "captain"
}

function buildPlan(root: string, opts: { create?: boolean } = {}): BuildPlan | undefined {
  const host = join(root, "..")
  const vendored = join(root, "bin")
  if (opts.create) {
    const engine = euclidEngine()
    if (!engine) return undefined
    return {
      host,
      steps: [
        // init --repo also bootstraps VISION/MAP/BRAIN from the repo's docs
        // when the brain is up; the explicit step covers a brain that was down.
        [captainBin(), "euclid", "init", "--repo"],
        [captainBin(), "euclid", "bootstrap", "--apply"],
        ["python3", join(engine, "engine", "build-catalog.py")],
        ["python3", join(engine, "dashboard", "build-dashboard.py")],
      ],
      env: { EUCLID_ROOT: host, CAPTAIN_CWD: host },
      staticFrom: join(engine, "dashboard"),
    }
  }
  if (existsSync(join(vendored, "build-dashboard.py"))) {
    const steps = [["python3", join(vendored, "build-dashboard.py")]]
    if (existsSync(join(vendored, "build-catalog.py"))) steps.unshift(["python3", join(vendored, "build-catalog.py")])
    return { host, steps, env: { EUCLID_ROOT: host } }
  }
  const engine = euclidEngine()
  if (!engine) return undefined
  return {
    host,
    steps: [
      ["python3", join(engine, "engine", "build-catalog.py")],
      ["python3", join(engine, "dashboard", "build-dashboard.py")],
    ],
    env: { EUCLID_ROOT: host },
    staticFrom: join(engine, "dashboard"),
  }
}

// openDashboard opens a built dashboard, or builds it first and opens it when
// the build finishes. The brain does the building (POST /v1/euclid/reindex:
// scaffold + bootstrap on ?create=1, then catalog + dashboard) - the same
// engine the launch-time `captain euclid ensure` and the dashboard's own
// Regenerate button use. Spawning python from the TUI is the fallback for a
// brain that is down.
async function openDashboard(l: DashLink, refresh: () => Promise<void>) {
  if (!l.build) return openInBrowser(l.url)
  const target = l.url.replace(/^file:\/\//, "")
  try {
    const which = l.label.includes("main brain") ? "main" : "local"
    const create = l.label.includes("(create)") ? "&create=1" : ""
    const r = await fetch(brainURL(`/v1/euclid/reindex?brain=${which}${create}`), { method: "POST", signal: AbortSignal.timeout(10 * 60_000) })
    if (r.ok) {
      await refresh()
      if (existsSync(target)) openInBrowser(l.url)
      return
    }
  } catch {
    /* brain down: build locally below */
  }
  try {
    const env = { ...((globalThis as any).process?.env ?? {}), ...l.build.env }
    for (const step of l.build.steps) {
      const proc = (globalThis as any).Bun?.spawn(step, { cwd: l.build.host, env, stdout: "ignore", stderr: "ignore" })
      await proc?.exited
    }
    if (l.build.staticFrom) {
      const { copyFileSync } = await import("node:fs")
      const dir = join(target, "..")
      for (const f of ["index.html", "app.js", "style.css"]) {
        if (!existsSync(join(dir, f)) && existsSync(join(l.build.staticFrom, f))) copyFileSync(join(l.build.staticFrom, f), join(dir, f))
      }
    }
  } catch {
    /* no python, or a step failed: the link stays a "(build)" link */
  }
  await refresh()
  if (existsSync(target)) openInBrowser(l.url)
}

// quitTui asks the app to exit the way ctrl+d would; if the keymap will not
// take it, exit the process. Exists because a click still lands when keys do
// not (2026-09-11: tab, ctrl+x and ctrl+p dead, mouse fine).
function quitTui(api: TuiPluginApi) {
  try {
    ;(api as any).keymap?.dispatchCommand?.("app.exit")
  } catch {}
  setTimeout(() => {
    try {
      ;(globalThis as any).process?.exit?.(0)
    } catch {}
  }, 800)
}

// The quit control exists for when keys are dead. That was the prompt-side
// tag stealing input (confirmed 2026-09-11: with the tag off, ctrl+c works
// again), so it is hidden unless CAPTAIN_UI_QUIT_LINK=1 asks for it.
const showQuit = (globalThis as any).process?.env?.CAPTAIN_UI_QUIT_LINK === "1"

function QuitLink(props: { api: TuiPluginApi }) {
  return (
    <box marginTop={1}>
      <text fg={RGBA.fromHex("#e06c75")} attributes={TextAttributes.UNDERLINE} onMouseUp={() => quitTui(props.api)}>
        {"quit captain-code ✕"}
      </text>
    </box>
  )
}

function Dashboards(props: { api: TuiPluginApi }) {
  const [links, setLinks] = createSignal<DashLink[]>([])
  const captainDash = join(homedir(), ".captaincode", "dashboard", "index.html")

  const refresh = async () => {
    try {
      await refreshLinks()
    } catch {
      /* a broken link check must never take the panel down */
    }
  }
  const refreshLinks = async () => {
    const out: DashLink[] = []
    if (existsSync(captainDash)) out.push({ label: "captain dashboard", url: "file://" + captainDash })
    // Euclid: only when this TUI has the euclid MCP, and only for a brain that
    // actually built a dashboard (build-dashboard.py writes <root>/dashboard).
    const hasEuclidMcp = (props.api.state as any)?.mcp?.()?.some?.((m: { id?: string; name?: string }) => (m.id ?? m.name) === "euclid") ?? false
    if (hasEuclidMcp) {
      try {
        const r = await fetch(brainURL("/v1/euclid/status"), { signal: AbortSignal.timeout(2000) })
        if (r.ok) {
          const st = (await r.json()) as {
            cwd?: string
            brains?: { Root: string; Label: string; Kind: string }[]
            checks?: { kind: string; root: string; present: boolean; journal: number; attention: string }[]
          }
          // The launch check, one short hint per brain: what waits for a
          // distill, or what is still a template. Long phrases are cut to the
          // first clause so the row stays on one sidebar line.
          const hint = (kind: "main" | "local") => {
            const c = (st.checks ?? []).find((x) => x.kind === kind)
            if (!c || !c.present) return ""
            if (c.attention) return " · " + c.attention.split(";")[0].split(",")[0]
            if (c.journal > 0) return ` · ${c.journal} to distill`
            return ""
          }
          // Two brains matter here: the main brain (~/.euclid) and the brain of
          // the folder captain-code was opened in. Linked repositories and other
          // developers' subtrees are search-only and would only add rows.
          const cwd = st.cwd ?? ""
          const seen = new Set<string>()
          // Kind "repo" is <repo>/.euclid, the brain a dashboard is built for;
          // "developer" is a subtree inside it and has no dashboard of its own.
          const mine = (st.brains ?? []).filter((b) => b.Kind === "main" || (b.Kind === "repo" && cwd && b.Root.startsWith(cwd)))
          for (const b of mine) {
            if (seen.has(b.Root)) continue
            seen.add(b.Root)
            // A dashboard is built per BRAIN (the memory in <repo>/.euclid) by
            // the engine vendored in that brain's bin/. Three states:
            //   built             -> link opens it
            //   buildable         -> "(build)": first click builds, then opens
            //   no engine anywhere -> nothing to show
            const idx = join(b.Root, "dashboard", "index.html")
            const who = b.Kind === "main" ? "main brain" : b.Label.replace(/^repo:/, "")
            const h = hint(b.Kind === "main" ? "main" : "local")
            if (existsSync(idx)) {
              out.push({ label: `memory: ${who}${h}`, url: "file://" + idx })
              continue
            }
            const plan = buildPlan(b.Root)
            if (plan) out.push({ label: `memory: ${who} (build)${h}`, url: "file://" + idx, build: plan })
          }
          // No repo brain for the folder captain-code was opened in: offer to
          // create one (listed after the main brain). `captain euclid init --repo` scaffolds <repo>/.euclid
          // (it honours CAPTAIN_CWD), then the engine builds the dashboard.
          if (cwd && !mine.some((b) => b.Kind === "repo") && existsSync(join(cwd, ".git"))) {
            const root = join(cwd, ".euclid")
            const plan = buildPlan(root, { create: true })
            if (plan) {
              const name = cwd.split("/").filter(Boolean).pop() ?? "local"
              out.push({ label: `memory: ${name} (create)`, url: "file://" + join(root, "dashboard", "index.html"), build: plan })
            }
          }
        }
      } catch {
        /* brain down: no euclid link this round */
      }
    }
    if (JSON.stringify(out) !== JSON.stringify(links())) setLinks(out)
  }
  void refresh()
  const timer = setInterval(() => void refresh(), 30_000)
  onCleanup(() => clearInterval(timer))

  return (
    <Show when={links().length}>
      {/* One link per line: the sidebar is 42 columns, two links do not fit. */}
      <box flexDirection="column" marginTop={1} marginBottom={1}>
        <For each={links()}>
          {(l) => (
            <text fg={CAPTAIN_BLUE} attributes={TextAttributes.UNDERLINE} onMouseUp={() => openDashboard(l, refresh)}>
              {`${l.label} ↗`}
            </text>
          )}
        </For>
      </box>
    </Show>
  )
}

// ── home_logo ────────────────────────────────────────────────────────────────
// The wordmark, lifted out of the fork. This slot is why Captain Code no longer
// needs a fork for branding: opencode 1.17.20 renders whatever a plugin puts
// here instead of its own logo. EDIT IT HERE: the launcher runs stock opencode,
// so packages/tui/src/logo.ts (the fork) only shows under CAPTAIN_USE_FORK=1
// (a logo edit there changed nothing on screen, 2026-09-13).
const logo = {
  // CAPTAIN (dim) + CODE (bold) - block geometry matching OpenCode's alphabet.
  // A must use ^^ crossbar; without it the wordmark reads "COPT…" not CAPTAIN.
  left: [
    "                                  ",
    // C     A     P     T     A     I     N
    // P: crossbar on mid row. I: top bar == bottom bar (█▀▀█ / █▄▄█).
    "█▀▀▀ █▀▀█ █▀▀█ █▀▀█ █▀▀█  █  █▀▀▄",
    "█    █▀▀█ █▀▀▀  █   █▀▀█  █  █  █",
    "▀▀▀▀ ▀  ▀ ▀     ▀   ▀  ▀  ▀  ▀  ▀",
  ],
  right: ["            ▄     ", "█▀▀▀ █▀▀█ █▀▀█ █▀▀█", "█___ █__█ █__█ █^^^", "▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀"],
}

const CAPTAIN_BLUE = RGBA.fromHex("#5c9cf5")
const CODE_WHITE = RGBA.fromHex("#ffffff")
const CAPTAIN_SHADOW = RGBA.fromHex("#28508c")
const CODE_SHADOW = RGBA.fromHex("#3a3a3a")

// The wordmark uses four marks: `_` a shadowed blank, `^` a lit half block,
// `~` a shadowed half block, `,` a shadowed lower half block.
function renderLine(line: string, fg: RGBA, shadow: RGBA, bold: boolean) {
  return Array.from(line).map((char) => {
    if (char === "_") return <text fg={fg} bg={shadow} attributes={bold ? TextAttributes.BOLD : undefined} selectable={false}>{" "}</text>
    if (char === "^") return <text fg={fg} bg={shadow} attributes={bold ? TextAttributes.BOLD : undefined} selectable={false}>▀</text>
    if (char === "~") return <text fg={shadow} attributes={bold ? TextAttributes.BOLD : undefined} selectable={false}>▀</text>
    if (char === ",") return <text fg={shadow} attributes={bold ? TextAttributes.BOLD : undefined} selectable={false}>▄</text>
    return <text fg={fg} attributes={bold ? TextAttributes.BOLD : undefined} selectable={false}>{char}</text>
  })
}

function Wordmark() {
  return (
    <box>
      <For each={logo.left}>
        {(line, index) => (
          <box flexDirection="row" gap={1}>
            <box flexDirection="row">{renderLine(line, CAPTAIN_BLUE, CAPTAIN_SHADOW, false)}</box>
            <box flexDirection="row">{renderLine(logo.right[index()] ?? "", CODE_WHITE, CODE_SHADOW, true)}</box>
          </box>
        )}
      </For>
    </box>
  )
}

// ── session_prompt_right ─────────────────────────────────────────────────────
// Pre-route, the session's nominal model says nothing useful: what is about to
// handle the prompt is the DIRECTOR. Label it honestly.
function DirectorTag(props: { api: TuiPluginApi }) {
  const [director, setDirector] = createSignal<string | null>(null)
  const pull = () =>
    fetch(`${BRAIN}/v1/health`)
      .then((r) => (r.ok ? r.json() : null))
      .then((h: { director?: string } | null) => setDirector(h?.director ?? null))
      .catch(() => setDirector(null))
  pull()
  const timer = setInterval(pull, 15_000)
  onCleanup(() => clearInterval(timer))
  return (
    <Show when={director()}>
      <text fg={CAPTAIN_BLUE} selectable={false}>{`director ${director()}`}</text>
    </Show>
  )
}

async function applyChrome(api: TuiPluginApi, next: Chrome, opts?: { toast?: boolean }) {
  if (next === "demo") {
    if (api.theme.selected !== DEMO_THEME) api.kv.set(KV_THEME_BEFORE, api.theme.selected)
    try {
      if (!api.theme.has(DEMO_THEME)) await api.theme.install(DEMO_THEME_PATH)
      api.theme.set(DEMO_THEME)
    } catch (e) {
      api.ui.toast({ title: "captain chrome", message: `demo theme failed: ${String(e).slice(0, 120)}`, variant: "error", duration: 6000 })
      return
    }
  } else if (api.theme.selected === DEMO_THEME) {
    const restore = String(api.kv.get(KV_THEME_BEFORE, "opencode") ?? "opencode")
    if (restore !== DEMO_THEME && api.theme.has(restore)) api.theme.set(restore)
    else if (api.theme.has("opencode")) api.theme.set("opencode")
  }
  setChrome(next)
  setChromeTick((n) => n + 1)
  api.kv.set(KV_CHROME, next)
  if (opts?.toast === false) return
  api.ui.toast({
    title: "captain chrome",
    message: next === "demo" ? "demo look · live brain data unchanged" : "default chrome restored",
    variant: "info",
    duration: 4000,
  })
}

function registerChromeCommands(api: TuiPluginApi) {
  const pick = () => {
    api.ui.dialog.replace(() => (
      <api.ui.DialogSelect<Chrome>
        title="Captain chrome"
        current={chrome()}
        skipFilter
        options={[
          { title: "default", value: "default", description: "OpenCode theme · ~ cooling" },
          { title: "demo", value: "demo", description: "Website demo colors · ❄ cooling · live data" },
        ]}
        onSelect={(opt) => void applyChrome(api, opt.value)}
      />
    ))
  }
  api.keymap.registerLayer({
    commands: [
      { name: "captain.chrome", title: "Captain chrome…", category: "Captain", namespace: "palette", run: pick },
      { name: "captain.chrome.demo", title: "Captain chrome: demo", category: "Captain", namespace: "palette", run: () => void applyChrome(api, "demo") },
      { name: "captain.chrome.default", title: "Captain chrome: default", category: "Captain", namespace: "palette", run: () => void applyChrome(api, "default") },
    ],
    bindings: [...api.tuiConfig.keybinds.gather("captain.chrome", ["captain.chrome", "captain.chrome.demo", "captain.chrome.default"])],
  })
}

const tui: TuiPlugin = async (api) => {
  // CAPTAIN_UI_TEST_QUIT_MS: exercise the quit path from a pty test.
  const testQuit = Number((globalThis as any).process?.env?.CAPTAIN_UI_TEST_QUIT_MS ?? "0")
  if (testQuit > 0) setTimeout(() => quitTui(api), testQuit)
  // CAPTAIN_UI_SLOTS=sidebar,logo,tag (default all) - lets one slot be run
  // alone to attribute a rendering problem to it; CAPTAIN_UI_SLOTS=none turns
  // the whole plugin off for a launch without touching the config.
  const enabled = new Set(
    // "tag" (a label inside the prompt component) is off by default and should
    // stay off: a renderable in session_prompt_right takes keyboard focus from
    // the text input, so tab, ctrl+x, ctrl+p and ctrl+c all die. Confirmed
    // 2026-09-11 by turning it off: ctrl+c worked again. The sidebar header
    // already names the director.
    ((globalThis as any).process?.env?.CAPTAIN_UI_SLOTS ?? "sidebar,logo").split(",").map((s: string) => s.trim()),
  )
  // CAPTAIN_UI_CHROME=demo|default overrides the persisted chrome profile.
  const initial: Chrome = envChrome() ?? (api.kv.get(KV_CHROME, "default") === "demo" ? "demo" : "default")
  setChrome(initial)
  if (initial === "demo") void applyChrome(api, "demo", { toast: false })
  const slots: Record<string, () => any> = {}
  if (enabled.has("sidebar")) slots.sidebar_content = () => <View api={api} />
  if (enabled.has("logo")) slots.home_logo = () => <Wordmark />
  if (enabled.has("tag")) slots.session_prompt_right = () => <DirectorTag api={api} />
  api.slots.register({ order: 150, slots })
  registerPromptCommands(api)
  registerChromeCommands(api)
}

// ── prompt commands ──────────────────────────────────────────────────────────
//
// Editing or deleting a prompt from the palette and a leader key. Two facts
// shape this (2026-09-19). A prompt typed while a turn runs is QUEUED in the
// TUI's own memory - opencode 1.18 keeps that queue client-side and only its
// "Queued prompts" dialog can touch it (ctrl+e edits, ctrl+d removes) - so
// "edit queued" and "delete queued" OPEN that dialog, from the palette or a
// key, and say so. A prompt that already left the queue is a message on the
// server, and the SDK's revert removes it: "delete last prompt" reverts the
// session's last user message (and everything after it). The right-click
// menu on a message is stock opencode's and takes no plugin items.
function registerPromptCommands(api: TuiPluginApi) {
  const sessionID = () => {
    const cur = api.route.current as { name: string; params?: { sessionID?: string } }
    return cur?.name === "session" ? cur.params?.sessionID : undefined
  }
  const openQueue = (why: string) => {
    const sid = sessionID()
    if (!sid) {
      api.ui.toast({ title: "captain", message: "open a session first", variant: "warning", duration: 4000 })
      return
    }
    // opencode's own dialog: the only place the client-side queue is editable.
    api.keymap.dispatchCommand("session.queued_prompts")
    api.ui.toast({ title: why, message: "queued prompts: ctrl+e edits one, ctrl+d (or delete) removes it", variant: "info", duration: 6000 })
  }
  const deleteLast = async () => {
    const sid = sessionID()
    if (!sid) {
      api.ui.toast({ title: "captain", message: "open a session first", variant: "warning", duration: 4000 })
      return
    }
    const msgs = api.state.session.messages(sid)
    const last = [...msgs].reverse().find((m) => (m as any).role === "user")
    if (!last) {
      api.ui.toast({ title: "delete last prompt", message: "no prompt in this session", variant: "info", duration: 4000 })
      return
    }
    const text = (api.state.part((last as any).id) ?? [])
      .map((p: any) => (p.type === "text" ? String(p.text ?? "") : ""))
      .join(" ")
      .trim()
    api.ui.dialog.replace(() => (
      <api.ui.DialogConfirm
        title="Delete last prompt"
        message={`Revert "${text.slice(0, 80)}${text.length > 80 ? "…" : ""}" and everything after it? (ctrl+x u undoes)`}
        onConfirm={async () => {
          try {
            await (api.client as any).session.revert({ path: { id: sid }, body: { messageID: (last as any).id } })
            api.ui.toast({ title: "delete last prompt", message: "reverted - session.unrevert brings it back", variant: "success", duration: 5000 })
          } catch (e) {
            api.ui.toast({ title: "delete last prompt", message: String(e).slice(0, 200), variant: "error", duration: 6000 })
          }
        }}
        onCancel={() => {}}
      />
    ))
  }
  api.keymap.registerLayer({
    commands: [
      { name: "captain.prompt.edit_queued", title: "Edit queued prompt", category: "Captain", namespace: "palette", run: () => openQueue("edit queued") },
      { name: "captain.prompt.delete_queued", title: "Delete queued prompt", category: "Captain", namespace: "palette", run: () => openQueue("delete queued") },
      { name: "captain.prompt.delete_last", title: "Delete last prompt (revert)", category: "Captain", namespace: "palette", run: () => void deleteLast() },
    ],
    bindings: [
      ...api.tuiConfig.keybinds.gather("captain.prompt", ["captain.prompt.edit_queued", "captain.prompt.delete_queued", "captain.prompt.delete_last"]),
    ],
  })
}

const plugin: TuiPluginModule = {
  id,
  tui,
}

export default plugin
