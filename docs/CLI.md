# CLI cheat sheet

Commands registered by the Go binary (`captain` / `captaincode`). This page is
the shell CLI; the TUI's `/…` control words are in [TUI.md](TUI.md).

## Run a task

| Command | What it does |
|---|---|
| `captain "<task>"` | Route one task: triage, pick a leg, run it, assess it |
| `captain with <leg> "<task>"` | Force one leg for this task |
| `captain --prefer quality\|speed\|save "<task>"` | A preference the director picks inside |
| `captain --until "<cmd>" [--max-iters 5] "<task>"` | Loop until the shell command exits 0 |
| `captain --no-manager "<task>"` | Skip the director; pure heuristic ladder |
| `captain` | Interactive REPL in a terminal (`/new`, `/why`, `/quota`, `/stats`, `/calibrate`, `/roles`, `/ui`, `/quit`) |

The TUI is the everyday surface; its `/…` words are in [TUI.md](TUI.md).

## Setup & health

| Command | What it does |
|---|---|
| `captain doctor` | Build identity, toolchain pins, capability probes, leg readiness |
| `captain doctor --rehearse` | Clean-machine install/upgrade/rollback rehearsal (M1.1) |
| `captain init [--check]` | Write opencode worker permissions, the `captain` provider, the slash commands and the `~/.config/captain/env` scaffold; `--check` reports what would change (exit 3) without writing |
| `captain upgrade [--check] [--no-restart]` | Update claude, cursor-agent, opencode and captain with each tool's own updater; restart services only when nothing is running |
| `captain upgrade --models [--apply]` | Model pins the ranking flagged as outdated, against today's feed; `--apply` writes them to the registry overlay |
| `captain legs` / `legs list` | Active legs: transport, model, price, prior, context |
| `captain legs caps` | Declared capabilities per leg |
| `captain legs add <id> <provider/model> [--prior N --note … --display … --aa slug --vision --ctx N --price-in X --price-out Y --transport t --frontier --subscription]` | Add or update a leg in `~/.captaincode/legs.json` (OpenRouter price and context fetched when omitted); `captain init` then adds its TUI entries |
| `captain legs remove <id>` | Drop an overlay leg, or disable a compiled one |
| `captain legs reopen <id>` | Lift a leg's cooldown now (credits topped up, an outage over) - no restart |
| `captain priors` / `priors sync [--apply]` | Active quality priors and their source; propose refreshed priors from the Artificial Analysis coding index, `--apply` writes `priors.json` |
| `captain adi` / `adi refresh` | ADI standing per leg (green, red, not measured) and green tuples no leg serves; `refresh` fetches the feed now |
| `captain state [--last] [<folder>]` | The per-folder TUI state directory (history, model, preferences); `--last` prints the newest session id |

## Routing & decisions

| Command | What it does |
|---|---|
| `captain director` / `director <leg>` / `director reset` | Show or switch the director ladder |
| `captain why` | Last routing decision: candidates, exclusions, policy, charges |
| `captain quota` | Observed/inferred quota per leg |
| `captain budget` | Per-task budgets (attempts, cost, mode, stopping reason) |
| `captain stats` | Aggregate ledger stats |
| `captain jev` / `jev classify <task>` / `jev ask --state … --questions …` | The jev decision leg (TypeSafe System One): probe, the triage questions with their probabilities, any typed question. `--open` asks the open backend instead |
| `captain jev shadow [--point p] [--backend name] [--target 0.9] [--min 20]` | The shadow record as a calibration: how often jev agreed with captain per decision point, by confidence, labelled with the tasks' outcomes, and the bar each point could be gated at |
| `captain jev conform [--open] [--json] [--for caps]` | Does this backend answer captain's questions? A fixed suite whose answers are not in doubt, reported per capability (triage, route, keep, gate, supervise). Exits 1 on any unusable capability |
| `captain gate --status` / `--check "<cmd>"` / `--report [--target 0.9] [--min 20]` | The action gate at the tool boundary: what it would do right now, one command screened by hand, and the screenings read as a calibration |
| `captain gate --hook` / `--tool <name> [--cwd dir]` | The hook bodies: Claude Code `PreToolUse` (hook JSON in, hook JSON out) and the opencode plugin's call (tool arguments as JSON on stdin; exit 3 refuses) |

## Watch & control runs

Run controls reach the brain over HTTP and work while a TUI turn streams.
`runs` and `show` read local history; `ui` attaches to the worker server.

| Command | What it does |
|---|---|
| `captain status` / `captain watch` | The live (or last) workflow checklist once, or redrawn until it ends |
| `captain runs [-n 20] [--leg l] [--kind solo\|team\|workflow\|frontier] [--grep text] [--failed]` | Run history (alias `history`) |
| `captain show [<id>\|last]` | One run with its full output |
| `captain kill [--loops]` | Stop the worker run in flight; `--loops` also ends `/repeat` threads |
| `captain stop [--finish\|--abort] [--all]` | End this folder's `/repeat` loops after the current round (`--abort` drops the round, `--all` every folder) |
| `captain send [--cwd <dir>] [--leg <leg>] [--from <who>] "<prompt>"` | Hand a prompt to the TUI open in a folder: its sidebar submits it into the session as if typed (a watcher, a cron, a script can drive a TUI) |
| `captain ui` | Open opencode's own TUI on the worker sessions (`opencode attach`) |

## Skills

| Command | What it does |
|---|---|
| `captain skills` / `skills list` | The vetted shelf: what is synced, from which commit, under which license |
| `captain skills sync [--source r] [--commit sha] [--only a,b] [--allow-scripts a,b] [--dry-run]` | Fetch a first-party catalog (`anthropics/skills`, `openai/plugins`, `cloudflare/security-audit-skill`) at a named commit, vet it, hash it into `skills.lock` |
| `captain skills select "<task>"` / `stage [--dir d] "<task>"` / `unstage [--dir d]` | What would be stocked for a task and why; stage or clear that shelf by hand |
| `captain skills verify` | Recompute every locked hash against disk (exits 3 on drift) |
| `captain skills report [--json] [--skill <name>]` | Most stocked, most used, best graded - and the director's notes on one skill |

## Services & plumbing

The launcher runs these; you rarely call them by hand.

| Command | What it does |
|---|---|
| `captain brain [--addr host:port]` | The brain: routing policy, the OpenAI-compatible endpoint the TUI talks to, the task API |
| `captain proxy` | The egress redaction proxy on its own (the brain runs one itself) |
| `captain redact [--restore \| --check <path> \| --hook \| --stats] [--source name]` | The redaction engine for the opencode plugin and Claude Code's `PreToolUse` hook: stdin masked (or restored) to stdout; `--check` exits 3 on a file a worker must not read |

### What every worker prompt carries

Solo, team, workflow-stage and `/frontier` workers get the same standing lines
after the task (a session title gets none of them):

- **A callback, not a promise.** A worker is one turn, so anything it leaves
  running (a watcher, a test gate, a long benchmark) cannot report by itself.
  The contract tells it to make that job's last step
  `captain send --cwd <dir> --from <leg> "<what landed>"` rather than promise
  to report later. `CAPTAIN_WORKER_CALLBACK=0` drops the line.
- **Security first.** Every change is written as one a security reviewer will
  read. Before adding or upgrading a library the worker prefers the standard
  library or a dependency the project already has; otherwise it confirms the
  exact package name and publisher on the official registry (typosquatted and
  hallucinated package names are live attacks), picks a maintained release,
  pins it in the lockfile and reads any install script before it runs. It
  names every dependency it added or changed in its final message. With the
  `security-audit` skill synced, the line also points at it - guidance mode;
  a full audit only when you ask. `CAPTAIN_WORKER_SECURITY=0` drops the line.

## Evaluation & release (M1)

| Command | What it does |
|---|---|
| `captain eval validate <suite.json>` | Suite schema; fixtures are reproducible |
| `captain eval verify <suite.json>` | Non-vacuous fixture gate against pinned revisions |
| `captain eval plan <suite.json>` | The executions the suite would run |
| `captain eval run <suite.json> [--out <result.json>] [--only ids] [--json]` | Execute arms serially (paid) |
| `captain eval review <result.json> [--accept\|--reject <key> --by <name> --note … --amend]` | List or record blinded verdicts |
| `captain eval report <result.json>` | Acceptance / cost / latency table |
| `captain release manifest [--json]` | Every version an evidence run was produced against |
| `captain release compat` | The compatibility table |
| `captain release check` | Extraction checks on the release contract |
| `captain release report [--json]` | Full dated evidence report (M1.5) |

## Tasks, MCP & hosts (M4)

| Command | What it does |
|---|---|
| `captain task plan "<prompt>"` / `submit "<prompt>"` | Plan a task or submit it for execution over HTTP |
| `captain task inspect <task-id>` / `events <task-id>` / `artifacts <task-id>` | Read task state, events or patch manifests |
| `captain task cancel <task-id>` / `resume <task-id> <attempt-id>` | Cancel a task or resume an interrupted attempt |
| `captain task fixture` | Run task API contract checks against a live brain (submits a test task) |
| `captain task mcp` | Stdio MCP server for the same seven operations |
| `captain host pi …` / `host jido …` | Host helpers against a live brain |
| `captain host cert [--host pi\|jido\|editor]` | Certification checklist |
| `captain cancel` / `lifecycle` / `resume` | Cancellation tree and recovery |
| `captain handoff [id] [--format json]` | Structured handoff brief |

## Outcomes & policy (M5)

| Command | What it does |
|---|---|
| `captain outcomes [<task-id>]` | Acceptance evidence, checks, corrections |
| `captain outcomes --settle` | Settle every outcome its evidence decides |
| `captain outcome <task-id> review <accept\|reject> --reviewer <name> [--note …] [--amend]` | Record a human verdict |
| `captain outcome <task-id> correction <minutes> [--reason …]` / `regression <reason> [--source …]` | Record the fix-up time, or a regression found later |
| `captain calibrate [--domain code\|editorial\|research\|general] [--json] [<leg>…]` | Quality/reliability estimates with uncertainty |
| `captain roles` | Role economics roll-up |
| `captain policy [--json]` / `policy show <id>` | Policy snapshots and one in full |
| `captain policy snapshot [name]` / `activate <id>` / `accept <id>` | Capture the current policy; make a snapshot active; accept it (what `rollback` returns to) |
| `captain policy canary start <candidate-id> [rate] [max-tasks]` / `canary stop` | Route a share of tasks through a candidate |
| `captain policy promote <candidate-id>` / `rollback` | Compare a candidate with the active policy per cohort (prints the `accept` command when nothing regressed); restore the last accepted snapshot |
| `captain policy distill [<out.jsonl>]` | Export the labelled routing history (one row per task) a small router would train on |
| `captain refine [--apply \| --rollback]` | Director-proposed overlay updates (leg notes, prior nudges) from the ledger; nothing written without `--apply` |

## Euclid (opt-in)

| Command | What it does |
|---|---|
| `captain euclid init [--repo]` | Scaffold `~/.euclid` or `<repo>/.euclid` |
| `captain euclid mcp` | Memory MCP (search/ask/recall/registers/note) |
| `captain euclid status` | Which brains are read/written, journal counts |
| `captain euclid check` | The launch check: main brain and local brain, filesystem only |
| `captain euclid ensure` | Launch step: every brain the folder reads exists and is freshly indexed |
| `captain euclid distill [--apply]` / `bootstrap [--apply]` | Propose register edits from the journal (or bootstrap a new brain from the repo's docs); `--apply` writes your write brain, never the shared one |
| `captain euclid link <repo\|path\|url> [--related]` / `links [--apply]` | Declare a cross-repo link; list resolved links and proposals |
| `captain euclid corpus [--apply]` / `reindex` / `probes [--force]` | Corpus roots in `euclid.yml`; rebuild the index; regenerate the benchmark gold sets |
| `captain euclid share [--apply]` | Make the repo brain shareable: developer brains local, `notes/`, the fold workflow |
| `captain euclid fold [--dry-run]` | Fold promoted notes into the shared brain (what CI runs on main) |

Euclid is not the default install. Nothing under `.euclid/` is committed with Captain itself; a repository that commits its brain shares it through `captain euclid share` (see [EUCLID.md](EUCLID.md), *Sharing the brain*).

## Related docs

- [TUI.md](TUI.md) — the `/…` control words in a session
- [CONFIGURATION.md](CONFIGURATION.md) — env vars and `state.json` ledger
- [TASK_MCP_CONTRACT.md](TASK_MCP_CONTRACT.md) — task MCP fixtures
- [TASK_API_COMPATIBILITY.md](TASK_API_COMPATIBILITY.md) — HTTP task API
- [INSTALL.md](INSTALL.md) — toolchain pins and doctor sections
- [ROADMAP.md](ROADMAP.md) — M1–M5 status
