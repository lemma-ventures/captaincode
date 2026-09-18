# Adding a leg

A **leg** is a named model behind a runtime. Legs are data, not code: the
registry has compiled defaults and an overlay at `~/.captaincode/legs.json`, and
everything else - ladder order, provider pins, env overrides, the director's
menu, prompt budgets, the CLI's model list - derives from it.

## The usual case

```sh
captain legs add gemini google/gemini-3.7-flash --prior 7.8 \
  --note "fast and cheap; excellent summariser, weak on multi-step architecture"
```

That writes the overlay entry, fetches price and context window from the
provider catalogue where it can, and adds the model to opencode's config so a
worker session can actually select it. Then:

```sh
captain doctor          # confirm it is ready, not just declared
captain /gemini "summarise what pkg/foo does"   # force it once
```

Restart the brain and relaunch the terminal afterwards - both read config at
startup.

## Fields that matter

| Field | Why it matters |
|---|---|
| `prior` | Cold-start quality, 0–10, on the same scale as every other leg. It decides routing until the leg has a scorecard of its own. Guessing high is worse than guessing low: a wrong high prior wins every ranking until enough runs correct it. |
| `price_in` / `price_out` | USD per million tokens. `0` means a subscription or free tier - those carry *quota pressure* instead of price. |
| `ctx` | Context window. Drives the prompt budget, so a wrong value shows up as truncated or rejected prompts. |
| `note` | One line shown to the director when it picks. Describe what the model is good and bad at; do not sell it. |
| `vision` | Image tasks only go to legs with this set. |
| `frontier` | Double worker budget, never auto-assigned on the cheap path, reroute target of last resort. |

## Verify before you trust the catalogue

A model listed by a provider is not a model that answers. We have shipped legs
that returned 404 (entitlement missing), 410 (retired but still documented) and
one that simply hung past three minutes. Always invoke it once, with generous
`max_tokens` - reasoning models spend their budget before emitting a token, so a
10-token probe returns empty and looks broken.

## When a provider retires a model

Repoint the leg rather than adding a new one:

```sh
export CAPTAIN_GLM_PROVIDER=openrouter
export CAPTAIN_GLM_MODEL=z-ai/glm-5.2
```

The leg keeps its identity, its scorecard and its place in the ladder. A new leg
id starts from a cold prior and loses the history.

## Decision legs (`system-one`)

Not every model behind a runtime takes a task. TypeSafe's **jev** answers typed
questions - a choice, a score, a yes/no - with calibrated probabilities, and
never generates text or runs a tool. Captain registers such a model as a
**decision leg** on the `system-one` transport:

```json
{"id": "jev", "transport": "system-one", "provider": "typesafe", "model": "jev-latest", "price_in": 0.042}
```

It is a leg everywhere a leg is *listed* - `captain legs` (flag `decision`),
`captain doctor` (ready by its key: `TYPESAFE_API_KEY`, or a `jev.env` drop-in like `aa.env`), `captain legs caps`
(`tools: no`), the ledger and `captain why` - and nowhere a task is
*dispatched*: never a worker rung, a TUI model, a `/jev` command, a workflow
stage or a reroute target. Its `prior` is 0 because a worker prior is not a
thing it has. What it does is answer the brain's own questions: triage
classification today (see `CONFIGURATION.md`, "Decision legs"), through
`captaincode.ClassifyWithJev`; `captain jev ask` asks it anything.

Adding a second decision model means a second `system-one` entry with its own
provider and key handling; adding a second *use* of one means a new question
set in `pkg/captaincode/systemone.go` beside `jevClassQuestions`.
