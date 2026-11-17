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

## dq-261116-a61645 posted: Next optional roadmap item after P7 step 1

- Posted: 2026-11-16 21:04 UTC
- Branch: main
- Question: P7 step 1, the routing dataset export, is delivered. Which item should the next build round take?
- Context: Step 1 joined decision, class, legs, gate result, repairs, cost and verdict per attempt into a prompt-free file: 3,755 rows on this Mac, tests and vet green, no routing change. Its steps 2 and 3 say in the roadmap that they need your go: step 2 splits that file by date and scores the current hand priors and director. The fleet queue's next four items are host runs only you can do, and the two owner decisions are closed. Everything else in the build queue is blocked, waiting, deferred or proposed.
- Needed before: the next build round
- Record: `docs/ROADMAP.md`
- Options:
  - a (recommended): P7 step 2 - Split the exported dataset by date and score the current hand priors and director on acceptance rate and cost per accepted task; no routing change, no spend
  - b: P1 trailing statistics - Two-week default window with minimum-sample fallback for the statistics that rank legs; this touches routing inputs
  - c: P4 needs-you inbox - Surface only the turns that wait on the user: endpoint, CLI and TUI count
  - d: Hold - No build round until you run the fleet host gates: clean macOS account, Linux user manager, two-host first prompt
- Why a: It scores the data we now hold, spends nothing and changes no route.

## dq-261116-a61645 answered: Next optional roadmap item after P7 step 1
- Question: P7 step 1, the routing dataset export, is delivered. Which item should the next build round take?
- Answered: 2026-11-16
- Choice: a: P7 step 2 - Split the exported dataset by date and score the current hand priors and director on acceptance rate and cost per accepted task; no routing change, no spend

## dq-261117-bb5-paths (dq-261116-f9c709) posted: Which local paths become critical path rules

- Posted: 2026-11-16 23:10 UTC
- Branch: main
- Question: Which directories should the signed local path policy protect on the development Mac?
- Context: BB5 enforcement is on with the sender scope 'exact package and local path rules'. Upstream has no path rules, so only LOCAL input can supply them: the export flag --paths now signs one local 0600 policy file (per-directory globs, at most 256 rules) into the snapshot, and the loader rules match the ones the receiver uses. The policy file must live outside git because a mode-0600 requirement makes any checked-in file unusable. Matching a critical path refuses a read or write before it happens; ordinary paths stay evaluated.
- Needed before: Renewing the pilot snapshot before 23 November 2026, and any expectation that protected_path is signed into production.
- Record: `docs/ROADMAP.md`
- Options:
  - a (recommended): "Locations and secrets - repo checkout roots, the captain config directory, shell and cloud key locations, written as ~/.config/captain/blackbox-paths.json at 0600"
  - b: "Repositories only - just the checkout roots in that same file; secret material keeps relying on the built-in secret patterns"
- Why a: It closes the exfiltration path the pattern rules cannot see (an agent reading a credential directory as an ordinary file) at the cost of one small policy file; the review will show any rule that is too broad.

## dq-261117-bb5-paths (dq-261116-f9c709) answered: Which local paths become critical path rules

- Question: Which directories should the signed local path policy protect on the development Mac?
- Answered: 2026-11-17
- Choice: a: Locations and secrets - repo checkout roots, the captain config directory, shell and cloud key locations, written as ~/.config/captain/blackbox-paths.json at 0600

## captaincode (dq-261117-5008de) posted: Make the codex app-server transport the default

- Posted: 2026-11-17 10:31 UTC
- Branch: main
- Question: The codex app-server transport (/btw steering on codex workers) passed live qualification on codex-cli 0.169.0: one real turn answered with thread id, usage and first-output time, and a mid-turn note amended the running turn's answer. Should it become the codex-cli default?
- Context: Exec stays the only default today, so a /btw for a running codex worker lands as the next turn. The app-server path now carries the same model, effort, sandbox, MCP and proxy settings, falls back to exec when the installed codex changes its schema, and doctor reports why. One live session is the evidence; a default flip changes every codex turn on both Macs at the next binary install.
- Record: `docs/ROADMAP.md`
- Options:
  - a (recommended): Default to app-server - codex-cli workers take /btw notes mid-run; CAPTAIN_CODEX_TRANSPORT=exec opts out; the schema gate still falls back to exec on a version mismatch
  - b: Keep exec - stay on the qualified-but-opt-in transport; flip later after more live use
- Why a: Steering is the feature the transport exists for and the fallback keeps the risk bounded

## captaincode (dq-261117-f0191b) posted: P7 step 3: the offline shadow router

- Posted: 2026-11-17 10:31 UTC
- Branch: main
- Question: P7 steps 1-2 are delivered: the routing dataset export and the by-date scoring of the director and hand priors. Should step 3 build the shadow router next?
- Context: Step 3 replays the exported dataset through the current router offline: it compares the shadow's routing decisions against what the director actually did, still with no routing change and no spend. It is the last step before any real routing change could be proposed from evidence.
- Record: `docs/ROADMAP.md`
- Options:
  - a (recommended): Build step 3 - offline shadow router over the exported dataset; no routing change, no spend
  - b: Hold P7 - keep steps 1-2 and pick a different proposed item (P1-P6) instead
- Why a: It completes the evaluation loop the first two steps opened, at zero spend

## captaincode (dq-261117-5008de) answered: Make the codex app-server transport the default

- Answered: 2026-11-17 12:14 UTC, in galaxy
- Choice: a, Default to app-server

## captaincode (dq-261117-f0191b) answered: P7 step 3: the offline shadow router

- Answered: 2026-11-17 12:14 UTC, in galaxy
- Choice: a, Build step 3
