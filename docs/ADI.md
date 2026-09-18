# Routing on the Agentic Determinism Index

The [Agentic Determinism Index](https://github.com/lemma-ventures/agentic-determinism-index)
(ADI) asks hosted model APIs one question: send the same request N times,
how identical are the answers? It scores **serving tuples** - provider,
model, and the pin that reproduces the deployment - not model families,
because determinism is a property of the deployment. Captain uses it for
`/deterministic`, and `/oss` beside it for open weights.

## Two connections, one source

- **The feed (what the brain uses).** ADI's `site` command writes
  `leaderboard.json` next to the page: every ranked tuple with its mean mode
  share, byte-exact streak, a `green` flag (latest scored appearance fully
  byte-exact) and the `provider_prefs` the reference config sends to pin the
  tuple (OpenRouter `provider.order`, `allow_fallbacks`). The brain fetches it
  every 6 hours (`CAPTAIN_ADI_URL`, cache `~/.captaincode/adi.json`), like the
  Artificial Analysis perf feed. A router needs data, not a conversation, and
  the director plans as a judge with no tools, so the brain reads the feed
  directly rather than calling MCP.
- **MCP (what agents use).** `python3 -m agentic_determinism_index mcp`
  serves the same rows as tools (`adi_leaderboard`, `adi_tuple`, `adi_green`)
  over stdio, standard library only. Register it in Claude Code, opencode or a
  worker's MCP config when an agent should ask "which stack is reproducible?"
  before choosing one. Captain's own workers do not need it: the pool has
  already chosen their leg.

## What `/deterministic` does

1. `MidPromptPool` reads the word wherever it stands in the turn (it composes
   with `/repeat`, `/team`, `/quality`, a leg prefix, in either order).
2. The route's menu - triage fast path, director menu, team menu - is
   narrowed to legs whose serving tuple is green: `ADIFor(leg)` matches the
   leg's provider and model id to the feed (opencode `openrouter` ↔ ADI
   `openrouter`, `nim` ↔ `nvidia_nim`, …), green first, then by rank.
3. The run is pinned: while the worker runs, the egress proxy rewrites every
   OpenRouter request for that model with the tuple's `provider_prefs` and
   `temperature: 0` unless the caller set one. The feed shows
   `deterministic: gpt-oss pinned to Cerebras via OpenRouter (ADI green, streak 10)`.
4. Nothing green among the registered legs → the turn runs without the pool
   and the feed names the green tuples no leg serves; `captain adi` prints the
   same table; `captain legs add <id> openrouter/<model>` registers one.

## Limits, stated

- Green is a snapshot of a stack, not a certification: it can turn red on the
  next reference run, and the brain follows the feed within 6 hours.
- Only tuples ADI measures can be green. The CLI legs (claude -p, codex exec,
  cursor-agent) and xAI are not probed, so `/deterministic` never picks them.
- The pin is only actionable on OpenRouter (provider routing). A green NIM
  tuple is chosen but not pinned beyond the model id.
- Reproducibility of the serving layer is not reproducibility of the agent: a
  10-call trajectory at mode share 0.9 repeats far less often than 90%.
