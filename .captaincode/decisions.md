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

## captaincode (dq-261116-0cd13b) posted: Enforcement hosts and rule categories

- Posted: 2026-11-16 13:54 UTC
- Branch: main
- Question: BB5 has a signed rule store and a green 1,000-action benign review. Which hosts and rule categories may turn on enforcement?
- Context: The pilot store pilot-20261116-1 is authenticated and expires 23 November 2026, and it must be re-exported or allowed to lapse before then. The benign review passed 1,000 actions across both supported hosts and two disposable workspaces in audit and enforce, with zero refusals and zero degraded screenings, and audit mode is now set on this Mac for the next brain start. Enforcement stays off until you choose: only exact-package and local-path rules at critical severity can ever refuse, and the second Mac stays owner-gated.
- Record: `docs/ROADMAP.md`
- Options:
  - a (recommended): This host first - enforce exact-package and local-path critical rules on the development Mac after its audit records stay clean; the second Mac stays audit-only until it passes the same review
  - b: Both Macs - enforce on both Macs at once, after the second Mac passes the same 1,000-action review and a week of clean audit records
  - c: Stay audit only - turn on nothing now; keep audit records and the signed store until real worker traffic gives more evidence
- Why a: One host carries the burden, the scope is proven green, and the rollback is one unset variable

## P7 (dq-261116-9b3a84) answered: Next optional roadmap item

- Question: BB1-BB4 are complete and the remaining fleet rows need host acceptance or owner approval. Which proposed Captain Code item should the next build round take?
- Answered: 2026-11-16
- Choice: a: P7 dataset export - Build only step 1: join routing decisions and outcomes without prompt text; no routing change

## captaincode (dq-261116-0cd13b) answered: Enforcement hosts and rule categories

- Question: BB5 has a signed rule store and a green 1,000-action benign review. Which hosts and rule categories may turn on enforcement?
- Answered: 2026-11-16
- Choice: a: This host first - enforce exact-package and local-path critical rules on the development Mac after its audit records stay clean; the second Mac stays audit-only until it passes the same review
