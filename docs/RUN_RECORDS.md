# Run records: what Captain keeps at the router boundary

Every time Captain hands work to an agent, it writes down what it decided and
what came back. All of it stays on your machine under `~/.captaincode/`.
Nothing here is uploaded; delete the directory and it is gone.

This page answers five questions about any run: which agent got the task, what
it ran, which files changed, the last error, and what the next agent was handed.

## The five questions

| Question | Where it is | How to read it |
|---|---|---|
| **Which agent got the task, and why** | `routing.jsonl` (one `decision` row per task: chosen leg, routing path, rationale, every candidate and why the others were excluded), `state.json` `events` (leg, model, effort, outcome, tokens, cost) | `captain why` (the last decision), `captain runs --leg <leg>`, `captain task inspect <task-id>` |
| **What it ran** | `runs/<run-id>-<leg>.log`: the worker's full answer text as it streamed, plus one line per tool call (`[42s] ⚙ Bash go test ./...`) with elapsed time | `captain show <run-id>`, or open the log (the history record names it under `logs`) |
| **Which files changed** | Solo turns: `diffs/solo-<leg>.<digest>.diff` plus `changed_files` and `diff_digest` on the attempt in `state.json`. `/team` and workflows: one manifest per worker (files, diff, gate result) and `runs/diffs/<leg>.<digest>.diff` | `captain task artifacts <task-id>` (also shows the director's ruling when two workers edited the same file) |
| **The last error** | The history record's `error` and each worker's `error`; the log's closing lines (`failed in 3m12s`, `error: …`); `events[].error`; the handoff brief's failed checks (command, exit code, output) | `captain runs --failed`, `captain show <run-id>`, `captain task inspect <task-id>` |
| **What the next handoff received** | `state.json` `handoffs`: one brief per task with the requirements, each attempt's leg and outcome, the verified artifacts, failed checks, remaining actions and anything uncertain. Workflows also write `runs/wf_<id>-<name>.md` with every stage's full output, in order | `captain task inspect <task-id>` prints the brief; open the `.md` for a workflow |

A task id (`t-…`) joins these together: it is on the decision, the events, the
attempts and the handoff brief. To find one from a prompt:

```sh
grep '"kind":"decision"' ~/.captaincode/routing.jsonl | grep 'words from the prompt' | tail -1
```

## The files

| Path | One line per | Kept |
|---|---|---|
| `history/<date>.jsonl` | Turn: kind (solo, team, workflow, frontier), legs, model, the task, full output, error, duration, each worker's text and error, log paths | Forever, one file per day |
| `runs/<run-id>-<leg>.log` | Worker run: leg, start time, working directory, the task, then everything it said and every tool it called, then the outcome and error | Forever (`CAPTAIN_WORKER_LOGS=0` stops writing them). Mode 0600 |
| `runs/wf_<id>-<name>.md` | Workflow: each stage's leg, duration, full output or error, and the review | Forever |
| `routing.jsonl` | Decision, run event or settled outcome, append-only | Up to 64 MB, then the newest half (`CAPTAIN_ROUTING_LOG=0` turns it off) |
| `state.json` | The ledger: decisions, events, charges, task and attempt lifecycle, budgets, handoff briefs, cooldowns | Ring buffers: the last 500 events, 200 decisions, 500 lifecycle states, 200 handoff briefs |
| `diffs/`, `runs/diffs/` | The exact diff a worker produced | Forever |
| `gate.log` | Action-gate screening of a tool call (see [CONFIGURATION.md](CONFIGURATION.md)) | Append-only |
| `redact.log` | Masking call: how many values were masked, never the values | Append-only |

## What is not recorded exactly (yet)

Two answers are partial today. Both are known, and both are on the list.

- **The exact command.** A tool-call line in the worker log is cut to 80
  characters and its whitespace collapsed, so a long command or a heredoc is
  shortened. The full command is in the agent CLI's own transcript (Claude Code
  under `~/.claude/projects/`, Codex under `~/.codex/sessions/`), which Captain
  does not link to yet.
- **The full prompt a rerouted or escalated worker received.** When a leg fails
  and the same task moves to the next leg, or a failed gate hands the task on
  with its output, the next worker's log shows only the first 600 characters of
  the user's request, not the whole prompt. The handoff brief and the
  workflow transcript record the failure that caused the handoff; the log does
  not repeat it.

One more thing to know when reading "files changed" for a solo turn: it is the
working tree against `HEAD` after the turn. Edits you had not committed before
the turn show up too, and anything the worker committed itself does not.

## Privacy

These files hold your prompts, the agents' answers and your diffs, in plain
text. Secrets are masked before they reach a provider ([SECRETS.md](SECRETS.md)),
but the local records are written from what the worker said and did on your
machine, so treat `~/.captaincode/` like your shell history.
