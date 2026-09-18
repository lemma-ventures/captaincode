# Architecture

Three processes and a directory of files.

```
 terminal (opencode TUI + patch)
        │  POST /v1/route   "which leg should answer this?"
        ▼
 captain brain  (Go, 127.0.0.1:14097, OpenAI-compatible)
        │  director plan → value ranking → dispatch
        ├─────────────► opencode serve (14096)  ── provider APIs
        ├─────────────► claude -p / codex exec / cursor-agent -p
        ▼
 ~/.captaincode/   ledger · run logs · registry overlay · priors · state
```

The brain is the only component that holds policy. The terminal asks it a
question; the runtimes answer prompts; the files remember what happened.

One brain serves every terminal on the machine at once. Each terminal is open
in one folder - its **workspace** - and names it on every call (the captain
provider sends `X-Captain-Cwd`, the sidebar passes `?cwd=`). Workers run there,
the workspace's own Euclid brain is read and journaled, and its worker list,
activity feed, last route, live workflow, `/repeat` threads and `/parallel`
runs are the ones its sidebar and control words show (`captain stop` ends
that folder's loops; `--all` reaches every folder). The TUI's own state -
the prompt history behind the arrow keys, the last model, its preferences -
is per folder too (`~/.captaincode/state/<folder>`, prepared by `captain
state`). What is shared is what is shared by nature: leg cooldowns (a rate
limit is per account, not per folder), the scorecard, the main Euclid brain,
and the one `opencode serve` that hosts worker sessions (each pinned to its
workspace).

## The brain speaks OpenAI

`captain brain` exposes an OpenAI-compatible `/v1/chat/completions` where the
**model id is a leg name** (`claude`, `grok`, `team`, `frontier`). Anything that
can talk to an OpenAI endpoint can therefore drive it, and the terminal patch is
a thin client rather than an integration. Control surfaces (`/team`, `/parallel`,
`/repeat`, `/context`, `/euclid`, `/captain`) are intercepted before dispatch.
`/btw <note>` steers a worker that is already running: the plugin posts the note
to `/v1/btw` the moment it is typed, and the turn's handle (`Steer`) hands it to
the transport - `claude -p` reads it on stdin (stream-json input) and folds it
into the running turn, an opencode session merges it as a second message; codex
exec and cursor-agent have no channel, so their note runs as the next turn.

## One task, end to end

1. **Triage** - static classification in under a millisecond: class (trivial /
   medium / high) and domain (code / editorial / research). No model call.
2. **Route** - the director plans and picks, unless the task is trivial or
   `CAPTAIN_FAST_ROUTE` is set, in which case the ladder decides alone. The
   director sees a menu of open legs with price and value rank, and is excluded
   from it.
3. **Fit the prompt** - pruned deterministically, then summarised only if still
   over the leg's budget, with the session's first instruction re-attached above
   the summary.
4. **Dispatch** - by transport: an `opencode serve` session pinned to the
   project directory, or a vendor CLI subprocess. Status lines stream back; the
   full transcript is teed to a run log.
5. **Watch** - three timers (first event, idle, stall) plus a hard cap. A stall
   is confirmed over a second channel before anything is killed, because the
   first channel has died silently before and killed healthy runs.
6. **Handle failure** - rate limit, provider down, refusal and harness fault are
   distinct outcomes with distinct cooldowns. Substantive partial work is kept.
   A fresh run goes to the next-best open leg; frontier work fails over by
   performance index: the Frontier legs in rank order (claude, codex-cli,
   grok-max), then every other leg by index - never the cheap ladder.
   Every request a worker or claude -p makes crosses the egress proxy
   (127.0.0.1:14098), where secrets become placeholders and the operator's
   identity a stand-in; tool output is masked before it enters the context
   at all. See [SECRETS.md](SECRETS.md).
7. **Record** - one ledger line and one scorecard update. The next decision
   reads them.

## Value ranking

```
value(leg, class) = w_q·quality/10 − w_c·cost/cost_ref − w_l·latency/lat_ref
```

Candidates must first clear a quality bar τ for the class. Cost and latency are
normalised against **absolute** references, not against the best candidate -
normalising within the candidate set gave a lone cheap model the full latency
penalty and broke the ranking. Subscription legs carry pressure instead of
price: cooling or near a quota window discounts them. Frontier legs never enter
the cheap path.

Quality is a blend of a cold-start prior and observed scorecards, per domain.
`CAPTAIN_EXPLORE` sends a small share of tasks to a leg the ranking would not
have chosen, so a leg that never wins never stops being measured.

## Teams and workflows

`/team` asks the director for a small plan - bounded fan-out, a named leg per
member, a synthesis step. Members named explicitly are binding: they head the
menu even when a filter would have dropped them, and a plan naming an
unavailable leg is repaired rather than rejected.

Workflows are scripted multi-stage runs in a small
[language](WORKFLOW_LANGUAGE.md) whose spec the compiler is tested against, so
the documentation cannot drift from the parser.

## What is deliberately not here

- **No daemon on a port you did not start.** `captain brain` is the process.
  Nothing in this repository backgrounds it, supervises it, or restarts it.
  Stop that process and its workers stop with it.
- **No telemetry.** Nothing leaves the machine except the prompts you asked a
  provider to answer.
- **No credential storage.** Captain Code holds no keys; it uses what the
  machine already has, and never reads the values of an auth store - only which
  providers are present.
- **No shared state between projects** unless you switch it on per project, in
  both directions (`/context publish on`, `/context consume …`).
