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

## OpenShell exports (experimental, unreleased)

An explicit OpenShell brain run or `captain with openshell` stores a verified
export on its attempt in `state.json`, separately from files applied to the workspace. The `exports`
array returned by `captain task artifacts <task-id>` includes its repository,
base revision, file list, SHA-256, patch path, sandbox runtime, exact verification
argv and `run.json` path. The CLI and handoff brief label it **exported (not
applied)**. These records survive a brain restart within the ledger's retention
limits; the underlying patch and evidence remain under `~/.captaincode/openshell/`.

Each worker's spend is in `run.json` under `tasks[].report.shield`: requests,
priced responses, token counts and `cost_usd`, as the provider reported them to
Shield. The attempt's single `call` charge in `state.json` sums them, and reads
`measured` only when every request was priced; otherwise it reads `unknown`.

The direct CLI prints its task ID and saves the task state and handoff brief
before returning. Its records can be read from `state.json` immediately; a
brain already running picks them up at its next save, when the newer copy of
each task and attempt row wins over its own. Cancellation is stored as `cancelled`, with no verified export.

The HTTP path saves dispatch before execution and saves exports and handoffs
before returning success. `X-Captain-Task-ID` and `X-Captain-Attempt-ID` identify
the records; streaming responses also announce the task ID. A disconnected
client cancels its controller; a cancelled or failed attempt remains queryable
without a verified export. These records do not depend on asynchronous grading.

The patch bytes, file list and reproduced tree are rechecked before returning the export.
Failed or interrupted runs expose no verified export. Host edits present before
or during the sandbox run are not counted as its changes. Explicit parallel
`/openshell ... + /openshell ...` workflows use the same durable export path:
one task and aggregate attempt point to `run.json`, whose `tasks` array retains
each worker's manifest, report, repairs and evidence. `require_all: true` means
all requested workers must pass and every conflict must have a valid ruling
before the combined patch can be verified and exported. Failed workflows keep
individual evidence without presenting it as a successful combined export.
Sequential `/openshell ... > /openshell ...` workflows retain the same aggregate
attempt plus a `stages` array in `run.json`. Each entry records its input
`revision`, verified `tree`, `next_revision` when another stage follows, and a
`run_record` pointing to `stage-N/run.json`. The separate `snapshot/` repository
retains intermediate commits; the final `integrated.patch` applies against the
original revision, not the last stage's base. Root records are replaced atomically
before stage dispatch and after verified handoffs. A failed later stage leaves
prior evidence available without exporting partial work. New sequences retain an
owner-only `sequence.json` containing their task assignments and execution settings;
`run.json.sequence_sha256` binds that plan. `captain openshell --resume <run-directory>`
reuses only fully verified stages and records explicit continuation times in
`resumptions`. It checks saved runtime fingerprints, worker and integration gates,
patch scope and hashes, and the complete snapshot chain before dispatch. A stage
run that a cancellation stopped has verdict `interrupted`. Recovery moves it to
`set_aside`, which records its stage, run record, revision, measured `attempts`
and Shield `requests`, `tokens`, `cost_usd` and `priced`. It then runs the stage
again in `stage-N-rerun-K`. A stage cancelled before any worker started leaves no
stage record, so recovery simply starts it. Failed stages, and stages still marked running
because their controller died, are not replayed. A completed sequence can be rechecked without
rewriting it or calling a model. The original brain task/attempt stays unchanged;
task-linked recovery is described below. Mixed host/sandbox workflows remain refused; JSON
sandbox teams are available through `captain openshell --team`.

New OpenShell run records include `deadline_at` when a task or caller deadline
applies. A sequence stores the same value in its checksum-bound `sequence.json`.
Recovery cannot extend that saved deadline, including when the environment limit
is removed or increased. CLI/HTTP tasks with `CAPTAIN_MAX_WALLTIME` also persist
their root start time and limit in the ledger budget; expiry during execution
records `time_exhausted`, fails the task and withholds its verified export.
Expired recovery is refused before dispatch and retains earlier evidence.

`run.json.attempt_usage` separates worker sessions, repair sessions and director
invocations from the worst-case `attempt_budget` admission bound. CLI/HTTP results
persist this breakdown in `AttemptState.openshell_attempts` and settle the root
budget once. Failed and cancelled runs still count completed invocations. A
recovery continuation owns the cumulative count, including reused stages;
replaying the same settlement does not add it again. `unmeasured` identifies
executions with unavailable counts, so `captain budget` displays known attempts
as a lower bound. Verification sandboxes use no model attempt. Set-aside runs
count in `attempt_usage` and in the run's spend; one whose record is missing
counts as unknown, not zero. In-flight usage is not yet reconciled; counts are
saved when the controller returns.

For new sequential CLI and HTTP tasks, `AttemptState.openshell` stores `run_dir`
and `sequence_sha256` before sandbox dispatch. `captain task inspect <id>` shows
that checkpoint after a restart. `captain task resume <task-id> <attempt-id>`
revalidates it under the run lock and creates a new attempt with `parent_attempt`
pointing to the interrupted attempt. The original attempt becomes failed, while
the continuation owns its final export, cumulative sequence usage and handoff.
Tasks with already-settled call usage are refused to avoid double counting.
The response means the continuation was accepted, not that verification passed;
inspect the task for completion and read `captain task artifacts <id>` for the
export. Invalid or incomplete checkpoints do not become running tasks. A brain
stop leaves a checkpointed sequence's attempt running, so the next start marks it
interrupted and nothing settles until a continuation finishes. A worker is never
resumed mid-run: a stopped stage runs again from its verified input.

With `CAPTAIN_OPENSHELL_AUTO_RESUME=1`, startup uses the same validation path for
previously running sandbox sequences with checkpoints from the last 24 hours.
Continuations run one at a time and have the attempt charge label `sandbox restart
recovery`. Default startup performs no recovery dispatch. The checkpoint timestamp,
not reconciliation time, controls freshness; pending cancellations and waiting tasks
are not resumed automatically. A cancellation requested before restart is retained
in `interrupt_reason` and blocks manual recovery too. The brain log records rejected
startup candidates, while their lifecycle remains interrupted for inspection.

After each verified stage, the controller saves the same checkpoint binding and
refreshes the attempt's `checkpoint_at`, including during task recovery. The stage
record reaches disk first. If the ledger save fails, the next stage does not start
and no final export is delivered; the verified stage remains available for explicit
recovery.

Explicit review tasks record `tasks[].mode: "review"` and `outcome: "unchanged"`.
Their report binds an empty patch checksum to the pinned snapshot and successful
sandbox checks; findings are retained in the worker's `answer.txt`. A review-only
run keeps its verified export evidence across ledger reloads, but the CLI and
handoff label it **verified unchanged snapshot** and offer nothing to apply.
An edit followed by a review still exports the earlier edits against the original
revision. Review text is evidence to read, not an automated approval gate.

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
