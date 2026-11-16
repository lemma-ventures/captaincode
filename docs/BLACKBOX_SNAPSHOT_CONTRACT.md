# Blackbox snapshot export contract (narrow sender contract)

Decision dq-261115-c32ef1 (16 November 2026) chose this path: write down the
exact export fields first, then build one small exporter around the pinned
upstream rule code and prove it with rejection tests. This document is the
field contract. The pieces live in
`pkg/captaincode/blackbox_upstream.go` (import from the upstream cache),
`pkg/captaincode/blackbox_export.go` (build and sign), `pkg/captaincode/blackbox_store.go`
(revocation and rollback state) and `pkg/captaincode/blackbox_snapshot.go`
(the fail-closed verifier), each with a matching `_test.go`.

## Scope

The sender is one small exporter built around the pinned upstream rule code
(upstream revision `7d697683e0d56608cfe3fee374d6a6866702058a`). The receiver
is Captain's offline evaluator (BB1). The contract covers only the selected
categories: `dependency`, `protected_path`, `secret_exposure` and `pattern`.
It does not port the upstream trust system. It pins one Ed25519 authority.

## Envelope fields (schema 2)

The export is one JSON object. Unknown fields, trailing data and inputs over
8 MiB are rejected.

| Field | Type | Rule |
|---|---|---|
| `schema` | int | Must be exactly `2`. |
| `id` | string | Non-empty snapshot identifier. |
| `source` | string | Must be exactly `agent-blackbox-export`. |
| `revision` | string | Non-empty upstream revision the rules came from. |
| `generated_at` | time | RFC 3339. Not more than 5 minutes in the future. |
| `expires_at` | time | RFC 3339. Must be in the future at verification time. |
| `engine.evaluator_min` | int | Minimum evaluator schema. Must be at most `1`. |
| `engine.upstream_version` | string | Upstream version, recorded not enforced. |
| `authority.kind` | string | Must be exactly `ed25519`. |
| `authority.key_id` | string | Must equal the pinned key id. |
| `authority.public_key` | string | Base64, 32 bytes, must equal the pinned key. |
| `authority.signature` | string | Base64, 64 bytes, over the canonical payload. |
| `authority.generation` | uint64 | Must exceed the last accepted generation. |
| `authority.trust_root` | string | Must equal the pinned trust root. |
| `authority.graph` | string | Must equal the pinned graph. |
| `authority.network` | string | Must equal the pinned network. |
| `manifest.sequence` | int | Positive, increases with each export. |
| `manifest.rule_counts` | map | Exact counts for `dependency`, `protected_path` and `pattern`. Unknown categories rejected. |
| `manifest.complete_categories` | list | Each entry appears once, is enabled and non-empty (except `secret_exposure`), and never appears in `manifest.omitted`. |
| `manifest.omitted` | list | Records what the sender left out: a whole upstream source (`ioc`) or one rule (`dependency:<rule id>`, `pattern:<rule id>`). Entries are non-empty and unique. |
| `revocations` | list | Rule ids. Every entry is non-empty and unique, and none may appear in `rules`. An empty list is valid when nothing is revoked. |
| `rules` | object | A schema 1 snapshot payload (the BB1 rule format), with `source` set to `agent-blackbox-export` or `synthetic`. |

## Import mapping

The exporter reads one upstream on-disk cache: the `ruleset.json` file that
upstream's own `plugins/blackbox/ruleset/disk_cache.py` writes with
`_serialize` at the pinned revision. The file is untrusted input. The reader
is bounded at 8 MiB, rejects unknown fields and trailing data, and takes only
these mappings:

| Upstream cache field | Export field | Rule |
|---|---|---|
| `context_graph_id` | `authority.graph` | Must be present, or the import fails. |
| `synced_at` | import metadata only | Recorded for status, not signed as a claim. |
| `dependency` rows | `rules.packages` | `public` rows only, with a non-empty `kind`, an allowed ecosystem, and an id, name, version, kind and severity that the schema 1 rules accept. |
| `injection[].pattern_src` | `rules.patterns` | `public` rows only, with an `injection:` id, and a pattern that Go's regexp compiles. Imported as kind `heuristic`, which stays advisory. |
| `curator_revoked` and `verified_scope.revoked_identifiers` | `revocations` | Merged, trimmed, sorted and de-duplicated. A row named by a revocation never enters `rules`. |
| `fileaccess`, `ioc`, `skill`, `escalation`, `community`, `graph_threats`, `kill_list` | `manifest.omitted` | Recorded by source name when the cache is non-empty. None of them has a schema 1 mapping today. |

## Local policy input (`--paths`)

Upstream has no path rules, so `protected_path` comes from one operator file
outside the cache: `captain blackbox export --paths <policy.json>`.

| Input | Export field | Rule |
|---|---|---|
| operator policy file | `rules.paths` | A JSON array of `BlackboxPathRule` objects (`id`, `path`, `kind`, `severity`). Read as one protected local file: no symlink, mode `0600`, one hard link, at most 256 KiB. The same path rule checks the receiver applies run on every entry: safe id, finding labels, no duplicate id, bounded glob that parses, no null byte. The bound of 256 rules and the export's `--max-rules` bound both apply. |
| the same rules | `rules.categories.protected_path`, `manifest.rule_counts.protected_path` | With at least one rule the export selects the category. With none the category stays disabled, which is scope, not an omission. |
| the same rules | `manifest.complete_categories` | The sender claims `protected_path` complete only while it recorded no omission with path material. Upstream's `fileaccess` source has no schema 1 mapping and stays in `manifest.omitted`, so while it is present the claim is withheld. When the cache carries no such source the claim is made. This is a sender obligation: the receiver only rejects a claim that its own omissions contradict. |

Rows outside the mapping are recorded as `dependency:<id>` or `pattern:<id>`
in `manifest.omitted`. A category is complete only when the sender records no
omission for it: this input marks `secret_exposure` complete (Captain carries
its own built-in patterns) and leaves `protected_path` disabled unless the
operator supplies `--paths`, because the operator's protected-path policy is
local configuration and is not exported by upstream. `manifest.omitted` and
`manifest.complete_categories` can therefore never agree on the same name, and
the receiver rejects that contradiction.

The test fixture `pkg/captaincode/testdata/blackbox/upstream_ruleset.json` was
produced by calling upstream's `_serialize` at the pinned revision, so the
importer is checked against the real file shape rather than a hand-written
copy.

## Canonical payload and signature

The signature covers the UTF-8 JSON encoding of the whole envelope with
`authority.signature` set to the empty string, produced by Go's
`encoding/json` for that struct. The receiver rebuilds these bytes and
verifies the Ed25519 signature against the pinned public key. A digest alone
does not authenticate the sender; only this signature does.

## Receiver rejection rules

The receiver rejects, with an explicit error and no rule state change:

1. Wrong schema, wrong source, empty id or revision.
2. A future `generated_at`, an expired `expires_at`, or an unsupported
   `engine.evaluator_min`.
3. A wrong authority kind, key id, public key, trust root, graph or network.
4. A signature that does not verify, or one from a key that is not pinned.
5. A generation at or below the last accepted generation (replay).
6. A missing, wrong or unknown category rule count.
7. A category marked complete but disabled or empty.
8. A revoked rule still present, an empty or duplicate revocation entry.
9. Unknown fields, trailing data, or a payload over 8 MiB.
10. Inner rules that fail the schema 1 rule checks: an unknown
    `rules.source`, a missing coverage category, an invalid rule id, package
    or pattern, an unsupported pattern, or a rule count above the limit.
11. An omission whose category, or category prefix before `:`, also appears in
    `manifest.complete_categories`; an empty or duplicate omission record; or a
    duplicated complete category.

A failed verification never replaces the last accepted snapshot and never
resets its verification time. Audit imports without authority evidence stay
`unverified` and cannot become blockable.

## Acceptance output

On acceptance the receiver returns the inner schema 1 snapshot with a digest
of the canonical payload. That snapshot feeds the existing BB1 evaluator
unchanged. Every recorded decision pins this snapshot id and digest.

The accepted envelope is stored whole with its metadata: schema, generation,
sequence, snapshot id, digest, upstream revision, acceptance time, last
verification time, expiry, revocations and the signed envelope. The record
file is written with a temp file and a rename, mode `0600` in a `0700`
directory, and the record written before it is kept as `store.prev.json`. A
failed verification, a refused generation, an unwritable directory or a
truncated or unknown field in the record file leaves the stored state
unchanged. Rollback swaps the current record with `store.prev.json` and keeps
both verifiable. On open and rollback, the duplicated generation, sequence,
snapshot id, upstream revision, digest, expiry and revocations must match the
signed envelope. A mismatch fails closed.

## Sender obligations

The exporter builds one export from one upstream cache at one recorded
revision, carries the authority key of the pinned root, signs the canonical
payload and raises the generation and manifest sequence on every export. It
computes every rule count from the rules it actually carries, runs the
receiver's own checks on its output before handing it over, and refuses an
export it cannot defend. It must not add fields: the receiver rejects unknown
fields, so contract changes need a new schema number.

## Status

- Receiver verifier and rejection tests: done, 16 November 2026 (38 checks).
  Each case uses a fresh fixture and signs its mutated envelope, so the named
  validation rule causes the rejection. Omission entries reject a completeness
  claim for their category prefix.
- Importer around the pinned upstream cache: done, 16 November 2026 (9
  rejections; fixture generated by upstream's own serializer).
- Exporter and sender-side rejection tests: done, 16 November 2026 (18
  rejections; the exporter proves its output by running the receiver).
- Revocation and rollback state on disk: done, 16 November 2026 (atomic
  replacement, replay refusal across restart, expiry retention, rollback).
- Export command: `captain blackbox export` reads the cache and a protected
  raw Ed25519 key file, accepts the signed envelope into the store, and writes
  the envelope to a new `0600` file. Generation and sequence default to the
  next store generation. `--paths` adds the operator's local protected-path
  rules to the signed snapshot; the result reports `path_rules`. It performs no
  network call and enables no enforcement.
- Local path policy: 16 November 2026. `DecodeBlackboxPathPolicy` reads the
  operator file with the snapshot parser's bounds. Fourteen decode rejections
  cover an empty policy, unknown field, trailing data, oversize input, no
  array, unknown kind, blank severity, unsafe id, blank path, oversize path,
  null byte, invalid glob, duplicate id and a rule count above 256, plus one
  bounded-reader case. Three import cases carry the rules into `rules.paths`,
  select `protected_path`, count them in the manifest, and withhold the
  completeness claim while an upstream path-shaped source is omitted. One
  end-to-end signed envelope decides enforcement on a critical path match, on
  an ordinary path and on an unknown-tool input. Six export command rejections
  cover a group-readable file, a symlink, an empty policy, an unknown field,
  an invalid glob and a missing file.
- Status integration: `captain blackbox status` and
  `GET /v1/blackbox/status?cwd=<folder>` read the accepted store when the
  operator supplies its path and all five trust pins. They report the
  authenticated verification time and signed expiry, never file modification
  time. Missing or wrong pins, unreadable state and expired state stay explicit.
  The audit evaluator's separate file input remains labelled unverified.
- Key generation: `captain blackbox keygen --output <new-key-file>
  [--public <new-public-file>]` writes one new raw Ed25519 signing key as the
  exact 64 bytes the export reads, mode `0600`, never an existing path, and
  prints the base64 public key with a key id hint. The pinned key id, trust
  root and network stay operator choices at export time.
- Hook evaluation from the store: `captain blackbox hook` resolves rules from
  the authenticated store when the operator configured its trust pins and no
  explicit snapshot file was given. A configured store that cannot be opened
  degrades the screening instead of falling back to the unverified file; with
  no store configured the file path keeps the previous behavior. An empty or
  expired store degrades the same way.
- Disabled-category scope: a category the signed snapshot declares disabled
  (`not_covered`) is a scope decision, not a degradation. Such a category no
  longer forces the whole result to `incomplete`; only a category that is
  enabled but carries no rules, or an incomplete action input, does. Every
  upstream export leaves `protected_path` disabled because that policy is
  local, so this is what keeps an exported snapshot's screenings complete.
- First live snapshot: on 16 November 2026 the pilot store accepted
  generation 1 of `pilot-20261116-1`, imported from a fresh upstream cache
  produced at the pinned revision inside an isolated throwaway container (103
  injection patterns, 32 skill rules, 135 graph threats from the default
  verified graph; upstream keeps dependency and IOC rules live in its node, so
  the cache carries none). The hook screened one benign action against it and
  recorded `evaluated` with the snapshot id. No enforcement is enabled.
- Formal acceptance model: `formal/blackbox_snapshot_model.py` checks
  acceptance, replay, expiry, write failures, restart and rollback. It assumes
  one sequential writer. It does not prove cryptographic correctness, concurrent
  store access, filesystem crash atomicity or Go refinement.

Enforcement (BB5) remains a separate roadmap item.
