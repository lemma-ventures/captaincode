---
name: land-parallel-agent-work
description: Run several coding agents or subagents on one git repository at the same time and land their file changes without losing work or splicing patches. Use when fanning a task out to parallel workers that may edit files, when bringing worker changes back into the user's checkout, when two workers changed the same file and one change has to win, or when chaining stages where later workers build on earlier ones. Covers one worktree per writer, capturing diffs before teardown, file-level conflict groups, one whole winner per group, and all-or-nothing apply.
license: MIT
---

# Land parallel agent work

Parallel agents on one repository are safe only if every change they make ends
in one of two places: applied to the user's checkout, or set aside on disk with
a record of why. These rules come from Captain Code, which runs Claude, Codex,
Cursor, Gemini and open-weight workers side by side on the same repository.
The incidents quoted below are Captain's own.

## 1. Isolate every writer, or none

- Workers that only read (review, research, planning) can share the checkout.
  Every worker that may write gets its own git worktree.
- If one worktree cannot be created, remove the ones already made and run the
  writers one at a time in the checkout. A half-isolated team cannot say
  afterwards which change came from which worker.
- A worktree isolates files and nothing else. Processes, ports, network,
  credentials, caches, databases and everything under `$HOME` stay shared.
  Give each worker its own port and temp dir if it starts services.
- A state file that several processes write (a JSON ledger, an index) must be
  re-read and merged right before each save, then written to a temp file and
  renamed into place. Loading once and saving later let a stale Captain
  process erase rows another process had just appended.

## 2. Start every worktree from one recorded base

```sh
root=$(git rev-parse --show-toplevel)
base=$(git -C "$root" rev-parse HEAD)
runs=$(mktemp -d)                  # outside every worktree; patches live here
for w in w1 w2 w3; do
  git -C "$root" worktree add --detach "$runs/$w" "$base"
done
```

- Keep `base`. Every capture and every landing check is against it.
- A worktree starts from the commit, not from the checkout: the user's
  uncommitted edits are not in it. Say so before fanning out; the landing check
  in step 6 catches any patch that collides with those edits.
- Tooling you drop into a worktree (skills, agent config, instruction files)
  must not show up in the diff, and a `.gitignore` written inside the worktree
  would itself be a change. Use the exclude file git actually reads, which for
  a linked worktree is the shared one:
  `$(git rev-parse --git-common-dir)/info/exclude`. Its patterns are relative
  to the repository root, so prefix them with `git rev-parse --show-prefix`
  when the worker runs in a subdirectory, and take your lines out again when
  the run ends.

## 3. Capture before teardown

Removing a worktree before saving what is in it deletes the worker's work
without a trace. Captain's `/team` once did exactly that: the worktrees were
removed at the end of the run, the workers' answers were blended as text, and
none of their file changes reached the user.

Before removing anything, for each worker:

```sh
wt=$runs/w1
git -C "$wt" add -N .       # untracked files become visible to diff
git -C "$wt" diff --binary --no-renames "$base" -- > "$runs/w1.patch"
git -C "$wt" diff --name-only --no-renames "$base" -- > "$runs/w1.files"
git -C "$wt" reset --quiet  # drop the intent-to-add entries again
```

- Diff against `base`, never `HEAD`. A worker that committed inside its
  worktree moved `HEAD`, and a diff against it leaves those commits out.
- `--binary` keeps binary files applicable. `--no-renames` records a rename as
  a delete plus an add, so conflict detection sees both paths.
- Save the worker's final report and its check verdict (pass, fail or
  inconclusive) next to the patch.
- A worker that timed out or crashed is captured like the rest and marked as
  such. Partial work is reviewed, not discarded.
- If a capture fails, keep that worktree and report its path. Remove only what
  was captured: `git worktree remove --force "$wt"`, then `git worktree prune`.

## 4. Find conflicts at the file level

- Two workers conflict when their changed-file lists share a path. Flag it even
  when the hunks do not overlap and both patches would apply: two edits that
  merge cleanly can still be two different designs, and telling them apart is a
  review, not a text merge.
- A worker that changed no files (a text-only answer) never conflicts.
- Group the conflicts: workers joined by shared files, transitively, form one
  group, and each group gets exactly one decision. Workers outside every group
  land as they are.
- A cross-file signal (A changed a function that B's new code calls) is
  advisory. Show it to whoever decides; do not block on it.

## 5. One whole winner per group, never a splice

Two plausible patches spliced together are often neither. For each group, one
decider (the orchestrating agent, a stronger model, or the user) picks exactly
one worker, and that worker's whole change set lands.

Show the decider every contender in a stable order (sorted by id): its id, the
files it changed, its objective evidence (check passed, failed or timed out)
and its report. Ask it to judge correctness first, then how completely the
change does what the task asked, then how little it touches beyond that.
Objective evidence outranks a confident report. Judge the work, not which
model produced it.

Ask for a structured reply and validate it:

```json
{"winner": "<one of this group's contender ids>", "reason": "<one line>"}
```

- A winner that is not one of the group's contenders is no ruling. Neither is
  no reply.
- No ruling: apply nothing from that group, keep every patch, and say so. Never
  fall back to a guessed winner (first to finish, biggest diff, most confident
  report). A guess is the harness making the call.
- The other contenders are set aside whole. Their patches stay on disk and
  their paths go in the record, so a person can apply one instead.

## 6. Apply all or nothing

Land only when every group is either free of conflicts or ruled on. Check
every patch before applying any:

```sh
for p in $landing; do git -C "$root" apply --check --whitespace=nowarn "$p" || exit 1; done
for p in $landing; do git -C "$root" apply --whitespace=nowarn "$p"; done
```

- Landing patches touch disjoint files by construction, so checking each one
  against the same tree is enough.
- A failed check means the checkout moved under you (the user kept editing, or
  another run landed first). Apply nothing, name the patch and the file, and
  hand back.
- What lands is uncommitted changes in the user's checkout, for them to review,
  stage or discard. Committing is a separate decision.

## 7. Chained stages land before the next one starts

A stage that builds on an earlier stage's files must start from a commit that
contains them. Worktrees created from the old base cannot see changes that were
only applied to the checkout, and a pipeline that keeps a single "latest
result" lets stage N+1 overwrite stage N's before either lands.

- Keep one landing record per stage, never one slot per run.
- Land stage N, then create stage N+1's worktrees from a commit that contains
  it. A scratch commit in a separate integration worktree works and never has
  to reach the user's branch.

## 8. Tell the summary what landed

Whatever writes the summary (a synthesis step, a PR description, the final
message) must be told what was applied: "only w2's changes were applied; w1
and w3 were set aside". Without that it describes set-aside changes as if they
had landed. If there was no ruling, it says none of the conflicting changes
were applied. When workers' answers contradict each other, the summary picks
one and says whose; it does not average them.

## 9. Keep a record

Per run: the base commit; per worker its id, agent and model, changed files,
check verdict and patch path; each conflict group with its ruling (winner,
reason, who decided); what was set aside and where its patch is; what was
applied; any apply error. That record answers "where did my change go" a week
later.
