# Setting up Captain Code — instructions for a coding agent

You are a coding agent (Claude Code, Codex, Cursor, opencode, …) and a person
has asked you to install Captain Code on their machine. Follow these steps in
order. Every step says how to check it worked; do not move on until it has.

Humans: you do not need to read this. Paste the prompt from the
[README](README.md#let-your-agent-set-it-up) into your agent instead.

## Rules for the whole run

- **Supported platforms: macOS and Linux.** On Windows, stop and say so.
- **Ask before installing anything global** (Go, bun, a vendor CLI). List what
  is missing and what each one enables, then let the person choose.
- **Never do a login yourself and never ask for a secret in chat.** Vendor
  logins (`claude` `/login`, `codex login`, `cursor-agent login`,
  `opencode auth login`) are interactive: give the person the command and wait.
  API keys go into `~/.config/captain/env`, which the person edits themselves.
  Never print that file's values, and never commit it.
- **Read [SECURITY.md](SECURITY.md) and tell the person the headline before
  you start:** workers run with approvals disabled, so they can run any command
  the person's account can run.
- **Do not `cp` over a running `captain` binary.** Build to a temp file and
  `mv`/`install` it into place (step 3 does this).
- **`captain doctor` is the source of truth.** Every blocked line it prints ends
  with the exact command that unblocks it. Prefer that command over guessing.

## 1. Check what is already there

```sh
uname -s
go version            # need 1.24.2 or newer
bun --version         # only needed for the full terminal
command -v claude codex cursor-agent opencode captain captaincode
```

Report the result as a short table: present / missing, and what each missing
tool enables:

| Tool | Enables | Install |
|---|---|---|
| Go 1.24.2+ | the brain (required) | `brew install go`, or https://go.dev/dl/ |
| opencode | the TUI and every remote-model leg (grok, glm, deepseek, …) | `curl -fsSL https://opencode.ai/install \| bash` |
| bun | the terminal's sidebar plugin | `curl -fsSL https://bun.sh/install \| bash` |
| Claude Code | `claude` leg (can also be the director) | `npm i -g @anthropic-ai/claude-code` |
| Codex CLI | `codex-cli` leg | `npm i -g @openai/codex` |
| Cursor agent | `cursor` leg | `curl https://cursor.com/install -fsS \| bash` |

Only Go is mandatory, plus **at least one** agent CLI or opencode — one ready
leg is enough to route. Install what the person approves.

## 2. Pick the install shape

Ask the person, defaulting to **full terminal**:

- **Full terminal** — the interactive TUI. Needs a git checkout, opencode and bun.
- **Brain only** — the router as a CLI/HTTP service (`captain "…"`, CI, MCP
  callers). No checkout needed:
  `go install github.com/lemma-ventures/captaincode/cmd/captaincode@latest`,
  then continue at step 4 using `captaincode` in place of `captain` (or add the
  alias from step 3).

## 3. Clone and build (full terminal)

If you are already running inside a checkout of this repository, use it and
skip the clone.

```sh
git clone https://github.com/lemma-ventures/captaincode ~/Gits/captaincode
cd ~/Gits/captaincode
mkdir -p ~/.local/bin
go build -o /tmp/captain.new ./cmd/captaincode && install -m 755 /tmp/captain.new ~/.local/bin/captain
(cd plugin/captain-ui && bun install)
```

`captain init` finds the plugins through `$CAPTAIN_SRC`, which defaults to
`~/Gits/captaincode`. **If the checkout is anywhere else**, persist the
variable, because `captaincode.sh` reruns `captain init` on later launches:

```sh
echo "export CAPTAIN_SRC=\"$(pwd -P)\"" >> ~/.zshrc   # or ~/.bashrc
```

Make sure `~/.local/bin` is on PATH (add
`export PATH="$HOME/.local/bin:$PATH"` to the shell rc if it is not), then
check:

```sh
command -v captain    # → ~/.local/bin/captain
```

If `command -v captain` names some other file, tell the person: an older
install shadows the new one, and `captain doctor` will report `mismatch`.

## 4. Write the config

```sh
captain init
```

This writes `~/.config/opencode/opencode.jsonc` (captain provider, slash
commands, plugin registration) and scaffolds `~/.config/captain/env` with the
optional API keys commented out. It is idempotent. It says so loudly if it
cannot find the plugin sources — if you see that, fix `CAPTAIN_SRC` and rerun.

Check: `captain init --check` exits 0.

## 5. Credentials (the person does this)

Hand over, for each leg they want, the one command or key:

| Leg | What the person does |
|---|---|
| `claude` | run `claude`, then `/login` |
| `codex-cli` | `codex login` |
| `cursor` | `cursor-agent login` |
| remote models | `opencode auth login`, **or** put a key (`OPENROUTER_API_KEY`, `NVIDIA_API_KEY`, `HF_TOKEN`, …) in `~/.config/captain/env` |
| `jev` (optional) | `TYPESAFE_API_KEY` in `~/.config/captain/env` |

Wait for them to confirm. Do not read the key values back.

## 6. Start the brain and verify

```sh
captain brain > /tmp/captain-brain.log 2>&1 &
sleep 2
captain doctor
```

The brain must be (re)started **after** credentials and config are in place —
doctor reports what the running brain loaded, not what is on disk. After any
later change to `~/.config/captain/env` or `opencode.jsonc`, restart it
(`./captaincode.sh restart` from the checkout).

Read the doctor output:

- `legs  N ready of M` — **N ≥ 1 is success.** `captain doctor` exits non-zero
  when no leg is ready.
- Each `✗` leg line ends with the command that unblocks it. Run the ones that
  are installs (after asking); hand the ones that are logins or keys to the
  person; restart the brain; run doctor again.
- `~` lines (newer / unknown version) and `✗ terminal … brain-only install`
  under `build` do **not** block anything. Mention them, do not chase them.
- A `✗ captain … mismatch` under `build` means a different `captain` is first
  on PATH — fix PATH (step 3).

## 7. Smoke test

```sh
captain "reply with the single word: ready"
```

An answer means routing works end to end. If it fails, `captain why` and
`/tmp/captain-brain.log` say which leg was tried and why it stopped.

## 8. Hand over

Tell the person, in a few lines:

- which legs are ready and which are blocked (with the unblock command),
- how to open the terminal: `cd` into any project and run `captaincode.sh`
  from the checkout (suggest an alias with the real path, e.g.
  `alias captain-code="$HOME/Gits/captaincode/captaincode.sh"`); `-c` resumes
  the last session,
- where to look when something breaks: `captain doctor`,
  `/tmp/captain-brain.log`, `/tmp/captain-route.log`,
- that `./captaincode.sh down` stops the shared brain.

Do not launch the TUI yourself — it takes over the terminal. Leave that to the
person.

## Further reading

[docs/INSTALL.md](docs/INSTALL.md) (version pins and doctor in depth),
[docs/CONFIGURATION.md](docs/CONFIGURATION.md),
[docs/ADDING_A_LEG.md](docs/ADDING_A_LEG.md),
[docs/EUCLID.md](docs/EUCLID.md) (optional per-project memory: `captain euclid init`).
