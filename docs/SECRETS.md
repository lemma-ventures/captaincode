# Secrets on the wire

Nothing that looks like a credential leaves the machine inside a prompt, and
the person at the keyboard is not named to the provider. On by default;
`CAPTAIN_REDACT=off` turns the whole layer off.

## Where it acts

| Layer | What | Who |
|---|---|---|
| **Tool boundary** | A tool's output is masked before it enters the context; a tool's arguments are restored on the way back; files that are only secrets are refused. | opencode workers (plugin hooks `tool.execute.before/after`), claude -p (PreToolUse hook `captain redact --hook`) |
| **Egress proxy** | `127.0.0.1:14098/<provider>/…` → the provider. Every request body is scanned again; responses come back with identity restored. The provider's own `Authorization` header passes through untouched. | opencode workers (provider `baseURL`s rewritten by the brain at start: xai, openrouter, nim/nvidia, opencode zen), claude -p (`ANTHROPIC_BASE_URL`), codex exec (a custom provider `captain` on `/chatgpt/backend-api/codex` that reuses the ChatGPT login - `chatgpt_base_url` alone only moves the side channels, and the built-in `openai` provider refuses an override; `CAPTAIN_PROXY_CODEX=0` opts out) |
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
