# Command safety: a formal model

Captain's commands compose. A program chains whole turns (`a > b`), falls back
(`a || b`), loops (`/repeat N body until: check`) and nests groups in
parentheses. A gated turn may take a repair turn, and a worker can send a new
prompt with `captain send`. This directory models those mechanisms in Lean 4
and proves three properties:

1. The scanner loses no text.
2. No prompt can start unbounded work.
3. A worker cannot keep work going without the user.

Build it with `lake build` in this directory (Lean 4.28, no dependencies). CI
runs the same build, so a broken proof fails the build.

## What is proved

| Theorem | File | Statement |
|---|---|---|
| `join_scanAll` | `Splitter.lean` | The segments the program scanner splits a line into, rejoined with the operators it found, are exactly the line. |
| `scanAll_noBoundary` | `Splitter.lean` | A line with no operator before a command is one segment, so prose never becomes a program. |
| `segment_shorter` | `Splitter.lean` | When a line splits, every segment is strictly shorter than the line, so parsing a group's inside again always ends. |
| `turnsNow_unbounded` | `Execution.lean` | Without a shared budget, no constant bounds the turns one prompt starts: k nested `/repeat`s run at least 100^k turns. |
| `runP_turns_le` | `Execution.lean` | A program, however it nests chains, fallbacks, loops and gated turns, and whatever succeeds or fails, dispatches at most its budget of turns. |
| `nestP_bounded` | `Execution.lean` | Nested `/repeat`s, read as one program, are bounded by that budget. |
| `runP_alt_first` | `Execution.lean` | The budget does not cut what fits: a fallback whose first alternative succeeds runs one turn. |
| `now_unbounded`, `now_loops_unbounded` | `Inbox.lean` | Before the inbox rules, with no user input, any number of sent prompts were accepted, and each could start a loop. |
| `fixed_accepted_le` | `Inbox.lean` | At most `quota` sent prompts are accepted between two turns the user types. |
| `fixed_no_loops` | `Inbox.lean` | No sent prompt starts a loop or a program. |

Every theorem depends only on Lean's standard axioms (`propext`, `Quot.sound`,
`Classical.choice`); none uses `sorry`. Termination of `scan`, `turnsNow` and
`runP` is checked by Lean itself, because all three are structurally
recursive.

## How the model maps to the code

| Model | Code |
|---|---|
| `Tok`, `scan`, `scanAll` | `scanTop` in `pkg/captaincode/program.go`. An operator (`>`, `->`, `\|\|`) splits only outside groups, before a command or a `(` opening on one (`commandHeadAt`), and not after a blank line since the previous operator. A group opens only at a step start and is skipped whole, every parenthesis inside counted (`matchParen`). A backtick span is one token: code is never syntax. |
| `Prog.turn gated` | One dispatched turn: a workflow, a leg, `/team`, a lane turn, or a loop body. A gate checked by the program runner adds at most one repair turn. |
| `Prog.seq`, `Prog.alt`, `Prog.loop` | A chain (a failed step stops it), a fallback (`\|\|` runs the next alternative only on failure), `/repeat N … until:` (rounds bounded by `rounds n`, `CAPTAIN_REPEAT_MAX`). |
| `runP` and its budget | `handleProgram` and the program runner in `cmd/captaincode/brain_program.go`: one budget of `CAPTAIN_REPEAT_MAX` turns per program, and a program's own turns never start a program. Plain `/repeat` threads share `runBudget` (`brain_budget.go`, `CAPTAIN_RUN_BUDGET`) the same way. |
| `stepFixed` | `inboxHTTP` and `noteTurn` in `cmd/captaincode/brain_inbox.go`. A prompt that starts a loop or a program gets a 403 (`StartsLoop`); beyond the quota a prompt gets a 429; a turn the inbox did not hand over refills the quota. |

## What the model does not cover

- **Fidelity.** The model is written by hand from the Go code; nothing extracts
  it automatically. The Go tests check the same properties on the real code:
  `pkg/captaincode/program_test.go` and the conformance suite
  (`testdata/language/conformance.txt`) for scanning and parsing;
  `TestRepeatStopsWhenTheRunBudgetIsSpent` and the program runner's tests for
  the budget; `TestInboxRefusesLoopsAndPrograms`,
  `TestInboxRefusesPromptsThatStartALoop` and
  `TestInboxQuotaRefillsOnlyWhenTheUserTypes` for the inbox.
- **Tokenizing.** The model starts from tokens. Recognising a command head
  (`legHeadRe`, paths excluded), a code span and a blank line is the code's
  job and is tested, not proved.
- **Who typed a turn.** The brain cannot authenticate the user. A turn counts
  as typed when its text is not a prompt the inbox handed to the TUI.
- **Cost of one turn.** The model counts turns. A turn's own cost is bounded by
  the worker caps and timeouts, not by this proof.
- **Settings.** The bounds hold for whatever values `CAPTAIN_REPEAT_MAX`,
  `CAPTAIN_RUN_BUDGET` and `CAPTAIN_INBOX_QUOTA` have. Raising them raises the
  bound.
