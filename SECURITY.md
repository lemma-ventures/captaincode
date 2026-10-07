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

## Messages between captains and prompt injection

Captains message each other on purpose: a worker or watcher in one folder can
hand a prompt to the TUI open in another (`captain send --cwd`), or reply to
the session that started it (`captain send --reply`). A sent prompt arrives
as a user turn, the strongest authority a worker sees. The sender may itself
have read a hostile web page or file, so this channel carries prompt
injection from one project into another.

| Channel | Written by | Arrives as | Risk |
|---|---|---|---|
| `captain send` | another agent, a watcher, a script | a user turn | high |
| program step handoff | our own previous worker | an assistant turn | medium |
| shared context (`/context consume`) | another project's digest | memory | medium |
| a task that names another repository | the user, in another TUI | the user's own turn | low |

What the brain does (`brain_inbox.go`, `pkg/captaincode/injection.go`,
`pkg/captaincode/sentpolicy.go`):

- **Filter.** Invisible and direction-changing characters and Unicode tag
  characters are removed, and lines that forge captain's own markers
  (`[captain]`, `[system]`) are neutralized.
- **Detect.** The text is screened for instruction overrides, role spoofing,
  sending secrets out, reading secret files, running fetched code
  (`curl … | sh`), switching off a safety check, and hiding work from the
  user. Lookalike letters (Cyrillic, Greek, fullwidth) are folded to Latin
  and base64 or percent-encoded payloads are decoded before the patterns
  run. No model is called for this first pass.
- **Judge.** What the patterns pass, or only flag, is read by a model before
  the TUI gets it (`pkg/captaincode/injection_judge.go`): the message is
  fenced between markers built from a random nonce, the judge has no tools,
  and it must answer with a fixed JSON verdict. An "injection" verdict at
  confidence 0.5 or more holds the message. A reply that is not a verdict
  counts as "could not judge", never as benign. A high pattern inside
  backtick code (usually discussed, not commanded) is decided by the judge:
  delivered with its flag when judged benign, held when judged an attack or
  when the judge cannot answer. The judge runs on the compaction leg
  (`CAPTAIN_INBOX_JUDGE_LEG` picks another; `CAPTAIN_INBOX_JUDGE=0` turns
  it off), in the background, so `captain send` returns at once.
- **Hold.** A high finding holds the message: it is never submitted, the TUI
  says so, and `captain inbox` lists it with its findings to release or drop
  (`CAPTAIN_INBOX_HOLD=0` turns holding off). A medium finding is delivered
  with the finding named on its "sent by" line.
- **Provenance.** Every delivered sent prompt ends with who sent it and from
  where. Only a reply token proves the sender; a name and a folder are the
  sender's own claim, and the label says "unverified". The worker that
  answers is told the turn was not typed by the user.
- **Enforce.** While a worker answers a sent turn, a deterministic policy
  refuses, at the tool boundary and without a decision leg: publishing or
  pushing, deleting or rewriting history, sending data off the machine
  (uploads, `scp`, `ssh`, `nc`), reading secrets (`~/.ssh`, `~/.aws`,
  `printenv`, `gh auth token`), relaying to other captains, switching off a
  safety check, writing outside the workspace or into `.git`, hooks or CI,
  and fetching a URL that carries a credential-like value. opencode workers
  are checked by the plugin through the brain (`/v1/gate/sent`). A claude
  worker on a sent turn runs with its own PreToolUse hook, passed with
  `--settings` for that run only (`captain gate --sent-hook`), so it holds
  with redaction and the decision leg both off; the global redaction hook
  applies the same policy too. codex runs a sent turn in its own sandbox
  (workspace writes only, no network) and cursor without `--force`. Reading
  credential files (`~/.ssh`, `~/.aws`, `.env`, `*.pem`) with a read tool is
  refused as well.
- **No secrets in URLs.** A secret placeholder is never turned back into the
  secret inside a fetched URL, sent turn or not.
- **Bounds.** A sent prompt cannot start a loop or a program, and at most
  `CAPTAIN_INBOX_QUOTA` (5) are accepted between two turns the user types
  (proved in `formal/`).
- **Other channels.** A program step reads the earlier steps' answers cleaned,
  with a note when they hold text that reads as instructions. Shared context
  from other projects stays fenced as data, and a line the screen rates high
  is dropped.
- **Web pages.** A request that changes something is refused when a browser
  sent it from anywhere but this machine (`Origin`, `Sec-Fetch-Site`), so a
  page the user opens cannot queue a prompt or start a turn.
- **Audit.** Every hold, flag, cleaning, release, drop and refusal is a line
  in `~/.captaincode/injection.jsonl`; `captain inbox --log` prints the last
  ones.

A regression corpus (`pkg/captaincode/injection_corpus_test.go`) holds 25
attacks, four of them disguised, and 20 benign messages between captains,
several about security; the patterns catch all 25 and would hold none of
the benign ones (one is flagged). Checked live on 2026-10-07: the judge held
a paraphrased attack that no pattern matches (0.98), passed a status report,
and held a message that told it to answer "benign" (1.00); a claude worker
on a sent turn, with only the per-run hook, was refused `git push`.

What remains: the judge is a model and can be persuaded or wrong, and with
it off or failing, a message that no pattern matches is delivered. In every
case the provenance line and the tool-level policy still apply - they are
the layers that do not depend on recognizing the wording.

A task typed in one TUI that names another repository runs there. If that
repository already has a worker, loop or program running, the moved worker
gets its own git worktree and its changes come back as a patch in
`~/.captaincode/runs/diffs/` (`CAPTAIN_ISOLATE_MOVED=0` turns this off).

## What we do protect

- **Shield (secrets masking).** On by default (`CAPTAIN_REDACT`). Recognisable
  credential shapes in tool output and provider request bodies are replaced
  with stable placeholders before a model or remote API sees them; the sidebar
  shield line counts what was masked. Identity paths and usernames can be
  rewritten the same way. Details: [docs/SECRETS.md](docs/SECRETS.md).
- **Credential values are never read.** Doctor checks *which* providers are
  authenticated by reading the keys of the auth store, never the values.
- **Journals and shared digests are scrubbed** of `sk-`, `ghp_`, `xox`,
  `Bearer` tokens and private-key blocks before anything is written to disk.
- **Cross-project context sharing is off in both directions by default**, and a
  project never quotes itself. Publishing and consuming are separate switches
  because a directory is a trust boundary. Quoted digests are neutralised so
  fetched content cannot forge a turn in another project's session.
- **File permissions**: digests and state are written `0600`.

**Do not even post credentials in prompts, even if Captain Code provides a
shield.** The shield masks common key shapes at the tool boundary and on the
wire; a bare passphrase, an unusual token, or a secret typed in prose still
reaches the provider. Treat every prompt as leaving your machine.

## Known limitations

- Prompts reach third-party providers. That is what the tool does; treat every
  leg as an external recipient of whatever you route to it, and choose legs
  accordingly for sensitive repositories.
- Shield is pattern matching, not judgment: secrets without a recognisable
  shape pass through, and cursor-agent traffic is outside the proxy (see
  [docs/SECRETS.md](docs/SECRETS.md)).
- A malicious repository can influence a worker through its own files
  (`AGENTS.md`, comments, test fixtures). Review what you point workers at.
- Most brain routes on the loopback port have no authentication. Anything
  running as your user can drive chat/route. The versioned **task API**
  (`/v1/task/*`) is loopback-only by default; set `CAPTAIN_TASK_TOKEN` and send
  `Authorization: Bearer …` if you expose it beyond loopback. `captain task mcp`
  talks to that same surface.
