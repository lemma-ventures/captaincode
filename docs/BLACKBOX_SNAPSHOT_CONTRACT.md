# Blackbox snapshot export contract (narrow sender contract)

Decision dq-261115-c32ef1 (16 November 2026) chose this path: write down the
exact export fields first, then build one small exporter around the pinned
upstream rule code and prove it with rejection tests. This document is the
field contract. The fail-closed verifier lives in
`pkg/captaincode/blackbox_snapshot.go` and its rejection tests in
`pkg/captaincode/blackbox_snapshot_test.go`.

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
| `manifest.complete_categories` | list | Each listed category must be enabled and non-empty, except `secret_exposure`. |
| `manifest.omitted` | list | Records what the sender left out. A required category may not be omitted. |
| `revocations` | list | Rule ids. Non-empty, unique, and none may appear in `rules`. |
| `rules` | object | A schema 1 snapshot payload (the BB1 rule format). |

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

A failed verification never replaces the last accepted snapshot and never
resets its verification time. Audit imports without authority evidence stay
`unverified` and cannot become blockable.

## Acceptance output

On acceptance the receiver returns the inner schema 1 snapshot with a digest
of the canonical payload. That snapshot feeds the existing BB1 evaluator
unchanged. Every recorded decision pins this snapshot id and digest.

## Sender obligations

The exporter reads the pinned upstream rule code at one recorded revision,
emits the complete selected categories, carries the authority key of the
pinned root, signs the canonical payload and increments the generation and
manifest sequence on every export. It must not add fields: the receiver
rejects unknown fields, so contract changes need a new schema number.

## Status

- Receiver verifier and rejection tests: done, 16 November 2026 (23 cases).
- Exporter around the pinned upstream code: next step.
- Revocation and rollback state on disk: next step with the exporter.
