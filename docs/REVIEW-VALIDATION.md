# Review runner validation

Initial validation: 8 October 2026. Base revision: `9601359`.
The results below describe the pre-release working tree on that date.
They are implementation evidence for the bounded draft runner.

## Checks

| Check | Result |
| --- | --- |
| `cd formal && lake build` | Pass; five review-policy theorems, standard `propext` axiom only |
| `go test -race ./pkg/captaincode ./cmd/captaincode -run '^TestReview' -count=1` | Pass; 15 tests plus table cases |
| `go vet ./...` | Pass |
| `gofmt` on changed Go files | Pass |
| `git diff --check` | Pass |
| `captain leakcheck` on changed public code and documentation | Pass |
| `go test ./...` | Fails in five pre-existing tests; each reproduced in a detached worktree at the base revision |

Baseline failures are `TestInboxRefusesPromptsThatStartALoop`,
`TestP1aDerivedConnectionCarriesScopeAndWrites`,
`TestP1aDerivedConnectionRefusesFlagsInArgs`,
`TestP1aMemoryConnectionRejectsDifferentBrainAndWritableForRead`, and
`TestP1aMemoryEventIsErrorLoggedAndNotQueued`. The new runner does not modify
their source or expectations. The inbox test expects 400 where the base returns
403. The four connection tests fail their scope, flag, or rejected-event checks.

## Red and green evidence

The first unit and CLI test runs failed because the interfaces did not exist.
Later behavior tests failed on a missing model identity, ambiguous gateway
retries, and standard archive root/PAX metadata. The fixes made them pass.
Receipts are in the ignored `.validation/review-*.txt` files. These include
`review-boundary-red.txt`, `review-gateway-red.txt`, `review-root-red.txt`,
`review-pax-red.txt`, `review-race.txt`, and the two baseline receipts.

Wire tests inspect the sent HTTP body, not only adapter flags. They verify no
tool definitions, output limits, capacity fallback, refusal/truncation handling,
redirect refusal, private exclusive output, credential redaction, and a four
attempt limit. Archive tests cover traversal, links, duplicate and case-colliding
paths, binary and invalid UTF-8 data, large files, decompression limits, file
count, explicit scope, and metadata.

## Rehearsal limits

Two operator-owned public source snapshots exercised capture. Parsing ran in a
read-only container with no network, no credentials, dropped capabilities,
256 MiB memory, one CPU, 32 processes, and explicit file and wall-clock limits.
No reviewed source was executed. One archive exceeded the full-scope limits;
its explicitly narrowed source scope passed with every omission recorded.

Live model calls used paid API authentication and requested ZDR routing. They
produced private drafts and simulated checks, not human validation. Provider
cost fields are reported charges, not invoice reconciliation. No cloud-hosted
agent adapter, external-runner authenticity check, or production budget ledger
was implemented. Native agent subscription adapters remain separate work.

No dependencies were added. No installed binary or running brain was changed.
