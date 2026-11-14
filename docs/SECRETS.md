# Secrets on the wire

Nothing that looks like a credential leaves the machine inside a prompt, and
the person at the keyboard is not named to the provider. On by default;
`CAPTAIN_REDACT=off` turns the whole layer off.

## Where it acts

| Layer | What | Who |
|---|---|---|
| **Tool boundary** | A tool's output is masked before it enters the context; a tool's arguments are restored on the way back; files that are only secrets are refused. | opencode workers (plugin hooks `tool.execute.before/after`), claude -p (PreToolUse hook `captain redact --hook`) |
| **Egress proxy** | `127.0.0.1:14098/<provider>/…` → the provider. Every request body is scanned again; responses come back with identity restored. The provider's own `Authorization` header passes through untouched. | opencode workers (provider `baseURL`s rewritten by the brain at start: xai, openrouter, nim/nvidia, opencode zen, huggingface), claude -p (`ANTHROPIC_BASE_URL`), codex exec (a custom provider `captain` on `/chatgpt/backend-api/codex` that reuses the ChatGPT login - `chatgpt_base_url` alone only moves the side channels, and the built-in `openai` provider refuses an override; `CAPTAIN_PROXY_CODEX=0` opts out) |
| **Gap** | cursor-agent takes `--endpoint`, but speaks connect/grpc-web+proto to it: a text scanner cannot rewrite a protobuf frame. It exposes no tool hooks either; its own sandbox is all that stands between it and its provider. | cursor |

Both layers run the same engine (`pkg/captaincode/redact.go`), so a value
masked at the boundary and the same value caught on the wire become the same
placeholder.

## What a secret becomes

`OPENAI_API_KEY=sk-proj-…` → `OPENAI_API_KEY=[[secret:openai:3f2a1c]]`

The placeholder is **stable** (same value → same hash), so the model can
reason about "the openai key" across turns and tell two different keys of the
same kind apart. The name, the file's shape, the fact that a key exists all
survive - "fix the env loading" still works; only the value is gone. The vault
(`~/.captaincode/vault.jsonl`, 0600) maps placeholders back to values for the
restore step.

Detected: vendor prefixes (`sk-ant-`, `sk-or-v1-`, `sk-`, `nvapi-`, `xai-`,
`hf_`, `ghp_`/`github_pat_`, `AKIA`, `AIza`, `xox?-`, Stripe), PEM private
keys, JWTs, `Authorization: Bearer …`, credentials inside URLs
(`postgres://app:pw@host`), and generic `*_KEY / *_SECRET / *_TOKEN /
PASSWORD / CREDENTIALS =|: value` assignments whose value looks like a secret
(8+ chars, letters and digits, not a `${REF}`, `<placeholder>` or `changeme`).

## Identity

The operator's home directory becomes `/Users/captain` (or `/home/captain`) -
an absolute path the model uses like any other; the username (4+ chars)
becomes `captain-user`; git's `user.name` / `user.email` become `Captain
Operator` / `captain@example.invalid`. `CAPTAIN_REDACT_IDENTITY=a,b` adds
strings. Stand-ins are deliberately distinctive: they are rewritten back in
tool arguments, so an ordinary word as a stand-in would corrupt commands that
happen to contain it.

Identity is restored in the provider's **answer** (streamed too, with a
holdback for a stand-in split across chunks) so paths in the transcript are
real; secret placeholders are restored **only at a tool boundary** - the
model can move a secret into a file or a command without ever seeing it, and
the transcript never shows it either.

## Secret files

Private keys and credential stores (`*.pem`, `*.key`, `id_rsa*`, `.netrc`,
`.npmrc`, `~/.aws/`, `~/.ssh/`, `credentials.json`, `service-account*.json`,
…) are refused at the tool boundary: there is nothing in them but the secret.
`.env` files are read **masked** by default; `CAPTAIN_REDACT=strict` refuses
them too.

## The separate secrets file

`~/.config/captain/secrets.env` (0600) holds credential lines
(`*KEY*`, `*TOKEN*`, `*SECRET*`, `*PASSWD*`, `*CREDENTIAL*` in the name) apart
from `~/.config/captain/env`. Both files load at startup; a name present in
both takes the secrets-file value, and a shell-set value still wins over both.
The loader opens the file once with no-follow and nonblocking flags. It checks
that descriptor before reading. The file must belong to the current user, have
one hard link, and have mode `0600` or `0400`. Directories, FIFOs, symlinks,
executable files, and files larger than 1 MiB are refused. A bounded read also
refuses a file that grows beyond 1 MiB after the initial size check. Refusal
prints a fixed warning without file contents. Other env settings still load.

The check applies to the final file component. The current user must control
the parent configuration directory. It does not isolate credentials from other
processes that run as that user. Worker environment isolation remains separate.

Regression cases are in `pkg/captaincode/envfile_safety_test.go`. They cover six
unsafe file types and the exact 1 MiB boundary. Existing env loader tests cover
file precedence and loose permissions. `BenchmarkLoadSecretsFile` measures
loading a small synthetic file without provider calls.

Validation status (14 November 2026): implementation and tests added, and the
three commands below pass on the development Mac (Apple M3 Max, arm64, cached
dependencies, no network and no provider call in the code path under test).
The tests finish in 0.36 s; `BenchmarkLoadSecretsFile` measures 14.4–19.4 µs
per call on the synthetic file (1,136 B, 9 allocations, five runs); `go vet
./...` reports no findings. The container-based isolated runner is still
unavailable locally (the daemon is stopped and macOS refused the memory
limits), so no isolated-runner result is claimed. Formal proof is not
applicable to this file-reader correction: it relies on OS descriptor
semantics. Negative tests and bounded reads provide the planned assurance.

Run in a qualified development sandbox with cached dependencies and no network:

```sh
go test ./pkg/captaincode -run 'TestSecretsFile|TestLoadCaptainEnv|TestIsSecretEnvName' -count=1 -timeout=30s
go test ./pkg/captaincode -run '^$' -bench '^BenchmarkLoadSecretsFile$' -benchmem -count=5
go vet ./...
```

- `captain secrets list` prints each file's variable names (never values) and
  its mode. Both inventory files use the same guarded reader as the secrets
  loader: current-user ownership, one link, mode `0600` or `0400`, a regular
  file, no final symlink, and a 1 MiB read limit. A missing file is reported
  as absent. An unsafe file stops inventory with an error.
  Names use ASCII letters, digits and underscores; the first character cannot
  be a digit. Invalid names are omitted. Names are sorted and deduplicated.
  Inventory reads assignments to obtain names. It does not print values,
  set environment variables, write files, or validate provider logins.
- `captain secrets template` prints five reviewed non-secret settings for a
  new account. It uses fixed values and does not read either env file. It
  does not copy the source machine's settings. See [INSTALL.md](INSTALL.md#settings-for-a-new-machine).
- `captain secrets split [--dry-run]` moves the secret-named lines from env
  into secrets.env and leaves everything else untouched. Terminal only: a
  worker, a sent turn or the jail cannot reorganize credentials.

Template assurance (14 November 2026): four regression tests cover fixed
output despite hostile source settings, unchanged source files and environment,
unsupported arguments, output failure, and startup before machine setup.
The secrets commands run before normal startup loads configuration or the
ledger. The split tests use isolated guard values and check all three refusal
flags. No production guard changes.

On the development Mac (Apple M3 Max, arm64), the in-memory template benchmark
measures 5.26–5.36 ns per call, zero bytes and zero allocations over three
200 ms samples. This measures only rendering into a reused buffer, not CLI
startup or installation. `go test ./cmd/captaincode -run '^TestSecrets'`,
`go vet ./...`, formatting and whitespace checks pass. These are local checks,
not a qualified sandbox or clean-machine installation result. Formal proof is
not applicable to fixed template text; exact output and negative tests check
the no-export contract. Two-machine inventory and login validation remain open.

### Inventory assurance

The inventory now reuses the loader's descriptor checks instead of opening
files with `Stat` followed by an unbounded `ReadFile`. A malformed name cannot
put control characters into the names list. The parent configuration directory
must still be trusted. This change does not harden `secrets split` or change
the legacy plain-env startup loader.

On 14 November 2026, the inventory output test failed against a snapshot with
the previous inventory reader: an escape character reached output. The fixed
snapshot passes the inventory, template, split and env-loader regression tests.
The seven inventory refusal cases cover a symlink, hard link, directory, FIFO,
loose permissions, executable file and oversized file. Output checks cover
value exclusion, invalid names, duplicate names, and unchanged files and process
environment. Missing files keep their absent status. `go vet ./...` passes.
Formatting and whitespace checks pass. The full Go test suite was not run in
this round.

These checks ran in an isolated Linux arm64 container with Go 1.27.3 and cached
dependencies. The process had no network, no host credentials, read-only source
and toolchain mounts, and one writable scratch filesystem. Limits were two
CPUs, 3 GiB memory with no additional swap, 128 processes, 2 GiB scratch space,
256 MiB per file, and a 900-second outer timeout. Docker Desktop started for
the checks; no provider or cloud host was used.

Three 200 ms samples of `BenchmarkSecretsInventory` used the same two-assignment
synthetic file on both snapshots:

| Reader | Time per call | Bytes per call | Allocations per call |
|---|---|---|---|
| Previous inventory | 2.75–2.90 µs | 1,288 | 11 |
| Guarded inventory | 2.20–2.28 µs | 1,040 | 9 |

This measures file inventory in that container. It does not measure CLI startup,
login health, or installation time. Formal proof is not applicable to this
reader and output correction; OS descriptor checks and negative tests supply
the assurance. The two-host acceptance tests remain open.

Commands inside the isolated environment:

```sh
go test ./pkg/captaincode ./cmd/captaincode -run 'TestSecrets|TestLoadCaptainEnv|TestIsSecretEnvName' -count=1 -timeout=60s
go test ./cmd/captaincode -run '^$' -bench '^BenchmarkSecretsInventory$' -benchmem -benchtime=200ms -count=3
go vet ./...
```

## What it costs a worker

- Nothing on structure: files keep their shape, paths stay absolute and
  usable, names of secrets stay visible.
- The value of the secret, which a worker has no business reading. A worker
  that must *use* a secret (`curl -H "Authorization: Bearer $KEY"`) still
  can: the shell expands `$KEY` from the real environment; only what the
  model *sees* is masked.
- One process spawn per tool call (`captain redact`, ~10 ms) and one extra
  local hop per provider request.
- False positives are possible on the generic assignment pattern (a
  `token_limit=Ab12Cd34` setting would be masked). The name stays, so the
  model still knows the setting exists.

## Modes and knobs

| Variable | Default | Effect |
|---|---|---|
| `CAPTAIN_REDACT` | `on` | `off` (nothing; provider blocks and the hook are rewritten back), `secrets` (secrets only, identity stays real), `strict` (`.env` files refused too) |
| `CAPTAIN_PROXY_ADDR` | `127.0.0.1:14098` | Where the egress proxy listens |
| `CAPTAIN_PROXY_CLAUDE` | `1` | `0` sends claude -p direct to Anthropic (proxy skipped) |
| `CAPTAIN_PROXY_CODEX` | `1` | `0` sends codex exec direct to chatgpt.com (proxy skipped; the ChatGPT login is untouched either way) |
| `CAPTAIN_REDACT_IDENTITY` | unset | Extra strings to rewrite, comma-separated |

`captain redact` (stdin → stdout), `captain redact --restore`, `captain redact
--check <path>`, `captain redact --stats`. The sidebar's **shield** line counts
what was masked; `GET /v1/proxy/stats` is the same number over HTTP.

## What this is not

A regex is not a classifier: a secret with no recognisable shape (a plain
passphrase in prose) passes. The proxy sees plaintext, so it must stay on
loopback (it does; it binds 127.0.0.1 only). And a worker's *own* transport
outside the proxy (cursor) is a real gap, named above rather than papered
over.
