# Security

## Reporting

Mail **security@lemma.ventures** with what you found and how to reproduce it.
We will acknowledge within a week. Please give us 90 days before publishing.

Do not open a public issue for a vulnerability. Do not send AI-generated bug
reports you have not verified yourself - we read every report, and we will stop
reading yours.

## Threat model

Captain Code is a **single-user, local** tool. It assumes:

- The machine is yours and not shared with an untrusted user.
- The repositories you point it at are ones you are allowed to modify.
- The credentials on the machine are yours to use.

Under those assumptions it holds no secrets of its own, opens no inbound port
beyond loopback, and sends nothing anywhere except the prompts you asked a
provider to answer.

## Workers run with approvals disabled - read this

By default Captain Code drives workers with the vendor CLI's approval prompts
turned off (`--dangerously-skip-permissions` and equivalents), and `captain
init` writes a permissive opencode ruleset. This is deliberate: a headless
worker that stops to ask a question waits forever - nobody is at the terminal to
answer, and the run dies on a watchdog instead.

The consequence is real: **a worker can run any command your user account can
run**, in the directory it was given. It is the same authority the vendor CLI
has when you run it interactively and approve everything, without the pause.

If that is not acceptable for your work:

- Set `CAPTAIN_CLAUDE_PERMISSIONS` (and the equivalent per-CLI settings) to keep
  approvals on, and drive workers interactively.
- Set `CAPTAIN_CODEX_CLI_SANDBOX` to a sandbox mode rather than bypass.
- Run the whole thing in a container or VM with only the repository mounted.
- Do not run it as root, and do not run it on a machine holding credentials for
  systems the work does not need.

`captain init` also **narrows** what a worker may read - notably denying the
read tool on `.env` files. That is a guard rail against an accidental read, not
a security boundary: a shell command can still read any file the user can.

### Workers are processes, not sandboxes

Be precise about what isolation exists. A worker is an ordinary subprocess of
the brain (`exec.CommandContext`), with `cmd.Dir` set to the working directory
and **the brain's entire environment inherited** - every credential in it. The
only isolation is a git worktree, and
[`pkg/captaincode/scope.go`](pkg/captaincode/scope.go) says so in its own
first paragraph: *a worktree isolates Git changes, not processes, credentials
or network access.* That file also carries the per-transport table of what
each CLI can actually enforce; read it rather than assuming.

The egress proxy is a closed set of provider origins, and
`CAPTAIN_EGRESS_ALLOW` narrows it further. That bounds **captain's own model
traffic**. It is not a network boundary for a worker: `curl` in a bash tool
call does not pass through the proxy at all.

### The action gate

Because there is nobody to ask, captain screens actions with a classifier
instead of a prompt: three calibrated nouls on the decision leg over the
command a worker is about to run (irreversible destruction, out of scope,
exfiltration). It runs as a Claude Code `PreToolUse` hook and from the
opencode plugin's tool boundary. See
[the configuration](docs/CONFIGURATION.md#the-action-gate).

Its limits, stated so nobody over-trusts it:

- It defaults to **shadow**: it records and allows. Enforcement is opt-in.
- **A failed or slow call allows.** It is an availability-preserving gate.
- **With no decision leg configured it does nothing at all.**
- It is a classifier. It will be wrong in both directions, and it is not a
  substitute for a container, a VM, or a machine without the credentials.

## What we do protect

- **Credential values are never read.** Doctor checks *which* providers are
  authenticated by reading the keys of the auth store, never the values.
- **Journals and shared digests are scrubbed** of `sk-`, `ghp_`, `xox`,
  `Bearer` tokens and private-key blocks before anything is written to disk.
- **Cross-project context sharing is off in both directions by default**, and a
  project never quotes itself. Publishing and consuming are separate switches
  because a directory is a trust boundary. Quoted digests are neutralised so
  fetched content cannot forge a turn in another project's session.
- **File permissions**: digests and state are written `0600`.

## Known limitations

- Prompts reach third-party providers. That is what the tool does; treat every
  leg as an external recipient of whatever you route to it, and choose legs
  accordingly for sensitive repositories.
- A malicious repository can influence a worker through its own files
  (`AGENTS.md`, comments, test fixtures). Review what you point workers at.
- The brain's loopback port has no authentication. Anything running as your user
  on the machine can drive it.
