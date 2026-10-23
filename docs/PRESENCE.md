# Signed prompts

Status: branch work, not released. The live brain is not changed by a build.

## Contract and threats

A user has a name and approved public credentials. OS account names are not
identity. A local dashboard and a hub are untrusted carriers. The brain checks
an exact prompt, machine, linked folder, validity window, nonce, origin, RP ID,
user presence, user verification, signature, and counter before it grants trust.

P1: A failed check changes no credential, counter, nonce, or inbox item.
P2: A nonce can authorize one prompt. Replay state survives a restart.
P3: Registration requires a five-minute terminal code or a trusted user's signature.
P4: A verified prompt resets the sent quota and gets typed-turn tools in its folder.
P5: High-tier actions need a separate signed decision. Prompt approval is not that decision.
P6: The client receives injection findings for the exact text before signing.
P7: Unsigned messages retain the existing sent-turn restrictions.

The finite model in `formal/presence_model.py` checks replay exclusion,
no state change for invalid input, and separation of prompt and decision authority.
It assumes correct cryptographic verification and durable serialized storage.
It does not prove cryptography, browser display, OS isolation, or shell semantics.

The attacker can edit, delay, duplicate, and route messages. The attacker cannot
use the user's passkey with user verification. A compromised browser origin,
compromised OS account, or an unjailed process that can alter the trust file is
outside this proof. Human review of findings remains a client responsibility.

## Local setup

Run these commands in a terminal on the target machine. A sent turn, a worker
process, a pipe, and HTTP cannot issue an enrollment code, import keys, remove
keys, link folders, or change settings.

```sh
captain presence link repo /path/to/repository
captain presence enroll operator --origin http://localhost:8781
captain presence list
captain presence remove operator CREDENTIAL_ID
captain presence window 900
```

`enroll` prints a random one-time code. It expires after 300 seconds. Enter the
code in a local dashboard. Registration binds the code, machine, user, origin,
RP ID, label, nonce, and expiry in the `webauthn.create` challenge. The code is
consumed only after validation. A failed registration leaves it usable until
expiry. The server stores its SHA-256, not its plaintext.

The default enrollment origin is `http://localhost:14097`. Set `--origin` to
the dashboard's actual origin. The brain does not serve a dashboard page.
Use `localhost`, including when an SSH tunnel carries the HTTP requests.

```sh
ssh -L 14097:127.0.0.1:14097 target-machine
```

A dashboard on `http://localhost:8781` can call `http://localhost:14097` through
that tunnel. The passkey operation occurs on the approving device. Configure
`http://localhost:8781` on the target brain. A credential's origin includes the
port; changing that port requires a credential for that origin.

A future hub uses an HTTPS origin. Add it in a terminal:

```sh
captain presence origin https://example.org
```

This implementation requires the RP ID to equal the origin's hostname. It
accepts HTTP only for `localhost` with an explicit port. It accepts HTTPS for
domain names. It refuses IP RP IDs, paths, queries, fragments, and user info.

## Public-key transfer

```sh
umask 077
captain presence export > public-credentials.json
captain presence import public-credentials.json
```

Export contains `{ "v": 1, "users": [...] }`. It contains credential public
keys and metadata. It excludes the machine ID, folder paths, origin allowlist,
nonces, tickets, grants, decisions, and audit records. Import merges users by
name and rejects duplicate credential IDs. Import never adopts another
machine's ID or adds an allowed origin. Configure the origin and folder links
on each receiving machine. The approving device must still possess the
private key. Copying this file cannot copy a passkey.

A user name is an ASCII label of 1 to 128 characters: a letter or digit first,
then letters, digits, `_`, `.`, or `-`. Folder labels use the same format. Device
labels can contain Unicode and have a 128-byte limit. A label is descriptive;
`attestation: "none"` does not prove a device manufacturer or hardware model.

## Store format

The store is `~/.config/captain/presence.json`, mode `0600`. It is denied to
sent-turn file tools and shell jails. No private key is stored. The adjacent
`.lock` file serializes terminal and brain changes. Writes use a temporary
file, file sync, atomic rename, and directory sync. A symlink or a store with
permissions other than `0600` is refused.

```json
{
  "v": 1,
  "machine": "random-base64url-id",
  "allowed_origins": ["http://localhost:8781"],
  "folders": {"repo": "/path/to/repository"},
  "window_seconds": 900,
  "users": [{
    "name": "operator",
    "credentials": [{
      "id": "base64url-credential-id",
      "public_key": "base64url-COSE-public-key",
      "algorithm": "ES256",
      "rp_id": "localhost",
      "origin": "http://localhost:8781",
      "sign_count": 0,
      "device_label": "laptop",
      "created_at": "2026-10-22T12:00:00Z"
    }]
  }]
}
```

Runtime fields are `used_nonces`, `screened`, `enrollment_tickets`, `grants`,
`decisions`, and `audit`. Expired replay and screening entries are removed on
successful writes. Rejections do not prune or change the trust state. The
nonce store survives restarts. The audit records accepted prompts and signed
decisions. Prompt records include user, credential ID, device label, machine,
folder, text hash, and Unix time. They contain no prompt text or signature.
Refusals log a reason code without credential material.

Limits: 1 MiB HTTP body, 512 KiB prompt text, 64 KiB CBOR, nesting depth 8,
256 CBOR nodes, 64 members per CBOR collection, 1,024 screening receipts,
32 pending enrollment codes, and 10,000 active prompt grants/nonces. The store
has an 8 MiB limit and fails closed when full; audit entries are not silently
removed. Counter updates, nonce use, and the prompt audit entry share one
transaction. Queue delivery remains in memory, as for the existing inbox.
After a crash between acceptance and delivery, prepare and sign a new nonce.

## Prompt envelope and verification

`GET /v1/presence/info` returns the machine ID, allowed origins, window in
seconds, linked folder labels, user names, credential IDs, RP IDs, origins,
and device labels. It never returns public keys, local paths, or enrollment
codes. Browser CORS is limited to configured origins on presence routes and
the signed inbox route. CLI and relay requests can omit the HTTP Origin header;
the signed `clientDataJSON.origin` is still checked.

The canonical JSON has exactly these fields, in this order, without spaces:

```json
{"v":1,"machine":"MACHINE_ID","folder":"repo","text_sha256":"LOWERCASE_SHA256_HEX","issued_at":1792670400,"expires_at":1792671300,"nonce":"BASE64URL_RANDOM_NONCE"}
```

Time values are integer Unix seconds. The nonce contains 16 to 64 random
bytes; clients should use 32. The challenge is the SHA-256 of these UTF-8 JSON
bytes. Strings in this envelope are ASCII. Duplicate keys, extra fields,
non-integer times, malformed base64url, and unsupported versions are refused.
There is no text normalization before hashing. All binary wire values use
unpadded base64url. The stored COSE key also uses unpadded base64url.

The validity window defaults to 900 seconds. A terminal can set 1 to 604800
seconds with `captain presence window`. `issued_at` must be no later than the
brain clock. `expires_at` must be later than that clock and `issued_at`.
The difference cannot exceed the configured window. There is no clock-skew
allowance. The machine and linked folder must match; paths from a carrier are
not used to route a signed prompt.

The verifier checks `webauthn.get`, the challenge, the credential's exact
origin and the brain's allowlist, the credential's RP ID hash, UP and UV,
the signature, and the counter. It rejects cross-origin iframe assertions.
ES256 uses P-256 and an ASN.1 DER ECDSA signature over SHA-256 of
`authenticatorData || SHA256(clientDataJSON)`. Ed25519 verifies that same
concatenation directly. Both-zero counters are accepted. If either the saved
or supplied counter is nonzero, the supplied value must be greater.

## Browser calls

These snippets define the wire format for a separate client. This repository
does not implement that client. `showFindingsAndWaitForApproval` and
`showNewCredentialAndWaitForApproval` are required client UI steps.

```js
const utf8 = new TextEncoder();
const b64 = bytes => btoa(String.fromCharCode(...new Uint8Array(bytes)))
  .replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
const unb64 = s => Uint8Array.from(atob(s.replaceAll("-", "+")
  .replaceAll("_", "/") + "=".repeat((4 - s.length % 4) % 4)), c => c.charCodeAt(0));
const sha = async bytes => new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
const hex = bytes => [...bytes].map(b => b.toString(16).padStart(2, "0")).join("");
const random = () => b64(crypto.getRandomValues(new Uint8Array(32)));
const canonical = value => JSON.stringify(value).replace(/[<>&\u2028\u2029]/g,
  c => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"));
const api = "http://localhost:14097";
const post = async (path, body) => {
  const response = await fetch(api + path, {
    method: "POST", headers: {"Content-Type": "application/json"},
    body: JSON.stringify(body)
  });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error);
  return result;
};
const assertion = credential => ({
  credential_id: b64(credential.rawId),
  authenticatorData: b64(credential.response.authenticatorData),
  clientDataJSON: b64(credential.response.clientDataJSON),
  signature: b64(credential.response.signature)
});
const get = async (challenge, keys, rpId) => assertion(await navigator.credentials.get({
  publicKey: {
    challenge, rpId, userVerification: "required", timeout: 60000,
    allowCredentials: keys.map(k => ({type: "public-key", id: unb64(k.id)}))
  }
}));
```

Prepare and sign a prompt:

```js
const info = await (await fetch(api + "/v1/presence/info")).json();
const user = info.users.find(u => u.name === selectedUser);
const keys = user.credentials.filter(k => k.origin === location.origin);
if (!keys.length) throw new Error("No credential for this origin");
const now = Math.floor(Date.now() / 1000);
const envelope = {
  v: 1, machine: info.machine, folder: selectedFolder,
  text_sha256: hex(await sha(utf8.encode(text))),
  issued_at: now, expires_at: now + info.window_seconds, nonce: random()
};
const challenge = await sha(utf8.encode(canonical(envelope)));
const screen = await post("/v1/presence/prepare", {envelope, text});
if (screen.challenge !== b64(challenge) || screen.text !== text)
  throw new Error("Screening response mismatch");
await showFindingsAndWaitForApproval(text, screen.findings, screen.screen);
const proof = await get(challenge, keys, keys[0].rp_id);
const signed = {envelope, text, ...proof};
await post("/v1/inbox", signed);
```

`prepare` runs the existing pattern screen and the configured model panel.
It returns findings even when the panel would hold an unsigned message.
When model screening is disabled, its response says so. The brain records the
screening receipt only after screening completes. The user can approve flagged
text after reviewing it. The signed text is not sanitized or rewritten.
A carrier cannot prove that a client actually displayed a warning; that is part
of the client's trusted UI.

To approve a held inbox item, use that item's exact stored `text`, its linked
folder label, and a new nonce. Add `held_id` to the signed request. Before
consuming the signature, the brain checks that the ID names a held item with
the same text and folder. The ID can only select an identical item; it cannot
change signed execution. Successful approval removes that held item.

A saved request uses the same verifier:

```sh
captain send --signed signed-prompt.json
```

`--cwd`, `--reply`, `--leg`, and extra text cannot accompany `--signed`.
Put route directives in the signed prompt text. A signed request cannot fall
back to unsigned delivery after a verification failure.

## Browser registration

Construct this registration request. For terminal enrollment, `code` is the
one-time code. For approval by an existing credential, `code` is empty.
`origin` is the new credential's browser origin. `rp_id` is its hostname.

```js
const r = {
  machine: info.machine, user: selectedUser, origin: location.origin,
  rp_id: location.hostname, device_label: deviceLabel,
  nonce: random(), expires_at: Math.floor(Date.now() / 1000) + 240,
  ...(code ? {code} : {}), credential_id: "", clientDataJSON: "",
  attestationObject: ""
};
const binding = {
  purpose: "captain.presence.create.v1", machine: r.machine, user: r.user,
  origin: r.origin, rp_id: r.rp_id, device_label: r.device_label,
  nonce: r.nonce, expires_at: r.expires_at, code: code || ""
};
const created = await navigator.credentials.create({publicKey: {
  challenge: await sha(utf8.encode(canonical(binding))),
  rp: {id: r.rp_id, name: "Captain"},
  user: {id: await sha(utf8.encode("captain.user.v1:" + r.user)),
    name: r.user, displayName: r.user},
  pubKeyCredParams: [{type: "public-key", alg: -7}, {type: "public-key", alg: -8}],
  authenticatorSelection: {residentKey: "preferred", userVerification: "required"},
  attestation: "none", timeout: 60000,
  excludeCredentials: (info.users.find(u => u.name === r.user)?.credentials || [])
    .filter(k => k.rp_id === r.rp_id)
    .map(k => ({type: "public-key", id: unb64(k.id)}))
}});
Object.assign(r, {credential_id: b64(created.rawId),
  clientDataJSON: b64(created.response.clientDataJSON),
  attestationObject: b64(created.response.attestationObject)});
```

Only `fmt: "none"` with an empty `attStmt` is accepted. The brain validates
attested credential data, credential ID, RP hash, UP, UV, and the COSE public
key. ES256 requires EC2/P-256; Ed25519 requires OKP/Ed25519. Unknown key shapes,
private key members, indefinite CBOR, duplicate CBOR keys, and trailing data
are rejected.

For terminal-code enrollment:

```js
await post("/v1/presence/register", r);
```

For approval by a credential already trusted for this user:

```js
await showNewCredentialAndWaitForApproval(r);
const approvalChallenge = await sha(utf8.encode(
  "captain.presence.enroll.v1\n" + canonical(r)));
r.approval = await get(approvalChallenge, trustedKeysForThisOrigin,
  trustedKeysForThisOrigin[0].rp_id);
await post("/v1/presence/register", r);
```

If approval happens on another device or origin, relay `r` there unchanged.
That device reconstructs the registration object in the field order shown
above, excludes `approval`, displays the new key and metadata, and signs it.
The approval must come from the same named user. A relay cannot issue a code,
import a key, or add a key using an unsigned message.

## Trust and separate decisions

The inbox hands the exact signed text to the existing TUI consumer. A TUI must
be open to consume it. The brain keeps the verified grant outside client
fields, checks the exact text again when the turn starts, resets the sent quota, and puts
its grant ID into the worker contract. It prevents automatic routing to a
second repository. The signed prompt has typed-turn tools in its project:
the linked folder, any folder inside it, and any linked worktree of the same
repository (captain's scratch worktrees included). A tool call elsewhere is
refused with `signed_folder_mismatch`; a shell command that starts with `cd`
back into the project is checked where it lands, so one `cd` elsewhere cannot
lock the turn. An unsigned prompt still receives the sent-turn contract and jail.

The signed-turn gate runs independently of `CAPTAIN_ACTION_GATE`. Known
default-branch pushes, merges, release/publish commands, deletes, payment
actions, and unknown service tools create a pending decision. Feature pushes
are allowed only in the unambiguous form `git push origin feature`, with a
known `refs/remotes/origin/HEAD` and a target other than that default branch,
`main`, or `master`. `git -C <dir>` is accepted when `<dir>` is in the same
repository as the turn's folder, and so is output handling at the end (`2>&1`,
`| tail -N`, `| head -N`). Ambiguous push syntax requires a decision. Git is
classified by its subcommand: `merge`, `reset`, `clean`, `rebase`,
`filter-repo`, `filter-branch`, and `tag` other than a listing.

A signed prompt that asks to push ("commit and push on gh"; not "do not
push") covers a plain push of its project, the default branch included: no
force, delete, tag, mirror or `--all` push, no `+` or `:` refspec, any
`git -C` inside the project, and nothing else in the command that needs a
decision. The grant records this as `push`.

`GET /v1/presence/decisions` returns pending decisions and their challenges.
The decision binds its random ID, grant ID, exact action, and expiry to the
machine. Non-shell actions bind the complete tool argument object. Simple
shell invocations bind the parsed argument vector and working directory.
Its challenge is SHA-256 of canonical JSON in this field order:

```json
{"purpose":"captain.presence.decision.v1","machine":"MACHINE_ID","decision":{"id":"DECISION_ID","grant_id":"GRANT_ID","action":"EXACT_ACTION_JSON","expires_at":1792671300,"approved":false}}
```

Display the action, then call the same `get` helper with that challenge.
Send `{ "id": "DECISION_ID", "assertion": proof }` to
`POST /v1/presence/decision`. Only a credential of the original user can approve
it. A Claude Code worker's hook waits up to nine minutes for the approval
(checking every two seconds), so the same command goes on in the same turn;
Galaxy's review button shows how many decisions are waiting. Other workers
retry the exact action. Approval is consumed once. A fresh decision gets a
new random ID, so a zero-counter authenticator cannot replay the old approval.
A text reply such as `yes` has no effect.

`POST /v1/presence/decisions/approve` with `{ "ids": [...], "assertion": proof }`
approves up to 32 pending decisions with one passkey assertion. Its challenge is
SHA-256 of `{"purpose":"captain.presence.decisions.v1","machine":"MACHINE_ID","decisions":[...]}`
with the decisions sorted by ID (`POST /v1/presence/decisions/challenge` with
`{ "ids": [...] }` returns it); all must belong to the same user, and only
that user's credential can approve them. `POST /v1/presence/decisions/purge`
with `{ "ids": [...] }` removes pending decisions without approving them; it
needs no passkey because it can only refuse.

An exhausted recorded task cost cap creates a separate `increase_task_cap`
decision before another worker dispatch. It binds the task ID, previous cap,
new cap, settled cost, and reserved cost. The proposed cap is the old cap plus
the larger of that cap and recorded/reserved spend. Approval cannot override
an attempt or wall-time limit. Cost changes during approval require a fresh
decision. Charges within an already delegated call remain adapter-dependent.

The current execution boundary is the existing tool hooks and CLI command
shims. It is not an OS sandbox for a signed worker. Interpreted code, an
absolute executable path, a tool that bypasses hooks, or a compromised full
trust worker can bypass command classification. The existing budget controller
also cannot bound charges inside every delegated provider run. This branch
must not be described as complete containment of high-tier actions or a hard
spending guarantee across all adapters. Those adapter boundaries need further
work before such a guarantee can ship.

## Hub carriage

A future hub `prompt` message carries the entire signed request unchanged:

```json
{"type":"prompt","payload":{"envelope":{},"text":"...","credential_id":"...","authenticatorData":"...","clientDataJSON":"...","signature":"..."}}
```

The empty envelope above denotes the full v1 envelope defined earlier. The hub
must not change text, folder, timestamps, or nonce. It can queue, forward, and
return status and findings. It cannot grant trust. The receiving brain runs
the same verifier used by the local inbox and `captain send --signed`.
No transport authentication substitutes for the passkey assertion.

Prepare on the target brain before signing. If it is offline before preparation,
there is no screening receipt. If it is offline after preparation, the queued
assertion remains usable only until expiry. A longer window must be set on the
brain in advance. Do not re-sign or extend an envelope at the hub.

## Error codes

All signature and registration refusals return HTTP 403 with `{"error":"CODE"}`.
Oversized inbox requests return 413. Malformed unsigned JSON returns 400.
Unsupported methods return 405. Unknown presence routes return 404.

| Codes | Meaning |
|---|---|
| `not_configured`, `unsafe_store`, `invalid_store`, `storage_error`, `store_full` | Local store unavailable or invalid. |
| `invalid_json`, `invalid_encoding`, `invalid_envelope`, `invalid_nonce` | Invalid wire data or canonical envelope. |
| `wrong_machine`, `unknown_folder`, `text_mismatch` | Wrong destination or changed text. |
| `future_envelope`, `expired_envelope`, `window_exceeded`, `replayed_nonce` | Time or replay check failed. |
| `screen_required`, `capacity` | Screening missing/expired or a bounded store is full. |
| `unknown_credential`, `wrong_user`, `invalid_credential`, `invalid_key`, `duplicate_credential` | Credential check failed. |
| `invalid_client_data`, `wrong_type`, `cross_origin`, `origin_not_allowed`, `credential_origin_mismatch`, `challenge_mismatch` | Client data check failed. |
| `invalid_authenticator_data`, `wrong_rp_id`, `user_presence_required`, `user_verification_required` | Authenticator data check failed. |
| `counter_not_increased`, `bad_signature` | Counter or cryptographic check failed. |
| `terminal_required`, `invalid_user`, `invalid_import` | Local management request refused. |
| `expired_registration`, `invalid_enrollment_code`, `enrollment_proof_required`, `invalid_attestation`, `unsupported_attestation` | Registration proof failed. |
| `held_text_mismatch`, `expired_grant`, `unknown_decision` | Held item, execution grant, or decision no longer valid. |

Tool refusals use `signed_decision_required: ID` or `signed_folder_mismatch`.
They do not execute the action. Store failures refuse instead of falling back
to full trust. Signed grants expire at the envelope expiry; long tasks need a
fresh approved prompt. A later `/repeat` round does not inherit signed trust.
It keeps sent-turn restrictions until submitted as a new signed prompt.

## Evidence and open decisions

The implementation uses only Go standard-library dependencies. Tests generate
P-256 and Ed25519 keys at run time. No private-key fixtures are checked in.
`formal/presence_model.py` checks all 768 transitions in a finite abstraction
with four nonce classes and six quota states. This is not a cryptographic proof
or an unbounded proof of the Go implementation. The local Lean toolchain was
unavailable; no Lean proof is claimed.

| Requirement | Evidence | Status |
|---|---|---|
| P1–P3 | `internal/presence/*_test.go` | Tree only; automated checks required. |
| P4, P6, P7 | `cmd/captaincode/brain_presence_test.go` | Tree only; generated-key integration. |
| P5 | Decision and action-gate tests | Tree only; hook/shim boundary, not complete containment. |
| Browser device approval | Exact calls above | Not tested with a physical passkey. |
| Hub transport | Payload contract above | Specified; hub implementation is separate. |

Open decisions for the operator: the maximum window for an offline hub queue,
whether accepted tasks should retain trust past envelope expiry, and whether
later `/repeat` rounds should retain it. Current defaults are 900 seconds,
expiry-bounded execution grants, and no inherited trust for later rounds.

Formalized: finite model checked. Specified: this contract. Implemented: branch
work. Tested: see the command evidence reported with the change. Released:
not released; no push, binary replacement, or live-brain restart.
