# Evaluation harness (ROADMAP M1.3)

`captain eval` runs a frozen suite of coding tasks across several arms and
writes a result record the M1.4 baseline report is aggregated from.

```
captain eval validate docs/eval/pilot.example.json   # are the fixtures reproducible?
captain eval verify   suite.json                     # do the fixtures measure anything?
captain eval plan     suite.json                     # what would run, in what order
captain eval run      suite.json --out res.json      # run it, serially
captain eval report   res.json                       # aggregate a finished run
captain eval review   res.json                       # what still owes a verdict
captain eval review   res.json --accept t1/arm-A/1 --by you --note why
```

## What the harness guarantees

**A fixture is pinned or it is not a fixture.** Every task names a full
40-character commit sha. A branch name is rejected at load, because a suite
whose meaning moves cannot be replayed.

**A fixture that no arm can fail is not a fixture.** `captain eval verify`
clones each task's pinned revision and runs its checks against the *untouched*
tree, before any arm exists. At least one check must **fail** there: a failing
check is the task's statement of what is not yet true. If none fails, the task
is `vacuous` — every arm is accepted for free, all four acceptance rates rise
together, and nothing in the report says so. Every check must also **run**
there: a missing command exits non-zero too, and would read as a healthy
baseline failure while in fact rejecting every arm no matter what it wrote —
that is `broken`. A blinded task is the one legitimate exception
(`review-decides`): its checks are necessary and not sufficient, so the
verdict, not the checks, carries the acceptance. `verify` also names a
`must_not_change` guard over a path or glob that matches nothing in the
snapshot, which forbids nothing, and marks that fixture `broken`. Malformed glob patterns are also broken. Unknown
`--only` task IDs and interrupted verification return errors instead of a
successful empty or partial report. Elapsed time includes snapshot creation,
setup and checks. It exits non-zero when any fixture is not
runnable, so a script cannot spend 144 executions on a suite that measures
nothing.

**Run enforces verification.** `captain eval run` verifies every selected task
before dispatching any arm. Broken or vacuous fixtures, unknown `--only`
IDs and cancellation during preflight return an error without starting workers.
Errors identify the failed fixture and retained snapshot. Successful result
exports include `baselines` with check results, durations and snapshot paths.
Preflight uses separate snapshots and is excluded from per-arm execution time.
A standalone `verify` remains useful for inspecting fixtures without running arms.

**Every execution starts pristine.** Each task/arm/repeat gets its own clone
of the pinned revision. The source repository is only ever read: a developer's
dirty worktree survives an evaluation run untouched, and no repeat inherits
the previous one's tree. Task IDs and arm names must be single portable path
components. Verification and execution allocate unique snapshot directories,
including when reusing `--work`; earlier artifacts are never deleted by a rerun.

**Acceptance is decided by commands.** A check is an argv and the exit code it
must produce in the snapshot afterwards, plus `must_change` / `must_not_change`
constraints over file differences from the pinned revision, including committed
changes and untracked files. Renames count as changes to both paths; unusual
filenames are preserved. Failed Git inspection blocks acceptance with a harness
error rather than silently producing an empty change list. Where checks are not
sufficient, a task sets `"review": "blinded"` and its executions end
`pending-review` — which is *not* accepted, is excluded from the numerator, and
is listed for the reviewer under the arm's alias (`arm-A`) rather than its name.

**A verdict is recorded, attributed and reversible only on the record.**
`captain eval review` lists each pending execution with its prompt, its
snapshot directory and the files it changed — everything needed to judge the
change, and nothing that says which worker made it. A verdict carries the
reviewer's name (an anonymous acceptance cannot be audited) and moves the
execution to `accepted` or `rejected` in place. Three refusals keep that
honest: review decides sufficiency and never necessity, so an execution the
checks already rejected cannot be reviewed into acceptance; a second verdict
on the same execution needs `--amend` and is stored marked as an amendment;
and the report counts human acceptances in their own `(reviewed)` column, so
"the checks passed" and "a person judged it good enough" are never summed into
one indistinguishable number.

**Outcomes are distinguished.** `accepted`, `rejected` (the arm ran and the
evidence says no), `pending-review`, `timeout`, `error` (the harness could not
run it). Nothing is dropped: failures stay in the denominator, so a cheap arm
that fails often cannot look cheap.

**Order is randomized and reproducible.** `plan` shuffles under the suite's
`seed`, so run order does not track time-of-day load while the report still
replays.

## What the numbers mean

`$/accepted` and `s/accepted` divide the **whole workload** — failures,
timeouts, coordination — by the accepted count, per the roadmap's metric
definitions. With nothing accepted they print `-`, never `0`.

Execution durations include snapshot creation, setup, worker execution and
acceptance checks, including time spent on rejected, failed and timed-out
executions. These durations survive JSON export and feed `s/accepted`.

Money comes from the M1.2 ledger: the charge rows that appear while an arm
runs are attributed to that execution. This is why executions are **serial**,
and why a parallel runner is not offered — concurrent arms would make the
attribution a guess. An arm whose calls were never recorded prints `-`, not
`$0`; a total containing unknown-usage calls is marked `*` and labelled *not a
billed total*.

## Suite format

See [`pilot.example.json`](pilot.example.json). `{prompt}` in an arm's `run`
is replaced by the task's prompt. `version` is `1`; a reader that does not
understand a newer shape refuses it rather than mis-summing it.

Review amendments retain each superseded verdict in chronological
`review_history`, including reviewer, note and timestamp. `review` remains
the current verdict used by reports. Older result files without history
remain readable; their current verdict is preserved on the next amendment.
Previously overwritten verdicts cannot be reconstructed.

Review aliases must be unique single path components, including automatically
assigned aliases. Imported results with duplicate task/alias/repeat keys cannot
receive a verdict until the ambiguous evidence is resolved; no matching record
is changed by a rejected review.
