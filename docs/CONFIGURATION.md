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
| `CAPTAIN_DIRECTOR_PICK` | on (`0` restores the plan) | On the director path the judge answers a **typed choice** over the value-ranked menu (`{"leg","class"}`) instead of writing a plan and a rationale: a few hundred prompt tokens, one call, no brief. Named legs (`/team`, "have grok and codex …") keep the plan path. |
| `CAPTAIN_DIRECTOR_PICK_TIMEOUT` | `8s` | The pick's cap; past it the menu's first row runs and the decision says so (`director pick failed … → value #1`). |
| `CAPTAIN_LANES` | on (`0` disables) | `/frontier`, `/quality` and `/save` spread their turns across the legs that qualify instead of the top row every time (see *Lanes* below). Off, `/frontier` is claude, `/quality` and `/save` go back to the director, and `/save` runs at low effort. |
| `CAPTAIN_LANE_WINDOW` | `40` | How many of a lane's recent turns the balancer counts. |
| `CAPTAIN_LANE_FLOOR` | `0.85` | A leg shares a lane when its score is at least this fraction of the lane's best (perf index on the frontier lane, blended quality on the others). |
| `CAPTAIN_DIRECTOR_SELF` | on (`0` keeps it off) | A **high**-class task's menu includes the judge's own leg when it takes tasks and is open, so the hardest work can reach the best model even when that model directs. Trivial and medium menus never carry it. |
| `CAPTAIN_ROUTING_POLICY` | `expected` | How the cheap path orders the eligible legs: `value` (quality − cost − latency), `expected` (cost per **successful** task over `(leg, effort)` arms, gated by `CAPTAIN_EXPECTED_MIN_LABELED`), or `bandit` (Thompson sampling over the same arms, gated by `CAPTAIN_BANDIT_MIN_LABELED`; falls back to `expected`, then `value`). Under a gate the arms are still scored and recorded on the decision; the value order runs. See *Learning* below. |
| `CAPTAIN_EXPECTED_MIN_LABELED` | `50` | Settled outcomes the routing history must hold before the expected-cost order is allowed to run. |
| `CAPTAIN_BANDIT_MIN_LABELED` | `200` | Settled outcomes before the bandit may act. |
| `CAPTAIN_SUCCESS_FLOOR` | `0.30,0.45,0.55` | Least estimated P(success) an arm needs per class (trivial, medium, high) to be dispatched to on the expected path. |
| `CAPTAIN_LATENCY_TOL` | `90s,10m,30m` | Per-class latency tolerance; an arm's observed duration past it scales its expected cost up. |
| `CAPTAIN_WINDOW_ALLOWANCE` | `90m` | Worker wall-clock a subscription's 5h window is assumed to hold. The **burn rate** against it is the quota term (0 idle → 1 spent), replacing the rate-limit flag alone. |
| `CAPTAIN_TRIAGE_SHADOW_RATE` | `0.2` | Share of confident tier-0 turns (above `CAPTAIN_TRIAGE_JEV_BELOW`) that ask jev beside the route, act on nothing, and record the comparison - the triage point had never been measured where the heuristic is surest. |

### The helm: choosing the director at runtime

`/captain <word>` in the TUI, `captain director <word>` in a shell, or `POST /v1/director {"director":"<word>"}` — one switch, persisted in `state.json`, so a brain restart keeps it:

| Word | Picks |
|---|---|
| `<leg>` (`claude`, `grok`, `glm`, `kimi`, `gemini`, `codex`, …) | That leg, pinned. Only **judges** qualify: claude (`claude -p`) and opencode-served pins run with tools off; `codex-cli` and `cursor` are agents and are refused. |
| `frontier` | The best-ranked judge by the performance index (today claude). |
| `quality` | The best of **tier 2**: the strongest judge below the top band (today glm; grok directs as grok-4.7 and ranks next). |
| `auto` | The least-used capable judge over `CAPTAIN_DIRECTOR_WINDOW` — usage is wall-clock seconds from the run history plus the ledger's director/review calls; a judge in a rate-limit cooldown is skipped; the standing pick keeps the helm unless another has used under 60% of its time. |
| `director` | Show the helm, the reason, the ladder, and each judge's recent usage. |
| `reset` | Back to `CAPTAIN_DIRECTOR` / the default. |

A mode re-resolves once a minute against the live ranking and usage; `grok` and `grok-max` count as one judge (same model, same credential).

### Routing mix

`/captain more <axis>`, `/captain less <axis>` and `/captain <axis>=N% …` set a standing target for unprefixed turns. Axes: `frontier`, `quality`, `cheap`, `fast`, `oss`, `deterministic` (`speed`/`save`/`open`/`det` are aliases). The command saves the mix in `state.json` and prints the targets.

Until one is set, the mix is `frontier=quality=cheap=fast=oss=20%`, `deterministic=0`, and that default does **not** move the ranking. A bare `more` or `less` moves 5 points. `more oss 20%` means 20% more than the current share (20% becomes 24%); the other axes fund the change so the mix still sums to 100. From 0, a percent is absolute, so `more deterministic 20%` leaves zero. Repeating compounds. `/captain targets` prints the mix; `/captain mix reset` clears it.

The director then prefers legs that close the gap between recent routes and the target (open-weight for oss, ADI-green for deterministic, frontier-class for frontier, and the cheap / fast / quality bands for the rest). An explicit `/quality`, `/speed`, `/save`, `/frontier`, `/oss` or `/deterministic` on a turn still wins. `CAPTAIN_STEER=0` keeps a saved mix from moving the ranking.

### Lanes: /frontier, /quality, /save

A stated preference names a lane, not a leg. Each lane counts where its last `CAPTAIN_LANE_WINDOW` turns went (`lane_runs` in `state.json`, noted when the turn is dispatched) and sends the next to the leg furthest behind an equal share. Only legs scoring within `CAPTAIN_LANE_FLOOR` of the lane's best take part. A lower-ranked leg goes next only once it is a full run behind, so the best leg runs a lane's first turns and wins ties, and over a full window each leg has its share. A leg that keeps failing (two or more provider faults, a third of its runs) sits out unless every leg in the lane does.

| Lane | Legs | Runs at |
|---|---|---|
| `frontier` | The frontier-class legs and claude, ranked by perf index (claude and codex-cli today; grok-max sits below the floor) | claude as the frontier pseudo-leg (pinned strongest model, max thinking); any other leg at max effort (codex-cli: its frontier model at `xhigh`) |
| `quality` | The two best legs by blended quality, the director's own leg included when it is open | high effort |
| `cheap` (`/save`) | Open-weight legs that clear the class's quality bar (`CAPTAIN_VALUE_TAU`) | medium effort: the leg's own model, not its flash sibling |

With no open-weight leg open, `/save` routes over the whole ladder at low effort, as before lanes, and the feed says so. A forced leg (`/glm …`), legs named in the prompt, and a task-API plan that sends nothing are not counted or balanced. `/team`, workflows and a `/frontier` stage inside a workflow keep their own leg choice. The route's rationale names the lane, the leg and the tally (`frontier lane: codex-cli (under-used: 3 of the last 8, share 4.0; claude 5 · codex-cli 3)`).

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
| `CAPTAIN_JEV_SUPERVISE` | on when a decision leg is configured (`0` disables) | Samples each **running** worker on a slow interval and asks the decision leg four nouls about its floor state - is it stuck, is it on the wrong thing, does it need the user, is it departing from the repository's `AGENTS.md`. Recorded beside what the run turned out to be; **acted on by nothing**. `CAPTAIN_SUPERVISE_EVERY` (default `90s`, minimum `10s`) moves the interval. See [Shadow decisions](#shadow-decisions-reading-jevs-calibration). |
| `CAPTAIN_SYSTEMONE_OPEN_URL` | unset | A System One-shaped endpoint that runs **beside** the primary backend rather than replacing it - a local sidecar (`sidecars/laya`). Not `CAPTAIN_SYSTEMONE_URL`, which swaps the decision leg out. Keyless: it is loopback. Unset, nothing about captain changes. See [A sidecar beside jev](#a-sidecar-beside-jev). |
| `CAPTAIN_SYSTEMONE_OPEN_FOR` | unset (shadow only) | Promotes the sidecar, one capability at a time, each with the bar read off **its own** rows: `triage=0.85,route=0.9`. Unset, the sidecar answers beside captain's choices and decides nothing. Only `triage` and `route` are promotable; `gate`, `supervise` and `keep` stay on the primary and an entry naming them is refused out loud at startup. Nothing acts on a System One answer to the route points yet, so promoting `route` names the backend that would answer them and changes no behaviour today. |
| `CAPTAIN_SYSTEMONE_OPEN_CONTEXT` | `512` | What the sidecar reads in one call - instructions, options and state together. laya's default checkpoint holds 512 and its multilingual and typed-decisions checkpoints hold 1,024. A call whose state overruns this goes to the primary instead, because laya truncates an oversized state and answers anyway. |
| `CAPTAIN_SYSTEMONE_OPEN_MODEL` | `laya-mlx` | The model id the sidecar is asked for. It answers with what actually served, which is the figure the shadow row carries. |
| `CAPTAIN_SYSTEMONE_OPEN_TIMEOUT` | `2s` | Bounds one sidecar call. A local answer is ~13ms; two seconds is not a budget, it is the point past which the thing is wedged and triage stops waiting. |
| `CAPTAIN_ACTION_GATE` | `shadow` | The action gate at the tool boundary: the decision leg screens what a worker is about to do, because a headless fleet has nobody to answer an approval prompt. `shadow` records every screening to `~/.captaincode/gate.log` and allows everything; `enforce` refuses an action whose risk reaches `CAPTAIN_GATE_BAR`; `off` screens nothing. An unrecognised value reads as `shadow`, so a typo cannot turn enforcement on. **With no decision leg configured nothing is screened, no call is made and no tool call waits on one.** See [The action gate](#the-action-gate). |
| `CAPTAIN_GATE_BAR` | `0.9` | The risk at which `CAPTAIN_ACTION_GATE=enforce` refuses. Choose it from `captain gate --report`, not from taste. |
| `CAPTAIN_OUTCOME_SETTLE` | `24h` | How long a task whose checks all passed waits before the checks alone accept it. Inside the window it stays `pending`: "the director graded it acceptable three seconds ago" is not acceptance, and a day of silence from the person who asked for the work is the weakest honest evidence that it stood. A **failed** check rejects at once and does not wait. See [Acceptance evidence](#acceptance-evidence-what-settles-an-outcome). |
| `CAPTAIN_EGRESS_ALLOW` | unset (all nine provider origins) | Narrows the egress proxy's upstreams to a comma-separated set of its own route names (`anthropic,openrouter,huggingface`, …). A provider outside the list is refused at the socket. This bounds **captain's own model traffic**; it is not a network boundary for a worker, whose `curl` in a bash tool call never crosses the proxy. |
| `CAPTAIN_FAST_ROUTE` | off (`1` enables) | Skip the director entirely; pure ladder. |
| `CAPTAIN_FALLBACK_LEG` | - | Leg the terminal degrades to when the brain does not answer in time. |
| `CAPTAIN_REDACT` | `on` | Secrets on the wire (see [SECRETS.md](SECRETS.md)): credentials in tool output and request bodies become stable placeholders, the operator's home/name become stand-ins, private-key files are refused. `off`, `secrets` (no identity rewrite), `strict` (`.env` refused too). |
| `CAPTAIN_PROXY_ADDR` | `127.0.0.1:14098` | The egress proxy the workers, claude -p and codex exec call providers through. `CAPTAIN_PROXY_CLAUDE=0` / `CAPTAIN_PROXY_CODEX=0` send that CLI direct. |
| `TYPESAFE_API_KEY` | unset | TypeSafe key (console.typesafe.ai/settings/keys) - or, like `aa.env`, a `jev.env` file holding the console download's `API_KEY=…` line in `~/.config/captain/`, next to the captain source (`CAPTAIN_SRC`) or in the current directory; the variable wins over the file. With it the **jev** decision leg is ready (`captain doctor`), triage asks it first, and `captain jev` answers by hand. `CAPTAIN_JEV_MODEL` repins it (default `jev-latest`, the alias TypeSafe moves; pin `jev-1.13.0` to freeze a tuned confidence bar); `CAPTAIN_SYSTEMONE_URL` points the client at another System One-shaped endpoint - **with or without a key**, so an open backend needs no `TYPESAFE_API_KEY` at all. See [Decision legs](#decision-legs-jev) and [Open backends](#open-decision-leg-backends). |
| `CAPTAIN_AA_API_KEY` | unset | Artificial Analysis key (or an `aa.env` file holding `API_KEY=…` in `~/.config/captain/`, next to the captain source, or in the current directory; the variable wins). With it the brain refreshes the perf ranking from the live feed every six hours (cached in `~/.captaincode/perf.json`), retrying hourly after a failed fetch; without it the ranking is the cache, then the snapshot compiled into the binary, and the brain says so once at startup and checks for a key hourly. Each refresh names what changed in the brain log and in every open TUI's Last Runs: models new to the feed, a leg whose row moved (grok-max read Grok 4.6 until 4.7 was listed), and legs newly flagged ⇡. Nothing retargets on its own: click the ⇡ or run `captain upgrade --models --apply`. `captain upgrade --models` reads today's feed when a key is present, and `captain doctor` reports the ranking's source, age and flagged legs. The ranking orders the sidebar's Frontier and Models sections, the `/frontier` failover chain, and the ⇡ "newer model in this family" flags. |
| `CAPTAIN_PERF_REFRESH` | `6h` | How often the brain re-reads the Artificial Analysis feed (a duration, a minute at least). The key's free tier allows a thousand reads a day. |
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


#### Open decision-leg backends

`CAPTAIN_SYSTEMONE_URL` can be **keyless**. Any endpoint that speaks the
System One shape - a local re-implementation, a model served behind a small
adapter - serves the decision leg with no `TYPESAFE_API_KEY`, which makes the
decision leg optional rather than a subscription. The three configurations
are: no decision leg (triage falls back to the heuristics and the free-leg
classify, and nothing else in captain changes), the vendor with a key, or any
URL with or without one.

What a keyless backend does **not** inherit is the vendor's calibration.
Two things enforce that rather than hope for it:

```sh
captain jev conform          # a fixed suite whose answers are not in doubt
captain jev conform --json   # the same, machine-readable; exits 1 on any unusable capability
```

`conform` reports usability **per capability** - `triage`, `route`, `keep`,
`gate`, `supervise` - because a backend can be fine at one and useless at
another: a lexical scorer handles keep/drop and cannot rank legs. It is a
smoke test, and says so in its own output: a passing suite means a backend is
not broken, never that captain should route on it.

The numbers that decide that come from the shadow, and they are per backend.
Every shadow row stamps which implementation and which versioned model
answered, and `captain jev shadow` **declines to suggest a bar** when a
point's rows came from more than one - `jev-latest` is an alias that moves
under the record, and an open re-implementation is a different model
entirely. Pin the model (`CAPTAIN_JEV_MODEL`) and the backend before reading
a bar off a calibration.

#### A sidecar beside jev

`CAPTAIN_SYSTEMONE_URL` replaces the decision leg. That is the right shape
when there is no key and the wrong one for the case that turned up:
[laya-mlx](https://github.com/mizorewww/laya-mlx) is an open MLX port of the
Laya typed-decision models that answers a short question in **7-14ms on Apple
silicon for nothing**, in the same wire shape, and holds **512 tokens** (1,024
on two of its checkpoints). jev is slower, costs $0.042/M input, holds 32k and
runs wherever there is a key. Neither replaces the other.

So `CAPTAIN_SYSTEMONE_OPEN_URL` names a backend that runs *beside* the primary
one. `sidecars/laya/serve.py` is one, in about 200 lines:

```bash
pip install laya-mlx                          # Apple silicon, macOS 14+, not a captain dependency
python3 sidecars/laya/serve.py --port 8181

# ~/.config/captain/env
CAPTAIN_SYSTEMONE_OPEN_URL=http://127.0.0.1:8181
```

jev stays the default. It keeps the action gate, the supervisor and
compaction, it keeps every call whose state the sidecar cannot hold, and it is
what runs where there is no Apple silicon.

**The sidecar decides nothing until you say what it earned.** Configured, it
answers the triage and routing questions beside every turn captain was already
unsure about - the band under `CAPTAIN_TRIAGE_JEV_BELOW`, where a decision was
going to be made either way - and its answers land on rows of their own,
stamped with its host, compared against what captain actually did, charged to
nobody. Read them alone:

```bash
captain jev conform --open                        # is it broken? (exit code speaks for triage and route)
captain jev shadow --backend 127.0.0.1:8181       # its agreement, by confidence, and its own bar
captain jev shadow                                # names every backend that answered, and declines to pool them
```

When that report prints a bar for a point, promote it with that number:

```bash
CAPTAIN_SYSTEMONE_OPEN_FOR=triage=0.85
```

The number is the promotion. There is no way to promote a backend without
stating the bar you read off its own calibration, jev's bar is never available
to it, and a promoted sidecar that answers under its own bar is dropped
exactly as jev would be - the heuristic stands and nothing else is called.

Two things are enforced rather than advised. `gate`, `supervise` and `keep`
are not promotable: the first two send the largest states captain produces and
being wrong there means a destructive command screened on part of its text,
and the third decides what compaction drops. And because laya's sequence
builder **cuts** an oversized state and answers anyway, the sidecar counts
tokens and refuses with `400 max_tokens_exceeded` rather than answering about
two thirds of a command, while captain checks the size first and routes
oversized calls to the backend that holds them.

### The action gate

Captain runs its workers at full permission, on purpose. `claude
--dangerously-skip-permissions`, `codex
--dangerously-bypass-approvals-and-sandbox`, `cursor-agent --trust --force`,
and an opencode ruleset that allows bash and edits and **denies `question`** -
because an ask wedges a headless worker until its cap. There is no human at
the terminal to answer a prompt, so tightening a CLI's permissions does not
buy prompts. It buys denials, and mostly silent ones.

The action gate is the screening that replaces the prompt: three nouls on the
decision leg, a few hundred milliseconds, asked about the action a worker is
one instant from taking - would this destroy something unrecoverable, does it
reach outside the work it was given, does it send this machine's contents
somewhere else.

```sh
captain gate --status                 # the mode, the bar, whether a decision leg exists
captain gate --check "rm -rf build"   # screen one command by hand
captain gate --report                 # the screenings read as a calibration
```

It runs as a Claude Code `PreToolUse` hook (`captain gate --hook`, installed
beside the redaction hook) and from the opencode plugin's
`tool.execute.before` (`captain gate --tool <name>`), on the **restored**
arguments - the command that will actually run is the one worth screening.
Reads are allowed deterministically, without a call: a gate that priced
`git status` is a gate nobody leaves on.

Three properties worth stating plainly:

- **The default is `shadow`.** Every screening is recorded; every action is
  allowed. `enforce` is opt-in and should follow `captain gate --report`.
- **A failed or slow call allows.** A gate that turns a provider outage into
  a stopped fleet is worse than no gate.
- **With no decision leg there is no gate.** No call, no latency, no
  behaviour change. This is a tested property.

A gate noul is a *prediction* about an action, not a second opinion on a
choice captain made beside it, so nothing captain decided at the same moment
can settle it. What settles one is the **task's own acceptance**: if the user
accepted a task with no correction and no regression, then no action taken
during it destroyed unrecoverable work, wandered out of the assignment, or
shipped the machine's contents off it - so every screening on that task
settles as `false`. `captain gate --report` applies that join at read time
(the log is append-only and written by every process that runs a tool, so
nothing is rewritten) and says how many rows it reached.

The converse does **not** hold, and the report says so: a rejected or
regressed task says the work was bad, not which of its forty actions was the
dangerous one, and spreading the blame over all of them would manufacture
agreement out of nothing. So the sample is one-sided by construction. The bar
it yields is a **false-positive bound** - how often a noul at or above a floor
fired on an action that turned out to be fine - and says nothing about what
the gate misses. That is still the bar `enforce` needs, because the cost of
turning it on too early is a refused worker, not a missed threat; it must
never be read as a detection rate.

Screenings that carry no task identity can never be settled at all. The
transports that spawn one process per worker set `CAPTAIN_TASK_ID`; the
opencode workers share one `opencode serve`, so theirs stay uncompared
however many outcomes settle.

### Skills: stocking the worker's shelf

Agent Skills are procedures written down once - a directory with a `SKILL.md`
whose frontmatter carries a `name` and a `description`. Every runtime captain
drives already reads them and already does progressive disclosure, so captain
writes **nothing** into the prompt about the skills it picks: it decides which
skills EXIST where the worker runs, and the worker's own runtime picks what to
open.

Nothing is synced by default. With an empty catalog there is no directory, no
listing and no call - a run is byte-for-byte what it was before the feature
existed.

**One skill is always on: `security-audit`.** Cloudflare's security guidance
and vulnerability-review skill
([cloudflare/security-audit-skill](https://github.com/cloudflare/security-audit-skill),
MIT, pinned in code to the commit reviewed for it) is stocked for every worker
on every path - solo, team, workflow stage and `/frontier` - whatever the task
says. Security is not a topic a task has to name to need: "add a login form"
and "wire up this SDK" never say the word, and they are exactly the changes a
security reviewer reads. It takes the first place on the shelf and counts
toward both budgets below. It still has to be synced once, and `captain
doctor` says so until it is. Every worker prompt also carries a security-first
line (dependencies checked on the official registry, pinned, named in the
answer - see [CLI](CLI.md#what-every-worker-prompt-carries)); with the skill
synced, that line is the one place captain names a skill in the prompt,
pointing the worker at it in guidance mode - the skill's own default. A full
audit writes a report tree and fans out across many agents, so it runs only
when you ask for one. The two validators it ships beside its `SKILL.md` are
plain Node that reads the files it is pointed at: no network, no install, no
child process.

```bash
captain skills sync --source cloudflare/security-audit-skill   # the always-on security skill, at its reviewed commit
captain skills sync --commit <full-40-char-sha>   # anthropics/skills, vetted and hashed
captain skills                                    # what is on the shelf, and its provenance
captain skills select "merge these PDFs"          # what a task would be handed, and why
captain skills report                             # stocked vs used vs graded
```

| Variable | Default | What it does |
|---|---|---|
| `CAPTAIN_SKILLS_DIR` | `~/.captaincode/skills` | The synced catalog and its `skills.lock` |
| `CAPTAIN_SKILLS_CAP` | `8` | How many skills may be stocked for one task |
| `CAPTAIN_SKILLS_ALWAYS` | `security-audit` | Skills stocked for every task, first, whatever its words - comma-separated, replacing the default. `0`, `off`, `none` or `false` stocks none. A name the catalog does not hold is not stocked (always-on is a place on the shelf, not a fetch) |

The cap is a context budget, not a preference: every stocked skill costs its
name and description in every worker's startup listing, and codex truncates
that listing at roughly 8,000 characters - past which it silently shortens
descriptions and degrades selection for every skill at once. A second budget
(6,000 bytes of name+description) bounds the same thing directly, and the
tighter of the two wins.

Three refusals are worth knowing about:

- **`scripts/` is quarantined.** That directory is arbitrary code; it ships
  only for skills named in `--allow-scripts`. A skill script that does run is
  a tool call like any other, screened by the action gate above - no more and
  no less.
- **Frontmatter outside the spec's set fails the skill**, rather than being
  ignored. That includes `allowed-tools`, which the spec marks experimental.
- **First-party catalogs only.** `anthropics/skills`, `openai/plugins` and
  `cloudflare/security-audit-skill`, each a publisher shipping its own work,
  each at a named commit. Community directories are not read: a 2026 audit
  found prompt injection in 36% of tested community skills, and the standard
  offers no signing to lean on.

Anthropic's four document skills (`docx`, `pdf`, `pptx`, `xlsx`) are
source-available rather than open source. They can be fetched onto your
machine at your request; the lock records that beside their hashes, and they
are never vendored into captain.

Where the shelf lands depends on the path. A parallel worker gets it in its
own worktree and it dies with the worktree; a solo worker gets it in your own
directory for the turn, excluded from `git status` while it is there and
removed when the turn ends. The exclusion names each staged path exactly, in
the exclude file git actually reads (the one a linked worktree shares with its
repository), so a worktree's diff never carries the shelf and a skill of yours
beside it stays visible. Turns that overlap in one directory share one staged
copy, and the last one out removes it. A skill you already have at that name
is never overwritten and never deleted, by `captain skills unstage` either: it
takes back only what carries captain's marker, and names what it leaves. A
copy left behind by a brain that died mid-turn carries that marker, so the
next turn takes it back.

**What the shelf was worth.** When the director grades a run it also grades
the shelf, in the same call: for each stocked skill, does the answer show that
procedure being followed, and was it worth its place on this task. A grade for
a skill captain never staged is dropped, a usefulness score without a use is
discarded (a grade of a book the director did not see opened), and every
stocked skill gets a row whether or not it was graded - so `captain skills
report` and the dashboard's skills panel can show "stocked forty times, used
twice", which is selection's failure rather than the skill's. Only the runs
the director scores carry a grade (`CAPTAIN_ASSESS_MIN_SCORED`).

### Acceptance evidence: what settles an outcome

Every completed task opens an `OutcomeEvidence` row, and for a long time only
`captain outcome <id> review` ever moved one off `pending` - which left the
column 100% one value: a constant, not a signal, under every reading built on
it (the shadow calibration's outcome labels, the M1 acceptance rate, the
gate's join above).

An outcome now settles from evidence captain already holds, and records
**what** settled it so a derived acceptance is never read as a human one:

| Evidence | Status | `decided by` |
|---|---|---|
| `captain outcome <id> review accept\|reject` | accepted / rejected | `reviewer` - authoritative, never overwritten |
| A later regression | regressed | `regression` |
| The task never reached delivery (`failed`, `exhausted`) | rejected | `lifecycle` |
| Any task-linked check failed | rejected | `checks` - at once, no window |
| A **commit** after the run touched the files the worker changed | accepted | `commit` - at once: the user kept the work |
| The next prompt on that workspace, inside the window, was a **correction** ("no, …", "still broken", "revert") | rejected | `reprompt` - at once |
| Every check passed, nothing came back for `CAPTAIN_OUTCOME_SETTLE` | accepted | `checks` |
| Something was delivered (an answer, a diff), no check ran, nothing came back for the window | accepted | `silence` - the weakest honest acceptance, labelled so calibration weighs it under the others |

Checks come from a workflow gate (`gate`), the director's grade of a solo
run (`solo`), and - since the solo verification below - the repository's
own test command run after a solo worker changed files (`tests`). Every
outcome also records what was delivered and when (`delivered_at`,
`delivered_chars`, `changed_files`), the effort and model of the
delivering attempt, and the repair/escalation sequence when one ran.

Two cases deliberately stay **pending**. A **cancelled** task is the user
changing their mind about the question, not a verdict on the answer, and
counting it as a rejection would charge the leg for the interruption. A task
whose checks passed and that still cost the user **correction minutes** is a
statement about the checks, not an acceptance and not a rejection - only a
human can call it.

```bash
captain outcomes                    # every outcome, with what decided it
captain outcomes <task-id>          # one task's evidence in full
captain outcomes --settle           # settle everything whose evidence has decided it
```

The brain sweeps on every turn it records (looking for follow-up commits
first); `--settle` exists so a window that has just elapsed can be applied
now, and so the counts are visible.

### Learning: the routing journal, the estimator and the policies

`state.json` keeps ring buffers - 500 events, 200 decisions, 200 outcomes -
which is what a sidebar needs and less than what learning needs. Every
decision that reached a task, every run event and every outcome that
settled is therefore also appended, one line each, to
`~/.captaincode/routing.jsonl` (`CAPTAIN_ROUTING_LOG=0` turns it off; a
path names another file; `CAPTAIN_ROUTING_LOG_MAX_MB`, default 64, keeps
the newest half past the cap). Each event now records the class that
routed, who settled it (`heuristic`, `classify`, `jev`, `director`) and
how sure, the effort, the model the leg ran as, the decision path, and the
attempt number - the attribution the scorecards were missing.

The **success estimator** reads that history: a task is hashed into a
bag-of-tokens vector (no model, microseconds), its nearest labelled
neighbours vote per `(leg, effort)`, observations from another model
version count 0.3, other efforts 0.5, and a 30-day half-life ages them. A
leg nobody has tried is scored by a **prior** read off the performance
feed's per-effort rows (`claude-opus-5-5-medium`, …) and reported as one.
jev's mid-tier answer, when asked, blends into a mid-tier leg's prior.

The **expected-cost** policy scores every arm as `cost + (1 − P) · repair`
(reasoning tokens scale cost by rung; a subscription window's burn rate is
priced against `CAPTAIN_VALUE_COST_REF`; a redo one rung up is the repair),
scales it past the class's latency tolerance, drops arms under the success
floor, and runs the cheapest expected cost per successful task - once the
history holds `CAPTAIN_EXPECTED_MIN_LABELED` settled outcomes. Below that
the value order runs and the decision says why; the arms are recorded
either way (`captain why` prints them).

The **bandit** (`CAPTAIN_ROUTING_POLICY=bandit`) samples each arm's success
probability from its Beta posterior and pulls the best sampled reward. It
refuses below `CAPTAIN_BANDIT_MIN_LABELED` and says so.

```bash
captain policy distill [path]   # the labelled history as JSONL rows a small router would train on
```

The export is the honest gate for a distilled router: it prints how many
labelled rows exist and by what they were decided. Training one is a
separate job that wants a few thousand rows; captain does not pretend to
have one before then.

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

**Two more axes are in the shadow, and are read by the same report.**

The **action gate** (`gate-destructive`, `gate-out-of-scope`, `gate-exfiltration`) screens what a worker is about to do; its rows live in `~/.captaincode/gate.log` and are folded into `captain jev shadow` automatically, or read on their own with `captain gate --report`. See [The action gate](#the-action-gate).

The **supervisor** (`worker-stuck`, `work-off-track`, `needs-human`, `agents-md-drift`) asks about a worker that is still running - the floor, not the dispatch. One call every `CAPTAIN_SUPERVISE_EVERY` per running worker, off the status feed the worker already produces, charged to the turn like any other decision-leg call. Nothing acts on an answer.

Both are **predictions**, not comparisons with a choice captain made at the same moment, so they are stamped from what captain later observed rather than at the moment they were asked: `worker-stuck` against whether the stall watchdog fired, `needs-human` against whether the user interrupted, `work-off-track` and all three gate nouls against the task's acceptance evidence (joined by task id, as every point is). The gate's join is one-sided and its bar is a false-positive bound, for the reason given under [The action gate](#the-action-gate). `agents-md-drift` is left **uncompared** because nothing in captain observes it - the report showing a point with no comparisons is the honest record, not a gap to fill with a guess.

### Pools: `/oss` and `/deterministic`

`/oss <task>` runs the task on open-weight models only (registry `open`, else the model family: llama, qwen, glm, deepseek, kimi, minimax, gpt-oss, mistral, gemma…). `/deterministic <task>` runs it on a leg whose **serving tuple** is green in the [Agentic Determinism Index](https://lemma-ventures.github.io/agentic-determinism-index/) right now - the latest scored appearance was byte-exact - and pins the request to that tuple through the egress proxy (OpenRouter `provider.order`, `allow_fallbacks: false`, `temperature: 0`), so the run uses the stack that was measured, not whatever OpenRouter routes to this minute. Both are modifiers like `/quality`: they count wherever they stand in the turn and compose with `/repeat`, `/parallel`, `/team`, `/frontier` and a leg prefix in either order (`/oss /repeat 5 <task>` is `/repeat 5 /oss <task>`; a named leg outside the pool still wins). Nothing in the pool → the turn runs without it and the feed says why, naming the green tuples no leg serves yet. `captain adi` prints every leg's standing and `captain legs add` registers a green tuple; the compiled `gpt-oss` leg (gpt-oss-120b via OpenRouter, Cerebras) is the one green today.

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_ADI_URL` | the published `leaderboard.json` | Where the feed is read from (a URL or a local path, e.g. a checkout's `website/leaderboard.json`). Cached in `~/.captaincode/adi.json`, refreshed every 6h. `CAPTAIN_ADI=0` turns the feed off (`/deterministic` then finds no green leg). |

### Stopping a running worker without losing its work: `/interrupt`

`ctrl+c` in the TUI (opencode's abort) is forwarded by the plugin as a stop: the folder's running workers end at once, what they produced is kept as a partial, no handoff is asked. A brain that is stopped or restarted (`captaincode.sh restart`, `-rr`, a plain `kill`) ends its workers first - they are its children, and one left behind ran on for half an hour into nowhere (2026-09-19); the launcher reaps any that survive.

`/interrupt [reason]` ends the run that is in flight and keeps what it did. A worker that takes notes mid-run (**claude**, every **opencode-served leg**) is asked to stop, write a handoff - what it implemented file by file, what is left in order, the exact way to resume - and end its turn on that note; the note reaches the TUI as the turn's answer, the run history, the journal (`kind: interrupt`) and memory as a promoted note. A worker with no mid-run channel (**codex-cli**, **cursor-agent**) is stopped at once and what it streamed is kept as a partial, marked so. A worker asked to hand off that is still running after `CAPTAIN_INTERRUPT_GRACE` (default `3m`) is stopped the same way. The plugin sends the word the moment it is typed; nothing is queued. A turn the brain is still preparing (compaction, routing) is withdrawn: its worker never starts. An interrupted run is never rerouted.

A prompt that is **queued** (typed while a turn runs, shown with a `QUEUED` badge) is an ordinary user message waiting its turn; opencode's right-click menu on a message only offers copy / revert / fork, and its "Manage queued prompts" key does nothing in 1.18. Captain's sidebar plugin adds the missing pieces: **Prompts: edit or delete…** in the command palette (`ctrl+p`) or `ctrl+x p` lists the session's prompts, queued ones first, and each one can be **edited** in place (a queued prompt then runs with the new text), **deleted** on its own (a queued prompt never runs; an answered prompt loses only itself, its answer stays), or **reverted to** (stock revert: that prompt and everything after it, file changes included, `session.unrevert` brings it back). **Delete last prompt** is `ctrl+x d`. The palette entries *Edit queued prompt* and *Delete queued prompt* open the same list narrowed to what is queued.

### Prompts typed while a turn runs

A prompt typed while a worker is busy waits (opencode shows it `QUEUED`).
opencode starts one next turn for everything that waited, so several
queued prompts reach captain together; captain runs them **one after the
other**, in the order typed, each as its own turn - its own head
(`/codex-cli …`, `/cursor …`, a bare prompt routed as usual), its own
worker - and each seeing the answers before it. The answers stream into the
one assistant message, each under a `[captain] queued k/n` line naming the
prompt it answers. To reorder or drop what is waiting, use the prompts
dialog (`ctrl+x p`) before the running turn ends.

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
| `CAPTAIN_WORKER_CALLBACK` | on (`0` disables) | The line on every worker prompt that tells a worker to arm `captain send` for work that outlives its turn, instead of promising to report later ([CLI](CLI.md#what-every-worker-prompt-carries)). |
| `CAPTAIN_WORKER_SECURITY` | on (`0` disables) | The security-first line on every worker prompt: prefer the standard library or an existing dependency, confirm a new package's exact name and publisher on the official registry, pin it, read install scripts, never weaken TLS/auth/sandbox checks, and name every dependency added or changed in the answer. Independent of `CAPTAIN_SKILLS_ALWAYS`, which governs the skill. |
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

Frontier legs additionally take `CAPTAIN_FRONTIER_MODEL` (default `opus`,
the alias Claude Code resolves to its newest Opus: Claude Opus 5.5 on Claude
Code 2.1.280 and later, Opus 5 before it; `fable` brings Claude Fable 5.1
back, a full name freezes a version), `CAPTAIN_CODEX_CLI_MODEL` (default
`gpt-6-astra`) and `CAPTAIN_CODEX_CLI_SANDBOX`.

#### Tiers: cheap, quality, frontier on every leg

Each worker leg runs one of three models on its own credential, chosen by
the request's effort. **Cheap** is low effort (`/save`, `/speed`, and bare
trivial work). **Frontier** is max effort (`/frontier`). **Quality** is
everything in between and is the leg's own model. A leg whose provider ships
a single model runs it in all three bands, at that band's effort.

| Leg | Cheap | Quality | Frontier |
|---|---|---|---|
| `claude` | `sonnet` (Sonnet 5) | Claude Code default (Opus 5.5) | `opus` at max |
| `codex-cli` | `gpt-6-sol` | `gpt-6-astra` | `gpt-6-astra` at xhigh |
| `codex` | `gpt-6-luna` | `gpt-6-sol-fast` | `gpt-6-astra` |
| `cursor` | `composer-2.5` | `grok-4.7-<effort>` | `grok-4.7-xhigh` |
| `grok` | `grok-build-0.1` | `grok-build-0.1` | `grok-4.7` |
| `gemini` | `gemini-3.5-flash-lite` | `gemini-3.7-flash` | `gemini-3.8-flash` |
| `deepseek` | `deepseek-v4-flash` | `deepseek-v4-pro` | `deepseek-v4-pro` |
| `glm` | `glm-5.3-flash` | `glm-5.3` | `glm-5.3` |
| `qwen` | `qwen3.6-35b-a3b` | `qwen3.5-397b-a17b` | `qwen3.5-397b-a17b` |
| `luna`, `grok-max`, `kimi`, `minimax`, `step`, `gpt-oss`, `ds4-flash`, `free` | one model | one model | one model |

`<PREFIX>_CHEAP_MODEL` and `<PREFIX>_FRONTIER_MODEL` pin a band
(`CAPTAIN_CODEX_CHEAP_MODEL`, `CAPTAIN_CURSOR_FRONTIER_MODEL`,
`CAPTAIN_CLAUDE_FRONTIER_MODEL`); `<PREFIX>_MODEL` stays the quality model,
and on claude it now pins `claude -p --model` too. A registry overlay entry
sets them with `"tiers": {"cheap": "…", "frontier": "…"}`.
`CAPTAIN_CHEAP_TIER=0` keeps every leg on its own model at low effort.
`captain upgrade --check` prints the resolved table.

The band follows the effort, not the prefix, so a verify climb from low to
medium also moves from the cheap model to the leg's own. That second attempt
starts a fresh prompt cache. Qwen has no frontier sibling here, because
`qwen3.6-max-preview` is only served by endpoints that OpenRouter refuses
under a zero-data-retention account setting.

OpenAI's GPT-6 family covers three tiers on one ChatGPT login, as three legs:

| Tier | Leg | Model | Override |
|---|---|---|---|
| Cheap | `luna` | `gpt-6-luna` | `CAPTAIN_LUNA_MODEL` |
| Quality | `codex` | `gpt-6-sol-fast` (directs as `gpt-6-sol`) | `CAPTAIN_CODEX_MODEL` |
| Frontier | `codex-cli` | `gpt-6-astra` through `codex exec` | `CAPTAIN_CODEX_CLI_MODEL` |

All three draw on the same subscription windows. `codex` runs Sol in fast
mode for interactive latency, which draws that quota at twice the rate;
`CAPTAIN_CODEX_MODEL=gpt-6-sol` trades the speed back for quota. Until
Artificial Analysis scores GPT-6 Sol and Luna, the ranking reads their GPT-5.6
rows.

### Effort

How hard a worker thinks is decided per task, not per leg. A stated
preference wins outright: `/frontier` → max, `/quality` → high, `/speed`
and `/save` → low. A bare prompt gets the **per-task decision**
(`DecideEffort`): the class sets the rung (trivial → low, medium → medium,
high → high), **frontier-class work defaults to medium** on claude or a
frontier-class leg (the published curve: medium gives up about two points
at half the cost), **irreversible** work - a migration, a deletion, a
deploy, a force-push, money moving, read by tier 0's patterns or by jev's
calibrated answer - climbs one rung, and every attempt after the first
climbs one more, on the same model first (see *Budgets, escalation* below).
The climb stops at `CAPTAIN_EFFORT_CEILING` (default `xhigh`); only
`/frontier` reaches max. The rungs are low, medium, high, xhigh, max.

Under the `expected` routing policy the effort is chosen **with** the leg:
each eligible leg is scored at its decided rung and one up, and a cheaper
leg thinking harder can beat a dearer leg thinking less.

Every transport with a knob gets the rung: claude -p `--effort`, codex exec
`model_reasoning_effort` (max is codex's xhigh), cursor-agent a **pinned
model rung** (below), an opencode worker's message `variant` fitted to
what the model offers (glm: low/high/max; grok-4.7: low…xhigh; none for
grok-build, kimi). The run's effort and model show on its Last Runs line in
the sidebar, on its ledger event, and in `captain why`.

Cursor used to run whatever its own `auto` router chose, which captain
could neither name nor attribute. It now pins a rung of one family:
`CAPTAIN_CURSOR_MODEL` names the family (default `grok-4.7`, the family
`/frontier` already pins), a listed family (`grok-4.7`, `cursor-grok-4.6`,
`gpt-5.3-codex`) takes the rung the effort asks for, a full model name is
used as is, and `auto` restores Cursor's router.

`/frontier` alone is the pseudo-leg: claude at the ceiling. In front of a
leg or a workflow it is a modifier - `/frontier /claude X > /grok >
/codex-cli` runs claude, grok and codex-cli, each at its most performant
settings (claude at max effort; grok upgrades from grok-build to grok-4.7;
cursor pins `grok-4.7-xhigh`), and claude at max effort *is* the frontier
configuration (the `opus` alias, `--effort max`). When the frontier
tier's own limit refuses such a run (the monthly spend cap, or a tier
limit such as "your Fable limit" with the model pinned to `fable`), the
tier is benched, not claude, and the same turn reruns claude at standard
settings.

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_FRONTIER_EFFORT` | unset | Pin claude's `/frontier` effort (`xhigh` to get the pre-2026-09-13 second-to-best) |
| `CAPTAIN_EFFORT_CEILING` | `xhigh` | The strongest rung a bare prompt may climb to (irreversible work, later attempts). |
| `CAPTAIN_EFFORT_COST` | `0.6,1,1.6,2.2,3` | Cost multiplier per rung (low…max) the expected-cost ranking prices reasoning with. |
| `CAPTAIN_CURSOR_MODEL` | `grok-4.7` | cursor-agent's family (rung chosen by effort), a full model name, or `auto` for Cursor's own router. |
| `CAPTAIN_CURSOR_FRONTIER_MODEL` | `grok-4.7-xhigh` | cursor-agent `--model` when the request is `/frontier` |
| `CAPTAIN_CHEAP_TIER` | on (`0` disables) | Run each leg's cheap-tier sibling at low effort (see *Tiers*). |
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
| `CAPTAIN_MAX_EFFORT_ESCALATIONS` | `1` | Between the repair and the leg escalation: the same leg one effort rung up (prompt cache and context survive). `0` = skip straight to the leg. |
| `CAPTAIN_MAX_ESCALATIONS` | `1` | Moves to a stronger leg after repair exhausts. `0` = stop after repairs. |
| `CAPTAIN_SOLO_VERIFY` | on (`0` disables) | After a **solo** worker changed files in a repository whose test command captain can detect (`go.mod`, `package.json`, `pyproject.toml`, `Cargo.toml`, `Makefile`), the tests run before the turn ends. A failure buys, in order and each bounded above and by the task's budget: a repair on the same leg with the failure output, the same leg one rung up, then the next stronger leg. Each attempt streams into the same answer and is its own event; the outcome carries the sequence. If a retry times out or yields no test verdict, verification is inconclusive and automatic retries stop. |
| `CAPTAIN_SUPERVISE_BAR` | `0.7` | jev's supervisor answers gate the sequence: "needs a human" above the bar stops it (the failure is delivered, not retried); "off track" above it skips the repair - the same model at the same effort went the wrong way. |
| `CAPTAIN_TASK_TOKEN` | unset | Bearer token for `/v1/task/*`. Unset ⇒ loopback-only. Set ⇒ every task HTTP request needs `Authorization: Bearer …`. |

### Miscellaneous

`CAPTAIN_BRAIN_URL` (default `http://127.0.0.1:14097`), `CAPTAIN_SRC` (source
checkout for `captain upgrade`), `CAPTAIN_REPEAT_MAX` (default `100`),
`CAPTAIN_AA_API_KEY` (the perf ranking's feed and `captain priors sync`).

### TUI chrome

The Models / shield / Last Runs sidebar keeps the same live brain data under
every chrome profile. What changes is OpenCode's theme and a few glyphs.

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_UI_CHROME` | unset (uses last pick, else `default`) | `demo` = website-demo colors (`captain-demo` theme) and ❄ for cooling; `default` = restore the previous OpenCode theme and `~` cooling. Overrides the palette command's persisted pick. |
| `CAPTAIN_UI_SLOTS` | `sidebar,logo` | Comma list of UI slots to register (`sidebar`, `logo`, `tag`); `none` disables the plugin chrome. |

In the TUI command palette: **Captain chrome…** (or **Captain chrome: demo** /
**Captain chrome: default**). You can also pick the `captain-demo` theme with
OpenCode's `/theme` command; the chrome commands remember the prior theme so
leaving demo restores it.

See also the [CLI cheat sheet](CLI.md).

## Precedence

Compiled registry defaults → `~/.captaincode/legs.json` overlay → environment
variables. Nothing routes off a file you have not seen: a fresh install with no
overlay and no priors file still routes, on the compiled defaults.
