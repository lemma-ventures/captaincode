# TUI cheat sheet

The words you type in a Captain session. The brain parses them before routing
without a model call. The grammar, the order in which readings are tried and the
rules for changing the language are in [LANGUAGE.md](LANGUAGE.md); `captain parse
"<line>"` prints how a line is read without sending it. Tasks, workflow compilation and memory distillation still
use models. `/captain` prints the short version in the TUI
(`cmd/captaincode/brain_help.go`); the shell commands are in [CLI.md](CLI.md).

A bare prompt is routed: triage, the director's pick, a worker, an assessment.
Everything below narrows or steers that.

## Pick who runs it

| Word | What it does |
|---|---|
| `/claude` `/codex` `/codex-cli` `/cursor` `/grok` `/grok-max` `/gemini` `/deepseek` `/ds-flash` `/kimi` `/glm` `/minimax` `/mistral` `/mimo` `/kolibri` `/qwen` `/step` `/gpt-oss` `/ds4-flash` `/luna` `/free` | Force one leg for this turn (`captain legs` lists yours; a leg added with `captain legs add` gets its word after `captain init`) |
| `/frontier <task>` | The frontier lane at maximum effort: the frontier legs near the top of the perf index (claude and codex-cli today) take turns; `/frontier /codex …` runs that leg's frontier model instead |
| `/team <task>` | The director plans an ensemble on one task and reviews it into one answer |
| `/team /quality <task>` / `/team /frontier <task>` / `/team /codex-cli …` | Bind the ensemble to the best-rated legs, or make one member binding; the director fills the rest |
| `/team /openshell <task>` | Sandbox-only team: the tool-less director splits the task into 1-4 assignments that run as one parallel OpenShell stage (experimental; see [CONFIGURATION](CONFIGURATION.md#openshell-workers)) |
| `/quality` (`/q`, `/best`) · `/speed` (`/fast`) · `/save` (`/cheap`) | A preference for this turn, before or after a leg prefix. `/quality` takes turns across the legs within 85% of the best by blended quality, at high effort, leaving the frontier-class legs (`codex-cli`, `grok-max`) to `/frontier`; `/save` across the open-weight legs that clear the quality bar, each on its own model at medium effort rather than its flash sibling (the whole ladder only when no open-weight leg is open); the director picks inside `/speed` |
| `/oss` (`/open`) | Open-weight models only |
| `/deterministic` (`/det`, `/adi`) | Only legs green in the Agentic Determinism Index, pinned to the measured serving tuple ([ADI.md](ADI.md)) |
| `/noslop` | Plain-writing rules for every worker of the turn, OpenShell sandboxes included. On by default; this word brings them back when `CAPTAIN_WORKER_NOSLOP=0` or a set `CAPTAIN_SKILLS_ALWAYS` drops them |

Modifiers compose in any order: `/oss /repeat 5 <task>`, `/team /deterministic <task>`, `/noslop /openshell <task>`.

A turn that starts with a `/word` captain does not know is refused before anything is sent, with the nearest commands: "/fontier is not a command - nothing was sent. Did you mean /frontier?". Remove the slash to send the text as is. opencode's own commands and your custom command files are never refused.

`/private` reviews the private-names list: what captain added on its own, the names it suggests from the folders you work in, grouped by project. `/private add <names>` keeps names out of public repositories, any word included; `/private dismiss <names>` stops a suggestion; `/private remove <names>` takes a name off the list. Nothing in it reaches a worker or a model.

`/frontier`, `/quality` and `/save` are lanes: each counts where its last 40
turns went and sends the next to the leg furthest behind an equal share, among
the legs scoring within 85% of the lane's best. The best leg runs a lane's
first turns and wins ties. A forced leg, or legs named in the prompt, are
never balanced. See [CONFIGURATION.md](CONFIGURATION.md#lanes-frontier-quality-save).

## Steer the director

| Word | What it does |
|---|---|
| `/captain director` (or `status`) | Who directs, why, and how much each judge has worked lately |
| `/captain <leg>` | Pin a judge leg as director |
| `/captain frontier` · `quality` · `auto` · `reset` | Best-ranked judge · best of tier 2 · least-used capable · back to the default |
| `/captain more <axis> [N%]` / `/captain less <axis> [N%]` | Raise or lower one axis of the routing mix: 5 points bare, or N% of its current share (20% more of 20% is 24%). Repeats compound; the other axes fund the change |
| `/captain oss=20% frontier=30% …` | Set the mix directly; unnamed axes scale to fill the rest |
| `/captain targets` (`target`, `mix`, `balance`) · `/captain mix reset` | Print the mix · back to the default |

The axes are `frontier`, `quality`, `cheap`, `fast`, `oss` and `deterministic`
(`speed`, `save`, `open`, `det` also work). Unset, the mix is
frontier=quality=cheap=fast=oss=20% and deterministic=0, and it does not steer.
Once saved it biases unprefixed turns toward legs that close the gap; a
modifier on a turn still wins, and `CAPTAIN_STEER=0` turns the bias off. Every
mix command prints the targets.

## Chain work (Captain Workflow Language)

| Form | What it does |
|---|---|
| `/grok analyse X > /codex review it` | `>` (or `then`, `->`) runs stages in sequence |
| `/grok fix A + /cursor fix B` | `+` (or `and`, `&`) runs legs in parallel inside a stage (max 4) |
| one task per line | Also fans out in parallel |
| `/grok review X > /codex` | A bare leg refines the previous stage's output |
| `… gate: <cmd>` | The stage's result counts only when the command exits 0 (one repair retry) |
| `/wf <english>` (`/workflow`) | Compile plain English into a workflow and preview it |
| `/run wf_x` (`/wfrun`) | Run a compiled workflow |
| `/wf save <name> <expression>` · `/wf run <name>` · `/wf list` | Named workflows |
| `/team research X > /codex implement it` | A **program**: `>` before `/team`, `/repeat`, a lane word or `(` chains whole turns. Each step reads the end of the step before |
| `( … )` | Groups steps: `(/repeat 4 …) > /claude review`. `(/codex draft) > /claude review` is two turns, each with its own answer |
| `/repeat N <steps> until: <cmd>` | Loops until the command exits 0 (checked before each round); a loop that ends with the check failing has failed |
| `A \|\| B` | Runs B only when A failed: an error, a gate still failing after its repair, a loop whose `until:` never passed |
| `/wf parse <program>` | Shows how captain reads a program and how many turns it can run. Runs nothing |

A connector counts only when a command follows it, so prose is never
misread: `then` and `and` join legs only, never whole turns. A program runs
on the `/repeat` machinery (`/repeat watch`, `finish`, `abort`, `captain stop`),
with one budget of 100 turns (`CAPTAIN_REPEAT_MAX`) for all its loops and
steps. Only a typed prompt starts one: `captain send` refuses loops and
programs. Grammar, limits and failure rules:
[WORKFLOW_LANGUAGE.md §11](WORKFLOW_LANGUAGE.md#11-programs-groups-chains-loops-and-fallbacks-level-2).
How LangGraph, CrewAI, AutoGen and others map onto programs:
[ORCHESTRATION_MAPPING.md](ORCHESTRATION_MAPPING.md).

```captain
/team research the API > /codex implement it > /claude review the diff
/repeat 10 /codex fix the failing tests until: go test ./... || /claude explain why the tests still fail
/frontier write the spec > (/repeat 4 /codex implement the next item gate: go test ./...) > /claude review the diff
```

## Run things in the background

| Word | What it does |
|---|---|
| `/repeat N <task>` | Repeat until N rounds (no N = until stopped); rounds stream in that turn. `… until: <cmd>` also ends the loop when the command exits 0 |
| `/repeat status` · `show` · `watch` | Loops in this folder · what recent rounds did · follow a loop live |
| `/repeat stop` (`finish`, `wrapup`) · `/repeat abort` | End after the current round · kill it now |
| `/parallel <task>` | Run a second task beside this chat; `/parallel show`, `status`, `stop` |
| `/btw <note>` | Tell the running worker more (claude and the opencode legs take it mid-run; codex and cursor get it as the next turn) |
| `/interrupt [reason]` | Stop the running worker and keep its work: a handoff (done, left, how to resume) or a marked partial |

`/repeat` control words take an optional `last`, `all` or `rp_…` thread id. Anything
wordier is a task: `/repeat watch the queue` repeats that task.

## Settings & memory

| Word | What it does |
|---|---|
| `/init` | Regenerate worker permissions (web, edits, bash; `.env` stays denied) |
| `/context status` · `publish on\|off` · `consume all\|none\|<path>` · `scrub` · `forget` | Cross-project context sharing (off by default) |
| `/euclid status` · `/euclid distill [apply]` | Euclid memory: your brains, and register edits from the run journal ([EUCLID.md](EUCLID.md)) |
| `/captain` (`/captain help`, `/help`) | The short cheat sheet |

## While a turn is streaming

The TUI queues what you type until the turn ends, so no `/…` word reaches the
brain mid-stream. Press **Esc** to free the input, or use the shell (`!` in the
TUI, or any terminal):

| Command | What it does |
|---|---|
| `captain kill [--loops]` | Stop the run in flight; `--loops` also ends `/repeat` |
| `captain stop` | End a `/repeat` loop after the current round |
| `captain status` / `captain watch` | What is running now |
| `captain runs` / `captain show <id>` | Every run, with full output |
| `captain send "<prompt>"` | Queue a prompt into this folder's session |
