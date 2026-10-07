// The jail rewrite for the TUI plugin (captain.ts): a sent turn's shell
// command runs as the brain's jail prefix plus the command as one quoted
// argument (pkg/captaincode/jail.go). Kept in its own module: opencode treats
// a plugin module's exports as plugins.

// shellQuote quotes one argument for /bin/sh.
export function shellQuote(s: string): string {
  return "'" + s.replaceAll("'", "'\\''") + "'"
}

// jailCommand is a shell command rewritten to run inside the jail.
export function jailCommand(prefix: string[], command: string): string {
  return [...prefix, command].map(shellQuote).join(" ")
}
