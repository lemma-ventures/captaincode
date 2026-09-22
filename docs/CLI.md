# CLI cheat sheet

Commands registered by the Go binary (`captain` / `captaincode`). The interactive
TUI has a separate `/…` control surface; this page is the shell CLI.

## Everyday

| Command | What it does |
|---|---|
| `captain doctor` | Build identity, toolchain pins, capability probes, leg readiness |
| `captain doctor --rehearse` | Clean-machine install/upgrade/rollback rehearsal (M1.1) |
| `captain init` | Scaffold config; `init --check` validates without writing |
| `captain legs` / `legs caps` | Registry and declared capabilities |
| `captain jev` / `jev classify <task>` / `jev ask --state … --questions …` | The jev decision leg (TypeSafe System One): probe, the triage questions with their probabilities, any typed question |
| `captain jev shadow [--point p] [--target 0.9] [--min 20]` | The shadow record as a calibration: how often jev agreed with captain per decision point, by confidence, labelled with the tasks' outcomes, and the bar each point could be gated at |
| `captain jev conform [--json]` | Does this backend answer captain's questions? A fixed suite whose answers are not in doubt, reported per capability (triage, route, keep, gate, supervise). Exits 1 on any unusable capability |
| `captain gate --status` / `--check "<cmd>"` / `--report` | The action gate at the tool boundary: what it would do right now, one command screened by hand, and the screenings read as a calibration |
| `captain gate --hook` / `--tool <name>` | The hook bodies: Claude Code `PreToolUse` (hook JSON in, hook JSON out) and the opencode plugin's call (tool arguments as JSON on stdin; exit 3 refuses) |
| `captain director` / `director <leg>` / `director reset` | Show or switch the director ladder |
| `captain why` | Last routing decision: candidates, exclusions, policy, charges |
| `captain quota` | Observed/inferred quota per leg |
| `captain budget` | Per-task budgets (attempts, cost, mode, stopping reason) |
| `captain stats` | Aggregate ledger stats |
| `captain runs` / `show <id>` / `watch` | History and live follow |
| `captain skills` | The vetted shelf: what is synced, from which commit, under which license |
| `captain skills sync [--source r] [--commit sha] [--only a,b] [--allow-scripts a,b] [--dry-run]` | Fetch a first-party catalog at a named commit, vet it, hash it into `skills.lock` |
| `captain skills select "<task>"` / `stage [--dir d] "<task>"` / `unstage [--dir d]` | What would be stocked for a task and why; stage or clear that shelf by hand |
| `captain skills verify` | Recompute every locked hash against disk (exits 3 on drift) |
| `captain skills report [--json] [--skill <name>]` | Most stocked, most used, best graded - and the director's notes on one skill |

## Evaluation & release (M1)

| Command | What it does |
|---|---|
| `captain eval validate <suite.json>` | Suite schema |
| `captain eval verify <suite.json>` | Non-vacuous fixture gate against pinned revisions |
| `captain eval run <suite.json> --out <result.json>` | Execute arms (paid) |
| `captain eval review <result.json>` | Blinded pending verdicts |
| `captain eval report <result.json>` | Acceptance / cost / latency table |
| `captain release report` | M1.5 package checklist |

## Tasks, MCP & hosts (M4)

| Command | What it does |
|---|---|
| `captain task …` | Versioned task API over HTTP (plan/submit/inspect/…) |
| `captain task mcp` | Stdio MCP server for the same seven operations |
| `captain host pi …` / `host jido …` | Host helpers against a live brain |
| `captain host cert [--host pi\|jido\|editor]` | Certification checklist |
| `captain cancel` / `lifecycle` / `resume` | Cancellation tree and recovery |
| `captain handoff [id] [--format json]` | Structured handoff brief |

## Outcomes & policy (M5)

| Command | What it does |
|---|---|
| `captain outcomes` / `outcome <task>` | Acceptance evidence, checks, corrections |
| `captain calibrate` | Quality/reliability estimates with uncertainty |
| `captain roles` | Role economics roll-up |
| `captain policy …` | Snapshots, shadow, canary, promote/rollback |

## Euclid (opt-in)

| Command | What it does |
|---|---|
| `captain euclid init [--repo]` | Scaffold `~/.euclid` or `<repo>/.euclid` |
| `captain euclid mcp` | Memory MCP (search/ask/recall/registers/note) |
| `captain euclid status` | Which brains are read/written |
| `captain legs reopen <id>` | Lift a leg's cooldown now (credits topped up, an outage over) - no restart |
| `captain send [--cwd <dir>] [--leg <leg>] [--from <who>] "<prompt>"` | Hand a prompt to the TUI open in a folder: its sidebar submits it into the session as if typed (a watcher, a cron, a script can drive a TUI) |

Every worker prompt names this command: a worker is one turn, so anything it
leaves running (a watcher, a test gate, a long benchmark) cannot report by
itself. The contract tells it to make that job's last step
`captain send --cwd <dir> --from <leg> "<what landed>"` rather than promise to
report later. `CAPTAIN_WORKER_CALLBACK=0` drops the line.
| `captain euclid share [--apply]` | Make the repo brain shareable: developer brains local, `notes/`, the fold workflow |
| `captain euclid fold [--dry-run]` | Fold promoted notes into the shared brain (what CI runs on main) |

Euclid is not the default install. Nothing under `.euclid/` is committed with Captain itself; a repository that commits its brain shares it through `captain euclid share` (see [EUCLID.md](EUCLID.md), *Sharing the brain*).

## Related docs

- [CONFIGURATION.md](CONFIGURATION.md) — env vars and `state.json` ledger
- [TASK_MCP_CONTRACT.md](TASK_MCP_CONTRACT.md) — task MCP fixtures
- [TASK_API_COMPATIBILITY.md](TASK_API_COMPATIBILITY.md) — HTTP task API
- [INSTALL.md](INSTALL.md) — toolchain pins and doctor sections
- [ROADMAP.md](ROADMAP.md) — M1–M5 status
