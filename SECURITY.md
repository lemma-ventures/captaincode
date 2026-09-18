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
