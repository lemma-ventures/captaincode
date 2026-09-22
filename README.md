<div align="center">

<img src="pkg/captaincode/assets/captaincode.svg" alt="Captain Code" width="96" height="96">

# Captain Code

**One terminal for every coding agent you already pay for.**

Claude Code, Codex CLI, Cursor, Grok, GLM, DeepSeek, Gemini, Kimi, Qwen and more,
behind one prompt. A director model sends each task to the right one, and when an
agent hits its limit the next one picks up the work.

[![CI](https://github.com/lemma-ventures/captaincode/actions/workflows/go.yml/badge.svg)](https://github.com/lemma-ventures/captaincode/actions/workflows/go.yml)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Runs locally](https://img.shields.io/badge/runs-100%25%20local-5c9cf5)
![No telemetry](https://img.shields.io/badge/telemetry-none-5c9cf5)

[Quickstart](#quickstart) · [Features](#features) · [How it works](#how-it-works) · [Docs](#documentation) · [FAQ](#faq)

</div>

---

```text
                               ┌─ claude     your Claude Code CLI, your subscription
   you ─▶ one TUI ─▶ director ─┼─ codex-cli  your Codex CLI
              ▲          │     ├─ cursor     your Cursor agent
              │          │     └─ grok · glm · deepseek · gemini · kimi · qwen  (via opencode)
              └──────────┴───────────────────────────────────────────────
                     one conversation · one ledger · one project memory
```

## Why

Every coding agent ties a session to one model. That causes two problems, and
you probably hit both every week:

| | The problem | What Captain Code does |
|---|---|---|
| 🧱 | **The wall.** A usage limit, a capacity error or a refusal stops the session, and the plan and the half-finished work are stuck inside it. Your other agents sit idle in other windows. | Hands the task to the next available agent with its context, so the work keeps going. |
| 💸 | **The bill.** A rename costs as much as an architecture migration, because both go to the same model. | Sends each task to the cheapest agent that is good enough for it. |

Captain Code sits **in front of** your models instead of inside one of them.

## Quickstart

### 🤖 Let your agent install it (recommended)

Paste this into Claude Code, Codex, Cursor or any other coding agent:

```text
Set up Captain Code on this machine by following
https://github.com/lemma-ventures/captaincode/blob/main/AGENT_SETUP.md
step by step. Ask me before installing anything, and hand me any login or
API key instead of doing it yourself.
```

It checks what you already have, installs only what you approve, builds the
brain, sets up the terminal, and finishes when `captain doctor` passes.
Prefer to do it yourself? The same steps are in [AGENT_SETUP.md](AGENT_SETUP.md).

### 🛠️ Install by hand

The full terminal needs a clone, because its two opencode plugins live in
[`plugin/`](plugin/README.md) and are not part of the binary:

```sh
git clone https://github.com/lemma-ventures/captaincode && cd captaincode
go build -o ~/.local/bin/captain ./cmd/captaincode
(cd plugin/captain-ui && bun install)   # dependencies for the panels
captain init                            # writes the opencode config and registers the plugins
captain doctor                          # shows which agents are ready and how to fix the rest
./captaincode.sh                        # starts the brain and the TUI in the current folder
```

> [!TIP]
> `captain doctor` is the real quickstart. It lists every agent as **ready**,
> **blocked** or **skipped**, and each blocked line includes the one command
> that fixes it.

<details>
<summary><b>Only want the router (no TUI)?</b></summary>

<br>

```sh
go install github.com/lemma-ventures/captaincode/cmd/captaincode@latest
```

Go names the binary after its package, so this installs **`captaincode`** into
`$(go env GOPATH)/bin`. The docs call it `captain`, so add an alias:

```sh
echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.zshrc
echo 'alias captain=captaincode' >> ~/.zshrc && source ~/.zshrc
```

Then:

```sh
captain init                                   # write the opencode config Captain Code expects
captain doctor                                 # check which agents are wired
captain brain &                                # the router, on 127.0.0.1:14097
captain "fix the failing test in pkg/foo"      # send it a task
```

</details>

<details>
<summary><b>Where does <code>captain init</code> look for the plugins?</b></summary>

<br>

It uses `$CAPTAIN_SRC`, or `$HOME/Gits/captaincode` if that folder exists, and
tells you clearly when it finds nothing there. See [plugin/README.md](plugin/README.md).

</details>

## Features

- **🧭 A director picks the agent.** A director model classifies each task and
  assigns it to a worker. It is never one of the workers, so it can't assign
  work to itself.
- **💰 Routes by value, not just strength.** Every agent has a quality score, a
  price and a measured latency. For each task, Captain Code picks the cheapest
  agent that meets the quality bar. Small tasks lean toward cheap; hard tasks
  lean toward quality.
- **🔁 Hands off when an agent stops.** When an agent hits a usage limit,
  Captain Code reads when that limit resets instead of guessing. Useful partial
  work is kept and marked as partial. Anything that needs a fresh run goes to
  the next best agent. Outages, capacity errors and refusals are each handled
  differently.
- **🧠 Keeps context across the switch.** Conversations are trimmed
  predictably before they are summarised, and your original instruction always
  stays at the top. A per-repository memory, [Euclid](docs/EUCLID.md), tells
  the next model where the project stands, so it doesn't start cold.
- **📒 Records everything.** For every run: which agent, what kind of task,
  duration, tokens, estimated cost, outcome and a quality score, plus the full
  worker log. It is all plain files you own.
- **⚡ Fast, calibrated decisions with Jev (optional).** A small decision
  model sorts each task in a few hundred milliseconds, and it also screens
  what workers are about to do. [More below](#jev-the-decision-leg-optional).
- **👥 Teams, workflows and background jobs.** Put several models on one task,
  script multi-step work with the [workflow language](docs/WORKFLOW_LANGUAGE.md),
  or run jobs in the background while you keep typing.

### In the TUI

| Command | What it does |
|---|---|
| `/team` | Put several models on the same task |
| `/claude`, `/frontier` | Send the task to a specific agent, or to the strongest one available |
| `/parallel`, `/repeat` | Run work in background threads while you keep typing |

### From the shell

| Command | What it does |
|---|---|
| `captain doctor` | Check which agents are ready and what is missing |
| `captain why` | Explain the last routing decision |
| `captain quota` | Show the known usage limits for each agent |
| `captain jev` | Check the Jev decision leg, or ask it a question by hand |
| `captain stats` · `captain runs` | Look back at what ran, what it cost and how it went |

See the full list in the [CLI cheat sheet](docs/CLI.md).

## How it works

Captain Code calls each model a **leg**: one model running through one tool.

| Kind of leg | How it runs | Login | Examples |
|---|---|---|---|
| **Local CLI agent** | The CLI you already have, run as a subprocess | Its own login | Claude Code, Codex CLI, Cursor |
| **Remote model** | A local `opencode serve` session | `opencode auth login` or your API key | Grok, GLM, DeepSeek, Gemini, Kimi, MiniMax, Qwen, free tiers |

Legs are configuration, not code. Add one with a single command, no rebuild needed:

```sh
captain legs add <id> <provider/model>
```

More in [Adding a leg](docs/ADDING_A_LEG.md) and [Architecture](docs/ARCHITECTURE.md).

### Jev, the decision leg (optional)

Most of Captain Code's own decisions are small questions: *What kind of task
is this? Is this worker stuck? Is this command dangerous?* Asking a full coding
model each time is slow and wasteful. Captain Code sends these questions to a
**decision leg** instead: a model that answers typed questions (pick one of
these options, or yes/no) with a calibrated probability, and never writes code
or runs tools.

The default decision leg is **[Jev](https://typesafe.ai)**, TypeSafe's System
One model. It answers in about 0.1–0.8 seconds, for a fraction of a cent.

| Where Jev is used | What it does | Acts on the answer? |
|---|---|---|
| **Triage** | Decides what kind of task each prompt is, replacing a ~5s call to a free model | ✅ Yes, when its confidence clears the bar (0.6 by default); otherwise the old path decides |
| **Action gate** | Before a worker runs a tool, checks whether it would destroy something, go beyond its task, or send data off the machine | 🟡 Records only by default; `CAPTAIN_ACTION_GATE=enforce` blocks risky actions |
| **Supervision** | Checks each running worker: is it stuck, on the wrong thing, in need of you, ignoring `AGENTS.md`? | ⚪ Records only |
| **Shadow decisions** | Answers routing questions next to Captain Code's own choices, so you can measure how often it agrees | ⚪ Records only |

"Records only" answers are saved next to what actually happened, so you can
check how accurate Jev is (`captain jev shadow`, `captain gate --report`)
before you let it make that decision.

**Turn it on:** get a key at
[console.typesafe.ai/settings/keys](https://console.typesafe.ai/settings/keys)
and add it to `~/.config/captain/env`, then restart the brain:

```sh
TYPESAFE_API_KEY=...
```

```sh
captain jev                                     # check the key and the connection
captain jev classify "fix the typo in README"   # see how it would triage a task
```

> [!NOTE]
> **Without a key, nothing breaks.** Triage falls back to its built-in rules
> and a free model, which is just slower. You can also skip TypeSafe entirely
> and point `CAPTAIN_SYSTEMONE_URL` at any compatible endpoint, including a
> local one with no key. `captain jev conform` then checks whether that
> backend gives usable answers. All the details are in
> [Configuration → Decision legs](docs/CONFIGURATION.md#decision-legs-jev).

## Documentation

| | |
|---|---|
| 🚀 [Agent setup](AGENT_SETUP.md) | Step-by-step install a coding agent can follow |
| ⌨️ [CLI cheat sheet](docs/CLI.md) | Every shell command |
| ⚙️ [Configuration](docs/CONFIGURATION.md) | All settings |
| 🏗️ [Architecture](docs/ARCHITECTURE.md) | How the pieces fit together |
| ➕ [Adding a leg](docs/ADDING_A_LEG.md) | Connect a new model |
| 🔀 [Workflow language](docs/WORKFLOW_LANGUAGE.md) | Script multi-step work |
| 🧠 [Euclid](docs/EUCLID.md) | Per-repository project memory |
| 🗺️ [Roadmap](docs/ROADMAP.md) | Milestones and what "done" means for each |

## FAQ

<details>
<summary><b>Is this a hosted service? Does it send my data anywhere?</b></summary>

<br>

No. It runs on your machine with your own logins and keys. It sends no
telemetry, and the record of what happened is a folder of files you own.

</details>

<details>
<summary><b>Can I use it to share one subscription with my team?</b></summary>

<br>

No. Captain Code runs vendor CLIs with **your** credentials, on **your**
machine, for **your** work. It does not pool or resell subscription seats,
share one subscription across users, or proxy anyone else's credentials. Using
it as a hosted multi-user router would break most providers' terms.

</details>

<details>
<summary><b>Do I need every agent installed?</b></summary>

<br>

No. Use whatever you have. `captain doctor` shows which agents are ready and
skips the rest.

</details>

<details>
<summary><b>Do I need a TypeSafe key for Jev?</b></summary>

<br>

No. Jev makes triage faster and enables the action gate and supervision
records, but everything else works without it. If `captain doctor` shows a
`✗ jev` line, that doesn't stop anything else from working. See
[Jev, the decision leg](#jev-the-decision-leg-optional).

</details>

> [!WARNING]
> Workers run with **approvals turned off by default**, because a background
> worker that stops to ask a question would wait forever. Read
> [SECURITY.md](SECURITY.md) before pointing Captain Code at anything you don't own.

## Status

We use Captain Code every day for our own work. We publish it because it is
useful, not as a product, so there is no support contract. Issues and pull
requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
