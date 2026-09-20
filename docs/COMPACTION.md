# Compaction: what it costs, and what it keeps

*Measured 18–19 September 2026 against `jev-1.13.0` and captain's own ledger.
Every figure here is captain's own measurement unless it names someone else's.*

Context compaction is the most-copied idea in agent harnesses right now and
one of the least-measured. The plugin ecosystem around it publishes designs -
which blocks to keep, how to batch them, where the cap is - and, as far as we
can find, no latency, no cost, and no recall figures at all. This page is
captain's, with the method attached, so the numbers can be argued with.

## What captain does today

A turn that no longer fits is fitted in stages
(`cmd/captaincode/brain_compact.go`, `brain_prune.go`):

1. **snip** — a deterministic pre-compaction: elide oversized blocks, drop
   what is provably redundant. Costs nothing.
2. **fold** — summarise what is left into prose, on a worker leg. This is
   where both the money and the seconds are.

### A note on the word "snip"

On `main`, **snip** is stage 1: the deterministic pass, no model involved.
The experiment below adds a *scoring* stage between snip and fold and was
also called "snip" while it was being built. They are different things, and
the rest of this page says **score** for the model stage to keep them apart.

### Status of the scoring stage

**The scoring stage is not on `main`.** It was built and measured in a
separate worktree, and the numbers in "Scoring instead of summarising" and
"Does it keep the right things?" below come from that branch. The fold
baseline and the context ceiling are from `main` and from production data.
`captain snip eval` and `CAPTAIN_SNIP_JEV` ship with the stage, not before
it.

## The fold baseline

From captain's own ledger, over 236 fold calls on a summarising leg:

| | |
|---|---|
| calls | 236 |
| total cost | **$1.98** |
| p50 latency | **5.6 s** |

That is $0.0084 and 5.6 seconds per compaction, spent inside a turn the user
is waiting on.

## Scoring instead of summarising *(experiment, not on `main`)*

The decision leg prices input tokens only and answers in a few hundred
milliseconds, which is the shape of "which of these blocks matter" rather
than "write me a summary". Scoring a whole session as a **manifest** - one
line per block, not the blocks themselves:

| | |
|---|---|
| blocks scored | 296 |
| manifest size | 59,000 characters |
| latency | **1.6 s** |
| cost | **$0.0011** |

Two orders of magnitude cheaper than the fold it replaces, and three and a
half seconds faster.

## Does it keep the right things? *(experiment, not on `main`)*

Cost is the easy half. The question that decides whether the stage is worth
having is recall: of the blocks a later turn actually needed, how many
survived. Measured with a proxy summariser so both arms face the same
downstream:

| arm | recall | p50 latency |
|---|---|---|
| snip → **score** | **0.583** | **409 ms** |
| snip → fold | 0.521 | — |

Scoring keeps more of what mattered, and does it in under half a second. The
kill criteria set before the experiment (recall below the fold arm, or
latency over the 2 s bound) were not tripped.

Read this honestly: 0.583 is not a good recall number in absolute terms. It
is a better number than the thing it replaces, measured the same way, which
is the only comparison that decides anything. Compaction loses context. The
choice is which losses.

## The ceiling, and why a manifest

The vendor documents no context window for the decision leg. There is one.
Measured 2026-09-19 against `jev-1.13.0`:

- a **150,000-character** state (32,412 input tokens) answers
- a **160,000-character** state returns `400 max_tokens_exceeded`

So `SystemOneContextTokens = 32768` (`pkg/captaincode/systemone.go`), and a
caller that would exceed it sends a manifest of what it is choosing between
rather than the content itself. Denser filler hits the wall earlier than
150k characters of natural language does - the limit is tokens, and it is
worth sizing against tokens rather than characters.

This corroborates, independently, the 30k cap that
[`fast-jev-compaction`](https://github.com/tamaratran/fast-jev-compaction)
describes as sitting "below Jev's 32k request cap". Two implementations
arriving at the same ceiling from opposite directions is the most useful
thing either of us can say about it.

## Turning it off

When the scoring stage lands, `CAPTAIN_SNIP_JEV=0` disables it and the
pipeline falls back to snip → fold. **With no decision leg configured the
stage does not run at all** - no call, no added latency - and compaction
behaves exactly as it does today. That is the rule every decision-leg
feature in captain follows, not a special case for this one.

## Method, and what these numbers are not

- The fold baseline is production ledger data from `main`, not a benchmark:
  236 real calls on real turns.
- The recall comparison ran on an unmerged branch and used a proxy
  summariser standing in for the downstream turn. It makes the two arms comparable to each other; it does
  not make 0.583 a statement about a real user's session.
- The manifest measurement is a single configuration (296 blocks, 59k
  characters) against `jev-1.13.0`. `jev-latest` is an alias that moves, and
  an open backend is a different model - `captain jev conform` and the
  per-backend shadow calibration exist because of exactly this.
- None of this is a quality measurement of the compacted turn. Recall is a
  proxy for it, not a replacement.

## Reproducing

On `main` today:

```sh
captain jev conform          # does this backend answer the keep_* questions at all
captain jev shadow           # the routing shadow, per decision point and per backend
```

With the scoring stage, once it lands:

```sh
captain snip eval            # the recall comparison, both arms
```
