<div align="center">

<img src="pkg/captaincode/assets/captaincode.svg" alt="Captain Code" width="96" height="96">

# Captain Code

**Your coding agents. One terminal.**

Build with Claude Code, Codex CLI, Cursor and API models in one conversation.
Route each task, bring in a second model, and make tests part of the workflow.

[![CI](https://github.com/lemma-ventures/captaincode/actions/workflows/go.yml/badge.svg)](https://github.com/lemma-ventures/captaincode/actions/workflows/go.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![macOS and Linux](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-5c9cf5)

**Open source. Free to use. Your accounts and keys.**
Agent subscriptions and API usage still cost what your providers charge.

[Get started](#quickstart) · [See it in action](https://captaincode.ai/#demo) · [Features](#features) · [Docs](#documentation)

</div>

## Build, test, review. In one line.

Ask Codex CLI for a fix, run tests, then hand the result to Claude:

```text
/codex-cli fix the retry bug gate: go test ./... > /claude review the diff for edge cases
```

`>` passes work to the next stage. `gate:` runs a check and feeds failures
back for a bounded repair. Inspect the test result before accepting the change.
Use your repository's test command and configure the named agents first.

Or just type the task. Captain Code picks an available worker using task fit,
estimated cost and observed performance. Use `captain why` to inspect its choice.

## Features

| What you want to do | What Captain Code gives you |
|---|---|
| **Use the agents you already have** | Claude Code, Codex CLI and Cursor keep their own logins. Add API models through OpenCode. Choose an agent or let Captain route. |
| **Keep working through usage limits** | Eligible provider failures can hand off to another worker. Captain keeps useful partial output and passes conversation context, not the provider's hidden state. |
| **Get more than one model's view** | `/team` asks a director to plan several workers and review their output into one answer. |
| **Turn prompts into workflows** | Chain stages with `>`, run parallel reviews with `+`, and attach test checks with `gate:`. `/wf` previews a workflow from plain English. |
| **Work on more than one task** | `/parallel` starts a separate task. `/repeat N` runs bounded rounds. `/interrupt` stops a worker and keeps a handoff or partial result. |
| **Run edits and tests in a sandbox** | Experimental `/openshell` runs on a prepared NVIDIA OpenShell runtime and returns a patch that passed your configured checks. Review it before applying it. |
| **See what happened** | Local run records show the worker, outcome, duration, token usage and estimated cost. Inspect routing with `captain why` and history with `captain runs`. |

### Commands worth trying

Type these in the Captain terminal:

| Command | Use it for |
|---|---|
| `/frontier investigate the deadlock` | Use a configured frontier model at maximum effort. |
| `/save update the changelog` | Prefer economical workers for this turn. This is not a spending cap. |
| `/team review the API design` | Get several workers' views and one reviewed answer. |
| `/parallel check the release notes` | Run a second task alongside your conversation. |
| `/repeat 3 fix the next failing test` | Run up to three rounds. |
| `/oss explain the parser` | Use only configured open-weight models. |
| `/btw keep the public API unchanged` | Add a constraint. Claude and OpenCode workers receive it mid-run; Codex CLI and Cursor receive it next turn. |

For parallel work, give workers separate files or use read-only reviews.
See the [TUI commands](docs/TUI.md) and [workflow reference](docs/WORKFLOW_LANGUAGE.md).

### OpenShell: choose the boundary

Set up the [OpenShell runtime](examples/openshell-pilot/README.md), choose the
existing files the worker may edit, and supply the test command. Captain takes
a snapshot of the selected Git revision, runs the worker and tests in the
sandbox, and exports the checked patch.

OpenShell is **experimental and opt-in**. It requires a prepared runtime.
The controller runs on the host; ordinary agent sessions are not sandboxed by
this integration. [Setup and limits](docs/CONFIGURATION.md#openshell-workers).

## Quickstart

**macOS or Linux.** The full terminal needs Go 1.24.2+, OpenCode, Bun and at
least one authenticated agent or model provider.

> [!WARNING]
> Host workers run with approval prompts disabled by default and can run
> commands with your account's permissions. Read [SECURITY.md](SECURITY.md)
> before your first task.

### Let your coding agent set it up

Paste this into your current agent:

```text
Set up Captain Code by following
https://github.com/lemma-ventures/captaincode/blob/main/AGENT_SETUP.md
Check what is installed first. Ask before installing software or changing
configuration. Let me handle logins and enter API keys locally, never in chat.
```

### Install it yourself

With the prerequisites installed:

```sh
git clone https://github.com/lemma-ventures/captaincode
cd captaincode
mkdir -p ~/.local/bin
export PATH="$HOME/.local/bin:$PATH"
export CAPTAIN_SRC="$PWD"
go build -o ~/.local/bin/captain.new ./cmd/captaincode && mv ~/.local/bin/captain.new ~/.local/bin/captain
(cd plugin/captain-ui && bun install --frozen-lockfile)
captain init
./captaincode.sh
```

`captain init` writes the OpenCode configuration and registers the plugins.
The launcher starts the local router and opens the terminal. In another shell,
run `captain doctor` to check agent readiness and see how to fix missing setup.
Keep `~/.local/bin` on your PATH and persist `CAPTAIN_SRC` in your shell config.
To work on another repository, launch `$CAPTAIN_SRC/captaincode.sh` from that folder.

Want only the router for CLI, HTTP or MCP use? See the
[installation guide](docs/INSTALL.md). `go install` installs a binary named
`captaincode`; it does not include the terminal plugins.

## Your tools, your records

Captain Code runs the coordinator on your machine. Connected providers still
receive the prompts and code sent to their models. Captain Code has no hosted
repository service and sends no product telemetry.

- **Connect more models.** A *leg* is one model through one tool. Add one with
  `captain legs add <id> <provider/model>`. [Model setup](docs/ADDING_A_LEG.md).
- **Carry project context.** Conversation handoffs work across agents. Optional
  [project memory](docs/EUCLID.md) adds repository notes; its separate engine
  is not bundled with the default install.
- **Inspect the work.** `captain why`, `captain quota`, `captain budget` and
  `captain runs` expose routing, known limits and local run history. Cost
  figures are estimates; teams and retries can add usage.
- **Use another client.** Connect through the local HTTP API or
  `captain task mcp` for task submission, inspection and cancellation.
  [CLI reference](docs/CLI.md).

## Documentation

| Start here | Go deeper |
|---|---|
| 🚀 [Agent setup](AGENT_SETUP.md) | Step-by-step install a coding agent can follow |
| ⌨️ [CLI cheat sheet](docs/CLI.md) | Every shell command |
| 💬 [TUI cheat sheet](docs/TUI.md) | Every `/…` word in a session |
| ⚙️ [Configuration](docs/CONFIGURATION.md) | All settings |
| 🏗️ [Architecture](docs/ARCHITECTURE.md) | How the pieces fit together |
| 🧾 [Run records](docs/RUN_RECORDS.md) | What each run leaves in `~/.captaincode/`: which agent, what it ran, files changed, errors, handoffs |
| [OpenShell pilot](examples/openshell-pilot/README.md) | Experimental NIM worker isolation, denial checks and recoverable diff landing |
| ➕ [Adding a leg](docs/ADDING_A_LEG.md) | Connect a new model |
| 📐 [Command language](docs/LANGUAGE.md) | The grammar of everything you type: heads, modifiers, workflows, programs, loops |
| 🔀 [Workflow language](docs/WORKFLOW_LANGUAGE.md) | Script multi-step work |
| 🧠 [Euclid](docs/EUCLID.md) | Per-repository project memory |
| 🗺️ [Roadmap](docs/ROADMAP.md) | Milestones and what "done" means for each |
| 🛠️ [Skills](skills/README.md) | Procedures other harnesses can load, taken from how Captain lands, verifies and checks claims |

## FAQ

**Do I need several subscriptions?** No. Start with one ready agent or model
provider. Teams and handoffs become useful when you connect more.

**Is Captain Code free?** Yes, under the [MIT license](LICENSE). Your provider
subscriptions, API charges and usage limits still apply.

**Do I need Jev or project memory?** No. [Jev](docs/CONFIGURATION.md#decision-legs-jev)
is an optional decision model for triage and action screening. Project memory
is also optional. Neither is required to route a task.

**Can I share one subscription with a team?** Captain Code is a single-user
tool that uses your own accounts. It does not pool or resell subscription seats.

## Contribute

We use Captain Code in daily engineering work. Bring a reproducible bug,
an adapter for another agent, or a clearer setup guide.
[Contributions](CONTRIBUTING.md) use DCO sign-off. See [NOTICE](NOTICE) for credits.
