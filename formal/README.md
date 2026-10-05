# Command safety: a formal model

Captain's commands compose. A chain runs commands in sequence (`a > b`), `/repeat` runs a
command again and again, a group in parentheses nests a chain inside a step, and a worker can
send a new prompt with `captain send`. This directory models those mechanisms as automata in
Lean 4 and proves three properties:

1. The parser loses no text.
2. No prompt can start unbounded work.
3. A worker cannot keep work going without the user.

Build it with `lake build` in this directory (Lean 4.28, no dependencies). CI runs the same
build, so a broken proof fails the build.

## What is proved

| Theorem | File | Statement |
|---|---|---|
| `join_split` | `Splitter.lean` | The steps a chain splits into, joined again with `>`, are exactly the input. |
| `split_noBoundary` | `Splitter.lean` | Text with no `>` before a command is one step, so prose never becomes a command structure. |
| `step_shorter` | `Splitter.lean` | When a chain splits, every step is strictly shorter than the input, so splitting a nested group again always ends. |
| `turnsNow_unbounded` | `Execution.lean` | **Before this change**, no constant bounded the turns one prompt could start: k nested `/repeat`s run at least 100^k turns. |
| `run_turns_le` | `Execution.lean` | With one budget shared by a prompt and every loop it starts, the turns never exceed the budget, however the commands nest. |
| `now_unbounded`, `now_loops_unbounded` | `Inbox.lean` | **Before this change**, with no user input, the inbox accepted any number of sent prompts, and each could start a loop. |
| `fixed_accepted_le` | `Inbox.lean` | After this change, at most `quota` sent prompts are accepted between two turns the user types. |
| `fixed_no_loops` | `Inbox.lean` | After this change, no sent prompt starts a loop. |

Every theorem depends only on Lean's standard axioms (`propext`, `Quot.sound`,
`Classical.choice`); none uses `sorry`. Termination of `scan`, `turnsNow` and `run` is
checked by Lean itself, because all three are defined by structural recursion.

## How the model maps to the code

| Model | Code |
|---|---|
| `Tok`, `scan`, `split` | `SplitChain` in `pkg/captaincode/chain.go`. A group opens only on `(` followed by a command, and a `>` splits only at depth 0 when a step starts after it. |
| `Cmd.turn` | One dispatched turn: a solo leg, a lane, `/frontier`, `/team` or a CWL workflow. Each runs a bounded number of workers (`maxWorkers`, `MaxWorkflowRuns`). |
| `Cmd.rep n body` | `/repeat n`, with `rounds n` bounded by `CAPTAIN_REPEAT_MAX` (`brain_repeat.go`). |
| `Cmd.seq a b` | One chain step, then the rest of the chain (`brain_chain.go`). |
| `run` and its budget | `runBudget` in `brain_budget.go`. A typed prompt gets a fresh budget (`CAPTAIN_RUN_BUDGET`, 200). Every repeat round and chain step spends one turn. A thread passes its budget to the turns it runs through their context, so nested loops spend from the same budget. |
| `stepFixed` | `inboxHTTP` and `noteTurn` in `brain_inbox.go`. A prompt that starts a loop gets a 403 (`StartsLoop`); beyond the quota a prompt gets a 429; a turn the inbox did not hand over refills the quota. |

## What the model does not cover

- **Fidelity.** The model is written by hand from the Go code; nothing extracts it
  automatically. The Go tests check the same properties on the real code:
  - `TestChainSplitsASoloBriefFromARepeatLoop` and `TestChainLeavesCWLAndProseAlone` for
    splitting;
  - `TestChainAndNestedLoopsShareOneRunBudget` and `TestRepeatStopsWhenTheRunBudgetIsSpent`
    for the budget;
  - `TestInboxRefusesPromptsThatStartALoop` and `TestInboxQuotaRefillsOnlyWhenTheUserTypes`
    for the inbox.
- **Who typed a turn.** The brain cannot authenticate the user. A turn counts as typed when its
  text is not a prompt the inbox handed to the TUI. A process that can submit to the TUI
  directly, without going through the inbox, is outside the model.
- **Cost of one turn.** The model counts turns. A turn's own cost is bounded by the worker caps
  and by each worker's timeout, not by this proof.
- **Settings.** The bounds hold for whatever values `CAPTAIN_RUN_BUDGET`,
  `CAPTAIN_INBOX_QUOTA` and `CAPTAIN_REPEAT_MAX` have. Raising them raises the bound.
