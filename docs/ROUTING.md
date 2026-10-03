# Routing a bare prompt

Status: describes the code as of 2026-10-03.

A bare prompt has no `/` command: no leg, no lane, no `/team`. The TUI sends it
as `model=auto`, and the brain picks the leg. This page explains how the brain
picks, what the picks were over the last two weeks, and where it goes wrong.

Short answer: a bare prompt rated **trivial** or **medium** always runs on an
open-weight or mid-priced leg. It can never reach claude, codex-cli or
grok-max. Only a prompt rated **high** reaches the director, and only the
director can pick a frontier leg. The rating comes from keywords and length in
the last user turn, not from the conversation.

## The path

### 1. Rate the prompt (triage)

`TriageTask` (`pkg/captaincode/triage.go`) scores the **last user turn** only.
There are three classes: trivial, medium and high (`captaincode.go`).

- **High** needs 3 or more "hard" points that outnumber the "easy" points.
  Hard points come from keywords such as refactor, security, debug, audit,
  architecture, proof. A prompt over 60 words adds 1; over 120 words adds 2.
- **Trivial** gets 2 easy points when the prompt has 12 words or fewer and no
  constructive anchor ("write", "implement", "test"…). A short question adds 1.
- Everything else is **medium**.
- Confidence is 0.35 + 0.15 × margin, plus a domain bonus.

### 2. Second opinion, when the rating is unsure

Only for trivial and medium (`decideRoute`, `cmd/captaincode/brain.go`):

| Heuristic confidence | What happens |
|---|---|
| below 0.6 | jev rates it; if jev fails, the free leg classifies it |
| 0.6 to 0.9 | jev only. Its answer counts only at confidence 0.6 or more; otherwise "heuristic stands" |
| 0.9 or more | the heuristic decides alone |

A jev answer under the bar is discarded, even when it rates the prompt
harder. The router never moves a prompt up a class on its own.

### 3. Trivial and medium: the fast path

The director is not asked. `valueCandidates` (`brain_value.go`) removes
claude and every frontier-class leg ("reserved for /frontier, /quality and the
director"). From the rest:

1. **Value score** (`value.go`): quality, cost and latency, weighted
   0.3 / 0.6 / 0.1 for trivial and 0.5 / 0.3 / 0.1 for medium. A leg needs a
   quality prior of 5.5 (trivial) or 7.0 (medium). Subscription and $0 legs
   cost 0.
2. **Expected-cost policy** (`expected.go`, the default once 50 outcomes are
   labelled; 395 today): cost + (1 − P(success)) × cost of a redo. It decides
   the leg. Value only orders the menu.
3. **Exploration**: 10% (trivial) or 5% (medium) of turns try the runner-up.
4. **Routing mix** (`/captain more oss`…): adds a bonus only when a mix is
   saved. None is saved today.

Effort follows the class: trivial runs at low effort, medium at medium. The
class also caps how much conversation the worker gets: 48k characters for
trivial, 200k for medium, 400k for high.

### 4. High: the director

The director (claude) picks from the six best-valued legs of the full ladder.
The menu includes claude, grok-max and codex-cli. This is the only way a bare
prompt reaches a frontier leg. Effort is high, but medium on a frontier leg
(`effort.go`).

## What actually happened

Last 400 bare `auto` turns in the brain log, 2026-09-22 to 2026-10-03:

| Path and class | Turns | Open-weight | cursor, gemini | Frontier |
|---|---|---|---|---|
| fast path, trivial | 98 | 82 (84%) | 16 | 0 |
| fast path, medium | 87 | 63 (72%) | 24 | 0 |
| director, high | about 33 | 4 | 9 | 20 |

On the 215 fast-path turns the legs were step 85, ds-flash 51, cursor 41,
gemini 15, ds4-flash 13, others 10. The heuristic alone settled the class on
197 of 218 journalled decisions. About 15% of bare prompts rate high.

Open-weight legs: step, ds-flash, ds4-flash, glm, kimi, minimax, qwen,
deepseek, free. Frontier legs: claude, codex-cli, grok-max.

## Where it goes wrong

Ranked by how much they cost today.

1. **Fixed 2026-10-03: expected cost favoured cheap paid legs, whatever their failure rate.** A redo is now priced at no less than the user's time (half the reference cost) on every leg. Replayed on the live history, a medium code task that went to step now goes to cursor (P 0.93), then glm and kimi; step ranks fourth. The original finding: A
   redo on a per-token leg is priced at that leg's own tokens: step costs about
   $0.0024 for 16k tokens. A redo on a subscription or $0 leg is priced at
   $0.25. So step's expected cost stays at $0.003 to $0.008 even at
   P(success) = 0.41, and cursor would need P above about 0.98 to win. Live: a
   "commit and push" went to step at P = 0.41 over cursor, the value leader.
   The log often reads "value-ranked gemini > cursor > step" and step runs.
2. **Fixed 2026-10-03: short meant trivial, whatever the conversation.** A follow-up ("continue", "go on", "do it", "yes", up to 8 words) is now routed on the last real request before it, and is never rated below medium, even after a second opinion. The original finding: "continue where we
   stopped" is four words, so trivial at confidence 0.65, above the bar for a
   second opinion from the free leg. jev said medium at 0.57, under its bar,
   so it was dropped. The turn ran on step at low effort with a 48k-character
   replay of a 306k-character session, and timed out after 26 to 30 minutes
   (2026-10-03).
3. **An unsure, harder second opinion never escalates.** When jev rates the
   prompt harder but under its bar, the heuristic stands.
4. **High is keywords only.** A trivial or medium rating can never reach a
   frontier leg, however hard the work turns out to be. The proposal in
   [FRONTIER_ROUTING.md](FRONTIER_ROUTING.md) covers escalating on the kind of
   thinking a task needs.
5. **Two heuristics disagree.** `Classify` (`captaincode.go`) sets the
   director's starting rung and ranking; `TriageTask` decides the fast path.
6. **High on a frontier leg runs at medium effort** (`effort.go`).
7. **The leg list narrows the field.** The running brain's `CAPTAIN_LEGS`
   leaves out codex (GPT-6.1 Sol, quality prior 8.4) and luna. That leaves
   cursor as the only closed leg with a prior of 8 or more on the fast path.

## What to change

Proposals, not implemented:

- Done: price a redo by the user's time on every leg.
- Done: route a short follow-up on the request it continues.
- When jev rates a prompt harder than the heuristic, take the harder class
  even under jev's confidence bar: a wrong escalation costs money, a wrong
  de-escalation costs the turn.
- Unify `Classify` and `TriageTask`.
- Add codex back to `CAPTAIN_LEGS` so the fast path has a strong mid-cost leg.

## Settings

| Setting | Effect |
|---|---|
| `CAPTAIN_ROUTING_POLICY` | `expected` (default with 50+ outcomes), `value`, `bandit` |
| `CAPTAIN_PICK` | `shadow` (default), `on`, `off`: the time-based pick (SCORING.md) |
| `CAPTAIN_LEGS` | the legs auto may use |
| `/captain more <axis>` | a standing routing mix; see CONFIGURATION.md |
| `/quality`, `/frontier`, `/save` | lanes: skip the fast path for one turn |
