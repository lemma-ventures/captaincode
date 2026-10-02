# Scoring and picking: one estimate per model, one rule to pick

Status: all four phases implemented (2026-10-03). Phase 3's rule runs in
shadow mode by default: every decision records what it would have picked,
and `CAPTAIN_PICK=on` lets it decide once two weeks of Phase 0-2 data are in.

This spec replaces how Captain grades runs, scores legs and picks one. It
came out of a review of the scoring code and a five-way blind red-team review
of a first proposal (statistics, bias and gaming, cost and operations, model
identity, routing decisions). Numbers below are from the ledger
(`~/.captaincode/state.json`, 2026-09-21 to 10-02) unless stated.

## Problem

Captain has two quality signals that barely meet, and the evidence it treats
as strong is mostly noise.

**Benchmarks and local grades live on different paths and scales.**

- The live Artificial Analysis feed (refreshed every 6 h, `perf.go`) ranks
  legs for `/frontier` and director choice on the intelligence index (0-53).
- `/quality` and value ranking use `priors.json` (0-10 per domain, anchored so
  claude is 9.5 and clamped there). It is written only by
  `captain priors sync --apply` and had gone 22 days without a refresh: it
  scored GLM-5.3 at 7.5 when the benchmark gave 8.8.
- The blend `(prior·5 + Σgrade)/(5+n)` mixes a 0-10 benchmark scale with judge
  grades that cluster around 7.4, so every graded leg drifts toward 7.4 at a
  speed set by how often it was graded, not how good it is. Claude falls from
  9.5 to 8.2 after 12 grades; a leg with 3 grades keeps most of its prior.

**The grades cannot separate the top legs.**

- One model grades: whichever leg directs (claude here), including its own
  runs. It sees the task and 6,000 characters of output, never the test
  results (solo runs pass `objective "none"`).
- 75% of grades are 7 or 8. Claude 7.67 vs grok-max 7.50 at n=12 each is a
  0.17 gap against a ±0.36 interval.
- Only first attempts of at least 200 characters and 5 s are graded, one in
  ten after the first ten; repairs and quick wins never are.

**The "strong" labels are weak.**

- 151 of 158 settled outcomes are accepted (96%). 42 of those are silence.
- 71 are accepted by a later commit that touched the same files; they share
  only 15 commits, and one squash commit accepted 15 tasks, including runs a
  later leg had to redo.
- 29 of the 39 test-checked turns changed test files themselves, and tests
  passed 37 of 39 times.
- The judge's grade is also recorded as a pass/fail check: 17 of the 38
  "accepted by checks" outcomes rest on that grade alone, and 6 of the 7
  rejections came from checks.

**Captain does not know which model ran.**

- `Event.Model` is computed from configuration (`ModelIDAt`), never read back
  from the tool. Claude runs are recorded as the aliases `claude-opus` and
  `claude-opus-frontier`, so Opus 5 and 5.5 share one score.
- Effort is folded into the name inconsistently (`grok-4.7-medium`,
  `claude-opus-frontier`, codex switching model at low effort).
- 131 of 500 events have no model, no charge carries one, and OpenRouter can
  serve one model id from different providers and quantizations.
- Stats are keyed by leg, so an upgrade inherits the old model's grades, and
  `priors.json` (also keyed by leg) can pin an old model's benchmark score.

**The picking layer ignores most of it.**

- `/quality` balances equal shares inside a band (score ≥ 85% of the best);
  the score only decides who is in the band. Claude (median run 17.5 min) and
  grok-max (3.2 min) took equal shares on a near-equal grade.
- The busiest path, `expected` (81 of 200 decisions), ranks mostly on cost:
  its success probabilities sit at 0.41-0.62 against a 95% observed
  acceptance rate.
- The claude director picked claude in 18 of 26 decisions.
- `/frontier` turns are barely recorded as decisions, the bandit has never
  run, and the "explored" flag is set on 51 of 81 `expected` decisions.

There is also too little data to split finely: about 95 grades in 11 days,
and the ledger keeps only the last 500 events and 200 outcomes.

## Goal

Pick, for each turn, the leg expected to give the user an accepted answer
soonest, within their subscription quotas and their chosen balance across
vendors and open weights. Learn it from evidence that measures the model that
actually ran.

Non-goals:

- Overriding the user. A typed leg, `/frontier`, `/quality` or `/save` is
  honoured.
- Replacing the benchmark. It seeds every estimate; local evidence corrects it.
- A judge panel on every run.

## Design

### Phase 0: record the right data (no routing change)

Two weeks of this data comes before anything is re-keyed.

1. **Observed model identity.** Each transport reports the model it ran:
   claude's stream `system/init` model, codex's resolved model, opencode's
   response model and provider. Store `model`, `effort` and `route`
   (transport, provider, quantization) as separate fields with
   `resolved: observed|projected`. Charges carry them too.
2. **A permanent evidence log.** This existed already: `routing.jsonl`
   (`journal_routing.go`) appends every decision, event (with its grade) and
   outcome (with its checks), outside the 500/200 caps, up to 64 MB. It
   holds everything since 2026-09-22.
3. **Every pick is a decision record.** Lanes, `/frontier` included, record
   their candidates and the probability each had of being picked, so later
   phases can weight evidence by how it was collected.
4. **`Explored` means explored.** It is set only by an exploration draw, not
   whenever the pick differs from the value ranking's first row.

### Phase 1: labels that mean something

1. **Commits accept the right task.** A commit accepts a task only when that
   task was the last writer of the lines it commits. Tasks sharing a commit
   split it. A corrective turn between delivery and commit voids it.
2. **Tests the worker did not write.** A test check labels only when the
   run changed no test file. A failure labels only when the suite passed at
   the same commit before the run; the brain remembers the last result per
   folder, commit and command. With no earlier result a failure is
   inconclusive: it still buys a repair, but settles nothing. A timeout is no
   signal.
3. **Silence is unknown**, not accepted: the outcome stays pending, out of
   every label.
4. **The judge is not a check.** Its grade stops being recorded as pass/fail,
   and past grade-only checks are removed from the labels.
5. **Rework and time-to-accepted** are recorded per task: follow-up
   corrections, a redo on another leg, a cancel followed by a reprompt.

### Phase 2: one estimate per model

For each model family, then version, then effort (a hierarchy, so a version
with three runs borrows from its family), with a domain adjustment for code
versus prose:

- P(accepted without rework), and
- run time (log-normal).

Each starts from the benchmark, counted as 2-3 runs of evidence, through an
explicit table that maps a model version and effort to its Artificial
Analysis row and marks it `exact` or `fallback`. A fallback never resets local
evidence. Evidence decays with time, not with a count cap.

Judging, when used: one cheap judge from another vendor than the worker, on a
rubric (does it run, does it meet the stated requirements), with the model's
identity hidden. It runs after the user has the answer, only where the
estimate is uncertain, and each judge's average offset is measured against
test-verified runs before its grades count. About two thirds of graded runs
(research and writing) have no tests, which is why a judge stays at all.

### Phase 3: one picking rule

- **Objective:** expected minutes to an accepted answer = run time +
  P(rework) × (repair time + run time), plus a quota cost from how full each
  subscription window is.
- **Selection:** Thompson sampling from the Phase 2 estimates, so legs with
  little evidence still get tried, capped per quota window. A user waiting on
  a turn only gets legs whose estimate is close to the best; weaker legs are
  explored in background runs on cheap tasks.
- **Lanes become filters:** `/frontier` = frontier-class legs, `/quality` =
  legs within a margin of the best, `/save` = open weights. The equal-share
  rotation and `OSSTurn` (2026-10-02) go.
- **The routing mix is a soft pull:** a penalty proportional to each axis's
  gap from its target share. A clearly better leg wins unless the user's
  balance is actually being violated.
- **The director classifies; it does not pick.** It sets class, constraints
  and risk; the rule picks the leg.
- The `value`, `expected` and bandit paths collapse into this rule.

As built (`pick.go`, `brain_pick.go`):

- **Shadow by default.** `CAPTAIN_PICK` unset records the rule's pick
  (`time_pick` on the decision, with minutes and draw shares per leg) and
  changes nothing. `on` decides; `off` silences it.
- **What it replaces when on.** The value path's epsilon draw, the lanes'
  equal-share rotation and `OSSTurn`, the frontier lane's balancer, and the
  director's leg choice: each still builds the menu, the rule picks from it.
  The `value`, `expected` and bandit code stays in place until the rule has
  run long enough to retire them.
- **Constants.** A full subscription window adds 15 minutes, a unit of mix
  deficit is worth 20, a waiting user is offered legs within 0.15 of the
  best P, and at most 20% of recent decided picks may be draws away from the
  best by mean. All are first guesses for the data to correct.

### Benchmark priors

Exact matches are applied automatically. Fallback matches and large changes
are shown with the routing shift they would cause, and wait for the user.

### Independent fixes

- **Fixed:** the success estimator's starting probability (`EffortPrior`,
  about 0.5) sat far below the observed acceptance rate, so its floors
  excluded legs on no evidence. With 30 or more labels its level is now
  shifted to the observed rate; the line still ranks legs against each other.
- **Fixed:** charges carry their model (Phase 0).
- **Not a bug:** the bandit policy is opt-in (`CAPTAIN_ROUTING_POLICY=bandit`);
  Phase 3 replaces it.
- **Not a bug:** Gemini's compiled default is 3.7 on purpose (the upgrade
  check flags 3.8 above it); `captain upgrade` writes the 3.8 model and its
  benchmark slug together, so an upgraded install is scored as 3.8.

## Rejected

- **Splitting stats by version and domain directly, carrying 30% of old
  grades.** Most cells would hold 0-2 grades and routing would quietly fall
  back to benchmarks; the hierarchy replaces it.
- **Rescaling benchmarks to the best model in the feed.** Every new model in
  the feed would move every prior and reshuffle the lanes.
- **A cross-vendor judge panel on every run.** About 90-135 extra calls a day,
  on quota the workers need, while the flash judge returns nothing on about
  three calls in ten.
- **Exploration on interactive turns** without a cap. A weak leg's failed turn
  plus its repair costs the user 10-30 minutes.

## Open questions

- The quota cost's units: how much a full Claude window is worth in minutes.
- Whether a shadow run (a second leg on the same task, in the background) is
  affordable often enough to be the main exploration source.
