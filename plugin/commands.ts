// The unknown-command check for the TUI plugin (captain.ts). Kept in its own
// module: opencode treats a plugin module's exports as plugins.

import { readdirSync } from "node:fs"
import { join } from "node:path"
import { homedir } from "node:os"

// opencode's own commands never reach this hook, but a custom command file
// (~/.config/opencode/command/<name>.md, <repo>/.opencode/command/<name>.md)
// is the user's word, not a typo: never refused.
export const OPENCODE_WORDS = ["help", "init", "new", "sessions", "models", "compact", "summarize", "share", "unshare", "undo", "redo", "export", "editor", "exit", "quit", "themes", "details", "thinking", "status", "review", "agents", "mcp", "connect", "clear"]
export function customCommands(dir: string): string[] {
  const out: string[] = []
  for (const d of [join(homedir(), ".config", "opencode"), dir ? join(dir, ".opencode") : ""]) {
    if (!d) continue
    for (const sub of ["command", "commands"]) {
      try {
        for (const f of readdirSync(join(d, sub))) if (f.endsWith(".md")) out.push(f.slice(0, -3).toLowerCase())
      } catch {
        /* no such folder */
      }
    }
  }
  return out
}

function editDistance(a: string, b: string): number {
  const prev = Array.from({ length: b.length + 1 }, (_, j) => j)
  for (let i = 1; i <= a.length; i++) {
    let diag = prev[0]
    prev[0] = i
    for (let j = 1; j <= b.length; j++) {
      const up = prev[j]
      prev[j] = Math.min(prev[j] + 1, prev[j - 1] + 1, diag + (a[i - 1] === b[j - 1] ? 0 : 1))
      diag = up
    }
  }
  return prev[b.length]
}

// unknownCommand returns the refusal for a turn that starts with a /word
// captain does not know, or null. A typo used to run on auto routing with
// the word left in the text, so the lane the user meant never applied
// (/fontier ×3, /codex-ai, 2026-10-03). "/Users/me/x" is a path, not a word.
export function unknownCommand(text: string, known: Set<string> | null, custom: string[] = []): string | null {
  if (!known) return null
  const m = text.match(/^\s*\/([A-Za-z][\w-]*)(?=\s|:|$)/)
  if (!m) return null
  const word = m[1].toLowerCase()
  if (known.has(word) || OPENCODE_WORDS.includes(word) || custom.includes(word)) return null
  const all = [...known]
  const near = all
    .map((w) => ({ w, d: w.startsWith(word) || word.startsWith(w) ? 1 : editDistance(word, w) }))
    .filter((x) => x.d <= (word.length <= 4 ? 1 : 2))
    .sort((a, b) => a.d - b.d || a.w.length - b.w.length)
    .slice(0, 3)
    .map((x) => `/${x.w}`)
  const ask = near.length ? `Did you mean ${near.length === 1 ? near[0] : near.slice(0, -1).join(", ") + " or " + near[near.length - 1]}?` : `Lanes: /quality /frontier /save /speed /team; or name a leg.`
  return `captain: /${word} is not a command - nothing was sent. ${ask} To send it as plain text, remove the slash.`
}

