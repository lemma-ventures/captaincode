# Captain Code

**One terminal for every coding agent you already pay for.**

You have Claude Code, Codex CLI and Cursor installed, each in its own window
with its own session and its own usage limit. Beyond them sit dozens of models
you would never open a separate client for - Grok, GLM, DeepSeek, Gemini, Kimi,
MiniMax, Qwen - all reachable through [opencode](https://opencode.ai).

Captain Code puts the local CLI agents and the remote models behind a single
TUI. You type once. A **director** model decides which of them should answer,
dispatches it, and the answer comes back in the same conversation. When one hits
its limit, the next one continues the work instead of you opening another
window.

```
                        ┌─ claude   (your Claude Code CLI, your subscription)
   you ─▶ one TUI ─▶ director ─┼─ codex-cli (your Codex CLI)
              ▲         │      ├─ cursor    (your Cursor agent)
              │         │      └─ grok · glm · deepseek · gemini · kimi · qwen
              └─────────┴────────  (remote models, via opencode)
                     one conversation · one ledger · one project memory
```

It runs on your machine, under your own credentials. It is not a service, it
does not proxy anyone else's keys, and it sends no telemetry anywhere. The
record of what happened is a directory of files you own.

## Why

Every coding agent binds a session to one model. Two things follow, and both
happen weekly:

- **The wall.** The model hits a usage limit, a capacity error or a policy
  refusal, and the session stops - with the plan and the half-finished work
  stranded inside it. Your other three agents are idle in other windows.
- **The bill.** A rename and an architecture migration cost the same, because
  they went to the same endpoint.

Captain Code sits in front of the models instead of inside one of them.

## What it does

- **Directs.** A director model classifies each task and assigns it. It is
  excluded from the worker pool, so it never assigns itself.
- **Routes by value, not by strength.** Each leg carries a quality prior, a
  price and an observed latency; the router scores `quality − cost − latency`
  with per-class weights (a trivial task weights cost at twice quality; a high
  one inverts it) and takes the cheapest leg that clears the quality bar.
- **Hands off when a leg stops.** A usage limit is read for its *reset time*,
  not a fixed cooldown; substantive partial work is kept and delivered marked
  as partial; anything that needs a fresh run reruns on the next-best open leg,
  frontier peer first. Outages, capacity errors and refusals each take their
  own path.
- **Keeps context across the swap.** Sessions are pruned deterministically
  (duplicate blocks collapsed, oversized blocks snipped) before anything is
  summarised, and the session's first instruction is re-attached above every
  summary. A per-repository "brain" (see [Euclid](docs/EUCLID.md)) gives the
  next model the project's state instead of a cold start.
- **Records.** Leg, class, domain, duration, tokens, estimated cost, outcome
  and a quality score per run - plus a full worker log on disk.
- **Runs teams and workflows.** `/team` puts several models on one task;
  `/frontier` or `/claude` forces a specific one; the
  [workflow language](docs/WORKFLOW_LANGUAGE.md) scripts multi-stage work; and
  `/parallel` and `/repeat` run detached threads while you keep typing.

## What a leg is

A **leg** is one named model behind one runtime:

| Leg kind | How it runs | Credential | Examples |
|---|---|---|---|
| Local CLI agent | the CLI you already have, as a subprocess | its own login | Claude Code, Codex CLI, Cursor |
| Remote model | a local `opencode serve` session | `opencode auth login`, or your API key | Grok, GLM, DeepSeek, Gemini, Kimi, MiniMax, Qwen, free tiers |

Legs are data, not code: `captain legs add <id> <provider/model>` writes an
overlay and wires the model through, no rebuild. See
[Adding a leg](docs/ADDING_A_LEG.md).

## Quickstart

### The brain, on its own

```sh
go install github.com/lemma-ventures/captaincode/cmd/captaincode@latest
```

Go names the binary after its package directory, so this installs **`captaincode`**
into `$(go env GOPATH)/bin`. The documentation says `captain`; pick one:

```sh
echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.zshrc
echo 'alias captain=captaincode' >> ~/.zshrc && source ~/.zshrc
```

Then:

```sh
captain init                 # write the opencode config Captain Code expects
captain doctor               # which of your agents are wired, and what is missing
captain brain &              # the router, on 127.0.0.1:14097
captain "fix the failing test in pkg/foo"
```

`captain doctor` is the real quickstart: it lists every leg as ready, blocked or
skipped, and each blocked line carries the one command that unblocks it.

### The full terminal

The terminal is stock [opencode](https://opencode.ai) plus two plugins that live
in `plugin/` - the router and the panels. They are **files in this repository,
not code in the binary**, so `go install` alone cannot give you a terminal:
clone it.

```sh
git clone https://github.com/lemma-ventures/captaincode && cd captaincode
go build -o ~/.local/bin/captain ./cmd/captaincode
(cd plugin/captain-ui && bun install)   # the panels' dependencies
captain init                            # registers both plugins by absolute path
./captaincode.sh                        # brain + TUI, in the folder you are in
```

`captain init` points opencode at the checkout it finds - `$CAPTAIN_SRC`, or
`~/Gits/captaincode` - and says so loudly when there is nothing there to point
at. See [plugin/README.md](plugin/README.md).

Full settings reference: [docs/CONFIGURATION.md](docs/CONFIGURATION.md).
How the pieces fit: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
Milestones and completion criteria: [docs/ROADMAP.md](docs/ROADMAP.md).
That plan covers Captain's orchestration; reusable memory belongs to Euclid's roadmap,
and optional Trace/Atlas evidence services have a separate backlog.

## What this is not

Captain Code drives vendor CLIs under **your** credentials, on **your**
machine, for **your** work. It does not pool or resell subscription seats,
multiplex one subscription across users, or proxy anyone else's credentials.
If you are looking for a hosted multi-tenant router, this is not that, and
using it as one would breach most providers' terms.

It also runs workers with approvals disabled by default, because a headless
worker that stops to ask a question waits forever. Read
[SECURITY.md](SECURITY.md) before pointing it at anything you do not own.

## Status

We run this every day on our own work; it is published because it is useful,
not because it is a product. There is no support contract. Issues and patches
are welcome - see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT - see [LICENSE](LICENSE) and [NOTICE](NOTICE).
