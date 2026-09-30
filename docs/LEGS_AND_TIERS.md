# Legs and tiers

Status: proposed (2026-09-30). Not implemented.

## Problem

A leg name answers two different questions, and the registry mixes them:

- **Where the model runs.** Which binary or API is called, which credential and
  quota window it draws on, how reliable and how slow that route is. `claude`
  (the claude CLI), `codex-cli` (the Codex CLI), `cursor` (cursor-agent), and
  `deepseek` beside `ds4-flash` (OpenRouter beside Hugging Face) are named this
  way.
- **How strong a model the prompt needs.** `luna` and `codex` both run
  through opencode on the same ChatGPT login and differ only in model. So do
  `grok` and `grok-max` on the xAI login. `frontier` is not a route at all: it
  is a pseudo-leg meaning claude at max effort. codex-cli is flagged
  `Frontier: true` although the Codex CLI also serves Sol, and the codex leg
  can serve Astra.

The mix has costs:

- The same model keeps two scorecards. Astra through `codex exec` and Astra
  through opencode are scored as two different things.
- Legs that share one quota window cool down separately. `luna` and `codex`
  draw on the same ChatGPT window, but a 429 on one does not cool the other.
- A new model means a new leg. GPT-6.1 Sol had to replace a model inside the
  `codex` leg, and a cheaper sibling would need a leg of its own, as `luna`
  did.
- Nothing states which route can serve which model. That `codex exec`
  refuses `gpt-6.1-sol` on a ChatGPT account (2026-09-30) is written only in a
  comment.

## The model

### A leg is a vendor and a route

A leg names what runs locally and what it calls. The leg owns only the
properties of the route:

- the credential and its quota windows, and so the cooldowns
- reliability, latency and time budget
- tool behaviour (an agent CLI with its own tools, or a model behind
  opencode's tools)
- which models the route actually serves

Naming rules:

1. `<vendor>` is the vendor's models reached through opencode, whether by
   OAuth subscription or API key: `codex`, `grok`, `gemini`.
2. `<vendor>-cli` is the vendor's own agent CLI: `claude-cli`, `codex-cli`,
   `cursor-cli`.
3. `<vendor>-<host>` is the same vendor's models served by a second host:
   `deepseek-hf`, `deepseek-nim`.
4. An open-weight family whose vendor name is already taken keeps its family
   name: `gpt-oss` (OpenAI's open weights, not the `codex` route).

### A tier is the quality a prompt needs

| Tier | Meaning | Preference word |
|---|---|---|
| `cheap` | smallest model that does the job | `/save` |
| `fast` | latency-optimised model or mode | `/speed` |
| `quality` | the route's strong everyday model | `/quality` |
| `frontier` | the strongest model the route serves, at max effort | `/frontier` |

The four preference words exist already. Today they choose an effort and a
lane. After this change they choose a tier, and the lane picks the leg.

Every leg maps each tier to a model. A route with one model serves it in
every tier. An **empty** slot means the route cannot serve that tier. That is
different from a weak model, and the router skips the leg for that tier.

Tier and effort stay separate. The tier picks the model slot, and the effort
picks the reasoning budget within it. Defaults: `cheap` runs at low, `fast` at
low, `quality` at medium or high from the task's class, and `frontier` at max.
Today `TierOf(effort)` (`tiers.go`) derives the tier from the effort. That is
reversed: the tier is chosen, and the default effort follows from it.

### The router picks a pair

A route decision is a (leg, tier) pair, which resolves to a (leg, model,
effort) run. `expected.go` already scores (leg, effort) pairs, and this
generalises it. Lanes become tier choices. `/frontier` means "frontier tier,
on whichever leg the lane balancer picks next".

### Scores follow the model, health follows the route

- **Quality** (director grades, the perf index, priors) is keyed by model and
  effort band. Astra's grade is the same whether it ran through `codex exec`
  or opencode.
- **Health** (failures, harness failures, timeouts, latency, cooldowns,
  quotas) is keyed by leg. The same model can be healthy on one route and
  failing on another.

## Naming table

| Today | Leg | Tier slots (cheap · fast · quality · frontier) | Old name keeps working as |
|---|---|---|---|
| `claude` | `claude-cli` | sonnet · sonnet · opus (default) · opus at max | `/claude` |
| `frontier` (pseudo-leg) | - | - | `/frontier`, now a tier |
| `codex-cli` | `codex-cli` | gpt-6-sol · gpt-6-sol · gpt-6-astra · gpt-6-astra at xhigh | - |
| `codex` | `codex` | gpt-6-luna · gpt-6.1-sol-fast · gpt-6.1-sol · gpt-6-astra | - |
| `luna` | `codex` | (folded into `codex:cheap`) | `/luna` = `/codex:cheap` |
| `cursor` | `cursor-cli` | composer-2.5 · composer-2.5 · grok-4.7 at the task's effort · grok-4.7-xhigh | `/cursor` |
| `grok` | `grok` | grok-build-0.1 · grok-build-0.1 · grok-4.7 · grok-4.7 at max | - |
| `grok-max` | `grok` | (folded into `grok:frontier`) | `/grok-max` = `/grok:frontier` |
| `deepseek` | `deepseek` | v4-flash · v4-flash · v4-pro · v4-pro | - |
| `ds4-flash` | `deepseek-hf` | v4-flash in every slot | `/ds4-flash` |
| `ds-flash` (in progress) | `deepseek-nim` | v4.1-flash in every slot | `/ds-flash` |
| `free` | `nemotron` | nemotron-3.5-lightning-free in every slot | `/free` |
| `gemini`, `glm`, `qwen`, `kimi`, `minimax`, `step`, `gpt-oss` | unchanged | the leg's current tiers; one model fills every slot | - |
| `jev` | unchanged | not a worker; no tiers | - |

Two changes to how grok works: its quality slot moves from grok-build to
grok-4.7, and grok-build becomes `fast`. The old grok leg ran grok-build as
its everyday model because grok-4.7 lived on `grok-max`.

## Where names and tiers are shown

- **The right-panel roster lists legs only:** `claude-cli`, `codex`,
  `codex-cli`, `cursor-cli`, `grok`… One row per leg, never with a tier. The
  row's detail may list the tier slots, but the name has no suffix.
- **Anything about a run shows `leg:tier` plus the model and effort.** This
  covers the Workers panel, Last Runs, the route line, the run log header,
  `captain why`, the ledger and the dashboard. Example: `codex-cli:quality ·
  gpt-6-astra · high`.
- The sidebar's Frontier section is currently built from legs flagged
  `Frontier: true`. It becomes the legs whose `frontier` slot is filled, and
  it lists them by leg name.

## Prefix syntax

| Prefix | Meaning |
|---|---|
| `/codex-cli` | this leg; tier from the task (routing decides) |
| `/codex-cli:quality` | this leg at the quality tier |
| `/frontier` | frontier tier; the lane picks the leg |
| `/luna`, `/grok-max`, … | aliases from the naming table |

`legHeadRe` (`workflow.go`) currently takes the leg name followed by
`[\s:]*`, so `/codex: review this` already means "codex, prompt `review
this`". A tier suffix therefore counts only when it follows the leg with no
space **and** is one of the four tier words: `/codex:quality`. `/codex:
quality review` stays leg `codex` with prompt `quality review`. The workflow
language takes the same form: `/codex-cli:frontier spec > /codex:quality
implement`.

A `leg:tier` whose slot is empty is an error that names the slots the leg
does fill. An empty slot is refused, never silently filled from another. A
slot that repeats the leg's only model is not empty: `deepseek-hf:frontier`
runs v4-flash at max effort, and the route line says it is the same model.

## Ledger and config migration

- **Read-time aliases, no rewrite.** Old events keep their leg names on disk.
  When the ledger loads, an alias table maps each old name to (new leg, tier,
  model): `luna` → (`codex`, `cheap`, `gpt-6-luna`), `grok-max` → (`grok`,
  `frontier`, `grok-4.7`), `claude` → (`claude-cli`, from the event's effort).
  New events record the leg, tier and model explicitly. The ledger version is
  bumped. An old binary refuses the new format, as every versioned type
  already does.
- **Quotas and cooldowns** are re-keyed to the new leg. `luna` and `codex`
  collapse into one entry, which is correct because they share one window.
- **Environment variables.** `CAPTAIN_<PREFIX>_MODEL` and its `_CHEAP_MODEL` /
  `_FRONTIER_MODEL` siblings gain a `_FAST_MODEL`, and take the new prefixes
  (`CAPTAIN_CLAUDE_CLI_…`, `CAPTAIN_CURSOR_CLI_…`). The old variables are
  read as fallbacks, and `captain doctor` names each one still in use.
- **Registry overlays** (`"tiers": {…}`) accept `fast`. An overlay keyed by an
  old leg name is applied to its new leg, with a warning.

## Order of work

Each step ships on its own with the suite green:

1. **Tiers without renames.** Add `fast`, fill every leg's four slots in the
   registry, and record tier and model on every event. Nothing visible
   changes.
2. **Tier as a choice.** Make the four preference words tiers, reverse
   `TierOf`, and add the `leg:tier` prefix and workflow syntax.
3. **Split the scores.** Quality by model and effort band, health by leg.
   Priors and `captain why` read the new keys.
4. **Rename and fold.** Apply the naming table with aliases. Fold `luna`,
   `grok-max`, `ds4-flash` and the `frontier` pseudo-leg into tiers.
5. **Display.** Roster rows by leg only; run rows as `leg:tier · model ·
   effort`; the Frontier section from filled frontier slots.
6. **Docs.** `CONFIGURATION.md`, `ADDING_A_LEG.md`, `WORKFLOW_LANGUAGE.md`,
   `TUI.md`, and `FRONTIER_ROUTING.md` (its "frontier lane" becomes "frontier
   tier").

Aliases for prefixes stay indefinitely, because they cost nothing. Aliases for
environment variables are removed after two releases, with `captain doctor`
warning in the meantime.

## Tests

- Every leg resolves all four tiers, and a route's refused model (for
  example `gpt-6.1-sol` on `codex-cli`) is never placed in a slot.
- Prefix parsing: `/codex:quality`, `/codex: quality review`, `/luna`,
  `/grok-max`, `/frontier`, and a workflow using `leg:tier` in several stages.
- Ledger: a fixture of old events loads to the same totals under the new
  keys; `luna` and `codex` cooldowns merge.
- Scores: two runs of one model on two routes share a quality score and keep
  separate health.
- The roster never shows a tier; a run row always does.

## Open questions

- Should the `fast` slot be filled on every leg, or only where a
  latency-optimised variant exists (`gpt-6.1-sol-fast`, Composer,
  grok-build)? Filling it with the quality model elsewhere keeps `/speed`
  working everywhere but blurs what fast means.
- Is `nemotron` the right name for `free`? The name `free` describes the
  cost, not the route. It is also how the `/free` pool reads, so it may be
  worth keeping as a pool alias only.
- Should a Claude API route (`claude`, through opencode with an API key) be
  added once the name is free? It would give Claude a route without the Max
  subscription's windows.
