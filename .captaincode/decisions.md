# Decisions for the user

Captain appends to this file. A worker adds an entry when it posts a question
that only the user can answer. Captain adds an entry when the user answers the
question or the worker withdraws it. The answer itself goes in the decision
record that the entry names. Do not edit old entries. After a merge conflict,
keep the entries from both sides.

## dq-261115-c32ef1 answered: Rule export contract for authenticated snapshots

- Question: BB3 is blocked on the rule export and authority contract. Which path should the next build take?
- Answered: 2026-11-16
- Choice: a: Narrow sender contract - Write down the exact export fields first, then build one small exporter around the pinned upstream rule code and prove it with rejection tests.

## P7 (dq-261116-9b3a84) posted: Next optional roadmap item

- Posted: 2026-11-16 10:32 UTC
- Branch: main
- Question: BB1-BB4 are complete and the remaining fleet rows need host acceptance or owner approval. Which proposed Captain Code item should the next build round take?
- Context: The roadmap forbids starting blocked, waiting, deferred, needs-you, or proposed rows without your choice. P7 step 1 only exports a privacy-bounded routing dataset and does not change routing. P1 changes the statistics window used by routing; P4 adds a waiting-turn inbox.
- Record: `docs/ROADMAP.md`
- Options:
  - a (recommended): P7 dataset export - Build only step 1: join routing decisions and outcomes without prompt text; no routing change
  - b: P1 trailing statistics - Add the proposed two-week default statistics window with minimum-sample fallback
  - c: P4 needs-you inbox - Add the waiting-turn endpoint, CLI and TUI count
  - d: Hold feature work - Wait for the fleet host-acceptance gates and BB5 evidence
- Why a: It adds evaluation evidence without changing routing or requiring new infrastructure.
