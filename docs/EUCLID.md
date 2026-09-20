# Euclid brains

A model swap is only a handoff if the next model knows what the project is. A
**brain** is a small set of local Markdown registers that carries that
state, and Captain Code reads it into every worker prompt.

Everything here is opt-in: with no brain on disk, nothing changes. `CAPTAIN_EUCLID=0`
disables it outright.

## Roadmap ownership

This page describes Captain's integration, including behavior currently implemented in
its Go wrapper. Euclid's own `docs/ROADMAP.md` owns the reusable engine, registers,
retrieval, provenance and portable memory MCP. Its standalone Python server currently
has a smaller, retrieval-only tool surface; generic notes, journals and scoped lifecycle
parity are planned there, not already available through every Euclid client.

Captain keeps worker prompt hooks, runtime event capture, model selection and accounting
for distillation calls. Task checkpoints, execution MCP and acceptance-based routing
belong to [Captain's implementation roadmap](ROADMAP.md). Euclid memory is not the
authoritative execution ledger or a source of permission to perform an action.

## Where brains live

```
~/.euclid/                        your personal brain (never committed)
<repo>/.euclid/                   the repository brain (local; `captain euclid init --repo`)
  SOUL.md VISION.md BRAIN.md WISDOM.md MAP.md
  memory/{MEMORIES,FAILURES,decisions-ledger,open-questions}.md
  journal/activity-<date>.jsonl   what each worker actually did
  euclid.yml                      budgets + links to related repositories
  developers/<handle>/            one subtree per developer (local, gitignored)
  notes/                          promoted notes waiting for the fold (shared, see below)
```

Captain Code's own repository gitignores the whole `.euclid/` tree: Euclid is
not the default install and its engine is not open-source yet, so nothing under
`.euclid/` is published. In a repository that commits its `.euclid/`, the
shared brain travels with the code - see *Sharing the brain* below. Scaffold
with `captain euclid init` / `init --repo`, or let `captain euclid ensure`
create it on first launch.

The **read set** for a session is: your subtree in this repository (writable) →
the shared repository brain (read-only for other writers on this machine) →
`~/.euclid` when the repository has no brain → repositories the task names →
linked repositories → other developers' subtrees, search-only and down-weighted.

**Which repository.** By default the one the terminal is open in. A prompt that
names another - a path (`~/Gits/captaincode/cmd/…`), a known folder name as a
whole word (`captaincode`; a short lowercase one like `arc` only next to a cue:
"the arc repo"), or the parts of a hyphenated name ("the lemma website" is
`lemma-ventures-website`) - is about that repository: the worker runs there and
its brain is the one read and written, so a journal page about captaincode never
lands in DLM's brain. Several named: the worker stays where the terminal is and
the named brains are read alongside (orientation, director memory; the MCP tools
stay on the terminal's own set). The move is announced on the sidebar's Last
Runs line. Known repositories are the git repos one or two levels under
`CAPTAIN_WORKSPACE_ROOT` (default `~/Gits`) plus `~/.euclid/links.yml` repos;
`CAPTAIN_REPO_REFS=0` turns the following off.

## What the worker sees

An `<euclid>` block of roughly 2,500 characters is prepended to every worker
prompt: the current BRAIN thesis, the repository MAP rows, and the WISDOM
theses. It is a cheap orientation, not a retrieval system - workers that need
more call the MCP tools (`euclid_search`, `euclid_read_register`,
`euclid_recent_runs`, `euclid_status`), registered once and following the
project the terminal is open in.

## Always there, always indexed

Every launch through the Captain TUI runs `captain euclid ensure` in the
background: the main brain exists, the folder's repo brain exists (scaffolded
on the first launch in a repository, its VISION/MAP/BRAIN bootstrapped from the
repo's own docs), and both indexes are rebuilt - the catalog that makes the
memory searchable and the dashboard that renders it. Nothing waits for a shell
command. `captain euclid reindex` does the same on demand, the sidebar's
`memory:` links build on click, and the dashboard's own **Regenerate** button
asks the brain (`POST /api/regenerate?root=<brain>`) - the brain answers the
dashboard's local-engine contract for every brain on the machine.
Between launches the index follows the brain: every journal write and every
applied distillation schedules a rebuild of that brain's index a few seconds
later (one per burst - a team turn journaling six workers rebuilds once), so a
dashboard left open shows the turn that just finished after a reload, not the
state at the last launch. `CAPTAIN_EUCLID_AUTOINDEX=0` turns that off.
The entire `.euclid/` tree is gitignored (indexes and dashboard included).

## How captain uses it

- **Workers** get the `<euclid>` orientation block and the memory tools -
  `euclid_search` (the repository's index: catalog + full text + relation
  graph, then the registers), `euclid_ask` (one-pass answer with git
  provenance), `euclid_recall` (decision archaeology over git history),
  `euclid_read_register`, `euclid_recent_runs`, `euclid_status`, and
  `euclid_note` to record a decision, open question, memory or failure in the
  write brain while the work is fresh. The opencode workers get them from
  `opencode.jsonc`; claude -p and codex exec are handed the same server on
  their own flags (`--mcp-config`, `-c mcp_servers.euclid.*`).
- **The director** plans with a memory block: BRAIN thesis, MAP rows, WISDOM,
  the last FAILURES, decisions and open questions - and is told to carry the
  relevant lesson or decision into each brief.
- **Captain itself** journals every run (a jsonl line and a markdown page
  with `Tokens-Spent`, which the dashboard's Tokens tab and the catalog read),
  writes FAILURES for incidents it diagnosed (a run it had to end, a `/repeat`
  round that deferred), distills every 5 runs, and at bootstrap authors the
  benchmark gold sets (`.euclid/probes/*.json`) from the repository's docs -
  every probe verified against the registers and documents before it is
  written - so the dashboard's Performance tab measures something.
- The brain's corpus (`roots`, `code_exts` in `euclid.yml`) is detected from
  the repository at scaffold (`captain euclid corpus --apply` re-detects).

Nothing here gates a run: no engine, no brain, a failed model call - each
degrades to the previous behaviour.

## The loop

1. **Journal.** Every worker run appends one line: task, leg, duration, outcome,
   files touched - parsed from the worker log, with credentials scrubbed
   (`sk-`, `ghp_`, `xox`, `Bearer`, private keys) before anything is written.
2. **Distil.** `captain euclid distill` asks a cheap leg to fold the journal into
   register edits - append or replace-section only, and only into BRAIN, WISDOM,
   MEMORIES, FAILURES and the decisions ledger. It prints a patch; `--apply`
   writes it to your own write-brain. The shared repository brain is written
   by the fold on the default branch only (*Sharing the brain*).
3. **Promote and fold** (*Sharing the brain*): every note is also a file under
   `.euclid/notes/`; merged to main, the fold turns them into the shared
   registers and ledgers.
4. **Link.** `captain euclid links` proposes cross-repository links from actual
   evidence - Go module requirements, package.json dependencies, Cargo path
   dependencies, and journal entries touching absolute paths under another
   brain. `--apply` writes them to `euclid.yml`.

## The launch check

Every start of Captain Code looks at both brains before the first prompt and
prints two lines, one for the main brain and one for the project's local brain:

```
memory  main  ~/.euclid · no runs to distill · distilled 1h ago
memory  local ~/Gits/dlm/.euclid · 4 runs to distill · never distilled · doctrine inherited
```

The same check runs at brain boot (in the brain log), in
`captain doctor`, in `/euclid` and `captain euclid status`, and as a hint on
the sidebar's memory links. It is filesystem only, so it answers with the brain
down and never blocks a launch. It reports, with the command that repairs it:
a missing brain (`captain euclid init`, `captain euclid init --repo`),
registers still holding scaffold placeholders, a local brain not bootstrapped
from the repository's docs (`captain euclid bootstrap --apply`), a local brain
without your developer subtree (runs would land in the main brain), doctrine
not inherited from the main brain, how many runs wait for a distill, and a
dashboard catalog older than the registers.

## Commands

```sh
captain euclid init [--repo]     # create a brain from templates
captain euclid status            # which brains are in the read set, and their sizes
captain euclid check             # the launch check, main brain + local brain (filesystem only)
captain euclid distill [--apply] # journal → register edits
captain euclid link <repo> [--related]
captain euclid links [--apply]
captain euclid mcp               # the MCP server (registered by `captain init`)
```

## What a brain is not

It is not a transcript store, and it is not memory that follows you between
projects. Registers are distilled, budgeted and pruned on purpose: a brain that
grows without bound stops being read. Secrets never belong in one - the scrubber
is a safety net, not a policy.

## Sharing the brain

Everything a worker records, every failure captain diagnoses and every poor
grade the director hands down lands in **your** brain
(`.euclid/developers/<handle>/`, local). The **shared** repo brain
(`.euclid/BRAIN.md`, `WISDOM.md`, `memory/`) is what a teammate, a fresh clone
and every worker read - and it is built so that git can never conflict on it:

- **Promotion at the source.** When the repository commits its `.euclid/`, the
  same note is also written as **one file of its own** under `.euclid/notes/`
  (`<utc-timestamp>-<handle>-<kind>-<hash>.md`). Two developers, or one
  developer on two branches, never touch the same file, so a pull request
  carries its notes as plain additions and the PR diff is the review.
- **Synthesis after the merge.** `.github/workflows/euclid-fold.yml` runs on
  every push to the default branch that touches `.euclid/notes/`: `captain
  euclid fold` asks a model (OpenRouter, `OPENROUTER_API_KEY`; without a key
  the registers are left as they are) for edits to `BRAIN.md` and `WISDOM.md`,
  appends every note verbatim to its ledger (`memory/decisions-ledger.md`,
  `MEMORIES.md`, `FAILURES.md`, `open-questions.md`), deletes the folded note
  files and commits `chore(euclid): fold … [skip ci]`. Only that job writes
  the registers and ledgers, only on main, so no branch ever carries a change
  to them and a merge cannot conflict. Regenerating on the PR branch instead
  would put two branches' `BRAIN.md` in front of each other - the conflict to
  avoid.
- **Developer brains stay local.** `.euclid/developers/` is gitignored: the
  journal is noise for the team and the personal registers are replace-section
  edits, the one thing two branches of the same developer would fight over.

Set a repository up once with `captain euclid share --apply` (developer brains
untracked, `notes/` created, the workflow written), commit, and add the
repository secret `OPENROUTER_API_KEY`. The runner's `captain` comes from
this repository's `fleet-captain` workflow, which builds main and installs it
as `~/.local/bin/captain` on every runner of the fleet - no token to read
captaincode is needed anywhere else.
`captain euclid fold --dry-run` shows what a fold would do; a fold with no
model reachable still moves the notes into the ledgers, so the queue never
grows.

