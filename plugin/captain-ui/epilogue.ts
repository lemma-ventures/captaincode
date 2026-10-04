// The exit screen. When the TUI closes inside a session, stock opencode
// writes one chunk to stdout: its own OPENCODE wordmark, the session title and
// "Continue  opencode -s <id>". No plugin slot reaches that text: it is
// written after the renderer and the plugins are torn down. So the plugin
// wraps process.stdout.write and swaps that one chunk for Captain Code's
// wordmark and the launcher command. Every other write passes through as is.

// CAPTAIN (blue) + CODE (bold white) - block geometry matching OpenCode's alphabet.
// A must use ^^ crossbar; without it the wordmark reads "COPT…" not CAPTAIN.
export const logo = {
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

const reset = "\x1b[0m"
const bold = "\x1b[1m"
const dim = "\x1b[90m"
const captain = { fg: "\x1b[38;2;92;156;245m", shadow: "\x1b[38;2;40;80;140m", bg: "\x1b[48;2;40;80;140m" }
const code = { fg: "\x1b[38;2;255;255;255m\x1b[1m", shadow: "\x1b[38;2;58;58;58m", bg: "\x1b[48;2;58;58;58m" }

// The same four marks as the home_logo slot: `_` a shadowed blank, `^` a lit
// half block, `~` a shadowed half block, `,` a shadowed lower half block.
function draw(line: string, c: { fg: string; shadow: string; bg: string }) {
  return Array.from(line)
    .map((char) => {
      if (char === "_") return `${c.bg} ${reset}`
      if (char === "^") return `${c.fg}${c.bg}▀${reset}`
      if (char === "~") return `${c.shadow}▀${reset}`
      if (char === ",") return `${c.shadow}▄${reset}`
      if (char === " ") return " "
      return `${c.fg}${char}${reset}`
    })
    .join("")
}

// Only these characters can be a session id. Anything else is not printed.
const SESSION_ID = /^[A-Za-z0-9_-]{1,128}$/

export function captainEpilogue(input: { title: string; sessionID?: string; command: string }) {
  const weak = (text: string) => `${dim}${text.padEnd(10, " ")}${reset}`
  // The title comes from a model: drop control characters, so it cannot carry
  // terminal escape sequences onto the user's screen.
  const title = input.title.replace(/[\u0000-\u001F\u007F-\u009F]/g, "")
  const lines = logo.left.map((line, index) => `  ${draw(line, captain)} ${draw(logo.right[index] ?? "", code)}`)
  lines.push("", `  ${weak("Session")}${bold}${title}${reset}`)
  if (input.sessionID && SESSION_ID.test(input.sessionID))
    lines.push(`  ${weak("Continue")}${bold}${input.command} -s ${input.sessionID}${reset}`)
  lines.push("")
  return lines.join("\n")
}

// The stock chunk, opencode 1.17.x-1.18.x (presentation.ts):
//   `  ${dim}Session   ${reset}${bold}<title>${reset}`
//   `  ${dim}Continue  ${reset}${bold}opencode -s <id>${reset}`
const STOCK_CONTINUE = /\x1b\[90mContinue\s*\x1b\[0m\x1b\[1mopencode -s ([^\x1b\s]*)\x1b\[0m/
const STOCK_SESSION = /\x1b\[90mSession\s*\x1b\[0m\x1b\[1m([^\n]*?)\x1b\[0m/

// rewriteEpilogue returns Captain's exit screen for the stock chunk, or
// undefined for any other write.
export function rewriteEpilogue(chunk: string, command: string): string | undefined {
  if (!chunk.includes("opencode -s ")) return undefined
  const cont = STOCK_CONTINUE.exec(chunk)
  if (!cont) return undefined
  const title = STOCK_SESSION.exec(chunk)?.[1] ?? ""
  const tail = chunk.endsWith("\n") ? "\n" : ""
  return captainEpilogue({ title, sessionID: cont[1], command }) + tail
}

// resumeCommand is what the Continue line prints. The launcher exports its own
// path as CAPTAIN_LAUNCHER, and `captaincode.sh -s <id>` resumes that session
// through the brain. A bare `opencode` launch has no launcher: say opencode.
export function resumeCommand(env: Record<string, string | undefined>) {
  const launcher = env.CAPTAIN_LAUNCHER?.trim()
  if (!launcher || /[\u0000-\u001F\u007F]/.test(launcher)) return "opencode"
  const home = env.HOME
  if (home && launcher.startsWith(home + "/")) return "~" + launcher.slice(home.length)
  return launcher
}

type Writable = { write: (chunk: any, ...rest: any[]) => boolean }
const HOOKED = Symbol.for("captain.epilogue")

// hookEpilogue rewrites the first matching chunk written to stream, then
// removes itself. A plain wrapper is not enough: OpenTUI's renderer sets
// `stdout.write = realStdoutWrite` when it is destroyed, just before opencode
// writes the epilogue (opencode 1.18.34, 2026-10-04). So `write` becomes an
// accessor: an assignment replaces the inner function, and a read returns that
// function wrapped.
export function hookEpilogue(stream: Writable, command: string) {
  const s = stream as Writable & { [HOOKED]?: boolean }
  if (s[HOOKED]) return
  const own = Object.getOwnPropertyDescriptor(stream, "write")
  if (own && !own.configurable) return
  const enumerable = own?.enumerable ?? true
  let current = stream.write
  const wrapped = new WeakMap<Function, Writable["write"]>()
  const release = () => {
    Object.defineProperty(stream, "write", { value: current, writable: true, configurable: true, enumerable })
    s[HOOKED] = false
  }
  const wrap = (fn: Writable["write"]) => {
    let w = wrapped.get(fn)
    if (w) return w
    w = function (this: unknown, chunk: any, ...rest: any[]) {
      if (typeof chunk === "string") {
        const next = rewriteEpilogue(chunk, command)
        if (next !== undefined) {
          release()
          return fn.call(this, next, ...rest)
        }
      }
      return fn.call(this, chunk, ...rest)
    }
    wrapped.set(fn, w)
    return w
  }
  Object.defineProperty(stream, "write", {
    configurable: true,
    enumerable,
    get: () => wrap(current),
    set: (fn: Writable["write"]) => {
      current = fn
    },
  })
  s[HOOKED] = true
}
