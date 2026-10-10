# Tool-free review runner

Status: implementation contract, 8 October 2026. This is a private-draft tool,
not a publication service or a security certification.

## Need and outcome

Operators need to review untrusted source without giving that source a coding
agent's tools. A local machine, a teammate's machine, or a cloud computer can
run the same command. Success means bounded capture, a tool-free model request,
and an inspectable record of each attempted provider call.

## Contract

`captain review capture --archive source.tar.gz --commit <40-hex> --out evidence.json`
reads a previously fetched archive. It does not fetch URLs, extract files, run
source, load target instructions, or follow links. The operator supplies the
commit; this command cannot authenticate the archive's Git origin. It records
the archive SHA-256. Fetch provenance and isolation remain deployment controls.

`--include README.md,src/` selects exact relative files or directory prefixes
below the single archive root. The evidence records this scope and each
excluded file. Without this option all files are in scope. The 1,000-file and
per-file limits apply to the selected scope. Archive-wide limits always apply.
A colon inside a path component is ordinary data; a drive prefix is rejected.

Capture limits are 32 MiB compressed, 64 MiB decompressed, 10,000 archive
entries, 1,000 text files, 1 MiB per regular file, and 20 MiB text in total.
Reject unsafe paths, duplicate paths, malformed archives, and exceeded limits.
Skip links and special files. Record invalid UTF-8 and NUL-containing files as
binary without their contents. Never return a partial successful capture.

`captain review run --profile profile.json --prompt prompt.txt --tier frontier
--legs primary,backup --out run.json` selects the first eligible profile leg.
`--leg` restricts it further; `--exclude-vendor` supports an independent check.
Profiles are trusted operator input and never part of captured source. They
declare a model, vendor, tier, endpoint, authentication environment variable,
and terms and retention references for each leg. IDs are unique. Supported
tiers are frontier, quality, cheap. The profile is separate from normal routing.

The only initial transport is `chat-completions`: a direct, non-streaming HTTP
request with no tools, no manager, no CLI subprocess, and no instruction-file
discovery. HTTPS is required except for a literal loopback IP. Redirects and
environment proxies are disabled. A loopback server must itself be a tool-free
model service. Its behavior is a deployment assumption, not a property of HTTP.
Remote endpoints require a key environment variable; keys never enter records.

Input is a regular UTF-8 file, at most 1 MiB. No positional prompt is accepted.
Outputs are new files with mode 0600; existing files are not overwritten.
The process never changes its directory. It does not create an agent session,
so the current directory has no effect on the model's instructions.

At most four distinct eligible legs run, once each. HTTP 429 and 503
permit fallback within the same tier and allow-list. Missing credentials skip
that leg and are recorded. All other failures stop, including HTTP 502 and 504: a timeout or transport
failure may have consumed money. A malformed response, refusal, truncation,
tool call, or empty answer does not trigger a new grading attempt. This avoids
turning fallback into review shopping. A frontier task never falls to cheap.

Each request has a 120-second deadline, a 2 MiB response limit, and an 8,192
output-token limit. The run records hashes of prompt and profile, requested and
returned models, leg, vendor, endpoint host, transport, terms, retention, UTC
time, duration, outcome, provider token counts, cost when reported, and answer.
Unknown cost is null, never zero. This is not a money reservation system: use a
provider-side spend cap before live use. Treat answers and records as private,
untrusted data. Do not render raw answers as HTML or execute their contents.

## Threats and verification

| Boundary | Control | Acceptance test |
| --- | --- | --- |
| Hostile archive to memory | No extraction; bounded decompression; path validation | Traversal, links, duplicates, binary, large entry, gzip bomb |
| Evidence to model | HTTP-only; no tool definitions or source instructions | Inspect actual HTTP payload; reject returned tool calls |
| Provider failure to fallback | Same allow-list, vendor constraint and tier; finite attempts | Capacity failure then success; all blocked; forced leg outside list |
| Provider to records | Size bound; no raw error body; credential-free records | Malformed, oversized, timeout, and secret-bearing error body |
| Host filesystem | Regular bounded inputs; exclusive private output | Symlink/FIFO rejection and output collision |

The Lean model proves selection membership, preserved eligibility, and the
attempt bound. It assumes correct profile validation and trusted provider
adapters. Go tests cover the wire contract and archive parsing. Neither proves
grading accuracy, provider conduct, OS isolation, or legal permission.

## Deployment and expansion

Local execution remains supported. A host location does not select a billing
method or establish subscription permission. Provider-native agents may launch
the CLI when their environment permits it. They must not read raw hostile
evidence with their own tools or inherit unrelated credentials. Cloud adapter
certification and teammate result authentication are separate future work.

Teammates can return run records tied to the same evidence and rubric hashes.
Until runner identity and record authenticity are verified, those results are
untrusted contributions. Do not pool credentials or count multiple runs from
one person or vendor as independent reviewers.

Release requires integration tests against the chosen endpoint, capture
isolation evidence, and review of provider terms. Fixture success alone does
not qualify a live leg or complete a product review.

## Profile example and commands

The [example profile](review-profile.example.json) uses API-key models through
OpenRouter with ZDR routing. Model presence and prices were checked on
8 October 2026; this is not a quality qualification or a price guarantee.
Set `OPENROUTER_API_KEY` through your environment or secret manager. The review
command does not load the normal registry, ledger, or environment file.

```sh
captain review capture --archive source.tar.gz --commit FULL_COMMIT_SHA --out evidence.json
captain review run --profile profile.json --prompt deep-dive.txt --tier frontier --legs review-opus,review-astra --out deep-dive.json
captain review run --profile profile.json --prompt check.txt --tier quality --legs review-gemini,review-deepseek --exclude-vendor anthropic --out check.json
```

For a local model server, make a profile leg with its literal loopback endpoint,
its actual model ID, and the correct tier. No `auth_env` is needed for a server
that intentionally uses no authentication. Do not point it at an agent server
that can execute tools. Run records from teammates are not authenticated by a
hash alone; the receiving operator must verify origin and scope separately.
