# Configuration

Captain Code reads three kinds of state: **config files** you own, **environment
variables** that override them, and **local data** it writes as it runs. Nothing
is sent anywhere; all of it lives under your home directory.

## Files

| Path | What it is | Written by |
|---|---|---|
| `~/.config/captain/env` | Environment for the brain and the workers it spawns. Loaded at startup. | `captain init` scaffolds it |
| `~/.config/opencode/opencode.jsonc` | opencode's config: providers, models, worker permissions, the `captain` provider entries. | `captain init`, `captain legs add` |
| `~/.captaincode/legs.json` | Leg registry overlay - add, re-point or disable legs without a rebuild. | `captain legs add/remove` |
| `~/.captaincode/priors.json` | Quality priors per leg and domain. Optional: compiled defaults are used when absent. | `captain priors sync` |
| `~/.captaincode/leg_notes.json` | Per-leg steering notes shown to the director. | `captain refine` |
| `~/.captaincode/history/*.jsonl` | Append-only daily run transcripts (human-readable history). Not the durable ledger. | the brain |
| `~/.captaincode/runs/*.log` | Full worker transcripts. | the brain |
| `~/.captaincode/state.json` | Durable ledger: charges, decisions, quotas, budgets, task/attempt lifecycle, outcomes, handoffs, cooldowns. Saves merge append-only collections so concurrent CLI/brain writers cannot clobber each other. | the brain / CLIs that touch the ledger |
| `<repo>/.euclid/`, `~/.euclid/` | Local Euclid brains (gitignored; opt-in) - see [EUCLID.md](EUCLID.md). | `captain euclid init [--repo]` |

`XDG_CONFIG_HOME` is honoured for the config paths.

## Environment

### Routing

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_DIRECTOR` | derived at `captain init` (claude when present; compile default grok) | Comma ladder; the **first** entry directs and is excluded from the worker pool. Later entries take over when the active director keeps failing. The first entry may also be a **mode** — `frontier`, `quality` or `auto` (see *The helm* below). |
| `CAPTAIN_DIRECTOR_WINDOW` | `14d` | The trailing window `auto` measures usage over (`Nd`, `Nw`, or a Go duration). |
| `CAPTAIN_DIRECTOR_AUTO_FLOOR` | `0.80` | `auto` only considers judges whose index is at least this fraction of the best judge's. |
| `CAPTAIN_DIRECTOR_TIER_BAND` | `0.10` | `quality` treats judges within this fraction of the best as tier 1 and picks the best below it. |

### The helm: choosing the director at runtime

`/captain <word>` in the TUI, `captain director <word>` in a shell, or `POST /v1/director {"director":"<word>"}` — one switch, persisted in `state.json`, so a brain restart keeps it:

| Word | Picks |
|---|---|
| `<leg>` (`claude`, `grok`, `glm`, `kimi`, `gemini`, `codex`, …) | That leg, pinned. Only **judges** qualify: claude (`claude -p`) and opencode-served pins run with tools off; `codex-cli` and `cursor` are agents and are refused. |
| `frontier` | The best-ranked judge by the performance index (today claude). |
| `quality` | The best of **tier 2**: the strongest judge below the top band (today glm; grok directs as grok-4.6 and ranks next). |
| `auto` | The least-used capable judge over `CAPTAIN_DIRECTOR_WINDOW` — usage is wall-clock seconds from the run history plus the ledger's director/review calls; a judge in a rate-limit cooldown is skipped; the standing pick keeps the helm unless another has used under 60% of its time. |
| `director` | Show the helm, the reason, the ladder, and each judge's recent usage. |
| `reset` | Back to `CAPTAIN_DIRECTOR` / the default. |

A mode re-resolves once a minute against the live ranking and usage; `grok` and `grok-max` count as one judge (same model, same credential).

### Minimal profile (Claude + Cursor only)

A common minimal machine has only claude and cursor installed (no opencode, no grok credential). After `captain init`:

- `captain init` now derives `CAPTAIN_DIRECTOR` and `CAPTAIN_FALLBACK_LEG` from what is on PATH, so it will write `claude` + `cursor` in this case.
- The director is excluded from the worker pool, so choosing the director chooses which of your two agents plans (and the other works).
- If the env still names an absent leg (pre-init or hand edit), `captain doctor` warns and the brain at startup + effectiveDirector will use the first runnable instead of failing every plan.

Example for Claude plans, Cursor executes:

```bash
# ~/.config/captain/env
CAPTAIN_DIRECTOR=claude
CAPTAIN_FALLBACK_LEG=cursor
```

After edit: restart the brain so doctor and health report the new director. Use `--no-manager` (or `CAPTAIN_FAST_ROUTE=1`) for pure heuristic with no director at all.
| `CAPTAIN_LEGS` | all registry legs | Allowlist. A leg outside it runs only when forced (`/<leg>`). |
| `CAPTAIN_VALUE_ROUTING` | on (`0` disables) | Value ranking on the fast path and in reroutes. Off falls back to the fixed ladder. |
| `CAPTAIN_VALUE_WEIGHTS` | `0.5,0.3,0.1` | `quality,cost,latency` weights for medium work. |
| `CAPTAIN_VALUE_WEIGHTS_TRIVIAL` | `0.3,0.6,0.1` | Same, for trivial work - cost dominates. |
| `CAPTAIN_VALUE_TAU` | `5.5,7.0` | Quality bar a leg must clear, `trivial,medium`. |
| `CAPTAIN_VALUE_COST_REF` | `0.50` | USD per task treated as "expensive" when normalising. |
| `CAPTAIN_VALUE_LAT_REF` | `600000` | Milliseconds treated as "slow" when normalising. |
| `CAPTAIN_EXPLORE` | `0.10,0.05` | ε per class (trivial, medium): the share of tasks sent to a leg the router would not have picked, so the ledger keeps learning. `0` disables. |
| `CAPTAIN_TRIAGE` | on (`0` disables) | Sub-millisecond static classification before any model call. |
| `CAPTAIN_TRIAGE_JEV` | on when `TYPESAFE_API_KEY` is set (`0` disables) | Triage tier 1 asks the **jev** decision leg first (TypeSafe System One: the class and domain questions as two typed choices, ~100-500ms, $0.042/M input, output free). Its answer counts when its calibrated confidence clears the bar; otherwise the ~5s free-leg classify decides as before. Every jev call is charged to the turn (`captain why`). |
| `CAPTAIN_TRIAGE_JEV_CONF` | `CAPTAIN_TRIAGE_CONF` (0.6) | The calibrated confidence (the weaker of the two answers) a jev classification must reach to be taken. |
| `CAPTAIN_TRIAGE_JEV_BELOW` | `0.9` | With jev configured, the heuristic confidence below which it is asked on its own: over the free-leg bar (`CAPTAIN_TRIAGE_CONF`) and under this, a sure jev answer replaces the heuristic and a miss keeps it, with no free-leg call. `0` closes the band (jev only under `CAPTAIN_TRIAGE_CONF`); `1` asks it on every triaged task. |
| `CAPTAIN_JEV_SHADOW` | on when `TYPESAFE_API_KEY` is set (`0` disables) | Asks jev the **shadow** questions - the shape of the turn, which leg should take it, which running worker a `/btw` note concerns - beside the decisions captain already makes, and records the answers next to what captain did. Nothing is acted on. Off, the tier-1 call asks only class and domain and no call is made beside the director or a note. See [Shadow decisions](#shadow-decisions-reading-jevs-calibration). |
| `CAPTAIN_FAST_ROUTE` | off (`1` enables) | Skip the director entirely; pure ladder. |
| `CAPTAIN_FALLBACK_LEG` | - | Leg the terminal degrades to when the brain does not answer in time. |
| `CAPTAIN_REDACT` | `on` | Secrets on the wire (see [SECRETS.md](SECRETS.md)): credentials in tool output and request bodies become stable placeholders, the operator's home/name become stand-ins, private-key files are refused. `off`, `secrets` (no identity rewrite), `strict` (`.env` refused too). |
| `CAPTAIN_PROXY_ADDR` | `127.0.0.1:14098` | The egress proxy the workers, claude -p and codex exec call providers through. `CAPTAIN_PROXY_CLAUDE=0` / `CAPTAIN_PROXY_CODEX=0` send that CLI direct. |
| `TYPESAFE_API_KEY` | unset | TypeSafe key (console.typesafe.ai/settings/keys) - or, like `aa.env`, a `jev.env` file holding the console download's `API_KEY=…` line in `~/.config/captain/`, next to the captain source (`CAPTAIN_SRC`) or in the current directory; the variable wins over the file. With it the **jev** decision leg is ready (`captain doctor`), triage asks it first, and `captain jev` answers by hand. `CAPTAIN_JEV_MODEL` repins it (default `jev-latest`, the alias TypeSafe moves; pin `jev-1.13.0` to freeze a tuned confidence bar); `CAPTAIN_SYSTEMONE_URL` points the client at a mock or a proxy. See [Decision legs](#decision-legs-jev). |
| `CAPTAIN_AA_API_KEY` | unset | Artificial Analysis key (or `aa.env` in `~/.config/captain/`). With it the brain refreshes the perf ranking daily from the live feed (cached in `~/.captaincode/perf.json`); without it the ranking is the snapshot compiled into the binary. The ranking orders the sidebar's Frontier and Models sections, the `/frontier` failover chain, and the ⇡ "newer model in this family" flags. |
| `CAPTAIN_ROUTE_TIMEOUT_MS` | `12000` (set `30000` with a frontier director) | How long the terminal waits for a routing decision. A director that plans slower than this is bypassed entirely. |

### Decision legs: jev

A **decision leg** answers typed questions - a choice among options, a score against a rubric, a yes/no with its probability - and never generates text or runs a tool. The first is **jev**, TypeSafe's System One model (`transport: system-one`, provider `typesafe`, `POST api.typesafe.ai/v1/systemone`). It is in the registry like any leg (`captain legs` marks it `decision`; `captain doctor` reports it ready by its key; `captain legs caps` declares `tools: no`) and nowhere a task is dispatched: it is not a worker rung, not a model in the TUI's picker, not a `/jev` forcing command, not a workflow stage, not a reroute target - `/jev <task>` is refused with the way to ask it instead. What captain asks it is what it is built for: the triage questions (class, domain) that used to cost a ~5s free-leg call, now answered in a few hundred milliseconds with a calibrated confidence the gate can read. Below the bar the free-leg classify decides, exactly as before, so a wrong or absent key changes nothing but speed. Each call is a charge row under the turn (estimated at the registry price - the API reports tokens, not dollars).

```bash
# ~/.config/captain/env
TYPESAFE_API_KEY=…            # keys: console.typesafe.ai/settings/keys
# CAPTAIN_JEV_MODEL=jev-1.13.0  # pin a version; jev-latest is an alias TypeSafe moves

captain jev                                   # probe: models the key reaches, one noul, latency, tokens, the estimate
captain jev classify "fix the typo in README" # the triage questions, their probabilities, and the gate verdict
captain jev ask --state @notes.txt --questions '{"urgent": {"type": "noul", "instructions": "Does this ask for something today?"}}'
```

How often it is asked: the free-leg classify (~5s) only refines a heuristic under `CAPTAIN_TRIAGE_CONF` (0.6), where jev goes first and a miss falls through to it. jev alone (~0.8s, ~$0.00002) is also asked while the heuristic is under `CAPTAIN_TRIAGE_JEV_BELOW` (0.9); in that band a miss keeps the heuristic. A task the heuristics are certain about (a typo fix, 0.95) routes in 0ms with no call. `captain why` shows the step: `hi=0[] lo=2[] dom=general[] → jev: …`.

A brain started before the key was set does not see it: stop it and start `captain brain` again, and the startup log names jev as tier 1.

### Shadow decisions: reading jev's calibration

Triage is the one decision jev is trusted with today. Three more are closed-set questions it could answer - how the turn should be staffed (one worker or a fan-out), which leg should take it, and which running worker a mid-turn `/btw` note concerns - and a frontier director answers all three in prose, on the critical path, seconds per turn. Before any of them is handed over, captain has to know how often jev would have agreed, at what confidence, and how those turns ended. So the questions are asked **in the shadow**: answered beside the real decision, recorded next to it, and never acted on.

jev is not a chat model, so nothing sends it a prompt. Each point is translated into the API's own shape by the code, deterministically: the leg question's options are the director's menu described from the registry (each leg's briefing line, its quality prior for this domain, its price), and the note question's options are the running workers' briefs. The translation is **not** done by asking the director to write the question - that would put a frontier call back on the path this work exists to shorten, make the same turn produce a different question twice (a calibration cannot be read off a moving question), and let the task's own text reach the option set. The facts the question carries are the registry's, which is the director's menu already. It costs nothing in latency where it matters - the shape and leg questions ride along inside the tier-1 triage request, one call; beside the director the call runs under a plan that takes seconds. Measured over six calls (18 Sep 2026, `jev-1.13.0`): the triage pair alone is 744-802ms, with the shadow questions 742-873ms - flat, inside the noise. What it does cost is tokens: 622 to 2169 per call, $0.000020 to $0.000068, because the leg question carries a line describing each open leg.

What leaves the machine is what already left it for triage: the redacted, truncated task head, or the note. Worker output, gate logs and memory do not.

```bash
captain jev shadow                  # agreement per decision point, by confidence, labelled with outcomes
captain jev shadow --point leg      # one point
captain jev shadow --target 0.95 --min 40   # a stricter bar, over more comparisons
captain why                         # one turn's: what jev said beside what captain did
```

The report reads each point as a cumulative calibration - "if the bar were here, jev agreed this often" - joined to the outcome ledger (`captain outcome`), so agreement on turns that were **accepted** can be told from agreement on turns that were rejected. `bar:` is the lowest confidence floor that reaches the target over enough comparisons, or says the sample is still too small. Four rows are counted apart from the agreement rate, so it stays honest: calls that **failed**, points jev's own answer **decided** (tier-1 triage agrees with itself), points the turn never **reached**, and picks that were **not on the menu** jev was given - forcing `/grok` names a leg that serves tasks but is no rung, and jev was never allowed to answer it.

Only when a point's bar holds up should it be gated for real - and then per point, not one bar for all of them.

### Pools: `/oss` and `/deterministic`

`/oss <task>` runs the task on open-weight models only (registry `open`, else the model family: llama, qwen, glm, deepseek, kimi, minimax, gpt-oss, mistral, gemma…). `/deterministic <task>` runs it on a leg whose **serving tuple** is green in the [Agentic Determinism Index](https://lemma-ventures.github.io/agentic-determinism-index/) right now - the latest scored appearance was byte-exact - and pins the request to that tuple through the egress proxy (OpenRouter `provider.order`, `allow_fallbacks: false`, `temperature: 0`), so the run uses the stack that was measured, not whatever OpenRouter routes to this minute. Both are modifiers like `/quality`: they count wherever they stand in the turn and compose with `/repeat`, `/parallel`, `/team`, `/frontier` and a leg prefix in either order (`/oss /repeat 5 <task>` is `/repeat 5 /oss <task>`; a named leg outside the pool still wins). Nothing in the pool → the turn runs without it and the feed says why, naming the green tuples no leg serves yet. `captain adi` prints every leg's standing and `captain legs add` registers a green tuple; the compiled `gpt-oss` leg (gpt-oss-120b via OpenRouter, Cerebras) is the one green today.

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_ADI_URL` | the published `leaderboard.json` | Where the feed is read from (a URL or a local path, e.g. a checkout's `website/leaderboard.json`). Cached in `~/.captaincode/adi.json`, refreshed every 6h. `CAPTAIN_ADI=0` turns the feed off (`/deterministic` then finds no green leg). |

### Stopping a running worker without losing its work: `/interrupt`

`/interrupt [reason]` ends the run that is in flight and keeps what it did. A worker that takes notes mid-run (**claude**, every **opencode-served leg**) is asked to stop, write a handoff - what it implemented file by file, what is left in order, the exact way to resume - and end its turn on that note; the note reaches the TUI as the turn's answer, the run history, the journal (`kind: interrupt`) and memory as a promoted note. A worker with no mid-run channel (**codex-cli**, **cursor-agent**) is stopped at once and what it streamed is kept as a partial, marked so. A worker asked to hand off that is still running after `CAPTAIN_INTERRUPT_GRACE` (default `3m`) is stopped the same way. The plugin sends the word the moment it is typed; nothing is queued. A turn the brain is still preparing (compaction, routing) is withdrawn: its worker never starts. An interrupted run is never rerouted.

A prompt that is only **queued in the TUI** (typed while a turn runs, shown as `QUEUED`) is opencode's to edit or drop, not captain's: open the command palette (`ctrl+p`) and pick **Manage queued prompts** (`ctrl+x q`); in the list, `ctrl+e` puts a prompt back in the editor to change it, `ctrl+d` removes it. The right-click menu on a message (copy, revert, fork) is for messages already in the transcript.

### Steering a running worker: `/btw`

`/btw <note>` hands a note to the worker that is **already running** - a constraint you forgot, a mistake you spotted in its progress feed - so it complements or amends the prompt it started with instead of arriving as the next turn, after the wrong thing is done. Two transports take a note mid-run: **claude** (`claude -p` reads it on stdin and folds it into the running turn at its next tool boundary) and every **opencode-served leg** (glm, grok, kimi, gemini, codex, free… the note is merged into the busy session's turn). **codex-cli** and **cursor-agent** have no such channel: the note runs as the turn that follows, which is what the TUI would have done anyway. The plugin sends the note the moment it is typed (`POST /v1/btw?cwd=`) and, once the brain has taken it, refuses the message so the TUI does not queue a copy behind the running turn - the toast is the receipt. A note typed while the turn is still being prepared (compaction, routing) is held and is the worker's first input. Only a note nobody could take goes through as an ordinary turn. The turn shows the note where it landed: `> /btw → grok: …` in the streamed answer, with the reason when several workers were running - a team, a workflow - and the note went to the one it concerns: the worker you addressed (`/btw @grok …`, `/btw claude: …`), else the director's pick from the workers' briefs (one short judge call), else all of them. The progress feed shows `btw from the user taken: …` when the worker has it. There is nothing to configure.

### Naming the thread: `/rename`

`/rename [title]` sets the session's title - what the TUI shows for this thread - instead of the auto-generated name, which is taken from the first message and can be meaningless when that message is an error. With no argument the name is built from the repository the TUI is open in and the thread's recent prompts: the plugin reads the session's last few user turns, sets `<repo>: <newest request>` immediately, then asks the brain's cheapest leg to fold repo and context into a tidier title and replaces it when that lands. `/rename <title>` sets exactly the title you type. The plugin writes it through opencode's session API and refuses the turn, so nothing is queued; the toast is the receipt. Registered as a captain command, it overrides opencode's built-in `/rename` dialog.

### Workers and watchdogs

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_WORKER_TIMEOUT` | `15m` | Base cap per worker run (frontier legs get 2×). Past it a run is cut only when **quiet**: silent longer than the idle window with no tool running, or longer than the tool window with one. A run that keeps producing output or has a tool in flight is never cut for taking long. |
| `CAPTAIN_WORKER_CEILING` | none | Optional absolute wall-clock stop, whatever the run is doing. Unset by default. |
| `CAPTAIN_WORKER_STALL_TIMEOUT` | `4m` | No session activity for this long with no tool running ⇒ stalled, subject to a REST cross-check. |
| `CAPTAIN_WORKER_TOOL_TIMEOUT` | `12m` | How long a run may be silent **while a tool is running** (a build, a test suite, a CI watch) before it counts as quiet. opencode bounds a bash tool at 10m, so longer is a wedged tool, not a slow one. |
| `CAPTAIN_WORKER_IDLE_TIMEOUT` | `90s` | Idle gap inside an opencode worker run with no tool in flight. |
| `CAPTAIN_WORKER_CLI_IDLE_TIMEOUT` | `30m` | Same for the CLI legs (claude -p, codex exec, cursor-agent), whose reasoning is invisible: minutes of silence between two tool calls is thinking, not a stall. |
| `CAPTAIN_WORKER_CLI_TOOL_TIMEOUT` | `2h` | How long a CLI leg may be silent while one of its tools runs (a benchmark, a long test): the CLI bounds its own tools, opencode's 10m bash cap does not apply. |
| `CAPTAIN_WORKER_FIRST_EVENT_TIMEOUT` | `90s` | Nothing at all from a fresh worker ⇒ treat the leg as down. |
| `CAPTAIN_WORKER_LOGS` | on (`0` disables) | Write `~/.captaincode/runs/<id>-<leg>.log`. |
| `CAPTAIN_CWD` | process cwd | The terminal's workspace. The TUI names it on every brain call (`X-Captain-Cwd`). The brain itself only uses it for CLI commands (`captain euclid …`) and as the fallback for a caller that sent none. |
| `CAPTAIN_WORKSPACE_ROOT` | `~/Gits` | Where linked repositories are looked for. |
| `CAPTAIN_CLAUDE_PERMISSIONS` | `--dangerously-skip-permissions` | Flags passed to `claude -p`. See [SECURITY](../SECURITY.md) before changing. Workers also get `--add-dir` for the workspace root's siblings and the temp dirs, and `captain init` removes `permissions.blockReadsOutsideWorkingDirectories` from `~/.claude/settings.json` - under it a worker cannot run any shell command with a `$expansion`, redirect or computed path. |
| `CAPTAIN_CURSOR_PERMISSIONS` | `--trust --force` | Flags passed to `cursor-agent -p` (`default` = `--trust` only; a headless approval prompt is a hang). |

### Context

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_COMPACT` | on (`0` disables) | Prune-then-summarise long sessions. |
| `CAPTAIN_COMPACT_LEG` | a fast cheap leg | Which leg summarises. Benchmark candidates on *distinct* prose - repetitive filler makes a slow model look fast. |
| `CAPTAIN_REPLAY_BUDGETS` | on (`0` disables) | Per-leg prompt budgets derived from context windows. |
| `CAPTAIN_WRAPPER_MAX_PROMPT` | derived | Hard character ceiling on a worker prompt. |

### Models per leg

Every leg takes `<PREFIX>_PROVIDER` and `<PREFIX>_MODEL`, where the prefix is
`CAPTAIN_<ID>` uppercased with `-` as `_` (`CAPTAIN_DS_FLASH_MODEL`). This is how
you survive a provider retiring a model: repoint the leg, keep its scorecard.

Frontier legs additionally take `CAPTAIN_FRONTIER_MODEL` (default
`claude-fable-5`), `CAPTAIN_CODEX_CLI_MODEL` (default `gpt-6-astra`) and
`CAPTAIN_CODEX_CLI_SANDBOX`.

### Effort

How hard a worker thinks is decided per request, not per leg: `/frontier`
→ max, `/quality` → high, `/speed` and `/save` → low, a bare prompt → the
task's difficulty rating (high → high, medium → medium, trivial → low). The
model is picked separately, by the balanced ranking. Every transport with a
knob gets it: claude -p `--effort`, codex exec `model_reasoning_effort`
(max is codex's xhigh), an opencode worker's message `variant` fitted to
what the model offers (glm: low/high/max; grok-build, kimi: none). The
run's effort shows on its Last Runs line in the sidebar.

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_FRONTIER_EFFORT` | unset | Pin claude's `/frontier` effort (`xhigh` to get the pre-2026-09-13 second-to-best) |
| `CAPTAIN_CODEX_CLI_EFFORT` | unset | Pin codex exec's effort whatever the request |
| `CAPTAIN_EFFORT_VARIANTS` | `1` | `0` sends opencode workers no variant (the model's default reasoning) |

### Memory (Euclid)

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_EUCLID` | on (`0` disables) | Read repository brains into worker prompts; journal each run. Nothing happens without a brain. |
| `CAPTAIN_EUCLID_ORIENTATION_CHARS` | `2500` | Size of the `<euclid>` block prepended to a worker prompt. |
| `CAPTAIN_EUCLID_DISTILL_LEG` | `gemini`→`deepseek`→`free` | Which leg distils the journal into registers. |
| `CAPTAIN_EUCLID_AUTODISTILL` | `5` | Journaled runs that trigger an automatic distillation into the write brain (`0` = never; at most one every 30 minutes). |
| `EUCLID_HANDLE` | git user | Your subtree name under `<repo>/.euclid/developers/`. |
| `EUCLID_HOME` | `~/.euclid` | Your personal brain. |

### Budgets, escalation and task API

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_MAX_ATTEMPTS` | unset (`0` = unlimited) | Shared attempt cap across director, workers, reviews and retries for a task. Enforced by the budget controller. |
| `CAPTAIN_MAX_COST` | unset | USD cost cap for a task. **Tracked** in admission mode; with `CAPTAIN_STRICT=1`, legs that cannot report per-turn cost are rejected before dispatch. |
| `CAPTAIN_STRICT` | off | When set with a cost cap, only cost-reporting adapters may run (see `captain budget`). |
| `CAPTAIN_MAX_REPAIRS` | `1` | Same-leg objective-failure repairs before escalation. `0` = no repairs. |
| `CAPTAIN_MAX_ESCALATIONS` | `1` | Moves to a stronger leg after repair exhausts. `0` = stop after repairs. |
| `CAPTAIN_TASK_TOKEN` | unset | Bearer token for `/v1/task/*`. Unset ⇒ loopback-only. Set ⇒ every task HTTP request needs `Authorization: Bearer …`. |

### Miscellaneous

`CAPTAIN_BRAIN_URL` (default `http://127.0.0.1:14097`), `CAPTAIN_SRC` (source
checkout for `captain upgrade`), `CAPTAIN_REPEAT_MAX` (default `100`),
`CAPTAIN_AA_API_KEY` (only for `captain priors sync`).

See also the [CLI cheat sheet](CLI.md).

## Precedence

Compiled registry defaults → `~/.captaincode/legs.json` overlay → environment
variables. Nothing routes off a file you have not seen: a fresh install with no
overlay and no priors file still routes, on the compiled defaults.
