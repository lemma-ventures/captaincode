# Small calls and efficiency evidence

Captain can use an operator-selected local model for short calls. This is opt-in.
The local helper has no tools and cannot run coding tasks, join a workflow, enter
the worker ladder, or direct a team. Normal coding routes stay separate.

## Configure and qualify a local model

Use a trusted local Ollama runtime with an already installed model. Captain does
not install a runtime, download weights, or choose a model from its size alone.
Set the exact model name that the runtime reports in `/api/tags`:

```sh
export CAPTAIN_LOCAL_URL=http://127.0.0.1:11434
export CAPTAIN_LOCAL_MODEL='your-installed-model:tag'
captain local qualify
captain local status
```

`qualify` runs three complete rounds of nine checks. All 27 must pass. Checks cover
simple and security classification, missing-context abstention, titles, commit
subjects, factual summaries, missing-fact abstention, empty learning input, and a
memory update that preserves an attempt limit. These are small-call smoke tests;
they do not qualify a model for general coding or prove memory quality on a corpus.

A pass binds the endpoint, model, runtime-reported model digest, and suite version.
It expires after 30 days. Each call checks the digest again. Requalification
revokes the old pass first, including when the runtime fails or the run stops.
The record lives in `~/.captaincode/local-qualified.json` with mode `0600`.
`status` prints this saved evidence; the next call still checks current validity.

Enable automatic uses in the environment of the brain:

```sh
export CAPTAIN_LOCAL_TASKS=classify,title,learn
```

- `classify`: use the local helper at triage tier 1, with an eight-second deadline.
  Strong heuristic risk signals cannot be downgraded. Failure or abstention returns
  to heuristic handling, without a cloud classification call. The coding task can
  still route to a cloud worker. This flag does not make coding private.
- `title`: generate session titles locally. A failure is reported, without a cloud
  title fallback.
- `learn`: use the helper for Euclid learning, distillation, and the related memory
  JSON calls. Existing file, parse, cursor, and apply controls still govern edits.
  Review a dry-run learn pass before enabling automatic memory writes.

Standalone helpers read their input from stdin and require a qualification pass:

```sh
printf '%s\n' 'Fix retry timeout handling' | captain local title
git diff --stat | captain local commit
printf '%s\n' '4 tests passed; 1 retry test failed.' | captain local digest
```

Commit subjects and digests are explicit commands. Captain does not create commits
or replace dashboard prose automatically. Each completion has a 30-second limit,
a 64-KiB input limit, a 64-KiB output limit, and a 2,048-token output request cap.
Oversized input and incomplete or truncated streams fail. There is no silent
truncation and no remote fallback for a selected helper call.

Only literal loopback HTTP origins are accepted. DNS names, URL credentials,
paths, proxies, and redirects are refused. Remote-backed models are refused using
runtime metadata and the model name. The operator must trust the local runtime:
Captain cannot prove that another process on loopback never forwards its input.

## Token and timing evidence

Worker events in `~/.captaincode/routing.jsonl` can now carry `token_usage`:

| Field | Meaning |
|---|---|
| `input` | Fresh input, excluding reported cache reads and writes |
| `output` | Reported output tokens |
| `cache_read` | Input served from a cache |
| `cache_write` | Input written to a cache |
| `source` | Transport that supplied the counts |
| `scope` | `run` or `final_message` |

Claude reports additive buckets for the run. Codex includes cache reads inside
input; Captain subtracts those reads before storing fresh input. OpenCode reports
the final message, which is a narrower scope than a whole agent run. The legacy
`tokens` total stays for compatibility. Missing fields remain absent, including
historical events. Unknown cache writes do not become measured zero.

`ttft_ms` measures time to the first observed text delta. It includes process,
transport, and generation delay; it is not a pure prefill measurement. Codex emits
completed messages on this path, so it supplies `first_output_ms`, not TTFT.
Failed calls keep detail when the transport supplied it; unavailable usage stays
unknown. A provider reroute can still leave an earlier failed attempt without
usage detail. Report coverage with totals instead of treating gaps as zero.

The TUI usage response uses measured buckets when sufficient fields exist.
Otherwise it labels its existing split `estimated_3_to_1`. The local performance
dashboard separates sources and scopes, and shows coverage for token counts,
cache-read share, and first-text timing. Historical totals cannot be split later.

## Prompt and tool overhead

`windowPrompt` now uses a fixed truncation marker and includes that marker in its
byte budget. Its head can remain stable as history grows. The moving tail can
still break prefix reuse; this change does not establish a server cache hit.

For solo chat dispatches, Captain compares the rendered prompt with the previous
prompt for the same workspace, leg, and conversation opening. This is an
approximation of conversation identity. It compares 256-byte hashed blocks over
at most the first MiB, retaining at most 128 entries for 24 hours in process memory.
The log stores byte counts only. It stores no prompt text, paths, keys, or hashes.
Restarts start a new baseline. Replayed answers do not produce dispatch samples.

`~/.captaincode/efficiency.jsonl` stores these counts and local helper usage. It
rotates at 16 MiB and uses mode `0600`. Set `CAPTAIN_EFFICIENCY_LOG=0` to disable it.

Set `CAPTAIN_EUCLID_TOOL_PROFILE=lean` to expose only `search` and `code_search` in
Captain's Euclid MCP server. The default `full` profile retains all tools. Calls
to omitted tools are also refused. The setting is forwarded to CLI worker MCP
configuration. Claude and Codex worker events record the offered Euclid tool count,
JSON bytes, and an estimate of tokens (rounded-up bytes / 4). This excludes other
worker tools and is not a tokenizer measurement or a bill for every model turn.

## Abstention and routing comparisons

`examples/evals/abstention.json` contains missing-context and missing-fact cases.
Local qualification exercises both. The Euclid recall benchmark also scores
abstention separately from retrieval recall. Without supplied answers, its result
is **unmeasured**, never a pass. In the updated Euclid engine:

```sh
python3 .euclid/bin/benchmark-recall.py --abstention-answers=answers.json
```

The answers file maps probe IDs to objects such as
`{"missing_attachment":{"abstain":true,"answer":"CANNOT_TELL","citations":[]}}`.
An invented answer or citation fails even when `abstain` is true. New benchmark
helpers are copied with the other Euclid scripts when indexes rebuild.

Director prompts now state `cheap-capable-v1`: among equally capable and safe
choices, prefer lower estimated cost, then lower subscription pressure, then lower
latency. Doubt about capability or safety is not a tie. Explicit user choices win.
Decision records mark the policy only when that director path actually answered
without an explicit preference. The dashboard compares seven- and thirty-day
cohorts and excludes explicit preferences, forced models, and lanes.

This supplies future evidence. It does not prove savings, faster reviews, or a
lower frontier share. Compare acceptance and repair outcomes with cost before
changing the routing defaults further.
