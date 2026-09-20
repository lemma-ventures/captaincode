# Installation contract

**Before installing, please read the [security disclaimers](../SECURITY.md).**
Workers run with approvals disabled by default and can execute any command
your user account can run.

**ROADMAP M1.1.** What a machine must have for a Captain result to mean
anything, and how to get it there, move it forward and put it back.

The rule underneath all of it: *an evidence run names its toolchain*. A leg is
a model **and** the CLI that drives it, and the CLI moves under us - `claude -p`
has changed its JSON envelope, `codex exec` has renamed flags, opencode's
server has moved the session route. A baseline produced against one adapter
version is not the same baseline against another, so the versions are pinned in
code and reported by `captain doctor` rather than assumed.

## Two shapes of install

| Shape | What runs | Who it is for |
|---|---|---|
| **Brain-only** | the Go binary (`captain brain`) plus at least one adapter CLI | routing, `captain why`, `captain stats`, HTTP/MCP callers, CI and evaluation runs |
| **Full terminal** | brain-only, plus [opencode](https://opencode.ai) as the TUI, the two plugins in `plugin/`, and `captaincode.sh` | interactive day-to-day use |

Brain-only is the supported shape for reproducing an evidence report, because
it has no terminal state, no saved session and no TUI version to pin. It is the
only shape `go install` can produce.

The full terminal needs a **checkout**, not just the binary. Routing, the
sidebar, the wordmark and the director tag are two plugins that stock opencode
loads from disk (`plugin/captain.ts` and `plugin/captain-ui/index.tsx`), and
`captaincode.sh` is the launcher that supervises the brain beside the TUI.
Neither is compiled into the binary, so a `go install` machine has nothing for
`captain init` to register - it will say so rather than come up silently
unrouted.

```sh
git clone https://github.com/lemma-ventures/captaincode && cd captaincode
go build -o ~/.local/bin/captain ./cmd/captaincode
(cd plugin/captain-ui && bun install)
captain init && ./captaincode.sh
```

`captain init` registers the plugins by absolute path out of `$CAPTAIN_SRC`
(default `$HOME/Gits/captaincode` when present), so set that variable when the
checkout lives anywhere else. Anything the terminal can do, the brain can be
asked to do directly.

## Captain's own version

The adapters are half the contract. The other half is the software doing the
routing: a report that names `claude 2.1.270` and `codex 0.153.4` but not the
binary that chose between them is still not reproducible, because the routing
policy, the prices and the accounting schema live in the brain.

Captain is built from source rather than installed from a release, so there is
no version to pin it *to*. The honest identity is the revision it was built
from and whether that revision was the whole truth - both of which Go stamps
into the binary whenever it is built inside a checkout, so no build flag can
forget them. `captain doctor` reports three rows under `build`:

| Row | What it names |
|---|---|
| `captain` | the running brain binary: its revision, and the path it ran from |
| `terminal` | the vendored opencode fork's checkout: its HEAD, and whether it is clean |
| `bun` | that fork's runtime, pinned by its own `packageManager` field |

The fork is retired: the terminal is stock opencode plus the plugins in
`plugin/`, and this repository carries no opencode source. So `terminal` and
`bun` read `missing` on every install of it, full terminal included - they are
kept because an evidence report produced against the old fork still has to be
readable. The row that matters here is `captain`.

The states reuse the adapter vocabulary, plus one:

- `dirty` - built from a modified tree. Usable, and what a developer runs all
  day, but the revision names a commit whose code is not the code that ran, so
  an evidence run may not quote it. It does not block: unreproducible is not
  broken.
- `mismatch` - the `captain` on PATH is a different file from the one that
  produced the report. That blocks, for the same reason a foreign `codex`
  does: the next command runs a binary this report never probed.
- `missing` on `terminal` is every install of this repository, brain-only and
  full terminal alike, because the fork it probes for is retired. It is not a
  fault.

`captain upgrade --check` prints the same two rows through the same probe, so
the two commands cannot disagree about which revision is installed.

## The pinned adapters

`pkg/captaincode/toolchain.go` is the single authority. Each adapter carries a
**tested** version (the one this build's behaviour was observed against) and a
**minimum** (the oldest whose contract captain still relies on).

`captain doctor` probes the binary that will actually run - not a lockfile -
and reports one of:

| Mark | State | Meaning |
|---|---|---|
| `✓` | ok | present and at the tested pin |
| `~` | newer | usable, but not the version the evidence was produced against |
| `~` | unknown | it would not say a version; never counted as satisfying the pin |
| `✗` | old | below the minimum - the flags captain compiles against are not there |
| `✗` | mismatch | that name on PATH is some other program |
| `✗` | missing | nothing on PATH under that name |

`newer` and `unknown` do not block a leg: a version captain cannot read is a
reason to distrust the *report*, not to refuse a tool the user installed. `old`,
`mismatch` and `missing` do block it, with the fix on the same line.

## Prerequisites

| Tool | Install (macOS) | Install (Linux) | Enables |
|---|---|---|---|
| Go 1.24.2+ | `brew install go` | distro package or https://go.dev/dl/ | the captain brain |
| Claude Code | `npm i -g @anthropic-ai/claude-code` | same | `claude` leg (director capable) |
| Cursor agent | `curl https://cursor.com/install -fsS \| bash` | same | `cursor` leg |
| OpenAI Codex | `npm i -g @openai/codex` | same | `codex-cli` leg |
| opencode | `curl -fsSL https://opencode.ai/install \| bash` | same | 10+ opencode legs (grok, glm, …) + TUI |

None are strictly required — one runnable leg is enough — but the table lets you pick what you need. `captain doctor` will list exactly what is missing and the one-line fix.

## Binary name, PATH and alias

`go install` (or direct `go build`) produces a binary named `captaincode` (last path segment of the package). The docs say `captain`. Either name works; an alias is the usual bridge:

```bash
# one-time PATH (zsh example)
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc

# the alias so docs (`captain …`) match the binary
echo 'alias captain=captaincode' >> ~/.zshrc
source ~/.zshrc
```

Or just use `captaincode` everywhere - every command in these docs works under
that name. A symlink is the other option:

```bash
ln -sf "$(go env GOPATH)/bin/captaincode" ~/.local/bin/captain
```

## Credentials

Captain holds no keys. Each leg uses a login or API key already on the machine.

| Leg kind | How to authenticate |
|---|---|
| Local CLI (`claude`, `codex-cli`, `cursor`) | Vendor login once: Claude `/login`, `codex login`, `cursor-agent login` |
| Remote (opencode providers) | `opencode auth login`, **or** API keys in `~/.config/captain/env` (`NVIDIA_API_KEY`, `OPENROUTER_API_KEY`, `HF_TOKEN`, …) |
| Optional decision / ranking | `TYPESAFE_API_KEY` (jev), `CAPTAIN_AA_API_KEY` (live perf ranking) |

`captain init` scaffolds `~/.config/captain/env` with those keys commented out. Never commit that file. After editing it, restart the brain so doctor and workers see the new values. Doctor reads auth-store **keys only**, never values; each blocked leg line names the one command that unblocks it.

## First-run order (config → credentials → brain → doctor → TUI)

Config is read at brain startup. `captain doctor` talks to the *running* brain (via /v1/health) and shows the director/config that brain loaded.

Canonical order:

1. Install Go + at least one agent CLI (see table).
2. `captain init`   ← writes opencode.jsonc + ~/.config/captain/env (derived director/fallback from PATH)
3. Sign in / set keys (see [Credentials](#credentials) above).
4. `captain euclid init`   *(optional; project memory — needs a brain for the first model call)*
5. Start the brain: `captain brain` (or let `./captaincode.sh` start it)
6. `captain doctor`   ← verify; every ✗ line names the exact command
7. Full terminal: from the checkout, `./captaincode.sh` opens the TUI in the current folder

**After any change to ~/.config/captain/env or opencode.jsonc: restart the brain.** Doctor (and health, and the director the workers see) reflect the running process, not the files on disk.

Exception: `captain euclid init --repo` needs the brain already running (it makes a model call), so it comes after step 5.

## Recipes (macOS and Linux)

Each adapter's install, upgrade and rollback commands live on its pin, so the
hint `captain doctor` prints is the command this document describes - they
cannot drift apart. `captain upgrade --check` lists installed versions;
`captain upgrade` runs each adapter's own updater.

```bash
# the brain
go build -o ~/.local/bin/captain ./cmd/captaincode/

captain init
captain brain &
captain doctor
```

Rollback pins the tested version back; `captain doctor` prints the exact string
for each adapter, so a machine that has drifted can be returned to the version a
report was produced against without consulting this file.

Deploy the brain atomically. Never `cp` over the running binary - write beside
it and `mv` into place, or the running process is corrupted mid-run.

## Verifying a machine

```bash
captain doctor
```

Exit status is non-zero when no leg is ready. For an evidence run, require
additionally that every adapter a participating leg uses reports `✓`: a `~`
line means the run cannot quote the manifest's version as the one it used.

The same rule applies to the `build` rows. The header line counts them -
`build  3 of 3 components name a revision an evidence run can quote` - and an
evidence run needs that count complete: a dirty brain or a dirty fork checkout
means the manifest's revision is not the code that produced the result.
