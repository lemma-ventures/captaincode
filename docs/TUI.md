# TUI cheat sheet

The words you type in a Captain session. The brain parses them before routing
without a model call. Tasks, workflow compilation and memory distillation still
use models. `/captain` prints the short version in the TUI
(`cmd/captaincode/brain_help.go`); the shell commands are in [CLI.md](CLI.md).

A bare prompt is routed: triage, the director's pick, a worker, an assessment.
Everything below narrows or steers that.

## Pick who runs it

| Word | What it does |
|---|---|
| `/claude` `/codex` `/codex-cli` `/cursor` `/grok` `/grok-max` `/gemini` `/deepseek` `/kimi` `/glm` `/minimax` `/qwen` `/step` `/gpt-oss` `/ds4-flash` `/luna` `/free` | Force one leg for this turn (`captain legs` lists yours; a leg added with `captain legs add` gets its word after `captain init`) |
| `/frontier <task>` | The frontier lane at maximum effort: the frontier legs near the top of the perf index (claude and codex-cli today) take turns; `/frontier /codex …` runs that leg's frontier model instead |
| `/team <task>` | The director plans an ensemble on one task and reviews it into one answer |
| `/team /quality <task>` / `/team /frontier <task>` / `/team /codex-cli …` | Bind the ensemble to the best-rated legs, or make one member binding; the director fills the rest |
| `/quality` (`/q`, `/best`) · `/speed` (`/fast`) · `/save` (`/cheap`) | A preference for this turn, before or after a leg prefix. `/quality` takes turns across the two best legs by blended quality, at high effort; `/save` across the open-weight legs that clear the quality bar, each on its own model at medium effort rather than its flash sibling (the whole ladder only when no open-weight leg is open); the director picks inside `/speed` |
| `/oss` (`/open`) | Open-weight models only |
| `/deterministic` (`/det`, `/adi`) | Only legs green in the Agentic Determinism Index, pinned to the measured serving tuple ([ADI.md](ADI.md)) |

Modifiers compose in any order: `/oss /repeat 5 <task>`, `/team /deterministic <task>`.

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

A connector counts only when a leg prefix follows it, so prose is never
misread. Grammar and limits: [WORKFLOW_LANGUAGE.md](WORKFLOW_LANGUAGE.md).

## Run things in the background

| Word | What it does |
|---|---|
| `/repeat N <task>` | Repeat until N rounds (no N = until stopped); rounds stream in that turn |
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
