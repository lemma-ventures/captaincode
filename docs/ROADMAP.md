# Captain Code implementation roadmap

Prepared 13 September 2026; status refreshed 16 September 2026.

**M1–M5 control surfaces have largely landed in source** (accounting, eval harness,
decisions, capabilities, quotas, budgets, escalation, isolation, artifacts,
lifecycle, cancellation, handoffs, task API, `captain task mcp`, host helpers,
outcomes, calibration, policy). What remains is mostly **evidence and packaging**:
pilot baseline publication, cost-attributed re-runs against an accounting brain,
productized host extensions, and TUI correction UI. Dates below are planning
guides, not release promises.

### Open items (honest remaining)

| Item | Why it is still open |
|---|---|
| M1.3 / M1.4 / M1.5 narrative | Pilot ran; blinded review + cost attribution + dated evidence report still owed |
| M4 host productization | Go helpers + cert exist; Pi/Jido/editor drop-in packages do not |
| M5.1 TUI correction UI | CLI `outcome` commands ship; TUI surface does not |
| M5.3 real numbers | Needs cost-attributed pilot data |

## Product direction

**The right model. For the work.** Not every task needs a frontier model.
Captain should choose an appropriate worker or team from the user's connected agents,
verify the result, and make that coordination usable from other development environments.

Optimize **total cost and time per accepted task at a stated quality target**.
Reducing tokens, choosing a cheaper first worker or launching more agents is useful only
when the complete result justifies it. Coordination must earn its overhead.

The initial audience is developers combining several coding subscriptions or subscriptions
and model APIs. The differentiating combination is task routing across separate runtimes,
bounded recovery, explicit workflows and portable project knowledge. The defensible asset
should become evidence that this combination improves accepted work on real projects.

## Ownership and companion roadmaps

This is the authoritative **Captain Code** backlog. M1–M5 own coding-task orchestration;
they are not a roadmap for the memory engine or an evidence infrastructure platform.

| Concern | Implementation owner | Captain's responsibility |
|---|---|---|
| Model/worker selection, subscriptions, quota, budgets and escalation | Captain Code, M1–M2 | Measure and control the complete coding task |
| Worktrees, patch integration, task checkpoints, cancellation and recovery | Captain Code, M3 | Preserve artifacts and reconcile uncertain execution |
| Task API and execution MCP; Pi, Jido and editor delegation | Captain Code, M4 | Own the task lifecycle and host adapters |
| Acceptance signals, routing calibration and policy rollback | Captain Code, M1/M5 | Keep authoritative outcomes and learn which worker completes the task |
| Registers, retrieval, provenance, generic memory lifecycle and memory MCP | Euclid, `docs/ROADMAP.md` E1–E5 in its repository | Consume pinned memory interfaces; supply prompt hooks and task-derived journal events |
| Deterministic inference, signed execution receipts, mandate gates, standing and evidence retention | Optional Trace/Atlas-style evidence services (separate backlog) | Optional client of those services; never infer their guarantees from Captain's logs |

Euclid owns retrieval-quality benchmarks. Captain owns cost/time per accepted coding task,
including memory and distillation overhead. The Agentic Determinism Index has its own
harness and measures repeatability; its results are not coding-acceptance or routing scores.

**Memory extraction boundary.** Standalone Euclid currently exposes five retrieval/register
tools. Captain's wrapper also composes brains, writes notes and records runs. Euclid E2/E4
will standardize reusable memory behavior; Captain keeps runtime event capture, scheduling
of model-assisted distillation, worker selection and its usage accounting. Extract through
compatibility fixtures, not a simultaneous rewrite. See [current integration](EUCLID.md).

**Separate authorities.** Captain's task store owns execution and budget state. Euclid owns
memory records and their provenance. Trace/Atlas owns receipt and mandate verification.
Sharing task IDs, artifact digests or references does not transfer that authority. A
memory summary cannot mark a task accepted, replenish a budget or authorize a tool action.
A checkpoint is not a deterministic replay guarantee, and a signed receipt is not proof
that a coding change is correct.

**Dependencies.** M1–M5 can ship without optional evidence services. M1 freezes the existing Euclid version (or
disables memory for a stated evaluation arm); it does not wait for memory extraction.
M3/M4 retain the existing memory adapter until Euclid E2/E4 pass compatibility gates.
Pi/Jido/editor execution belongs here; their memory-only recipes belong to Euclid.
If a user explicitly requires an optional evidence/mandate service, an unavailable or
unsupported service must stop that scoped operation rather than silently downgrade it.

## Starting point: extend what exists

| Area | Implemented foundation | Work this roadmap adds |
|---|---|---|
| Routing | Task/domain triage; quality priors blended with local assessments; cost/duration ranking; quality thresholds; reliability demotion; limited exploration | Calibrated acceptance predictions, complete decision records and enforceable resource policy |
| Frontier | `/frontier` initially requests Claude at maximum effort; performance-ranked fallback exists across frontier and other available workers | Capability-based initial selection, explicit quality floors and visible degradation policy |
| Registry | Native CLI and API legs; configurable prices, context, vision and frontier attributes | Verified runtime capabilities, accounting/permission contracts and compatibility tests |
| Accounting | Local ledger, per-worker outcomes, tokens/cost when available and run transcripts | Stable task/attempt identities, complete call attribution, explicit unknown usage and acceptance records |
| Recovery | Eligible provider-failure reroutes, cooldowns, partial preservation and native session reuse | Durable reconciliation, bounded retries and artifact-aware handoffs |
| Workflows | Stages, parallel workers, gates, review, live status and short-lived in-process deduplication | Isolated writers, reviewed integration, crash-safe checkpoints and end-to-end cancellation |
| Integration | Local route/assessment/status endpoints, chat-completion interface and Euclid MCP | A versioned execution contract and certified host adapters |
| Learning | Model-assisted quality assessments, class/domain statistics and exploration | Independent acceptance evidence, correction costs, held-out evaluation and rollback |

Current quality scores are estimates. Observed duration is not a universal model-speed benchmark.
Quota pressure is inferred from rate limits; it is not an exact allowance meter. Existing
prompt and timeout budgets are not aggregate dollar ceilings. Run transcripts and short-lived
deduplication do not establish crash-safe execution or replay protection.

## Delivery order and staffing

Planning assumption: two full-time engineers, one focused on routing/accounting and one on
runtime/integrations, plus part-time release testing and developer acceptance review.
The ranges below are rough engineering estimates, not commitments; recalibrate after M1.
Allow additional contingency for upstream CLI changes and evaluation variance.

| Release | Scope | Estimated duration | Dependencies | Required evidence |
|---|---|---|---|---|
| M1: evidence alpha | Reproducible installation and baseline economics | 3–4 weeks | Existing implementation | Clean setup and reproducible task-category report |
| M2: controlled routing | Decisions, quota provenance, budgets and escalation | 4–5 weeks | M1 task/call accounting | Resource invariants and measured escalation results |
| M3: dependable workflows | Isolation, reviewed integration, checkpoints and cancellation | 5–7 weeks | M2 resource/scope contract | Recovery and concurrency fault suite |
| M4: portable Captain | Versioned task API, execution MCP and three hosts | 4–5 weeks | M3 durable task lifecycle | Completion/cancellation/recovery in each host |
| M5: outcome adaptation | Accepted outcomes, calibration and policy promotion | 4–5 weeks | M1 data, M2 controls, M3 execution | Held-out improvement at the quality target |

Sequential planning envelope: **20–26 weeks** with that staffing, before contingency.
This estimates Captain M1–M5 only. Euclid releases are a separate capacity track.
Data collection starts in M1; API design starts during M2. M5 research can run before M4
finishes, but changing default routing requires the evaluation and execution gates.
One engineer should re-estimate the backlog rather than inherit the same calendar.

## M1 — Prove the value

**User outcome:** a new developer can install Captain and reproduce what its routing gains or costs.

| ID | Work package | Concrete deliverable | Owner |
|---|---|---|---|
| M1.1 | Installation contract | Pin brain, terminal and adapter versions; define brain-only versus full terminal setup; verify actual installed binary names; clean macOS/Linux install, upgrade and rollback recipes | Runtime/release |
| M1.2 | Task and call accounting | Versioned records for task, stage, attempt and individual provider call; parent/child links, idempotent usage updates and price/config provenance | Routing |
| M1.3 | Evaluation harness | Versioned task fixtures, isolated project snapshots, deterministic acceptance checks, blinded review and exportable results | Routing + reviewer |
| M1.4 | Baseline report | Category-level results for fixed frontier, fixed economical, Captain auto and explicit workflows; ablations for director/review/team overhead | Both |
| M1.5 | Release package | Reproducible artifacts, compatibility table, extraction/documentation checks and a dated evidence report suitable for the website | Runtime/release |

**M1.1 status (14 September 2026).** The adapter half of the installation
contract has landed. `pkg/captaincode/toolchain.go` pins each transport's
driving binary to a **tested** version (the one this build's behaviour was
observed against) and a **minimum** (the oldest whose CLI contract captain
still compiles against), together with its install, upgrade and rollback
recipes. `captain doctor` now probes the binary that will actually run - asking
it for its version the way a user would, not reading a lockfile - and reports
ok / newer / unknown / old / mismatch / missing per adapter.

The distinctions are the point. *Newer than tested* is usable and does not
block a leg, but it is named, because a report that quotes its manifest's
version while running another is not reproducible. *Unknown* - the tool printed
no version, or would not run - is never counted as satisfying the pin, and also
does not block: a version captain cannot read is a reason to distrust the
report, not to refuse a tool the user installed. *Mismatch* does block, and is
the check "is there a binary named codex on PATH?" could never make: a `codex`
that is somebody else's script fails in a way no retry fixes. `captain upgrade`
now reads versions through the same parser, so the two commands cannot disagree
about what is installed.

[`docs/INSTALL.md`](INSTALL.md) states the brain-only versus full-terminal
shapes and the macOS/Linux install, upgrade and rollback recipes; the recipes
live on the pins, so doctor's fix hints and the document cannot drift apart.

**Captain's own version now lands in the same report (14 September 2026).**
The adapters were only half the contract: a report that names `claude 2.1.270`
and `codex 0.153.4` but not the binary that chose between them is still not
reproducible, because the routing policy, the prices and the accounting schema
live in the brain. [`pkg/captaincode/selfversion.go`](../pkg/captaincode/selfversion.go)
adds a `build` section above the toolchain with three rows: the running brain
binary, the opencode TUI fork's checkout, and the fork's runtime.

Captain is built from source, not installed from a release, so there is no
version to pin it *to*; the honest identity is the revision it was built from
and whether that revision was the whole truth. Go stamps both (`vcs.revision`,
`vcs.modified`) into any binary built inside a checkout, so this needs no
ldflags and no build step can forget it. That yields one new state the adapters
did not need: **dirty** - built from a modified tree, which is what a developer
runs all day and which must not block, but whose revision names a commit whose
code is not the code that ran. Dirty is therefore reported, counted out of
"components an evidence run can quote", and still allowed to run. The blocking
case is the self-inflicted twin of the foreign-`codex` failure: the `captain`
resolved on PATH being a **different file** from the one that produced the
report, so the user's next command runs a binary nothing probed. That replaces
the state but never the revision - the running binary's revision is still the
truth about the report it produced. A missing fork checkout is the brain-only
shape, not a fault. The bun pin is read from the fork's own `packageManager`
field and probed through `ProbeTool`, so package.json stays the single
authority and the terminal's runtime cannot be quoted at a version the fork
does not install with. `captain upgrade --check` prints those rows through the
same probe, so it and doctor cannot disagree about which revision is installed.

Remaining for M1.1: ~~clean-machine rehearsal harness~~ (code landed — see status above).
(done). `captain doctor --rehearse` runs a static rehearsal: every adapter
has non-empty install/upgrade/rollback recipes that reference the right
binary, every rollback pins the tested version, the brain build command is
documented, `captain init --check` generates configs without error, every
transport with a leg has a pin, and the recipes in INSTALL.md match the
pins in toolchain.go. The exit gate ("no undocumented manual fix") is met
when every check passes. The live rehearsal (actually installing on a
fresh VM) is the human verification; the rehearsal is the contract that
makes it pass on the first try.

**M1.2 status (14 September 2026).** The record schema has landed in
[`pkg/captaincode/accounting.go`](../pkg/captaincode/accounting.go): versioned
task/stage/attempt/call charges with parent links, `measured`/`estimated`/`unknown`
usage and cost status, price provenance, idempotent reconciliation of late usage,
and leaf-only roll-ups with accounting-coverage counts.

Threaded onto the task identity so far: the worker call; the **repair** call a
narration nudge costs; the **review** calls the director's scoring makes; and -
new - the two calls that run *before* the worker, tier-1 **classification** and
the director's **plan**. Those two were the hard case: routing happens in a
different request from the one that records the run, so the plan call had no
task to be charged to and its 17.5s median simply vanished from the bill.
`brain.chargeRoute` now mints a task identity during routing and `recordRun`
adopts it, making one turn one tree. The identity is minted **lazily**, on the
first provider call routing actually makes, so a turn the deterministic triage
fast-paths leaves no empty task row; it is **consumed** on adoption and expires
after `routeTurnTTL`, so a repeat of the same prompt text is a new task rather
than a second charge on the first one. Failed calls are charged - a timed-out
plan spent quota - and `directorJSON`'s corrective retry is its own attempt.
A nudged turn merges two calls into one `Result` with no per-call split
reported, so the repair is recorded with `unknown` usage rather than half of a
number nobody measured; the turn's known total stays on the worker call and is
never billed twice. `captain why` prints the task's call tree and its roll-up;
`captain stats` prints ledger-wide accounting coverage. Neither presents an
incomplete sum as a bill.

**Fan-out now threads too (14 September 2026).** A team turn and a workflow run
are each **one task**. `brain.openTask` adopts the route-time identity (so the
director plan that chose the team is billed to the team it chose) or mints the
task itself, and `chargeStage` opens the `stage` row the schema already
defined: `team:<key>` for an ensemble, `stage n/N` for each workflow stage.
Every member is an **attempt under its stage**, so a five-run pipeline is one
task with four stage rows rather than five unrelated tasks - the shape the
"cost per accepted task" metric needs. Failed members are charged: a worker
that was rate-limited after consuming quota is part of the turn's bill.
A workflow gate's bounded retry is its own `gate-repair` attempt with
**unknown** usage, for the same reason a nudged solo turn is: the runtimes
report one figure for the pair and half of it would be invented. The team's
synthesis and the workflow's mandatory review are charged as coordination
overhead rather than treated as free. Worker events carry the shared
`TaskID`/`AttemptID`, so a decision row and its charge point at the same try,
and `captain why` prints the stage headings above the calls beneath them.

**Overhead and memory now thread too (14 September 2026).** The two call
families that were still free are charged. **Compaction** - the fold of the
whole conversation, the most expensive single call captain makes on a long
session - is billed to the turn it serves: `fitPrompt` mints that turn's
identity before the worker runs, and the worker adopts it, so a turn that
paid 12k summarizer tokens to fit its own history shows them. A **failed**
fold is charged as well, because a compaction that timed out and fell back to
windowing still spent the quota. **Memory consolidation** - journal
distillation, register bootstrap and probe authoring - belongs to no user
turn, so each gets a task row of its own (`memory: distill <brain>`) and saves
the ledger itself, since no turn follows that would; the same applies to the
`/repeat` round digest, which runs after its round has already been billed and
closed. A brain with no ledger (the CLI's throwaway) charges nothing.

Remaining for M1.2: a turn's identity does not yet survive a brain restart
(that durability is M3.3).

**M1.3 protected glob verification (14 September 2026).** Baseline verification
now rejects unmatched and malformed `must_not_change` glob patterns, with
regression coverage for matching wildcard patterns and missing targets.
The pilot corpus and measured baseline report remain open.

**M1.3 review identity (14 September 2026).** Suite validation rejects duplicate
or unaddressable review aliases, including collisions with generated aliases.
Reviewing imported results rejects ambiguous task/alias/repeat keys before any
verdict or history mutation. Regression tests cover both admission and imported
records. The pilot corpus and measured baseline remain outstanding.

**M1.3 snapshot retention (14 September 2026).** Task IDs and arm names
are validated before filesystem work. Baseline verification and execution use
unique snapshot directories instead of deleting a name-derived path, preserving
prior evidence across repeated runs in the same work directory. Regression
coverage checks invalid names and retained execution/baseline artifacts.

**M1.3 artifact evidence hardening (14 September 2026).** Acceptance now
compares tracked files against the pinned revision, so a worker commit cannot
hide protected edits. NUL-delimited paths preserve unusual filenames and both
sides of renames; Git inspection errors block acceptance. Regression tests
cover committed protected edits, missing Git metadata and renamed/untracked
paths. The pilot corpus and live baseline report remain open.

**M1.3 verifier hardening (14 September 2026).** Baseline verification now
rejects unknown task selections, propagates cancellation and refuses missing
protected paths. Recorded durations include snapshot/setup/check time. Regression
tests cover these gates; the pilot corpus and live baseline report remain open.

**M1.3 status (14 September 2026).** The harness has landed:
[`pkg/captaincode/eval.go`](../pkg/captaincode/eval.go) (fixtures, snapshots,
execution, acceptance), [`eval_report.go`](../pkg/captaincode/eval_report.go)
(the metric definitions below, enforced) and `captain eval
validate|verify|plan|run|report|review`. See [docs/eval](eval/README.md).

Four properties carry the weight. A fixture is **pinned** - a task names a full
commit sha, and a branch name is rejected at load rather than discovered to
have moved after the compute was spent; the same check rejects a suite with no
family, no arms, or no rule that decides acceptance. Every execution starts
from a **pristine clone** of that revision, one per task/arm/repeat, and the
source repository is only ever read: an evaluation run cannot disturb the
developer's dirty worktree, and no repeat inherits the previous one's tree
(the one failure mode that turns a broken arm into a passing one). Acceptance
is **a command and an exit code**, plus `must_change`/`must_not_change` over
the files the arm actually touched, so two people reading the same result
reach the same verdict. Where checks are insufficient a task declares
`review: blinded`; those executions end `pending-review`, which is not
acceptance, stays out of the numerator, and is listed to the reviewer under
the arm's alias rather than its name.

The outcome vocabulary is deliberately wider than pass/fail: a worker that
exits non-zero **rejected** the task (it ran and reported failure), which is
not the same as the harness failing to run it (`error`), and neither is a
`timeout`. All of them stay in the denominator - the whole point of dividing
the complete workload by accepted tasks is that a cheap arm which fails often
must not read as cheap.

Money comes from M1.2: the charge rows that appear while an arm runs are
attributed to that execution, which is why the runner is **serial** and why no
parallel mode is offered - concurrent arms would make the attribution a guess.
The report refuses three ways of overstating what it knows: with nothing
accepted the per-accepted ratios print `-` rather than `0`; an arm whose calls
were never recorded prints `-` rather than `$0`; and a sum containing
unknown-usage calls is marked and labelled *not a billed total*.

**The blinded-review verdict now has a path back (14 September 2026).**
[`eval_review.go`](../pkg/captaincode/eval_review.go) and `captain eval review`
close the loop the harness left open: an execution that ended `pending-review`
is listed to the reviewer with its prompt, its snapshot directory and the files
it changed - enough to read the diff rather than trust a summary of it - and
addressed as `task/alias/repeat`, never by arm. A verdict names its reviewer,
because an acceptance nobody signed cannot be audited, and rewrites the result
record in place so the numerator moves with it.

The three refusals are the design. Review decides **sufficiency, never
necessity**: an execution the checks already rejected has no verdict to give,
so a reviewer cannot promote a run whose tests failed. A **second** verdict on
the same execution needs `--amend` and is stored flagged as an amendment,
because a rubric applied twice is a fact about the review rather than something
to overwrite. And an acceptance that rested on human judgement is **countable
as such**: the report's new `(reviewed)` column separates it from a
check-decided acceptance, so an arm that passes only under a friendly reviewer
cannot hide inside one acceptance rate. `RunSuite` now validates the suite it
was handed, so a suite assembled in process cannot run with an arm whose
blinding alias was never filled in.

**A fixture must now prove it measures something (14 September 2026).**
[`eval_baseline.go`](../pkg/captaincode/eval_baseline.go) and `captain eval
verify` add the gate that has to stand between the format and the corpus.
Validation proves a suite can be *replayed*; it cannot prove the suite *asks
for anything*. Those are different defects, and the second is the expensive
one: a task whose checks already pass at its pinned revision is accepted by
every arm without any work being done, so it raises all four acceptance rates
together and leaves no trace in the report. 144 executions is too late to
discover it.

So each task is now put through the run it is about to receive - clone the
pinned revision, run the setup - and its checks are run against that untouched
tree, where they must do the opposite of what they must do afterwards. At
least one check must **fail**: a failing check is the task's statement of what
is not yet true, and a suite in which none fails is `vacuous`. Every check
must also **run**: a missing command exits non-zero as well, and would read as
a healthy baseline failure while in fact rejecting every arm regardless of
what it wrote - that is `broken`, and it is the distinction an exit code alone
cannot make. The one legitimate exception is named rather than silently
allowed: a blinded task whose checks are necessary but not sufficient ends
`review-decides`, because there the verdict and not the check carries the
acceptance. A `must_not_change` guard naming a path that does not exist at the
pinned revision is reported too - it forbids nothing and protects less than the
fixture claims. `verify` spends no provider call and exits non-zero when any
fixture is not runnable, so it is the cheap step that can be required before
the expensive one.

**M1.3 pilot corpus (14 September 2026).** The 12-task pilot fixture
corpus has landed:
[`docs/eval/pilot-12.json`](../docs/eval/pilot-12.json) and the fixture
repository at `docs/eval/fixtures/captainfix/` (reproducible via
`docs/eval/fixtures/captainfix.bundle` and `setup.sh`).

The corpus spans the six families the evaluation protocol names: routine
edits (rename flag, add doc comment, add config field, fix formatting),
test repair (fix header parser, add missing test case), debugging (nil
pointer, off-by-one), multi-file changes (method rename, type extraction),
navigation (find all usages, blinded review) and review (simplify buggy
code, blinded review). 10 of 12 are check-decided; 2 are blinded-review
where the checks are necessary but not sufficient.

Each task pins to a full 40-character commit sha in a self-contained Go
module (`captainfix`) with its own git history. The fixture repo has one
"working" baseline commit and 12 task branches, each introducing a
specific defect the worker must fix. `captain eval verify` confirms
every task is non-vacuous (at least one check fails at the pinned
revision) and non-broken (all checks execute). The suite plans 144
executions (12 tasks × 4 arms × 3 repeats).

The fixture repo is reproducible: a git bundle
(`docs/eval/fixtures/captainfix.bundle`) captures all branches, and
`docs/eval/fixtures/setup.sh` clones and promotes the task branches to
local refs so the eval harness's `git clone --no-local` transfers them.
The bundle is tracked in the captaincode repo; the fixture repo's `.git`
is gitignored to avoid the nested-repo issue.

Remaining for M1.3: running the pilot (144 executions against live
providers) to produce the M1.4 baseline report. The harness, the corpus
and the verification gate are complete.

**Accounting requirements.** Count classification, director planning, workers, reviews,
retries, repair, synthesis, compaction and memory-related provider calls when they occur.
Record input/output, cached and reasoning usage where reported without double-counting
overlapping provider fields. Store raw usage provenance alongside normalized values.

Each charge/usage record must state `measured`, `estimated` or `unknown`; zero must not
mean missing. Parent aggregates must not be billed again on top of their children.
Late usage reconciles against the original attempt. Local export omits credentials and
can omit prompts; exporting private project data must be an explicit user action.

**Evaluation protocol.**

1. Start with a 12-task development pilot: routine edits, navigation, test repair,
   debugging, multi-file changes and review. Use three repeats across four baseline
   arms, giving 144 planned executions. Confirm evaluation cost before running them.
2. Grow to an initial 100-task corpus, split by repository/task family into 60 development
   tasks and 40 untouched evaluation tasks. Use pilot variance to determine additional
   sample size. This starting size is not a guarantee of statistical power.
3. Freeze repository revisions, prompts, acceptance rules, worker/runtime/model versions,
   available tools, permissions and stopping conditions. Record versions that cannot be
   pinned and stratify those results. Randomize run order to reduce load/time bias.
4. Compare direct fixed workers against the full Captain system. Also run fixed-worker
   controls through Captain to isolate harness overhead. Report incomparable tool or
   permission configurations separately; a missing baseline is not an estimated result.
5. Evaluate pristine independent snapshots with reset sessions and memory. Run warm-session
   and quota-constrained scenarios separately. Freeze learned policy during a comparison
   so previous evaluation tasks do not train later ones.
6. Establish acceptance before the run: task-specific checks, artifact constraints and
   blinded developer review where checks are insufficient. Record rejected results,
   regressions, correction minutes, extra questions and eventual corrected acceptance.
7. Report paired differences and uncertainty by category, including unsuccessful tasks,
   timeouts and coordination overhead. Add a comparable competitor only after this
   internal baseline works and its interface/access has been checked.

**Metric definitions.**

| Metric | Definition |
|---|---|
| Acceptance rate | Accepted tasks divided by all assigned tasks, under the frozen rubric and stopping policy |
| API cost per accepted task | Total API spend for the entire workload, including failed attempts and coordination, divided by accepted tasks; undefined if none are accepted |
| Time per accepted task | Sum of end-to-end task durations, including unsuccessful tasks, divided by accepted tasks; report throughput and p50/p95 latency separately for concurrency |
| Developer effort | Review/correction minutes, interventions and additional instructions; report separately from compute spend |
| Subscription economics | Fixed fees, measured quota consumption and inferred/unknown pressure in separate columns; zero incremental API spend is not a free subscription |
| Accounting coverage | Calls with measured, estimated and unknown usage, by runtime; incomplete measured totals are not presented as complete billed totals |

**Exit gate.** Clean macOS/Linux setup reaches a first verified task with no undocumented
manual fix. Every provider call has a task identity and usage status. The report can be
reproduced from its manifest and includes failures, acceptance and uncertainty. M1 may
complete with a negative economic result; in that case narrow the routing use case and
do not market savings the experiment did not show.

Before comparing policies, set a quality non-inferiority margin and economic target from
the pilot and intended use case. Predeclare the primary comparison and use paired 95%
confidence intervals, accounting for tasks clustered within repositories. Promotion needs
the acceptance-difference lower bound above the agreed negative margin and the selected
cost/time improvement to clear its agreed threshold; expand the sample if inconclusive.
An illustrative target is at least 10% lower API cost
or completion time with no more than a two-percentage-point acceptance decrease.
Those numbers are a proposed decision rule, not existing performance or a universal
tolerance; critical task categories require their own stricter criteria.

**Implementation entry points:** [ledger](../pkg/captaincode/ledger.go),
[run recording](../cmd/captaincode/brain.go), [worker runners](../pkg/captaincode/legs.go),
[doctor](../cmd/captaincode/doctor_cmd.go),
[toolchain pins](../pkg/captaincode/toolchain.go), [registry](../pkg/captaincode/registry.go).
Create evaluation tooling and schema fixtures as new work; avoid replacing the ledger
storage technology until durability/retention requirements justify it.

**Ownership:** keep this harness and its acceptance corpus in Captain. Euclid's retrieval
benchmarks can supply a separately reported memory ablation; ADI provider repeatability
scores do not replace these baselines. Store caller-reported memory costs once in the
task ledger, without charging a journal's copy of those figures again.

**M1.3 execution timing fix (14 September 2026).** Execution records now retain
elapsed time on every return path, including setup errors and worker timeouts.
Regression tests exercise real snapshots and commands, JSON export, and report
aggregation; previously the deferred update affected only a discarded local copy
and exported zero durations. Existing zero-duration results need rerunning before
they can support M1.4 timing claims. The pilot corpus and baseline remain open.

**M1.3 enforced preflight (14 September 2026).** `RunSuite` now verifies
all selected fixtures before any worker dispatch and exports successful
baseline evidence. Invalid fixtures, unknown selections and cancelled
preflight fail before provider spending; retained snapshot paths support
diagnosis. The 12-task pilot corpus and M1.4 measured baseline remain open.

**M1.5 release package (14 September 2026).**
[`pkg/captaincode/release.go`](../pkg/captaincode/release.go) and `captain
release <manifest|compat|check|report>` land the M1.5 deliverable: a
reproducible manifest, a compatibility table, extraction checks, and a
dated evidence report.

The manifest is the "what was tested" record: the captain revision (from
Go's VCS stamp), the go version, the platform, the toolchain pins, the
capability and accounting/task-api schema versions, the registry legs and
their model pins. A reader who has the manifest and the source checkout
can reproduce the binary; a reader who has the manifest and the report can
verify the claims. `captain release manifest [--json]` prints it.

The compatibility table is what a host adapter scans: one row per
transport, with the tested/min versions and every capability's support
level, read through the same `CapabilitiesFor` API the router uses.
`captain release compat [--json]` prints it.

The extraction checks are the M1.5 analog of the M1.1 rehearsal: verify
every required doc exists (INSTALL.md, ROADMAP.md, eval README, TASK_API
compatibility, TASK_MCP contract, pilot corpus, fixture bundle), the
toolchain pins are self-consistent, every transport is covered, every
transport has capability declarations, and the build identity is
reproducible (not dirty). `captain release check` runs them; `captain
release report [--json]` produces the full dated evidence report combining
manifest, checks, and compat table.

Remaining for M1.5: the dated evidence report is the structure; the M1.4
baseline report (144 live executions) fills it with data. The release
package is complete as an artifact; the numbers are pending.

## M2 — Strengthen routing controls

**User outcome:** understand why Captain chose a worker and know what further work it may start.

| ID | Work package | Concrete deliverable | Owner |
|---|---|---|---|
| M2.1 | Decision evidence | Extend `captain why` with candidate ranking, exclusions, capability constraints, quality estimate/confidence, cost basis, observed duration, freshness and selected policy version | Routing |
| M2.2 | Capability registry | Versioned support for tools, context, vision, session reuse, cancellation, usage reporting and permission enforcement; select the initial frontier worker from eligible candidates | Runtime |
| M2.3 | Quota telemetry | Supported provider adapters returning source, account, observation time, reset time and remaining allowance when available; retain inferred/unknown states | Runtime |
| M2.4 | Shared resource controller | One budget authority for root task, nested workflows, parallel workers, director/review calls and retries; reserve before dispatch and reconcile afterward | Routing |
| M2.5 | Bounded escalation | Objective check failure or explicit rejection can trigger one repair and one escalation initially; configurable shared attempt/time limits and recorded stopping reasons | Both |
| M2.6 | Open decision-leg backend | Any System One-shaped endpoint serves the decision leg, with or without a key; a conformance suite per capability, and a calibration that refuses to pool two backends | Routing |

**M2.1 status (14 September 2026).** The decision record has landed:
[`pkg/captaincode/decision.go`](../pkg/captaincode/decision.go) defines a
versioned `Decision` - the ranked field with every candidate's terms, the
exclusions with the reason each was ruled out, the policy snapshot that ranked
them, and the M1.2 task identity its charges hang off - and `captain why`
prints it above the call tree.

Three things were missing and each was its own kind of silence. **Exclusions**
did not exist: `ValueRank` dropped a sub-τ candidate with a bare `continue`,
and the hard constraints (frontier-class, `CAPTAIN_LEGS`, no vision, cooling
down) filtered legs before any score was computed, so a leg that was never
eligible and a leg that lost on points were equally absent from the record.
Those are different answers to "why not the leg I expected", and captain could
give neither. Excluded candidates are now kept, marked with their reason and
sorted after every eligible one; `Legs()` drops them, so the try-order the
router walks is unchanged.

**The quality number arrived without its evidence.** A blended 8.2 from two
scored runs and a blended 8.2 from forty are not the same claim - the prior
counts as five pseudo-observations, so the first is mostly prior - and the
ranking printed only the result. Each candidate now carries its prior, its
scored-run and total-run counts, its observed duration and the date of its
last run, because a ranking built on a leg's numbers from three weeks ago is
also a different claim from one built on yesterday's. `LegStats.LastAt` dates
that evidence from **failures** as well as successes: a leg whose last three
runs all failed is not stale, it is freshly bad.

**The policy was unnamed.** Every term weight, the good-enough threshold and
both normalisation references are environment-overridable, so two installs
with different `CAPTAIN_VALUE_*` settings wrote indistinguishable rationales
for materially different decisions. `PolicyFor` snapshots them into a
fingerprint stored with the decision, which is what makes a ranking
reproducible rather than merely readable.

The record is assembled where the choice is made and parked until the turn
that executes it resolves its task identity - it may **not** mint one, because
a turn the deterministic triage fast-paths asks no model and must not leave a
task row claiming it spent something. It is consumed on attachment and expires
on the same TTL as the route-time identity, so a repeat of the same prompt is
a new decision rather than the last one's ranking explaining this turn. The
selection path is recorded as what it was - `value`, `ladder`, `director`,
`forced` - because a fast-pathed choice and a considered one are not the same
event, and a report that conflates them cannot say whether the policy was
exercised at all. On the director path the record carries the **menu** the
director was handed, scored but with τ cleared: a leg the director was offered
was eligible for it, whatever the cheap path's bar would have said.

**M2.2 status (14 September 2026).** The capability registry has landed in
[`pkg/captaincode/capability.go`](../pkg/captaincode/capability.go), and with
it M2.1's remaining item: capability constraints are now first-class
exclusions, so the gate no longer has only the word *vision*.

A leg's runtime facts were previously one boolean. Whether a session could be
resumed, whether a run in flight could be stopped, whether the runtime
reported usage at all, whether captain could constrain the worker's tools for
a single run - each of those decided real behaviour (an accounting row marked
`unknown`, a cancellation that had to fall back to killing a process) and none
could be asked for, recorded, or ranked on. Seven capabilities - `tools`,
`vision`, `session-reuse`, `cancel`, `usage`, `cost`, `permissions` - plus the
context window are now declared, versioned (`CapabilityVersion`) and readable
through `captain legs caps`.

They are declared **per transport**, because that is where they are true: a
capability belongs to the thing that drives the model, not to the model.
Repinning grok to another opencode model changes nothing on the table; moving
it to `claude -p` changes four rows. Vision and context are the exceptions and
are read from the registry, because only the model can answer them.

Three properties keep the table from being decoration. Support is **three-
valued**, and *unknown* is a real answer that never satisfies a requirement: a
runtime that reports cost for some providers and not others is unknown, and a
task needing a measured price is not served by a maybe. Every fact carries its
**source** - `transport` for this build's contract, `registry` for the model's
own facts, `overlay` for the `caps` block an operator writes in legs.json,
which wins because someone running a patched CLI knows something this build
does not. And a `no` carries its **reason**, so the exclusion a decision
record shows names the capability *and* the transport that answered.

`Requirements` (vision, a context floor, a list of required capabilities) is
the ask; `Missing` returns the exclusion sentence or nothing. The cheap path's
hard-constraint gate now asks it instead of testing vision, so any runtime
reason a leg cannot serve a task is recorded rather than silently filtered.
The frontier section is selected the same way (`FrontierLegsFor`): a frontier
leg that cannot see the screenshot the task is about is the wrong first
attempt however well it ranks. Both fall back rather than starve - a
capability floor that nothing meets narrows the choice back to the full field
instead of leaving `/frontier` with nothing to run, and `FrontierChainFor`
orders capable legs first while dropping none, because a chain that ran out of
links is worse than a last attempt that may answer partially.

**M2.2 capability probes (14 September 2026).**
[`pkg/captaincode/capability_probe.go`](../pkg/captaincode/capability_probe.go)
and the `caps` section in `captain doctor` close the remaining item: the
capability declarations are now exercised against the installed binary's
`--help` output, the same way toolchain.go probes `--version`.

For each transport, every capability declared `yes` is matched to a flag or
subcommand the binary must print: `--permission-mode` for Claude's
CapPermissions, `--sandbox` for Codex's, `--trust` for Cursor's, `serve` for
opencode's CapTools/CapSessionReuse. A flag the binary no longer mentions is
reported as `missing` with the probe command that failed, so a decision
record's capability claim is not trusted blindly. Capabilities that are not
CLI-probable - CapCancel (process kill), opencode's server-level CapUsage and
CapCost - are marked `skip` rather than silently omitted, so a reader
distinguishes "verified" from "not checked". A missing flag does not block a
leg; the version contract's `old`/`mismatch` gates already handle that. The
report prints a summary line (`N verified, N missing, N skipped of N probed`)
and each missing row with its transport, capability and the command that
failed.

**M2.3 status (14 September 2026).** Quota telemetry has landed in
[`pkg/captaincode/quota.go`](../pkg/captaincode/quota.go), and with it the
structured answer to "how much quota does this leg have left, and how do we
know?" that neither the cooldown timer nor the accounting schema could give.

A leg's quota state is now a versioned `Quota` observation: source (who said
it), account or tier, observation time, reset time, remaining allowance and
limit, each stamped with a three-valued status - **measured** (the adapter or
rate-limit message reported it), **inferred** (the cooldown window implies it),
or **unknown** (nothing reported). The same silence-as-zero bug M1.2 exists to
prevent applies here: a leg whose remaining allowance was never reported is
unknown, not "0 remaining", and `Exhausted` returns false for unknown, because
treating an unmeasured leg as exhausted would starve a healthy one.

Quota is **observed, not polled**. A rate-limit message is the one event every
adapter produces that carries real quota state: the window closed, and here is
when it reopens. `QuotaFromRateLimit` derives a measured observation from the
existing `RateLimitError` - remaining=0, reset time from the message, tier as
account - and `onWorkerError` records it before applying the cooldown, so a
single rate-limit hit leaves both a cooldown (for the routing gate) and a quota
observation (for the report). `QuotaFromCooldown` infers the same state from
a cooldown entry for legs that were benched without a parseable message.

The ledger stores one observation per leg (`Quotas map[Leg]Quota`), and the
update rule is the same as `reconcile` for charges: a newer observation
replaces an older one, a measured observation wins over an inferred one at
equal timestamp, and a newer inferred observation **must not** downgrade a
measured one - the adapter's own report is always better than our derivation.
`captain quota` now prints a headline summary ("N available, N exhausted, N
unknown") above the per-leg detail, each line naming its status, its source,
its reset time, and whether the observation is stale (older than 6h). A stale
observation is still readable - it is what the system last learned - but it is
marked so a reader does not treat it as current.

**M2.3 proactive quota gate (14 September 2026).** The routing gate now
excludes legs whose quota is measured-exhausted before dispatch, rather
than discovering it after a failed call.
[`pkg/captaincode/quota.go`](../pkg/captaincode/quota.go) adds
`QuotaFromHeaders` — parsing `x-ratelimit-remaining-*`, `x-ratelimit-limit-*`,
`x-ratelimit-reset`, `retry-after`, and Anthropic's
`anthropic-ratelimit-*-remaining` from HTTP response headers into a measured
`Quota` observation — and `QuotaLow`, the proactive steering signal that
reports whether a leg's remaining allowance is below a threshold (default 5
or 10% of the limit) without treating an unknown or stale observation as low.

The routing gate in `valueCandidates` (`brain_value.go`) now rejects legs
whose quota is measured-exhausted (with the reset time in the exclusion
reason) or below the low-quota threshold, so a leg with 3 remaining requests
out of 100 is deprioritized before dispatch rather than discovered after.
Unknown and stale observations do NOT exclude — treating an unmeasured leg
as exhausted would starve a healthy one, the same principle the reactive
quota gate follows.

**M2.3 adapter header capture (14 September 2026).** The wiring step is
done. [`pkg/captaincode/legs.go`](../pkg/captaincode/legs.go) now extracts
`responseHeaders` from the opencode error JSON via
`extractResponseHeaders`, carries them on a new `Result.Headers` field,
and the brain's `recordQuotaFromHeaders` calls `QuotaFromHeaders` +
`RecordQuota` after every worker dispatch — success or failure.

The opencode error JSON embeds upstream `x-ratelimit-*` response headers
inside `data.responseHeaders`. `opencodeErrorText` deliberately strips
them for classification (a 404 carrying ratelimit headers is not a rate
limit), but that stripping also discarded the quota signal. Now both
concerns are served: classification still ignores headers, and quota
telemetry reads them. A non-rate-limit error (404, 500) that carries
`x-ratelimit-remaining-requests:5` now records a measured quota
observation, so the routing gate steers around a leg that is about to
hit its limit BEFORE the window closes — the proactive polling the
roadmap named.

M2.3 is complete.

**Director piggyback and manual switching (14 September 2026).** The director
ladder's fallback used to require `directorFallbackAfter` (3) consecutive
failures before demoting — a rate-limited director burned three full timeout
cycles before the next model got a turn. A `RateLimitError` now demotes
immediately: the director's window is closed, so the next best model on the
ladder directs until the provider's reset time (parsed from the
`RateLimitError`, clamped to 6h). Generic errors still need the streak, so a
one-off JSON parse failure does not dethrone a good director.

The director is now switchable at runtime: `captain director <leg>` POSTs to
`/v1/director`, which rebuilds the director ladder with the named leg first,
resets the fallback state, and calls `SetDirector` to exclude it from the
worker ladder — no brain restart needed. `captain director` (no argument)
prints the current director, the full ladder, and whether a fallback is
active.

**M2.4 status (14 September 2026).** The shared resource controller has
landed in [`pkg/captaincode/budget.go`](../pkg/captaincode/budget.go), and
with it the aggregate ceiling that the per-path bounds — two reroute hops,
one gate retry, one narration nudge — never had.

A root task now carries a versioned `Budget`: max attempts, max cost (tracked,
not enforced without adapter support), and max wall-time. Every provider call
the task makes — worker, reroute hop, gate repair, narration nudge, director
plan, review, synthesis — reserves one attempt before dispatch and reconciles
after, so a task that has already spent its attempts cannot multiply them by
rerouting. The check is `settled + reserved + n`, not `settled + n`: two
concurrent team workers each seeing "room for one more" must not both dispatch
past the cap. The brain's `budgetMu` guards the reserve/reconcile across
parallel workers; the ledger stores the budget so a crash with unreconciled
reservations is visible (M2: no auto-continuation; M3 adds safe recovery).

The budget is opened at `openTaskLocked`, the single point every turn passes
through — solo, team, and workflow alike. `runWorkerRerouted` reserves before
the initial call and before each reroute hop, reconciling with the actual cost
after each. A workflow's gate retry reserves against the same root task budget
before its second call. A frontier worker in a workflow stage reserves too.
When the cap refuses, the turn stops with a recorded reason rather than
dispatching past it.

**Stopping reasons are the M2.5 foundation.** Seven tokens —
`attempts_exhausted`, `cost_exhausted`, `time_exhausted`, `all_legs_failed`,
`objective_met`, `objective_failed`, `user_cancelled` — record WHY a task
stopped dispatching. The first reason wins: the original cause is what the
report needs. `captain why` prints the budget summary line beside the call
tree; `captain budget` shows every task's budget with its settled/reserved
counts and stopping reason; `captain stats` prints aggregate budget coverage.

The controller is **conservative by default**: `CAPTAIN_MAX_ATTEMPTS=0`
(unset) means unlimited — the controller tracks everything and enforces
nothing. An operator who sets `CAPTAIN_MAX_ATTEMPTS=12` activates the cap;
the tracking is already in place, so the decision is made from real numbers,
not a guess. This matches the roadmap's exit gate: fault tests cover
concurrent reservations, missing/late usage, rejected calls, and cancellation.
A hard dollar ceiling remains tracked-not-enforced: no current adapter can
bound the cost of an entire delegated run including internal tool turns, so
the dollar cap is a report column, not a gate.

**M2.4 admission/strict-mode (14 September 2026).** The distinction the
resource invariants named — "a hard dollar ceiling is offered only when
the adapter can enforce an upper bound… otherwise label an
admission/estimated budget or reject that adapter in strict mode" — is
now enforced.

A `Budget` carries a `Mode` field: `admission` (the default) or `strict`.
In admission mode a cost cap is tracked for visibility and not enforced,
because no current adapter can bound the cost of an entire delegated run
including internal tool turns and late charges. In strict mode
(`CAPTAIN_STRICT=1` with `CAPTAIN_MAX_COST > 0`), legs whose transport
cannot report per-turn cost are rejected at the routing gate before
dispatch: `CostEnforceable` checks `CapCost` in the M2.2 capability
registry, and `valueCandidates` excludes a leg that cannot be held to a
dollar cap. Claude CLI (`total_cost_usd` on the result) passes; opencode
(CapCost unknown, absent for subscription rosters) and Cursor (CapCost no)
are rejected with a named reason in the decision record.

Strict mode without a cost cap is still admission: there is nothing to
enforce, so rejecting cost-incapable legs would starve the field for no
reason. `captain budget` and `FormatBudget` display the mode alongside
the cost cap, so a reader distinguishes "tracked, not enforced" from
"strict: cost-reporting legs only." 10 tests cover the mode logic,
CostEnforceable for each transport, env construction, and format output.

**M2.5 status (14 September 2026).** Bounded escalation has landed in
[`pkg/captaincode/escalation.go`](../pkg/captaincode/escalation.go), and
with it the policy the budget controller's stopping reasons were the
foundation for.

A worker that SUCCEEDS — the provider ran, the output was not narration —
but FAILS AN OBJECTIVE CHECK (a workflow gate, an acceptance check, or an
explicit rejection) is a different failure from a provider going down.
`runWorkerRerouted` handles the provider's fault; escalation handles the
work's. The policy bounds what happens after the objective fails: one
**repair** (retry the same leg with the gate's failure output) and one
**escalation** (move to a stronger leg, found through the perf-ranked
frontier chain), then stop.

`EscalationPolicy` is the type, `DefaultEscalationPolicy` reads
`CAPTAIN_MAX_REPAIRS` (default 1) and `CAPTAIN_MAX_ESCALATIONS` (default 1).
Zero means "no retries of this kind" — the worker's first objective failure
is final. `CanRepair(repairsUsed)` and `CanEscalate(escalationsUsed)` gate
each step; `NextEscalation(failed, tried, allowed, cooldowns, now)` finds the
next stronger leg that is not tried, not cooled down, and allowed under
`CAPTAIN_LEGS`. When the bounds are exhausted the task stops with
`StopObjectiveFailed`; when the budget refuses, it stops with the budget's
own reason (first wins).

The workflow gate retry now exercises the full sequence. The existing one
repair (retry on the same leg with the gate output) was already in place;
the escalation step is what M2.5 adds. After the repair fails the gate,
the brain reserves one more attempt, finds a stronger leg, runs the same
stage prompt with the gate's failure output, and checks the gate again.
A pass replaces the result; a second failure stops the task with
`StopObjectiveFailed` and delivers the failure knowingly to the review.

The `slot` struct now carries `escalated` beside `retried`, so the event
record distinguishes a leg that was repaired from one that was escalated,
and `captain why` can say "gate failed on grok, repaired on grok, escalated
to claude" rather than "gate failed".

**Decision behavior.** Hard constraints filter candidates before quality/cost/latency ranking.
A cheap initial attempt is not worthwhile if its expected repair cost exceeds starting
with a stronger worker. Initially estimate this from M1 data; do not disguise priors as
calibrated probabilities. Show estimated p50/p95 duration only where samples support it.

Keep explicit leg selection. Migrate `/frontier` deliberately: explain that it becomes a
capability policy, preserve a way to request Claude explicitly and version saved workflow
semantics. A fallback below the requested quality floor requires a declared policy; never
silently label an economical substitute as equivalent frontier work.

**Resource invariants.**

- Use a root task budget shared across all nested work. Child limits cannot exceed the
  parent, and callers cannot obtain fresh funds by reconnecting or retrying a request.
- Before concurrent dispatch, atomically require settled cost plus outstanding worst-case
  reservations to fit the cap. Reconcile the reservation once; handle duplicated or late
  usage reports without releasing the same reservation twice.
- Persist outstanding reservations before dispatch. In M2, a crash with unreconciled
  work prevents automatic continuation; M3 adds safe recovery of that state.
- A hard dollar ceiling is offered only when the adapter can enforce an upper bound
  across the entire delegated run, including internal tools/model turns and late charges.
  Otherwise label an admission/estimated budget or reject that adapter in strict mode.
- Missing usage is not zero. Retain a conservative reservation or stop further dispatch
  when a usable bound is unavailable. Cancellation does not retroactively refund work.
- Subscription workers use enforceable attempt, concurrency and wall-time limits plus
  quota evidence where available. A preference such as `/cheap` remains distinct from a cap.
- Provider retry, gate repair, stronger-worker escalation and user-requested repetition
  all draw from the same root limits. Nested routers must not multiply retry allowances.

**Exit gate.** Fault tests cover concurrent reservations, missing/late usage, rejected
calls, provider limits, nested retries, cancellation and process restart. No dispatch
violates the documented invariant. Unsupported strict-mode workers fail admission with
a reason. Escalation improves held-out acceptance on initially failed tasks within the
frozen resource envelope; the complete economic report remains visible.

**Implementation entry points:** [value ranking](../pkg/captaincode/value.go),
[brain routing/exploration](../cmd/captaincode/brain_value.go),
[triage](../pkg/captaincode/triage.go), [performance ranking](../pkg/captaincode/perf.go),
[registry](../pkg/captaincode/registry.go), [rerouting](../cmd/captaincode/brain.go),
[frontier execution](../cmd/captaincode/brain_team.go).

**Ownership:** execution budgets and capability-based frontier selection stay in Captain.
Euclid's context/token limits bound retrieved memory, not account spending. An optional
Atlas mandate is an additional action constraint; it does not replace Captain's resource
controller or imply that a worker's unobserved tool calls were mandate-enforced.

**M2.6 status (20 September 2026).** The decision leg stopped being one
vendor.

`CAPTAIN_SYSTEMONE_URL` can now be keyless: any System One-shaped endpoint
serves the decision leg with or without a credential, so an open
re-implementation on loopback, or a model served on a leg captain already
has, answers the same typed questions for nothing. Captain still works with
no decision leg at all - triage falls back to the heuristics and the free-leg
classify - and works with a key exactly as before. The keyless URL is the
third configuration, not a replacement for either.

Two things follow, and both are now enforced rather than hoped for.

`captain jev conform` asks a fixed suite whose answers are not in doubt - a
one-word typo fix is trivial, `rm -rf` on an uncommitted directory is
destructive, a worker eight minutes into one `cargo build` is not stuck - and
reports usability PER CAPABILITY (triage, route, keep, gate, supervise),
because a backend can be fine at one and useless at another; a lexical scorer
handles keep/drop and cannot rank legs. It is a smoke test and says so: a
passing suite means a backend is not broken, never that captain should route
on it.

The numbers that decide that come from the shadow, and they are now per
backend. Every shadow row stamps which implementation and which versioned
model answered, and `captain jev shadow` DECLINES to suggest a bar when a
point's rows came from more than one - `jev-latest` is an alias that moves
under the record, and an open re-implementation is a different model
entirely. A pooled bar would be a number no single configuration ever
produced.

**M2.6, second pass (21 September 2026): a backend beside the first, not
instead of it.**

`CAPTAIN_SYSTEMONE_URL` replaces the decision leg, which is the right shape
when there is no key and the wrong one for what turned up. laya-mlx is an
Apache-2.0 MLX port of the Laya typed-decision models that answers in the same
wire shape - the same three question types, the same `{model, answers, usage}`
envelope - in 7-14ms on Apple silicon, free after the download, holding 512
tokens (1,024 on two of its checkpoints). jev is slower, costs $0.042/M input,
holds 32k, and runs wherever there is a key. Neither is the other's
replacement, so `CAPTAIN_SYSTEMONE_OPEN_URL` names a sidecar that runs BESIDE
the primary client, and `sidecars/laya/serve.py` is one in about 200 lines.

jev stays the default: it keeps the action gate, the supervisor and
compaction, it answers wherever there is no Apple silicon, and it takes every
call whose state the sidecar cannot read whole.

Two rules carry the design, and neither is advice.

A bar is not transferable. jev's bar was read off jev's rows, so a sidecar
decides NOTHING when it is configured: it answers the triage and routing
questions beside every turn captain was already unsure about, lands them on
rows of its own stamped with its host, costs nobody anything, and is compared
against what captain actually did. `captain jev shadow --backend <host>` reads
those rows alone - the pooling check was right to refuse a mixed sample, but
once two backends answer on purpose every point is mixed for good, so the rows
had to become separable and not only detectable. The bar that report prints is
what promotes the backend, and `CAPTAIN_SYSTEMONE_OPEN_FOR=triage=0.85` is the
only way to promote one: there is no flag that skips naming the number.
Underneath its own bar a promoted sidecar is dropped exactly as jev is.

A truncated state is not a small state. laya's sequence builder CUTS a state
that overruns `max_len` and answers anyway, so a 512-token backend handed the
action gate's state would return a confident reading of two thirds of a
command - worse than no answer, and invisible. The sidecar therefore counts
tokens and refuses with `400 max_tokens_exceeded`, the status the vendor
already uses; captain estimates the size before dispatching and sends
oversized calls to the backend that holds them; and `gate`, `supervise` and
`keep` are not promotable at all, because the first two send the largest
states captain produces and the third decides what compaction drops.

## M3 — Make execution dependable

**User outcome:** parallel work produces reviewable changes and interruptions preserve progress.

| ID | Work package | Concrete deliverable | Owner |
|---|---|---|---|
| M3.1 | Worker isolation | Worktree per concurrent writer, pinned base revision, explicit read/write/project scope, serialized fallback when isolation is unsupported | Runtime |
| M3.2 | Artifact and integration contract | Immutable patch/output manifests, changed-file/test evidence, conflict detection and a reviewed integration candidate before user-branch changes | Runtime |
| M3.3 | Durable lifecycle | Persist task/stage/attempt states, reservations, process/session identities, checkpoints and artifact references before acknowledging transitions | Both |
| M3.4 | Cancellation and recovery | One cancellation tree for model calls, subprocesses, gates, review and nested work; startup reconciliation and explicit resume policy | Runtime |
| M3.5 | Structured handoffs | Compact brief containing requirements, completed work, verified artifacts, failed checks, remaining actions and uncertain side effects | Routing |
| M3.6 | Action gate | A calibrated classifier screens what a worker is about to do, because a headless fleet has nobody to answer an approval prompt; shadowed first, enforced only against its own record | Runtime |
| M3.7 | Borrowed isolation | An `ax`-transport leg for deployments that have a cluster, and the portable half of its Gateway - an outbound allowlist on the egress proxy - for the ones that do not | Runtime |
| M3.8 | `/btw` on codex | `codex app-server` (`thread/start` → `turn/steer`) instead of `codex exec`, so a mid-run note reaches a codex worker the way it reaches claude and opencode | Runtime |
| M3.9 | Vetted skills at bootstrap | A pinned, hashed set of Agent Skills staged from first-party catalogs and placed in the worker's worktree per task, so a worker starts holding the repeatable procedure instead of rediscovering it | Runtime |

**M3.1 status (14 September 2026).** Worker isolation has landed in
[`pkg/captaincode/worktree.go`](../pkg/captaincode/worktree.go): a git
worktree per concurrent writer, at the pinned base revision, with serialized
fallback when isolation is not possible.

A workflow stage or team plan that runs multiple workers in PARALLEL against
one workspace directory is a data race on the user's files: two workers
editing the same tree can stomp each other's changes, and a gate that runs
after one worker may see another's half-written output. The worktree gives
each concurrent writer its own checkout at the same base revision, so the
isolation is filesystem-level and not a prompt instruction the worker may
ignore.

`NewWorktree` creates a detached worktree at a pinned commit sha. The repo
root is resolved through `git rev-parse --show-toplevel`, so a worker whose
workspace is a subdirectory of the repo (`cmd/captaincode`,
`packages/opencode/src`) is still isolated — checking for `.git` in the
worker's own directory would have missed every subdirectory case.
`IsolateWorkers` creates N worktrees all-or-nothing: a stage that needs
three and gets two cannot run three isolated workers, so the two are cleaned
up and the stage falls back. `Close` uses `git worktree remove --force`,
falling back to `os.RemoveAll` + `git worktree prune` when the directory was
already moved or deleted. `CloseAll` attempts every close even if one fails,
because a leaked worktree is a disk leak the user will not notice until git
complains.

The integration is in both parallel execution paths. In
[`brain_workflow.go`](../cmd/captaincode/brain_workflow.go), a stage with
more than one leg calls `IsolateWorkers` before dispatch; on success each
worker gets its own `Workspace` (preserving `Effort` and `Brains` from the
original), the per-worker gate runs in that worktree, and the worktrees are
cleaned up after `wg.Wait()`. On failure the stage **serializes**: the same
`runSlot` function runs each worker one at a time in the shared workspace —
slower, but safe. The same pattern is wired into
[`brain_team.go`](../cmd/captaincode/brain_team.go): a team plan with
multiple workers isolates when possible and serializes when not. A
single-worker stage or team does not create a worktree at all.

The source repository is only ever read: `git worktree add` clones from it
without writing, so the user's dirty worktree and untracked files survive a
parallel run untouched — the same property `eval.go`'s snapshot enforces for
evaluation runs. Each worker's changes live in its own worktree and are
visible in that directory for review or artifact extraction (M3.2's
foundation).

**M3.1 scope contracts (14 September 2026).**
[`pkg/captaincode/scope.go`](../pkg/captaincode/scope.go) adds the
`ScopeContract` type the remaining item asked for: five dimensions
(write-paths, read-paths, network, subprocess, tool-set), declared per
transport the same way `transportCaps` are, with the same three-valued
enforcement (yes / no / unknown) and the same rule — unknown is never
read as yes.

`ScopeRequest{Level: "write-paths", WritePaths: ["src/"]}` is what a task
asks for; `CheckScope(contract, req)` returns nil when the transport can
enforce it or the first `ScopeViolation` when it cannot. A strict request
(`Level: "strict"`) requires all five dimensions. Cursor, whose `--trust`
and `--force` control approval but not path scope, is rejected for any
level above full. Claude and Codex pass write-paths (permission-mode plan
and `--sandbox` respectively) but fail strict (no network sandbox). The
check is conservative: unknown enforcement is treated as cannot-enforce,
because relying on a constraint the adapter may not enforce is worse than
refusing the adapter and picking one that can. 11 tests pass.

**M3.2 status (14 September 2026).** The artifact and integration contract
has landed in [`pkg/captaincode/artifact.go`](../pkg/captaincode/artifact.go):
when parallel workers finish in their isolated worktrees (M3.1), their
file changes are no longer discarded. Each worker's changes are captured as
an immutable `PatchManifest` — the files it touched, a sha256 digest of its
diff, the base revision, the worker leg, and the check evidence from the
gate — before the worktree is closed. The diff is saved to a file so the
manifest is a reference, not a container.

A `BuildIntegrationCandidate` collects the manifests from one stage's
workers and classifies what they produced. File-level conflict detection
finds two or more workers that changed the same file: they conflict even
if their edits are compatible, because the review — not the harness —
decides whether a mechanical merge is safe. A worker that answered in text
only (no file changes) is not a conflict; it is recorded as such. Three
statuses carry the result: `clean` (no conflicts, changes can be merged),
`conflicted` (same file touched by multiple workers), `empty` (no worker
produced any file changes).

The workflow executor captures manifests from each isolated worktree before
`CloseAll`, builds the integration candidate, and reports its status
(including per-conflict file and worker list) in the progress feed. The
candidate is stored on the brain so `captain why` and the director review
can read what each worker changed and whether they conflicted. Gate check
evidence (command, exit code, passed, output) is attached to each manifest
via `RecordCheck`.

**M3.2 test evidence capture (14 September 2026).**
[`pkg/captaincode/artifact.go`](../pkg/captaincode/artifact.go) adds
`CaptureTestEvidence`, which auto-detects the project's test suite from
manifest files (`go.mod` → `go test ./...`, `package.json` → `npm test`,
`pyproject.toml` → `pytest`, `Cargo.toml` → `cargo test`, `Makefile` →
`make test`) and runs it in the worker's isolated worktree before it closes.
The result is stored as `PatchManifest.TestEvidence` — a `CheckEvidence`
with command, exit code, pass/fail, and output — distinct from the gate
check because the gate is a specific acceptance command while the test
suite is broader evidence that the changes did not break existing tests.

The workflow executor calls `CaptureTestEvidence` after capturing each
manifest and before closing the worktree, so every parallel worker's test
result appears in the integration candidate. `IntegrationCandidate.Summary`
now includes a test count (`tests 2/2 pass` or `tests 1/2 FAIL`), and
`TestCounts` aggregates the evidence for the handoff brief and report. Nil
when no test suite is detected — not a failure, just no signal.

**M3.2 semantic conflict detection (14 September 2026).**
[`pkg/captaincode/artifact.go`](../pkg/captaincode/artifact.go) adds
`DetectSemanticConflicts`, which finds cross-worker dependency relationships
that file-level conflict detection misses: worker A changes `types.go`,
worker B changes `handler.go` which imports `types.go` — no file overlap,
but their changes are semantically coupled because a signature change in
`types.go` may break the usage in `handler.go`.

The function reads each manifest's changed files from its `WorktreeDir`
(while worktrees are still open), parses import statements for Go (using
the module path from `go.mod`), JS/TS (relative `import`/`require`), and
Python (relative `from .module import`), resolves them to repo-relative
paths, and checks whether any resolved dependency was changed by a different
worker. For Go, directory-level resolution handles the fact that an import
path maps to a package directory, not a specific file — any changed file
under that directory counts.

A `SemanticConflict` record is advisory: it does NOT change the integration
status (file-level conflicts still drive `conflicted`), and a clean
candidate with semantic conflicts is still applicable via
`ApplyIntegrationCandidate`. The review sees the coupling the file-level
check cannot, but the harness does not block on it — the review decides
whether a mechanical merge is safe. The workflow executor prints
`[semantic]` lines alongside `[conflict]` lines in the progress feed, and
the integration candidate's `Summary()` includes a semantic count.

`BuildIntegrationCandidate` now builds the candidate before worktrees close
(moved above `CloseAll` in `brain_workflow.go`) so semantic detection can
read the changed files. 6 tests cover Go cross-package imports, JS/TS
relative imports, unrelated files (no semantic conflict), synthetic
manifests without worktree dirs, summary output, and that semantic
conflicts do not block apply.

**M3.2 integration apply (14 September 2026).** A clean integration
candidate is now applied to the user's workspace as a reviewed merge.
`ApplyIntegrationCandidate` in
[`pkg/captaincode/artifact.go`](../pkg/captaincode/artifact.go) replays each
manifest's saved diff into the target directory via `git apply`, so parallel
workers' isolated changes land as uncommitted working-tree changes the user
can review, stage or discard — not silently discarded after the worktrees
close.

The apply fires after the director review passes, not before: a conflicted
or empty candidate is refused, so the review's verdict is the gate. The diff
capture now includes untracked (new) files via `git add -N` before diffing,
so a worker that created a new file is applied as faithfully as one that
modified an existing one. The apply is all-or-nothing per manifest: a diff
that does not apply cleanly stops the whole candidate rather than leaving
the user's tree in a half-merged state.

**M3.3 status (14 September 2026).** The durable lifecycle has landed in
[`pkg/captaincode/lifecycle.go`](../pkg/captaincode/lifecycle.go): the
execution state of every task and attempt is now persisted in the ledger
alongside the charges, budgets and decisions that were already there.

The lifecycle contract defines nine states and the legal transitions among
them: `admitted` → `running` → `waiting_for_input` → `cancel_requested` →
`interrupted` → terminal (`succeeded`, `failed`, `cancelled`, `exhausted`).
A terminal attempt never silently becomes a new attempt — `CanTransition`
rejects it. The state machine is the gate: `TransitionAttempt` and
`TransitionTask` enforce it, and a task cannot go terminal while any of its
attempts are non-terminal.

`AttemptState` carries what the charge tree does not: the brain `ProcessID`
that owns it, an `OwnerGen` ownership generation that increments on each
claim, the provider `SessionID` for reuse, the `WorktreeDir` and `DiffPath`
artifact references from M3.1/M3.2, and a `ParentAttempt` causal link for
resumption. Two brain processes cannot resume the same attempt:
`ClaimOwnership` increments the generation, and `VerifyOwnership` rejects a
stale holder. A terminal attempt refuses ownership claims entirely.

`ReconcileOnStartup` walks the persisted states on brain start and marks
any that were `running`, `waiting_for_input`, or `cancel_requested` as
`interrupted` — the process that owned them is gone. M3.3 makes the
interrupted state visible; M3.4 decides whether to resume. The
reconciliation is idempotent: a second startup pass finds nothing to
interrupt. `InterruptedAttempts()` returns the candidates for recovery.

The brain now mints a `TaskState` (admitted) at `openTaskLocked`, the
single point every turn passes through; transitions it to `running` when
`chargeTurn` dispatches; and marks both attempt and task terminal in
`recordRun` via `completeAttemptLocked`. The brain's `processID` is stamped
on every attempt it opens, so a restart can distinguish its work from a
predecessor's.

`LifecycleCoverage` returns aggregate state counts for `captain stats`,
the same pattern `BudgetCoverage` and `AccountingCoverage` follow.

Remaining for M3.3: persisting checkpoints beyond artifact refs (worktree
state for M3.4 recovery). The workflow and team paths now mark task/attempt
terminal via `completeWorkflowTask`, and the `captain lifecycle` CLI view
is available.

**M3.3 checkpoint persistence (14 September 2026).**
[`pkg/captaincode/lifecycle.go`](../pkg/captaincode/lifecycle.go) adds
checkpoint fields to `AttemptState` and a `Checkpoint`/`ReconcileCheckpoints`
pair that gives M3.4's recovery policy what it needs beyond artifact
references.

`AttemptState` now carries `BaseRevision` (the commit sha the worktree was
created from), `CheckpointPhase` (what the attempt was doing when last
checkpointed: `dispatching`, `producing`, `gating`, `reviewing`),
`CheckpointAt` (when), and `PartialOutput` (truncated text the worker
produced before interruption). The brain calls `Checkpoint` at the dispatch
boundary in `runWorkerRerouted`, stamping `PhaseDispatching` before the
worker call begins.

`ReconcileCheckpoints` walks interrupted attempts after a brain restart and
verifies whether their worktrees and diff files still exist on disk — paths
on `AttemptState` are references, but references are not survival. A
worktree that was manually removed or a temp dir that was cleaned is
reported as `missing`, so the resume policy does not assume it can recover
from a worktree that no longer exists. The brain prints the checkpoint
status on startup alongside the resume report.

M3.3 is complete.

**M3.4 status (14 September 2026).** The cancellation tree and recovery
policy have landed in [`pkg/captaincode/cancellation.go`](../pkg/captaincode/cancellation.go),
and with it the one cancellation signal M3.3's state machine was waiting
for.

A `CancelTree` is a process-local registry of in-flight cancellable work,
keyed by task ID. Every piece of work a task owns — a worker call, a gate,
a review, a subprocess — registers its context under the task. `Cancel`
fires them all, and `RegisterChild` links nested work (a workflow stage
under the workflow's root task, a worker under its stage) so cancelling a
root task cascades to every descendant. The tree is deliberately NOT the
lifecycle state machine: the state machine records what happened and
enforces legal transitions; the tree delivers the signal that makes it
happen. Cancelling transitions the lifecycle through `cancel_requested` →
`cancelled`, but the tree itself owns no state — it owns the wire.

The brain registers workflow and team root contexts at dispatch, so
`captain cancel <taskID>` (or `POST /v1/cancel?task=<id>`) reaches every
worker, gate and review beneath a task. `completeAttemptLocked` also
drops the task from the tree when all attempts are terminal, so a
finished task leaves no stale cancel registrations behind. `CancelAll`
exists for brain shutdown.

The **resume policy** is conservative by design.
`EvaluateInterrupted` walks the ledger's interrupted attempts after
`ReconcileOnStartup` and applies `ResumePolicy.Decide`: recent
interruptions (within 24h) are surfaced for user review; stale ones
(beyond `MaxAge`) are abandoned (transitioned to `failed`). The policy
does NOT auto-resume: a provider session belongs to the dead process, a
worktree may have been manually changed, and resuming a partial result
is not the same as retrying. The brain prints the resume report on
startup, and `captain lifecycle` surfaces it on demand.

The `captain cancel` and `captain lifecycle` CLI commands expose both:
`captain cancel` lists in-flight work or cancels a task; `captain
lifecycle` prints aggregate state counts or one task's full lifecycle
with its attempts.

Remaining for M3.4: confirmation that cancelled subprocesses are actually
dead (not just signalled). The deadline types and cooperative stop mechanism
are now in place; the brain's kill fallback for processes that do not
acknowledge within their deadline is the remaining wiring step.

**M3.4 kill fallback (14 September 2026).** `CancelWithDeadline` in
[`pkg/captaincode/cancellation.go`](../pkg/captaincode/cancellation.go)
now fires the cancel signal, waits the ack timeout, then checks any
registered processes for liveness via `signal(0)` (the POSIX existence
check). A process still alive after the deadline is killed with
`Process.Kill()` (SIGKILL) and reported in `CancelResult.Pending`. The
`RegisterProcess` method stores an `*os.Process` alongside the cancel
func, so the tree can confirm death rather than trusting the signal.

The brain's `cancelTask`, `cancelTaskHTTP`, and `taskCancel` (task API)
now all route through `cancelTaskWithDeadline`, which fires
`CancelWithDeadline` with `DefaultCancelDeadline()` (1s ack, 10s stop)
before transitioning the lifecycle. `captain cancel` reports the
`pending` list (processes killed after deadline) alongside the cancelled
labels, so a reader distinguishes "stopped cleanly" from "had to be
killed."

**M3.4 explicit resume (14 September 2026).** `captain resume <taskID>`
and `POST /v1/resume?task=<id>` land the explicit resume action the
conservative policy was waiting for.
[`ResumeTask`](../pkg/captaincode/lifecycle.go) creates a **new**
`AttemptState` under the interrupted task, with `ParentAttempt` pointing to
the interrupted attempt it resumes from. The old interrupted attempt is
transitioned to `failed` (its process is gone); the new attempt starts as
`running` under this brain's `ProcessID`. The budget is checked before
resuming — a task that has already spent its attempt cap does not get a
free retry. `captain resume` (no argument) lists interrupted tasks
awaiting a decision, the same `ResumeReport` `captain lifecycle` surfaces.

**M3.5 status (14 September 2026).** Structured handoffs have landed in
[`pkg/captaincode/handoff.go`](../pkg/captaincode/handoff.go): when a task
finishes — succeeded, failed, cancelled, or interrupted — the brain now
assembles a compact `HandoffBrief` from the data M3.1–M3.4 already persist,
rather than leaving the developer to reconstruct it from scattered records.

The brief carries six sections, each built from a different persisted source:
**requirements** (the task prompt from the first ledger event), **work** (each
attempt's leg, stage, state and duration from lifecycle states), **artifacts**
(the changed files, diff digest and check evidence from the M3.2 integration
candidate's `PatchManifest`s), **failed checks** (the checks that did not pass,
with their command and exit code), **remaining actions** (interrupted,
waiting-for-input, or non-terminal tasks), and **uncertain side effects**
(calls with unknown usage, worktrees from interrupted attempts). The budget
summary shows attempts used, cost, and unknown-usage count — the same
accounting-coverage discipline M1.2 enforces.

The brief is built at every terminal transition: `completeAttemptLocked`
(solo turns), `completeWorkflowTask` (workflow and team turns), and
`cancelTask` (cancellation). It is stored in the ledger's `Handoffs` slice
(one per task, updated in place) and is available through the HTTP API
(`GET /v1/handoff?task=<id>`, `POST /v1/handoff?task=<id>` to rebuild) and
the CLI (`captain handoff <task-id>`, `captain handoff --build <task-id>`,
`captain handoff` to list all). `FormatHandoffBrief` renders the compact
view a developer scans: heading per section, one line per item.

The brief is deliberately NOT an acceptance decision. A check that passed
is a fact; whether that fact is sufficient is the reviewer's call (M1.3). A
side effect marked uncertain is a flag, not a command — M3.4's resume policy
still decides what to do about interrupted work. The brief is what M4's
downstream hosts read to decide whether to accept, retry, or hand off.

**M3.5 solo-turn artifact summaries (14 September 2026).**
`buildArtifactSummaries` now builds summaries from the ledger's
`AttemptState` records (WorktreeDir, DiffPath, Leg) when no
`IntegrationCandidate` exists, so solo turns that produced file changes
appear in the handoff brief alongside parallel-workflow artifacts.

Remaining for M3.5: none on the original checklist (solo artifact capture and JSON export landed).

**M3.5 solo artifact capture (14 September 2026).**
[`pkg/captaincode/artifact.go`](../pkg/captaincode/artifact.go) adds
`CaptureSoloArtifact`, which diffs the user's workspace against HEAD after a
solo worker finishes and returns the same data `CaptureManifest` produces for
parallel workers: changed files, a sha256 digest, and a saved diff file. The
brain's `recordRun` calls it after the worker completes but before the attempt
transitions to terminal, and `Ledger.RecordSoloArtifact` stamps the result on
the `AttemptState`'s new `ChangedFiles` and `DiffDigest` fields.

`buildArtifactSummariesFromLedger` in
[`pkg/captaincode/handoff.go`](../pkg/captaincode/handoff.go) now populates
the full `ArtifactSummary` — changed files, diff digest, diff path — from
those fields, so a solo turn's handoff brief carries the same artifact
evidence a parallel workflow's `IntegrationCandidate` does. A text-only solo
turn (no file changes) produces no artifact summary, the same way a text-only
parallel worker produces an empty manifest.

`buildFailedChecks` now reads from the `OutcomeEvidence`'s `CheckResult`
entries when no `IntegrationCandidate` exists, so a solo turn's failed
director assessment appears in the handoff brief's failed-checks section
alongside the artifacts. 10 tests cover solo artifact capture (no changes,
with changes, no diff dir), `RecordSoloArtifact`, and handoff enrichment
(artifact from solo, failed checks from outcome, text-only turn).

M3.5 is complete.

**M3.5 JSON export (14 September 2026).** `captain handoff <task-id>
--format json` now outputs the full `HandoffBrief` as JSON, the same structure
the HTTP endpoint returns. An M4 host can consume the brief without a custom
HTTP client: the CLI is the transport, and `--format json` is the contract.

**Lifecycle contract.** Define legal transitions among admitted, running, waiting for input,
cancel requested, interrupted and terminal states. Separate execution completion from
developer acceptance. Terminal outcomes include succeeded, failed, cancelled and exhausted
resources; a terminal attempt never silently becomes a new attempt.

Resumption creates a new attempt under the same task and remaining budget, with causal
links to the interrupted attempt. A durable ownership lease/generation prevents two
brain processes from resuming the same attempt. Saved workflow definitions, live status
and execution checkpoints are separate objects with separate lifetimes.

**Isolation and replay rules.**

- Preserve the user's dirty worktree and untracked files; do not reset them to make a
  worker start. Account for base-revision changes before applying a patch. Validate the
  integrated result, since individually passing worker branches can conflict semantically.
- A worktree isolates Git changes, not processes, credentials or network access. Expose
  each adapter's actual boundary. A strict scope request must reject an adapter that
  cannot enforce it, rather than rely on prompt instructions.
- Record command/tool intent and completion where the runtime exposes them. For opaque
  CLIs, advertise stage-level recovery only; the brain cannot invent tool-level receipts.
- Use idempotency keys for external operations where supported. If a crash occurs after
  an external action but before its result is recorded, mark it uncertain and reconcile
  or request review. Do not claim exactly-once execution of arbitrary shell commands.
- Cancel gates and reviewers as well as workers. Stop admitting child work immediately;
  terminate supported process trees within the adapter deadline. Report any external
  request whose termination cannot be confirmed.
- Preserve artifacts after cancellation/failure. Garbage collection respects retention
  and active references; cleanup must not destroy a user's only copy of unfinished work.

**Exit gate.** Test conflicting writers, dirty starting trees, interrupted merges,
disconnects, lost brain processes, worker crashes, expired sessions, quota failure before
and after partial output, and cancellation during each stage. All fixtures preserve
artifacts and the root budget. No completed side effect is silently replayed; ambiguous
operations surface for reconciliation. Duplicate resume requests create one owner.

Set adapter-specific cancellation deadlines before testing. A provisional local target
is to acknowledge within one second and stop supported child processes within ten seconds;
unsupported or still-running external work remains explicitly visible.

Measure same-runtime session reuse and compact handoffs before adding persistent
worker/reviewer pairs. Cross-provider summaries do not transfer hidden state or token
caches; publish a cache optimization only when accepted-task measurements justify it.

**Implementation entry points:** [workflow executor](../cmd/captaincode/brain_workflow.go),
[status/transcripts](../cmd/captaincode/brain_workflow_status.go),
[parallel execution](../cmd/captaincode/brain_parallel.go),
[workspace/runners](../pkg/captaincode/legs.go),
[Codex runner](../pkg/captaincode/codexcli.go),
[workflow language](WORKFLOW_LANGUAGE.md).

**Ownership:** Captain stores checkpoints, patches and attempt ownership. Euclid may retain
a bounded, source-linked lesson from a handoff; it is not the workflow state store.
Verified inference replay and sealed evidence packages belong to Trace/Atlas. Keep any
integration with them outside M3's ordinary recovery gate.


**M3.6 status (20 September 2026).** The action gate has landed in
[`pkg/captaincode/gate.go`](../pkg/captaincode/gate.go), in the shadow.

Captain runs its workers at full permission, deliberately: `claude
--dangerously-skip-permissions`, `codex
--dangerously-bypass-approvals-and-sandbox`, `cursor-agent --trust --force`,
and an opencode ruleset that allows bash and edits and DENIES `question`.
That last denial is the whole argument. A headless fleet has nobody at the
terminal, so tightening a CLI's own permissions does not buy prompts - it
buys denials, and mostly silent ones: claude auto-denies a tool whose prompt
cannot be shown, codex is pinned `approval_policy=never` and refuses rather
than asks, cursor-agent exits 1 on the trust prompt, and an opencode
`question` wedges a headless worker until its cap.

So the screening has to be captain's own, and it has to answer in the time a
tool call can afford. That is the decision leg's shape exactly. Three nouls
over the action - would this destroy something unrecoverable, does it reach
outside the work it was given, does it send this machine's contents somewhere
else - and the highest of the three is the action's risk.

Where it runs: `captain gate --hook` as a Claude Code PreToolUse hook
(installed beside the redaction hook by the same pass), and `captain gate
--tool <name>` from the opencode plugin's `tool.execute.before`, on the
RESTORED arguments, because the command that will actually run is the one
worth screening. A deterministic pre-filter keeps the cost honest: a read
(`ls`, `git status`, a `read` tool) is allowed without a call, and anything
carrying a shell operator that could hide a second command is not treated as
a read.

What it does NOT do yet, on purpose. `CAPTAIN_ACTION_GATE` defaults to
`shadow`: every screening is recorded to `~/.captaincode/gate.log` and every
action is allowed. `enforce` is opt-in, and `captain gate --report` exists to
say whether a bar means anything yet.

What settles a gate row (20 September 2026). A gate noul is a PREDICTION
about an action, so nothing captain decided beside it can settle it. The
task's own acceptance can, in one direction: a task the user accepted with no
correction and no regression contains no action that destroyed unrecoverable
work, left the assignment, or shipped the machine's contents off it, so every
screening on it settles as `false` ([`settle.go`](../pkg/captaincode/settle.go),
applied at read time - the log is append-only and written by every process
that runs a tool). The converse is refused: a rejected task says the work was
bad, not which of its forty actions was dangerous, and attributing it to all
of them would manufacture agreement out of nothing.

So the sample is one-sided by construction, and the report labels it as such.
The bar it yields bounds FALSE POSITIVES - how often a noul at or above a
floor fired on an action that turned out fine - and says nothing about what
the gate misses. That is the bar `enforce` needs, since the cost of enforcing
too early is a refused worker rather than a missed threat, but it is not a
detection rate. Screenings carrying no task identity stay uncompared for
good: the opencode workers share one `opencode serve`, so `CAPTAIN_TASK_ID`
is not in their environment.

This is what the M5.1 acceptance-evidence defect was blocking: with every
outcome pending, no task was ever cleanly accepted, so the join had nothing
to settle against.

With no decision leg configured there is no gate: no call, no added latency,
no behaviour change. That is a tested property, not an intention.

**M3.7 status (20 September 2026).** The portable half has landed; the
cluster half is scoped.

`scope.go` has always said the honest thing - a worktree isolates Git
changes, not processes, credentials or network access - and `M3.1`'s promise
of "isolated execution" has been outstanding ever since. [`google/ax`](https://github.com/google/ax)
is that substrate, already built: `Task` (sandbox with CPU/memory caps),
`Workspace` (pre-wired repos, MCP, skills), `Gateway` (an outbound host
allowlist), plus `suspend`/`resume`, which maps onto captain's durable
lifecycle. An `ax`-transport leg would satisfy M3.1's remaining line without
captain writing a sandbox. Two caveats keep it a deployment target rather
than a default: it is `v1alpha1` with breaking changes promised, and it wants
a Kubernetes cluster.

The `Gateway` idea is portable on its own, and the egress proxy is where it
belongs: `CAPTAIN_EGRESS_ALLOW` narrows the proxy's upstream set to the
providers a machine is meant to talk to, refused at the socket rather than
billed. Stated plainly because the opposite would be worse than nothing: this
bounds captain's OWN model traffic. It is not a network boundary for a
worker, whose `curl` in a bash tool call never passes through the proxy at
all. A real boundary is a container, a VM, or a machine without the
credentials - which is what the `ax` leg would buy.

**M3.8 status (20 September 2026).** Named, not built.

`/btw` reaches a running claude worker (stream-json input) and a running
opencode worker (a second POST into a busy session). It does not reach codex,
because captain's codex transport is `codex exec`, which has no channel into
a running turn - the note runs as the following turn, which is what the TUI
queues anyway. `codex app-server` does have one (`thread/start` →
`turn/steer`). Moving the transport is a contained change with a real payoff:
it is the difference between `/btw` working on two of captain's three vendor
CLIs and on all three.

**M3.9 status (20 September 2026).** Built. The design below is what
landed; the implementation notes follow it.

A worker that rediscovers the same procedure every run is paying frontier
tokens for something already written down. Agent Skills are that written-down
form, and every runtime captain drives already reads them: a directory with a
`SKILL.md`, two required frontmatter fields, `name` and `description`.

The seam is cheaper than it looks, because **captain injects no prompt text at
all**. Each runtime already does progressive disclosure - it loads every
skill's name and description at startup and the body only once it decides to
activate one. So captain's job is not to write a skill into the prompt. It is
to decide which skills EXIST in the worktree for this task. Captain stocks the
shelf; the worker's own runtime picks the book.

One directory does nearly all of it:

| Runtime | Reads |
|---|---|
| codex | `.agents/skills/` (repo), `~/.agents/skills` |
| gemini | `.gemini/skills/`, with `.agents/skills/` as the documented alias |
| opencode | `.opencode/skills/`, `.claude/skills/`, `.agents/skills/` |
| cursor | `.cursor/skills/`, and `.agents/skills/` |
| claude | `.claude/skills/` only - but a `<name>` entry there may be a symlink |

So: one real tree at `.agents/skills/<name>`, and a `.claude/skills/<name>`
symlink pointing into it. Two writes, five runtimes, no per-runtime copy to
keep in step.

**Source.** [`anthropics/skills`](https://github.com/anthropics/skills) is the
primary: the standard's author publishing its own reference implementation,
with a plugin marketplace manifest and most skills under Apache-2.0.
[`openai/plugins`](https://github.com/openai/plugins) is the secondary -
skills live inside plugins there, under a `.codex-plugin/plugin.json` manifest
(`openai/skills` is deprecated and must not be pinned). The spec repository's
`skills-ref validate` is the validator, not a supply.

One licensing trap to respect: Anthropic's four document skills (`docx`,
`pdf`, `pptx`, `xlsx`) are **source-available, not open source**. They may be
fetched onto the user's machine at their own request; they may not be vendored
into this repository or redistributed with it. The sync records each skill's
license next to its hash so the distinction survives.

**The pipeline.**

1. `captain skills sync` fetches each source at a NAMED COMMIT. Never at spawn
   time, never a network call on the hot path.
2. Vetting at sync: `skills-ref validate`; frontmatter restricted to the spec's
   fields; size caps. `allowed-tools` is ignored - the spec marks it
   experimental and support varies, so trusting it would be trusting a field
   nobody implements the same way.
3. `scripts/` is QUARANTINED by default. That directory is arbitrary code, and
   it is where a poisoned skill would keep its payload; it ships only when the
   source and the individual skill are both allowlisted by name.
4. `skills.lock` pins source repository, commit, per-file sha256 and license -
   the same shape as `Toolchain()` pins and `APIContracts()`, so `captain
   doctor` can report a skill set the way it already reports an adapter.
5. Selection happens POST PROMPT, at spawn: the class and domain triage already
   answered (by the jev leg, or the free-leg classify below its bar) matched
   against each skill's `description`, which is the field the standard designs
   for exactly this. Hard cap around eight.
6. Placement into the M3.1 worktree, plus a `.git/info/exclude` line. The shelf
   dies with the worktree; nothing appears in the user's repository, and two
   workers on the same repo can hold different shelves.

**The cap is not a nicety.** Every stocked skill costs its name and description
in every worker's startup context, and codex truncates that list at roughly
8,000 characters - past which it silently shortens descriptions, degrading the
selection for every skill at once. An unfiltered catalog is worse than none.

**What this is not.** It is not a marketplace and it does not read community
catalogs: a 2026 audit found prompt injection in 36% of tested community
skills, and the public directories index millions scraped from GitHub. That is
not a supply captain can stand behind, and the standard offers no signing or
attestation to lean on. Nor is it a sandbox - a skill script that does run is
a tool call like any other, screened by the M3.6 gate, no more and no less.
Opt-in by construction, like Euclid: with nothing synced there is no directory,
no listing, and a run is byte-for-byte what it is today.

**M3.9 implementation (20 September 2026).** The pipeline above is in
[`pkg/captaincode/skills.go`](../pkg/captaincode/skills.go) (catalog, lock,
vetting, selection, staging),
[`skills_sync.go`](../pkg/captaincode/skills_sync.go) (the fetch at a named
commit), [`skilluse.go`](../pkg/captaincode/skilluse.go) (what the shelf was
worth) and [`cmd/captaincode/skills_cmd.go`](../cmd/captaincode/skills_cmd.go).

`captain skills sync` clones a first-party source at a full 40-character sha
(an abbreviated one is refused: a remote cannot resolve it, and git's own
error reads like the commit does not exist), walks it for directories holding
a `SKILL.md`, and vets each one. Vetting is REFUSAL, not repair: frontmatter
outside the spec's set fails the skill rather than being ignored - including
`allowed-tools`, which the spec marks experimental - and so do a name outside
the standard's shape, a description too short to select anything, a `SKILL.md`
over 64 KiB, a directory over 2 MiB or 64 files, and any symlink. Against
`anthropics/skills` at `34040c9` that vets 18 skills and refuses 2 (one 86 KiB
`SKILL.md`, one 2 MiB+ asset bundle), each with its reason on the screen.
`scripts/` is dropped unless the skill is named in `--allow-scripts`, and the
lock's hashes are recomputed FROM THE CATALOG afterwards, so what is attested
is what is on disk rather than what was in the checkout.

Selection scores each synced skill's own words against the task's: a name hit
counts triple, a description hit once, and the triage's class and domain are a
mild prior on top. One description word in common is not a match - live, "fix
a typo in the readme" drew the spreadsheet skill because its description
happens to contain "fix", so a skill now earns shelf space by its NAME
appearing in the task or by at least two distinct words of its description
doing so. Two budgets then bound the shelf and the tighter one wins: the
eight-skill cap, and 6,000 bytes of name+description, under codex's ~8,000
character truncation point.

Staging is wired into all three dispatch paths, not only the isolated ones. A
parallel team or workflow worker gets its shelf in its own M3.1 worktree,
where it dies with the worktree. A SOLO worker runs in the user's own
directory - the dominant path, so skipping it would have been a feature that
never ran - and there the shelf is staged for the turn, kept out of `git
status` by captain's own block in `.git/info/exclude`, and removed when the
turn ends. `Remove` takes back exactly what it created: a skill directory the
user already had at that name is never overwritten and never deleted, and a
created directory is removed only when it is empty.

**What the shelf was worth.** A stocked skill costs every worker its name and
description at startup, and the only honest way to know whether that purchase
was sound is to ask the judge already reading the output. So the director's
assessment carries a second, smaller verdict: for each skill on the worker's
shelf, does the answer show that procedure being followed, and was it worth
its place on THIS task. No extra call and no extra quota - the question rides
in the assessment that was already happening, and with no shelf staged the
prompt is byte-for-byte what it was before.

Three refusals keep the number honest. A grade for a skill captain never
staged is dropped (a director naming a skill it invented is grading nothing).
A usefulness score with `used: false` keeps the observation and discards the
number - that is a grade of a book the director did not see opened. And every
stocked skill gets a ledger row whether or not it was graded, because
"stocked forty times, used twice" is the finding selection has to be able to
make about itself; recording only the graded rows would report selection as
perfect by construction.

`captain skills report` and the captain dashboard's skills panel read that
record: stocked, used, use rate, and the mean usefulness over the runs that
carried a grade. Only the runs the director scores are graded
(`CAPTAIN_ASSESS_MIN_SCORED`), so a skill can be stocked many times and carry
no grade at all - the report says so rather than showing a mean of one.


## M4 — Make Captain portable

**User outcome:** delegate coding work to Captain from Pi, Jido or an editor while retaining
the same project scope, budgets, results and recovery behavior.

| ID | Work package | Concrete deliverable | Owner |
|---|---|---|---|
| M4.1 | Versioned task API | Schemas, compatibility policy, error taxonomy, bounded event stream and contract fixtures | Runtime |
| M4.2 | Execution MCP | Scoped tools for planning/submission/status/results/cancel/resume, backed by the same durable task service | Runtime |
| M4.3 | Pi integration | Pinned supported/community extension path, project-root handling, artifact display and lifecycle tests | Integrations |
| M4.4 | Jido integration | Pinned client/application example, polling or event consumption, restart ownership and lifecycle tests | Integrations |
| M4.5 | Editor integration | One chosen editor MCP client, tested approval/scope handling, result links and lifecycle behavior | Integrations |

**Proposed contract, not current commands.** Operations cover plan, submit, inspect, events,
artifacts, cancel and resume. An envelope includes protocol version, request identity,
task identity, project/base revision, intent, capability/permission policy, acceptance
checks, resource limits, parent task, delegation depth and supported resume checkpoint.

Authenticate access to the local service and bind every operation to its project/task
scope; loopback binding alone is not an authorization policy. Artifact references must
not allow arbitrary filesystem reads. Reject permission widening. Carry a server-validated
delegation lineage and shared resource authority through nested calls; do not trust a
worker-supplied depth counter as the only recursion protection.

Plan calls may consume a bounded planning allowance but cannot execute a proposed patch.
Submission returns a durable task ID. A duplicate identical request attaches to that task;
reuse with different scope/policy/input returns a conflict. Disconnect semantics are
explicit and distinct from cancellation; reconnect resumes observation, not execution.

**MCP compatibility.** Use one internal lifecycle for HTTP and MCP. Negotiate protocol
capabilities and provide a submit/status/result tool fallback for clients without task
support. The reviewed MCP 2025-11-25 specification marks Tasks experimental and requires
capability negotiation; task cancellation and ordinary request cancellation have distinct
protocol behavior. Pin the tested version instead of assuming every host implements it.
[Tasks specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/tasks),
[cancellation specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/cancellation).

Keep Euclid memory tools independently usable. Do not silently copy all host MCP servers,
secrets or permissions into every worker. Define explicit tool delegation and capability
propagation as part of the adapter contract.

**M4.1 status (14 September 2026).** The versioned task API has landed in
[`pkg/captaincode/taskapi.go`](../pkg/captaincode/taskapi.go), and with it
the portable surface a host adapter can depend on: seven operations — plan,
submit, inspect, events, artifacts, cancel, resume — each carried in a
stamped `TaskRequest`/`TaskResponse` envelope with a protocol version, a
request identity, a task identity, and a structured error channel.

The envelope is the contract. Every request carries
`TaskAPIVersion` (currently 1); `CheckCompatibility` rejects a version the
server does not support with `ErrUnsupportedVersion` rather than a 500, so a
future client speaking protocol 2 against a protocol 1 server gets a
machine-readable refusal. Every response carries the same version back, so a
client can refuse a body it does not understand. The error taxonomy is
twelve stable tokens (`ErrCode`): `unsupported_version`, `bad_request`,
`not_found`, `conflict`, `method_not_allowed`, `exhausted`,
`already_terminal`, `unauthorized`, `forbidden`, `internal`, `unavailable`.
A client switches on the code; the message is human context, not a contract.

Three properties carry the design weight from the roadmap. **Plan does not
execute**: `OpPlan` calls `decideRoute` (the same routing path the TUI uses)
and returns the selected leg, class, rationale and decision record, but does
not mint a task ID or dispatch a worker. **Submit is idempotent**:
`OpSubmit` with a `TaskID` attaches to the existing task (returning its
current state) rather than creating a new one, and a terminal task is
rejected with `ErrAlreadyTerminal`. `IsDuplicateSubmit` compares the fields
that define the work (prompt, leg, intent, caps, permissions, checks,
limits), so a reused request ID with different scope is a conflict the
caller can detect. **Disconnect resumes observation, not execution**:
`OpEvents` reads a bounded batch of `TaskEvent` records from a cursor
(`EventsRequest.Cursor` → `EventsResponse.NextCursor`), and reconnecting
passes back the last cursor to resume the stream without re-running the
task.

Delegation lineage is server-validated. `DelegationLineage` carries the
parent task ID, a depth counter, and an ancestor list.
`ValidateLineage` checks two things: depth against `MaxDelegationDepth`
(4), and the task ID against the ancestor list to catch recursive loops. A
worker-supplied depth counter alone is not the only recursion protection,
because the server checks the chain itself.

The HTTP endpoint is `POST /v1/task` (one endpoint, seven operations in the
envelope). The CLI is `captain task <plan|submit|inspect|events|artifacts|
cancel|resume>`. The existing ad-hoc endpoints (`/v1/route`, `/v1/assess`,
`/v1/cancel`, `/v1/lifecycle`, `/v1/handoff`) remain for the TUI; `/v1/task`
is the portable surface for Pi, Jido and editor hosts. 17 contract tests
cover compatibility checking, error taxonomy, lineage validation,
idempotency, cursor round-trips, and JSON envelope serialization.

Remaining for M4.1: ~~contract fixtures~~ (done — see status above). `captain task fixture`
is the standalone contract verification program a host adapter developer
runs against a live brain endpoint — the executable companion to the 17
unit tests and the compatibility policy document. It exercises the full
wire: version compatibility, plan, submit, inspect, events, artifacts,
cancel, error handling (not_found), and idempotent submit. 3 tests in
`task_fixture_test.go` serve as the executable fixtures.

**M4.1 authentication and authorization (14 September 2026).**
[`pkg/captaincode/taskapi.go`](../pkg/captaincode/taskapi.go) adds a
`TaskAuthPolicy` with two enforcement modes, addressing the roadmap's
explicit requirement: "loopback binding is not an auth policy."

When `CAPTAIN_TASK_TOKEN` is **set**, every request to `/v1/task` must carry
`Authorization: Bearer <token>`. The loopback restriction is lifted, because
the operator explicitly chose to expose the endpoint and the token is the
gate. A missing, malformed, or wrong token is rejected with
`ErrUnauthorized` (HTTP 401).

When `CAPTAIN_TASK_TOKEN` is **unset** (the default), the endpoint is
loopback-only: non-loopback connections are rejected with
`ErrUnauthorized`. This is safe for local development, the TUI, and MCP stdio,
where the brain listens on 127.0.0.1 and only local processes connect.

The check runs before any operation dispatch in `taskAPIHTTP`, so every
operation is covered by the same gate. 9 tests cover loopback enforcement,
token validation (valid, missing, wrong, malformed), policy construction
from env, and the loopback address classifier.

[`docs/TASK_API_COMPATIBILITY.md`](TASK_API_COMPATIBILITY.md) defines the
formal compatibility policy: when a version bump is required (breaking
changes), what constitutes a minor addition (no bump needed), client behavior
on version mismatch, the error taxonomy with HTTP status mapping, and the
contract fixture protocol a host adapter follows to verify its implementation.

**M4.2 status (14 September 2026).** The execution MCP server has landed in
[`cmd/captaincode/task_mcp.go`](../cmd/captaincode/task_mcp.go): the seven
task API operations — plan, submit, inspect, events, artifacts, cancel,
resume — are now exposed as MCP tools over stdio JSON-RPC 2.0, the same
transport as the Euclid MCP server. A host like Pi, Jido or an editor can
delegate coding work to Captain through MCP rather than HTTP.

The server is a thin adapter: each tool call constructs the same
`TaskRequest` envelope M4.1 defines, posts it to the brain's `/v1/task`
endpoint, and returns the response body as text content. One internal
lifecycle for HTTP and MCP — the same protocol version, error taxonomy,
delegation lineage validation, and idempotency semantics. A host that
switches from HTTP to MCP does not get a different contract.

The seven tools — `task_plan`, `task_submit`, `task_inspect`,
`task_events`, `task_artifacts`, `task_cancel`, `task_resume` — map
one-to-one to the task API operations. Each tool's input schema carries
the operation-specific fields (prompt for plan/submit, task_id for
inspect/events/artifacts/cancel, task_id + attempt_id for resume) so an
MCP client's tool picker presents them without reading the HTTP contract.
The brain URL is read from `CAPTAIN_BRAIN_URL` (default
`http://127.0.0.1:14097`), so a test server can point the MCP server at a
mock without environment mutation beyond one variable. `captain task mcp`
starts the server; 11 tests cover tool listing, unknown method/tool
errors, plan/submit/cancel round-trips against a test server, brain
unreachable, missing-argument validation, and a full submit → inspect →
cancel flow.

**M4.2 MCP capability negotiation (14 September 2026).** The execution MCP
server now advertises the experimental Tasks capability from the MCP
2025-11-25 specification, and a client that supports Tasks gets native
task-level semantics rather than treating everything as tool calls.

Protocol version is **negotiated**, not assumed. A client that requests
`2025-11-25` in its `initialize` gets that version back with `tasks`
advertised alongside `tools`; a client on `2025-06-18` gets tools only,
and the existing seven tools work synchronously as before. The server's
`tasks` capability declares `list`, `cancel`, and `requests.tools.call`
— the three sub-capabilities the spec defines for a server that accepts
task-augmented tool calls.

Tool-level negotiation follows the spec: `task_submit` declares
`execution.taskSupport: "optional"`, so a client MAY augment it with a
`task` field (to receive a `CreateTaskResult` and poll asynchronously)
or call it normally (synchronous, returns the result directly). A
`tools/call` with a `task` field submits to the brain, creates an MCP
task entry, and returns the task metadata immediately — the actual
result is retrieved later via `tasks/result`.

Four task operations map to the Captain task API:
`tasks/get` (inspect, with lifecycle→MCP status mapping),
`tasks/result` (blocks until terminal, returns the formatted inspect
response with `related-task` metadata), `tasks/list` (cursor-paginated),
and `tasks/cancel` (rejects terminal tasks with `-32602` per the spec).
The status mapping is many-to-one: Captain's nine lifecycle states map
to MCP's five (`working`, `input_required`, `completed`, `failed`,
`cancelled`). `notifications/tasks/status` is sent on status change
when the client is polling.

**M4.2 contract fixtures (14 September 2026).**
[`docs/TASK_MCP_CONTRACT.md`](TASK_MCP_CONTRACT.md) defines the
executable contract a host adapter runs against the MCP server: protocol
version negotiation, tool-level task support, the full task lifecycle
(create → get → result → cancel → list), error handling, and the status
mapping table. The 22 tests in `task_mcp_test.go` serve as the
executable fixtures — run with `go test ./cmd/captaincode/ -run TestTaskMCP`.

**M4.3–M4.5 host adapters (14 September 2026).** The three host
integrations have landed. A shared task API HTTP client
([`pkg/captaincode/taskapi_client.go`](../pkg/captaincode/taskapi_client.go)),
host adapter framework
([`pkg/captaincode/host_adapter.go`](../pkg/captaincode/host_adapter.go)),
and the host certification checklist are the foundation all three share.
Each host embeds `HostAdapterBase` for the shared lifecycle (submit,
poll, retrieve artifacts, cancel, resume) and overrides the hooks it
needs.

**Pi** ([`pkg/captaincode/host_pi.go`](../pkg/captaincode/host_pi.go)):
the one-shot terminal host. Submits a task, polls to terminal, prints
the result. No approval dialog (the operator is already at a terminal).
`captain host pi <prompt>` runs the full lifecycle; `inspect`, `cancel`,
`artifacts`, and `resume` subcommands cover the lifecycle operations.
The pinned extension path is `CAPTAIN_BRAIN_URL` (default localhost) and
`CAPTAIN_TASK_TOKEN` (default loopback-only) — Pi does not auto-discover
the brain.

**Jido** ([`pkg/captaincode/host_jido.go`](../pkg/captaincode/host_jido.go)):
the event-driven automation agent. Submits a task and then polls the
event stream (Events operation with cursor) rather than blocking on
Inspect. Restart ownership is explicit: Jido persists its task IDs to a
state file, and `RecoverOnStartup` inspects each on restart to classify
them as running, interrupted, or terminal. Interrupted tasks are
candidates for ResumeTask. `captain host jido <prompt>` runs the full
lifecycle; `captain host jido recover` prints the recovery report.

**Editor** ([`pkg/captaincode/host_editor.go`](../pkg/captaincode/host_editor.go)):
the interactive host with an approval/scope dialog. The editor calls
OpPlan first, shows the routing decision to the user, and only submits
on approval. `EditorPlanAndApprove` is the two-step flow: plan, then
ask the user via `EditorApprover` (the editor's "Allow this task?"
dialog), then submit. The editor knows its workspace folder and does not
walk up looking for .git. `OnTaskTerminal` opens the changed files and
shows the handoff brief.

**Host certification checklist.**
[`pkg/captaincode/host_adapter.go`](../pkg/captaincode/host_adapter.go)
defines `CertChecklist`, the roadmap M4 exit gate: project root
detection, base revision pinning, cancel during execution, delegation
loop rejection, delegation depth rejection, and conflicting retry
detection. `captain host cert [--host pi|jido|editor]` runs the
checklist from the CLI. 31 tests in
[`host_adapter_test.go`](../pkg/captaincode/host_adapter_test.go)
cover the client, each adapter, and the certification suite — all
runnable without a live brain via a test server that speaks the task
API envelope.

The certification checklist covers the static gates (project root,
base revision, delegation validation). The dynamic gates (cancel
during execution, recover after restart, reject conflicting retries,
retain root budget, deny scope widening) require a live brain and
are exercised by the lifecycle tests against the test server.

**Host certification checklist.** The same fixture must complete with an inspected artifact,
cancel during execution, recover after host and brain restart, reject conflicting retries,
retain the root budget, deny scope widening, reject recursive delegation loops and expose
an unsupported capability clearly. Record tested client/extension/runtime versions and
whether support is official, community or a Captain-maintained adapter.

**Exit gate.** All three selected hosts pass the lifecycle contract suite and have reproducible
installation recipes. A memory-only configuration is never labeled execution integration.
Additional harness workers follow demonstrated demand and interface validation.

**Implementation entry points:** [brain HTTP surface](../cmd/captaincode/brain.go),
[chat-completion bridge](../cmd/captaincode/brain_openai.go),
[Euclid MCP](../cmd/captaincode/euclid_mcp.go),
[architecture](ARCHITECTURE.md), [Euclid](EUCLID.md).

**Ownership:** implement execution MCP here. Euclid E4 owns portable memory MCP and the
compatibility contract with Captain's existing wrapper; memory parity is not a substitute
for this milestone's execution tests. Any future Trace/Atlas API or MCP surface is
consumed through an explicit optional adapter, never assumed present.

## M5 — Improve from accepted outcomes

**User outcome:** Captain learns which worker or combination completes this user's tasks
without treating a model's own opinion as proof of success.

| ID | Work package | Concrete deliverable | Owner |
|---|---|---|---|
| M5.1 | Outcome evidence | Task-linked checks, review verdicts, accepted/rejected patches, correction minutes and later regressions, with source and timestamp | Routing |
| M5.2 | Calibrated estimates | Quality/reliability estimates by task family and runtime version; sample sizes, uncertainty, ageing and fallback priors | Routing |
| M5.3 | Role economics | Compare economical solo, frontier solo, repair/escalation and teams using total accepted-task cost/time | Routing + reviewer |
| M5.4 | Policy evaluation | Versioned policy snapshots, frozen holdouts, shadow decisions and bounded opt-in canaries | Both |
| M5.5 | Promotion and rollback | Reproducible decision report, cohort-level regression checks and immediate return to the last accepted policy | Both |

**M5.1 status (14 September 2026).** Task-wide outcome evidence has landed
in [`pkg/captaincode/outcome.go`](../pkg/captaincode/outcome.go), and with
it the task-level acceptance record the evaluation harness could not
supply: the checks a real task ran, the human verdict on it, the
correction minutes it cost, and any later regression that revoked an
earlier acceptance.

An `OutcomeEvidence` is one task's acceptance record, stored in the ledger
(one per task, updated in place). It carries four evidence types beyond
the eval harness's reach: **check results** (`CheckResult` with command,
exit code, pass/fail, source, timestamp) from gates or solo-turn checks;
**task reviews** (`TaskReview` with verdict, reviewer, note, amend flag)
addressed by task ID rather than alias — the same amendment discipline as
`EvalReview`, preserving prior verdicts in `ReviewHistory`; **correction
records** (`CorrectionRecord` with minutes and reason) tracking developer
effort, the metric M1's rubric names; and **regression records**
(`RegressionRecord`) that supersede an earlier acceptance without erasing
it — `status` moves to `regressed` but `accepted_at` stays.

The ledger methods (`RecordCheckResult`, `RecordTaskReview`,
`RecordCorrection`, `RecordRegression`) each create the outcome if it
does not yet exist, so a correction on a task that was never formally
reviewed still records. A duplicate review without `amend` is rejected,
the same guard `EvalReview.RecordReview` enforces. `OutcomeCoverage`
returns aggregate counts for `captain stats`; `AcceptedOutcomes` returns
the accepted (and regressed) subset. The brain records a pending outcome
at `recordRun` when no prior outcome exists, so every completed task has
an acceptance row the user can amend.

HTTP: `GET /v1/outcome` (list or single), `POST /v1/outcome` (review,
correction, regression). CLI: `captain outcomes` (list), `captain
outcomes <task-id>` (detail), `captain outcome <task-id> review
<accept|reject> --reviewer <name> [--note ...] [--amend]`, `captain
outcome <task-id> correction <minutes> [--reason ...]`, `captain outcome
<task-id> regression <reason> [--source ...]`.

**M5.1 defect, fixed 20 September 2026: outcomes never left `pending`.** The
brain opened a pending outcome for every completed task and nothing but
`captain outcome <id> review` moved one off. On the author's machine that
left 442 outcomes, all pending, 75 of them carrying checks nobody read and
0 reviews - a column that is 100% one value, which is a constant rather than
a signal. Everything downstream was reading it: the M5.2 shadow
calibration's outcome labels, the M1 acceptance rate, and the M3.6 gate's
settle, which needs a cleanly accepted task and could never find one.

[`settle.go`](../pkg/captaincode/settle.go) derives the status from evidence
the ledger already holds and records WHAT decided it (`DecidedBy`), so a
check-settled acceptance is never read as a human one. A failed check rejects
at once; an all-passed set accepts after `CAPTAIN_OUTCOME_SETTLE` (default
24h) in which no correction, regression or verdict arrived; a task that never
reached delivery (`failed`, `exhausted`) is rejected by lifecycle; a human
verdict is authoritative and never overwritten. Two cases stay pending on
purpose: a CANCELLED task is the user changing their mind about the question,
not a verdict on the answer, and a task whose checks passed but that cost the
user correction minutes is a statement about the checks that only a human can
call.

The brain sweeps on every turn it records; `captain outcomes --settle`
(`POST /v1/outcome` action `settle`) applies a window that has just elapsed
and prints the counts. Swept over the author's ledger this settles 57 of 442
- 54 accepted, 3 rejected - and the 54 clean acceptances are the first rows
the gate's calibration can be read against. The rest carry no check at all,
which is a coverage problem, not a settlement one.

Remaining for M5.1: the correction-time UI in the TUI (currently CLI-only),
and check coverage - 367 of 442 outcomes carry no task-linked check, so
nothing can settle them without a human.
The evaluation harness's review history (the prior partial progress) is
unchanged and remains the source for eval-run verdicts.

**M5.1 solo-turn check wiring (14 September 2026).** The brain's
`recordRun` now records the director's assessment verdict as a
`CheckResult` on the task's `OutcomeEvidence` via `RecordCheckResult`.
When `doAssess` returns a verdict (good / acceptable / poor), the
assessment is recorded as `source: "solo"`, `command: "director:assess"`,
`passed: verdict != "poor"`. A solo turn's outcome evidence now carries
what the director observed, not just a pending status. A "poor" verdict
is a failed check the user can see in `captain outcome <id>`.

**M5.1 gate check wiring (14 September 2026).** The workflow executor now
records gate check results as `CheckResult` entries on the task's
`OutcomeEvidence` via `RecordCheckResult`. Each gate command that ran (initial,
repair, or escalation) is recorded with its command, pass/fail, and `source:
"gate"`. The outcome acceptance record now includes what the gate observed,
not just the pending status. The evaluation harness's review history is
unchanged.

**M5.2 status (14 September 2026).** Calibrated estimates have landed in
[`pkg/captaincode/calibration.go`](../pkg/captaincode/calibration.go), and
with it the uncertainty, ageing and reliability the point estimate was
hiding.

`BlendedQuality` does simple shrinkage: prior counts as 5
pseudo-observations, local scored runs add their actual count, and the
weighted mean is the estimate. A leg with two scored runs at q9.0 reads
the same as one with forty, and a leg whose scores are three weeks old
reads the same as one scored yesterday. `Calibration` adds what the point
estimate hides: a 95% confidence interval on quality (normal approximation
with decayed-variance), a reliability estimate (decayed success rate) with
its own Wilson 95% interval (robust at small n), exponential time-decay
(half-life 14 days, env-overridable), an effective sample size (sum of
decay weights) so the CI widens as evidence ages, and a stale flag when
the effective sample falls below the threshold (default 3, env-overridable).

The prior's weight decays as local evidence grows: at n=0 the prior
dominates, by n=20 it is a tie-breaker. The estimate is computed from the
ledger's existing Events, filtered by domain — no new persistence. It is a
read at decision time, not a write on every run.

`Calibration` is wired into the ranking: `valueLadder` enriches every
`Scored` row with its calibrated estimate, and the director's decision
record carries it too. `captain why` prints the calibrated estimate —
`8.2 [7.1–9.3] (n=12, eff=9.5, reliability 85%–96% on 14)` — beside each
candidate, so two legs with the same point estimate but different evidence
are distinguishable. `captain calibrate` exposes the full table per domain
with `--json` for machine consumption.

**M5.3 status (14 September 2026).** Role economics has landed in
[`pkg/captaincode/role_econ.go`](../pkg/captaincode/role_econ.go): the
comparison the M1 baseline report needs — economical solo vs frontier
solo vs repair/escalation vs teams — is now computable from the ledger's
existing charge tree.

Each task is classified by its charge pattern: a task with only non-
frontier worker attempts is `economical_solo`; one with only frontier-
class worker attempts is `frontier_solo`; one with `gate-repair` or
`escalation` labels is `repair_escalation`; one with `stage` charges
(fan-out) is `teams`. The classification is from the durable record of
what each task actually spent, not from the routing decision that started
it — a task the director routed as economical but that escalated after a
gate failure reads as `repair_escalation`, because that is what it cost.

`RoleEconomics` aggregates per role: total tasks, accepted (lifecycle
`succeeded`), total cost (from call rows), total time (call durations), and
unknown-usage count — the same accounting-coverage discipline M1.2 enforces.
`captain roles` and `/roles` print `FormatRoleEconomics`'s table, which
shows `-` for cost/time per accepted when no task in a role was accepted
(undefined, not zero) and marks totals containing unknown-usage calls.

Remaining for M5.3: the comparison needs the M1.3 pilot corpus to produce
real numbers — the classification and aggregation are done, but the data
they aggregate is the 12-task fixture set M1.3 has not yet written.

**M5.4 status (14 September 2026).** Policy evaluation has landed in
[`pkg/captaincode/policy.go`](../pkg/captaincode/policy.go), and with it
the four artifacts the roadmap names: versioned snapshots, frozen holdouts,
shadow decisions, and bounded opt-in canaries.

A `PolicySnapshot` captures the FULL routing state at a point in time:
the director, the value weights per class, the quality thresholds (tau),
the cost/latency normalization references, the explore rate, and every
leg's quality prior. The snapshot carries a fingerprint (a deterministic
hash of all its fields) and a short ID derived from it, so two snapshots
of the same state are identical and a policy change is detectable. The
ledger stores snapshots (capped at 50); one can be marked `active` (the
policy currently routing) and `accepted` (a policy that passed promotion
and was rolled out). `captain policy` lists them; `captain policy snapshot
[name]` captures one; `captain policy activate <id>` and `captain policy
accept <id>` manage lifecycle.

A `Holdout` is a set of task families or domains held out from policy
tuning: a candidate policy is NOT tuned on holdout members, and they
validate that the candidate does not regress on them. Matching is by
domain, class, or task substring.

A `ShadowDecision` records what a candidate policy WOULD have chosen for
a task alongside the active policy's actual decision — recorded but not
executed, so post-hoc comparison measures divergence and quality
difference without risking the user's work.

A `Canary` is a bounded opt-in experiment: a candidate policy, a sample
rate, max tasks, max cost, start/stop, and quality comparison.
`ShouldRoute` respects the sample rate, the max-tasks bound, and the
stop state. The ledger tracks one active canary at a time; `captain
policy canary start <candidate-id> [rate] [max-tasks]` begins one and
`captain policy canary stop [reason]` ends it.

**M5.5 status (14 September 2026).** Promotion and rollback has landed
alongside M5.4 in [`pkg/captaincode/policy.go`](../pkg/captaincode/policy.go).

`Promote` produces a `PromotionReport` comparing the active policy
against a candidate across cohorts (overall, per-class, per-domain).
Each `CohortResult` carries the mean quality, acceptance rate, and
delta for both policies. A cohort regresses when the candidate's mean
quality or acceptance rate is worse than the active's; the overall
regresses when any cohort does. The recommendation is `promote` (no
cohort regressed, ≥5 candidate decisions), `reject` (at least one
cohort regressed), or `insufficient` (fewer than 5 candidate
decisions). `captain policy promote <candidate-id>` prints the report.

Rollback is immediate: `captain policy rollback` activates the last
accepted snapshot. The ledger tracks `LastAcceptedSnapshot` by
timestamp, so the return path is one command, not a hunt through the
snapshot list. `FormatPromotionReport` renders the comparison table
with per-cohort regression flags.

Keep execution success, passing tests, model-assisted assessment and developer acceptance
as distinct observations. Silence is unknown acceptance, not approval. A later revert or
correction can revise the outcome without erasing the original record. Weight signals by
their provenance and relevance; do not let a worker overwrite protected checks to label
its own work accepted.

Start with simple calibrated estimates and constrained rules. Address routing selection
bias: unchosen workers lack outcomes, and historical logs alone cannot prove their
counterfactual performance. Record selection policy/propensity where available; use
randomized evaluation or opt-in bounded exploration before claiming causal improvement.
The existing exploration mechanism is a starting point, not a solved learning policy.

Freeze evaluation repositories and feedback between arms; hold out repositories/task
families to reduce leakage. Retire a holdout used for tuning. Reset or discount evidence
when model/runtime versions change, and use conservative priors for small samples.

**Exit gate.** The candidate policy meets the predeclared acceptance target and improves
cost or time per accepted task with uncertainty excluding an inconclusive result. No
important task cohort silently loses quality to improve the average. Start with a small
opt-in canary, stop on budget/scope violations or quality regression, then promote with
an auditable policy version. If evidence is insufficient, retain the existing default.

Project data stays local by default; export, retention, deletion and learning opt-out
are explicit controls. A public leaderboard is not needed for useful personal routing.

**Implementation entry points:** [ledger/statistics](../pkg/captaincode/ledger.go),
[assessment/recording](../cmd/captaincode/brain.go),
[value policy/exploration](../cmd/captaincode/brain_value.go),
[ranking](../pkg/captaincode/value.go), [performance evidence](../pkg/captaincode/perf.go).

**Ownership:** Captain captures and interprets acceptance evidence for routing. Euclid
can store attributed decisions and corrections, and independently tune retrieval using
its own evaluation corpus. Optional Trace/Atlas-style services validate receipt/mandate
evidence under their own contracts.
Neither a retrieved lesson nor a valid receipt may silently become developer acceptance.


**M5.4 status (20 September 2026), supervisor points.** The shadow gained a
second axis.

Captain's three routing shadow points - shape, leg, note route - are all
DISPATCH: asked once, before or beside the work, over a choice captain makes
at the same moment. Nothing in captain watched the floor. The stall watchdog
sees silence and kills on a clock; it cannot tell `cargo test --release` from
a loop. `/btw` and `/interrupt` both need the user to notice first.

[`pkg/captaincode/supervise.go`](../pkg/captaincode/supervise.go) adds four
nouls asked ABOUT A RUNNING WORKER, on a slow interval (90s), off the status
feed the worker already produces: `worker-stuck`, `work-off-track`,
`needs-human`, `agents-md-drift`. They are recorded and acted on by nothing,
which is the entire point: nobody has shown a decision model is accurate at
this, and captain already owns the apparatus for finding out.

Three of the four are stamped from what captain itself later observes, so the
record builds a calibration with nobody labelling rows by hand:
`worker-stuck` against whether the stall watchdog fired, `needs-human`
against whether the user interrupted, `work-off-track` against the task's
acceptance evidence, which the calibration already joins by task id.
`agents-md-drift` is asked because Euclid makes its state cheap, and left
UNCOMPARED because nothing observes it - an honest missing label rather than
an invented one.


## First ten working days

| Days | Routing engineer | Runtime/integration engineer | Reviewable output |
|---|---|---|---|
| 1–2 | Define task/attempt/call/outcome schema and acceptance rubric | Audit brain-only/full-terminal installation and adapter contracts | Schema proposal, capability table, installation failure list |
| 3–4 | Trace all planning/worker/review/retry usage paths | Build clean macOS/Linux install checks and pinned compatibility manifest | Accounting coverage map and install report |
| 5–6 | Implement call attribution and unknown-usage handling | Add fixture project isolation and worker invocation capture | First trace with reconciled totals and reproducible fixture |
| 7–8 | Assemble development pilot and frozen baseline configurations | Automate fixture reset, output/artifact capture and deterministic checks | Dry-run evaluation package and measured cost estimate |
| 9–10 | Run the approved pilot, inspect acceptance and uncertainty | Reproduce on a second environment; fix installation blockers | Pilot report, revised estimates and prioritized M1 remainder |

Set evaluation spend from observed pilot costs before expanding the workload. Installation
and deterministic fixture checks can run without live provider calls. Issue IDs here are
backlog identifiers, not existing external issues or release commitments.

## Release gates and scope discipline

Each release includes code and migration notes, relevant unit/integration/fault tests,
compatibility records, known limitations and a rollback path. Runtime changes use the
existing Go test suites; terminal changes use package-local tests/typechecks and repository
lint. Published installation checks must run against the extracted artifact, not only
the development checkout. Feature claims update after their gate has passed.

The first tranche excludes a new editor, hosted multi-tenant platform, marketplace,
global learning service and speculative model-logo expansion. Keep the terminal useful;
ship portability through a small number of maintained integrations. Broader platform
support and new worker adapters follow user demand and conformance evidence.

Open design decisions belong to their owning milestone: M1 chooses the release/install
contract and evaluation tolerance; M2 defines strict versus admission budgets and frontier
migration; M3 defines storage/ownership/replay boundaries; M4 chooses supported host
versions; M5 defines outcome retention and promotion policy. Resolve these through short
design records before the dependent implementation.

The work is done when developers can show that Captain chose appropriate intelligence,
delivered acceptable changes within declared limits, recovered predictably and remained
useful inside the environment they already prefer.
