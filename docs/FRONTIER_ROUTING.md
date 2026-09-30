# Frontier routing: using the frontier only where it pays

Status: proposed (2026-09-30). Not implemented.

## Problem

Captain uses a frontier model (claude at max effort, codex-cli on Astra at
xhigh) only when the user types `/frontier`. Auto mode never escalates on its
own: triage settles the class, the fast path or the director picks a leg, and
the effort stops at high (`DecideEffort`, `effort.go`). This fails in two ways:

- **Under-use.** A spec, a design or a proof step that needed the strongest
  model runs on whichever leg the lane balancer or the value ladder puts first.
  Whether it needed the frontier is left for the user to notice afterwards.
- **Over-use.** A typed `/frontier` gets max effort whatever the work. On
  2026-09-24 a copy-edit of six paragraphs ran 8m36s at max effort, most of it
  silent thinking.

What matters is the kind of thinking a task needs, not how hard it is. Mapping
out an architecture, weighing a trade-off or checking a cryptographic claim
gains from the frontier. Implementing a spec that already exists, applying an
edit or committing does not, however long the prompt is.

## Goal

Auto mode sends a turn to the frontier lane when the task is reasoning-bound
and the frontier has been measured to do better on that kind of task. Every
other turn keeps today's routing. The executor for the implementation turns
that follow is a strong mid-cost leg (the codex leg on GPT-6.1 Sol), not the
frontier.

Non-goals:

- Overriding the user. A typed `/frontier`, `/quality` or leg prefix is always
  honoured.
- A new leg or model. This uses the existing frontier lane (`lanes.go`).
- Changing the director's grading.

## Design

### 1. Triage records the kind of task

`TriageResult` gains `Kind`:

| Kind | Meaning | Examples |
|---|---|---|
| `think` | The answer depends on judgement that is not written down yet | spec, design, architecture, plan, trade-off, threat model, proof, "is this claim right", "why does X happen" |
| `execute` | The judgement exists and the task carries it out | implement the spec, apply these edits, rename, fix the failing test, commit and push, rewrite this paragraph |

- Tier 0 is a heuristic signal list beside `highSigs` in `triage.go`, with
  verbs for `think` and verbs for `execute`. When neither side scores, the
  default is `execute`, because a wrong `think` costs frontier quota.
- When jev or the director classifies the turn, it also returns `kind`, and
  its answer overrides the heuristic, as it already does for the class.
- `Kind` is stamped on the decision and the event (`stampTriage`), so every
  graded run carries it.

### 2. The escalation gate

The auto route (`decideRoute`, `brain.go`) sends the turn to the frontier lane
only when every condition below holds:

1. No preference or leg was stated.
2. `Class == high` and `Kind == think`.
3. The mid-tier estimate is weak: `MidTierP == 0` (nobody estimated it) or
   `MidTierP < CAPTAIN_FRONTIER_MIDTIER` (default 0.6). This estimate already
   exists (`triage.go`, "the ex-ante quality estimator the cascade literature
   calls the critical factor"). This spec gives it its first job outside
   `expected.go`.
4. The measured gain allows it (section 3).
5. The auto-escalation budget has room (section 4).

`Irreversible` does not escalate on its own; it already raises the effort one
rung. An irreversible `think` task passes condition 3 without needing a
`MidTierP`.

When the gate passes, the turn runs exactly as a typed `/frontier` runs today:
the frontier lane picks the leg and the effort is max. Otherwise routing is
unchanged.

### 3. Escalate only where it has paid off

The ledger already holds a director grade (0-10) for each scored run, with its
leg, class and domain. The gate compares two groups of `think`/`high` runs
over the last `CAPTAIN_FRONTIER_WINDOW` days (default 60), separately for each
domain:

- **frontier:** runs on the frontier lane (claude at max, codex-cli at xhigh),
  whether typed or escalated.
- **quality:** runs on the quality lane or the value ladder at high effort.

`gain = mean(frontier) - mean(quality)`

| State | Rule |
|---|---|
| Either group has fewer than `CAPTAIN_FRONTIER_MIN_N` runs (default 8) | Cold start: escalate on a `CAPTAIN_FRONTIER_EXPLORE` share of gated turns (default 0.5). Both groups fill up. |
| `gain >= CAPTAIN_FRONTIER_GAIN` (default 1.0 point) | Escalate. |
| `gain < CAPTAIN_FRONTIER_GAIN` | Do not escalate, but keep escalating 10% of gated turns so the measurement can still change its mind. |

Measuring per domain keeps the answer honest where it differs. Research and
editorial `think` tasks may gain less than code architecture. One number for
everything would hide that.

### 4. A budget, because frontier quota is shared

Claude Max and the ChatGPT subscription both count against 5-hour and weekly
windows. Typed `/frontier` turns draw on the same quota. Auto escalations are
capped at `CAPTAIN_FRONTIER_AUTO_MAX` per rolling 5 hours (default 6). Past the
cap the turn takes today's route, and the rationale says so. A frontier leg in
cooldown is skipped as it is now, and the lane falls back to its other leg.

### 5. Spec first, then execute

Once a frontier turn has written a spec, a plan or a design, the next turns in
the session implement it. Those are `execute` turns, so they fail the gate and
take the ordinary route, which is what should happen. The quality lane and the
value ladder put the codex leg (GPT-6.1 Sol, about Astra-level agentic coding
at a fifth of the price) and cursor in front for that work.

One addition: when an escalated `think` turn ends with a plan, the reply ends
with one line naming the handoff, for example
`next: implement with /codex, or /frontier spec > /codex implement as one
workflow`. Captain does not start that turn by itself.

### 6. Typed `/frontier` on an `execute` task

The user's prefix wins. When a typed `/frontier` turn triages as
`execute`/`trivial`, the route line shows a hint: `frontier on an execute
task; /quality would likely match it faster`. The hint changes nothing and is
shown at most once per session.

## Observability

- The route line and `captain why` give the reason: `frontier (auto):
  think/high, mid-tier p=0.42, gain +1.3 over 14/22 runs (code), budget 2/6`.
  A declined escalation reports the failing condition: `stayed: gain +0.4 <
  1.0 (research)`.
- The dashboard gets one panel with, per domain, the frontier and quality mean
  on `think`/`high` tasks, the gain, n, and auto escalations against the
  budget.
- `CAPTAIN_FRONTIER_AUTO=0` turns the whole feature off. `=shadow` records
  what the gate would have done on every decision and never acts.

## Rollout

1. **Shadow, one week.** Ship `Kind` and the gate with
   `CAPTAIN_FRONTIER_AUTO=shadow`. Check that the `think`/`execute` split
   matches a hand-labelled sample of 50 recent turns (target: 85% agreement,
   and fewer than 1 in 10 `execute` turns labelled `think`).
2. **On.** Default to on once the split holds. Keep the cold-start exploration
   until each domain has `MIN_N` runs in both groups.
3. **Review after 30 days.** Compare mean quality and frontier quota spent
   before and after, per domain.

## Tests

- Triage: `think` and `execute` on fixed prompts, including the 2026-09-24
  copy-edit (`execute`) and "check whether OPSIS implements recursive
  aggregation" (`think`).
- Gate: each condition failing on its own keeps today's route, and all
  conditions passing picks the frontier lane.
- Gain: the cold start, above and below the threshold, and each domain
  measured separately, all from a fixture ledger.
- Budget: the seventh escalation in 5 hours stays on the ordinary route.
- Precedence: a stated preference or leg is never overridden.

## Open questions

- Should `Kind` also steer the director's own pick on high-class turns that
  fail the gate? Its menu could list frontier legs first for `think`, below
  the threshold.
- The 1.0-point threshold is a guess. The shadow week should show the spread
  of director grades between the two groups before it is fixed.
- Should an escalated turn run both frontier legs and let the director pick,
  like `/frontier + /codex-cli`, when the gain is measured but close? That
  doubles quota and is left out of the first version.
