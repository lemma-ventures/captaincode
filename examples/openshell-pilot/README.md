# OpenShell pilot

Experimental acceptance harness for one API-backed OpenCode worker, one Git
snapshot, one OpenShell sandbox, and one edit, test, export and landing cycle.
The fixture pilot operates on the small `fixture/` repository, not your checkout.
[Task mode](#task-mode-and-captain-openshell) applies the same boundary to a
task spec, and `captain openshell` runs a team of such sandboxes and returns one
verified patch. Neither changes Captain's routing, scope declarations,
configuration or running brain.

## Current result

**Six bounded MicroVM runs passed with a locally patched OpenShell driver;
the latest passes all 18 checks, including request-scoped tool-secret restoration.**

Team runs on the experimental OpenRouter lanes are reported separately under
[Team results](#team-results): 127 of 134 sandboxed tasks passed in seven runs,
and all seven integrated patches were verified in a fresh sandbox. The last five
runs record the clean build they ran from.

The [tool-secret result](results/2026-09-30-tool-secrets.json) on 30 September
2026 completed **1 task from 1 worker attempt**, with zero tool errors. Shield
masked the synthetic credential on its way to NIM, restored it only in the
paired tool argument, and masked the restored argument in subsequent requests.
The tool produced the expected SHA-256. The final answer contained the restored
operator identity and no raw credential; the middleware audit contained no raw
credential either.

| Latest measurement | Seconds |
| --- | ---: |
| Gateway readiness | 2.648 |
| Sandbox creation, prepared image | 13.416 |
| Worker edit and test | 457.235 |
| Recovery gateway readiness | 0.727 |
| Sandbox restart | 2.256 |

Seven complete model responses passed through Shield. One additional response
was a non-JSON HTTP 504 and was refused; OpenCode retried within the same worker
attempt. The report keeps this provider error alongside the successful task.
File and network denials, cancellation, refusal when Shield is down, restart
recovery and exact diff landing all passed. The completed sandbox was deleted
and the pilot services exited. Image, driver, adapter and harness hashes are
recorded. This remains a fixture smoke test, not a reliability estimate or a
rollout to normal routing.

The preceding [response restoration result](results/2026-09-30-response.json)
passed 17 checks: one task completed, zero tool errors, sandbox creation in
13.547 seconds, worker execution in 257.311 seconds and restart in 2.254 seconds.
It restored identities while keeping all secret placeholders masked. Its report
also preserves two earlier unsuccessful development attempts: native SSE
whole-body inspection was refused by OpenShell, with no changes landed.
These different task runs do not establish the overhead of secret restoration.

The preceding [worker search result](results/2026-09-30-search.json) passed
16 checks with one completed task and zero tool errors. Its sandbox creation
took 13.678 seconds and its worker took 145.684 seconds. OpenCode's glob and
content search work under the policy with bundled `ripgrep`; the old lazy
download was correctly denied. These isolated samples do not establish either
a task-success rate or the response adapter's latency impact.

The earlier [controller ownership run](results/2026-09-30-controller.json) on 30 September
2026 refused competing execution and preparation processes without changing the
active state. Gateway readiness took 0.754 seconds and sandbox creation took
13.200 seconds. The worker did not exit within 600 seconds: **0 completed tasks
from 1 worker attempt**. Its tool output reported passing fixture tests, but
the controller's independent verification and export gates did not run. The
deadline stopped the sandbox and no patch landed. The retained stopped sandbox
holds the partial work; this is not a successful acceptance cycle.

The updated controller also resumed a previously completed landing in a fresh
process without an API key or another model request. Controller ownership and
invalid-entrypoint regressions pass on macOS and Linux.

On 30 September 2026, one NIM GLM 5.3 Flash worker edited the fixture, passed
its four tests, and exported a diff that landed byte-for-byte after a fresh
controller process and VM restart. All 15 acceptance checks passed, including
file/network denials, outbound Shield masking, refusal when Shield is down,
and cancellation by stopping the sandbox.

The [landing recovery result](results/2026-09-30-landing.json) records:

| Measurement | Seconds |
| --- | ---: |
| Gateway readiness | 0.769 |
| Sandbox creation, prepared image | 13.393 |
| Worker edit and test | 242.396 |
| Recovery gateway readiness | 0.735 |
| Sandbox restart | 2.207 |

This run, the [previous repeat](results/2026-09-30-vm.json), and the
[initial passing run](results/2026-09-30-vm-initial.json) completed
**1 task from 1 worker attempt** each. The latest completed landing was also
resumed twice in fresh processes without an API key or further model requests.
Earlier development attempts failed at runtime setup, environment configuration,
and acceptance checks. These smoke tests are not a task-success-rate estimate.
There is no automatic routing integration or expansion to other leg families.

The original Docker attempt remains in
[the 29 September result](results/2026-09-29.json): Docker Engine 28.0.4's
`6.10.14-linuxkit` kernel returns `ENOSYS` for Landlock, so no worker ran there.
The pilot keeps `hard_requirement` and verified TLS enabled.

The explicit `--runtime vm` option uses OpenShell's MicroVM kernel on Apple
Silicon without changing Docker Desktop. The VM provides Landlock ABI 6 and
passes the file and network denials. The pilot requires the journal-recovery
fix in [OpenShell PR #3940](https://github.com/NVIDIA/OpenShell/pull/3940) before
qualifying stop/start recovery. The PR is submitted, but upstream closed it
pending a maintainer vouch; the DCO acknowledgement also remains outstanding.
Do not treat the fix as released.

Supply the patched driver explicitly with the `--runtime vm` and `--vm-driver`
options to `prepare.py`. The gateway and CLI remain pinned to
0.1.2. The report records the actual VM driver version and SHA-256. A custom
driver is an explicit local build, not a replacement downloaded automatically.
The Docker backend still requires working Landlock ABI 3 or newer; an
unavailable kernel feature stops the pilot without reducing enforcement.

## Pinned provider comparison

The optional OpenRouter lanes select `openai/gpt-oss-120b` using
`OPENROUTER_API_KEY`: `--profile cerebras`, `sambanova`, `together`,
`deepinfra`, `crusoe` or `parasail`. The default remains `--profile nim`.
It uses the identical fixture, deadline, Shield and 18 acceptance checks.
The output cap is 16,384 tokens for this reasoning model, compared with 4,096
for the NIM profile; reports record that difference. Shield bounds the request
limit even when the worker asks for more.
Select the profile on the initial `pilot.py` invocation; resume recovers it from
the checkpoint and refuses a different profile.

Each lane pins one upstream provider. Shield replaces caller-supplied routing
with `only` and `order` set to the lane's provider (`cerebras`, `sambanova`,
`together`, `deepinfra/bf16`, `crusoe/bf16` or `parasail/fp4`),
`allow_fallbacks: false`, `data_collection: deny`, `zdr: true` and
`temperature: 0`. Alternate model-routing fields are refused. A successful
response must identify both the expected model and the lane's provider name,
or Shield blocks its delivery. These are enforced routing requirements and
provider-reported metadata, not independent proof of the provider's retention
or determinism.
The pilot does not claim ADI green status or reproducible agent trajectories.

Shield requests `Accept-Encoding: identity` so bounded response inspection can
run. A provider that still sends an unsupported compressed response is refused.

Only POSTs to `/api/v1/chat/completions` on `openrouter.ai` are permitted for
these lanes; NIM egress is absent. The provider secret remains in OpenShell's
credential store. The historical report key `nim_path_denied` now records the
selected provider's forbidden `/models` path, preserving existing consumers.
Reports bind the profile, model and generated policy hash. Each Shield request
audit records the enforced provider policy and exact masked request hash; paired
responses record the upstream body hash and confirmed provider. Payloads and
credentials stay out of these audit records.

All lanes share one OpenShell provider profile, `captain-openrouter-pilot`:
the same credential, host and path rule. OpenShell enforces the endpoint;
Shield decides which provider behind it may serve the request. A controller can
therefore spread concurrent sandboxes across providers instead of queueing them
behind one provider's rate limit. Each lane is a different serving path, with
its own hardware, quantization and stack, so reports and audits keep the lane
for every task. Results from different lanes are not pooled as one serving tuple.

These lanes are experimental. Their live qualification and scale measurements
must be reported separately from the earlier NIM results above.

### Bounded verification feedback

`--repair-attempts 1` permits one fresh worker invocation after independent
fixture tests return exit code 1. The default remains zero repairs. Captain
passes the bounded test output back as evidence, using the same sandbox, model,
Shield and policy. The second invocation gets only the time left in the original
600-second worker budget. Tests are hashed before every verification; changed
tests, worker errors, deadlines and isolation failures do not trigger repairs.
The task still needs all 18 gates, export recovery and exact landing to pass.

Each invocation records its own transcript and independent test output, hashes,
exit codes and verdict in `report.json.attempts`. `worker_attempts` includes the
repair; `worker.jsonl` retains both transcripts. Reports must distinguish tasks
that passed first try from tasks that needed repair. A later pass does not erase
an earlier failure. The repair budget is pinned in the checkpoint; resume only
recovers completed artifacts and never submits an interrupted model call again.

## Task mode and `captain openshell`

`task.py` applies the pilot's boundary to a task instead of the planted fixture
bug. A task names a repository revision, a prompt, a verify command, the files
the worker may change and the files it must leave unchanged. The pilot snapshots
the revision, runs one worker in one sandbox, and stops at a restart-recovered,
scope-checked patch with its evidence. It never writes the source repository.

| Field | Rule |
| --- | --- |
| `id` | 1-64 letters, digits, dots, dashes or underscores |
| `repo`, `revision` | The top of a Git worktree and a full commit id; `captain openshell` pins both |
| `prompt` | 1-16,384 characters; the pilot appends the scope rules and the verify command |
| `verify` | An argv list of 1-32 arguments, run in the sandbox |
| `allowed` | 1-64 existing regular files the worker may change |
| `protected` | Up to 256 existing regular files whose hashes must not change |
| `baseline` | What `verify` must do at the base revision: `fail` (default), `pass` or `any` |
| `deadline_seconds`, `verify_seconds` | Worker budget 60-3,600 (default 600); each verification 10-900 (default 120) |

The snapshot is the revision's `git archive`, up to 32 MiB. Its tree must
reproduce the revision's tree id, so submodules, export attributes and
case-colliding paths are refused. Edit mode runs 17 checks. The fixture's
runtime, denial, cancellation, Shield-down and restart gates are unchanged.
`worker_tools` requires OpenCode's search to list every named file, and
`protected_unchanged` rehashes protected files before each verification.
`shield_mediated` requires every model call to cross Shield on the pinned model
and, on an OpenRouter lane, the pinned provider. `diff_scope` accepts only text
edits to allowed files that apply to the base revision. Task mode plants no
synthetic secret, so the fixture's masking and restoration gates do not apply.
Verify mode starts no worker and makes no model call: it runs the first eight
checks and the verify command against a tree.

`captain openshell` runs a team of task-mode sandboxes against one pinned
commit and returns what holds up as one verified patch:

1. It validates the team spec on the host with `task.py`'s rules; unknown fields
   are errors. Each task adds a `profile` (its lane) and `repair_attempts`
   (0 or 1) to the fields above; `repo` and `revision` come from the command.
2. Each task gets a fresh private state with its own copies of the prepared
   OpenShell binaries and Shield. Up to `--concurrency` (1-8) pilots run at
   once, each with a minimal environment and a bounded time budget. On SIGINT
   or SIGTERM, Captain sends every pilot one SIGTERM and gives it three minutes
   to delete its sandbox.
3. Captain re-checks every export on the host instead of trusting the sandbox.
   The report must be this task's, with all 17 checks passed. The patch must
   match the report's SHA-256, contain only text edits to allowed files, and
   apply cleanly to the pinned revision. Nothing in the patch runs on the host.
4. Each surviving patch is applied in its own worktree of the pinned revision
   and recorded as a manifest: changed files, diff digest and the sandbox's
   verification. The manifests form one integration candidate, the same path a
   host worker's diff takes.
5. Tasks that changed the same files form a conflict group. With
   `--director none`, nothing in a group lands. With `--director claude`, a
   tool-less `claude -p` picks one winner per group. It runs in an empty
   directory with no tools, MCP servers or session persistence, and its reply
   can only name a contender.
6. The survivors are replayed into a fresh worktree to produce
   `integrated.patch`. Its tree is written through a temporary index, with no
   commit or ref. A verify-mode sandbox then runs every landed task's verify
   command against that tree, or the team's own `verify` when it names one.
7. `run.json`, the patches and each task's evidence go to
   `~/.captaincode/openshell/<run>/`. `run.json` also records what ran: the
   revisions Captain and Shield were built from, whether either checkout had
   uncommitted changes, and the SHA-256 of every pilot script and prepared
   binary. A passing task's state is deleted and a failed one's is kept.
   Captain never writes your working tree; apply `integrated.patch` yourself.

The example team works on `team/repo`: 11 small Python modules, each with a
planted bug and a unittest file that fails at the base revision. `team.json`
has 12 tasks: one per module on the Cerebras or SambaNova lane, and `semver` on
both, so its two patches collide. `both-lanes.json` runs every module on both
lanes: 22 tasks and up to 11 conflict groups. From the repository root, with
`$pilot_state` prepared as in [Run](#run) with `--runtime vm`, and
`OPENROUTER_API_KEY` in the environment:

```sh
demo=$(mktemp -d /tmp/cc-team.XXXXXX)
cp -R examples/openshell-pilot/team/repo/. "$demo"
git -C "$demo" init -q
git -C "$demo" add -A
git -C "$demo" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm fixture
captain openshell --team examples/openshell-pilot/team/team.json \
  --pilot "$PWD/examples/openshell-pilot" --prepared "$pilot_state" \
  --repo "$demo" --concurrency 6 --director claude
```

Per-task states go under `--state-root` (default `/tmp`), which must stay short
for the MicroVM socket path.

### Team results

On 30 September 2026, the example teams ran seven times on the OpenRouter lanes
for `openai/gpt-oss-120b`: `fixture-12` twice and `fixture-both-lanes` five
times, with one repair attempt allowed per task. They used the patched MicroVM
driver on one Apple M3 Max (16 cores, 128 GB). These lanes are experimental, and
these results are separate from the NIM results above.

The first two runs predate the provenance record in `run.json`. Their binaries'
build stamps, read afterwards, show Captain built from `186868f` and Shield from
`5bea2e2`, both from checkouts with uncommitted changes, so no commit names the
code they ran. The next two ran from clean builds of `22eccb6`. The last three
ran Captain from a clean build of `7925bff` and Shield from `22eccb6`; `7925bff`
changed only the file times in the worker's payload and was later reverted
(see below). Their `run.json` records all of that.

| | [fixture-12](results/2026-09-30-team.json) | [fixture-both-lanes](results/2026-09-30-both-lanes.json) | [fixture-12](results/2026-09-30-team-22eccb6.json) | [fixture-both-lanes](results/2026-09-30-both-lanes-22eccb6.json) |
| --- | ---: | ---: | ---: | ---: |
| Built from | not recorded | not recorded | `22eccb6`, clean | `22eccb6`, clean |
| Tasks; sandboxes at once | 12; 6 | 22; 8 | 12; 6 | 22; 8 |
| Passed in their sandbox | 11 | 21 | 12 | 21 |
| Landed / dropped by ruling / failed | 10 / 1 / 1 | 11 / 10 / 1 | 11 / 1 / 0 | 11 / 10 / 1 |
| Wall time | 186.8 s | 263.4 s | 143.1 s | 242.0 s |
| Sum of task times | 712.1 s | 1,227.9 s | 610.9 s | 1,143.5 s |
| Task, median (max) | 50.7 s (104.0) | 58.1 s (115.8) | 50.7 s (65.8) | 48.7 s (71.3) |
| Sandbox creation, median (max) | 22.6 s (24.6) | 24.9 s (30.7) | 22.1 s (24.6) | 23.2 s (29.4) |
| Worker, median (max) | 12.7 s (67.1) | 11.0 s (70.7) | 10.0 s (23.8) | 11.2 s (19.6) |
| Model requests through Shield | 91 | 168 | 91 | 165 |
| Integrated verification in a fresh sandbox | 10 files, 23.8 s | 11 files, 23.6 s | 11 files, 23.0 s | 11 files, 23.8 s |

| | [fixture-both-lanes](results/2026-09-30-both-lanes-7925bff-1.json) | [fixture-both-lanes](results/2026-09-30-both-lanes-7925bff-2.json) | [fixture-both-lanes](results/2026-09-30-both-lanes-7925bff-3.json) |
| --- | ---: | ---: | ---: |
| Built from | `7925bff`, clean | `7925bff`, clean | `7925bff`, clean |
| Tasks; sandboxes at once | 22; 8 | 22; 8 | 22; 8 |
| Passed in their sandbox | 20 | 21 | 21 |
| Landed / dropped by ruling / failed | 11 / 9 / 2 | 11 / 10 / 1 | 11 / 10 / 1 |
| Wall time | 238.5 s | 374.2 s | 238.8 s |
| Sum of task times | 1,168.9 s | 1,374.2 s | 1,118.8 s |
| Task, median (max) | 57.5 s (65.6) | 47.4 s (297.6) | 45.3 s (79.6) |
| Sandbox creation, median (max) | 23.1 s (28.8) | 18.6 s (29.7) | 20.6 s (28.1) |
| Worker, median (max) | 12.6 s (25.7) | 11.2 s (248.8) | 10.4 s (31.0) |
| Model requests through Shield | 159 | 167 | 168 |
| Integrated verification in a fresh sandbox | 11 files, 23.7 s | 11 files, 22.9 s | 11 files, 23.5 s |

All seven runs passed. On the host, every exported patch matched its report's
SHA-256. Applying each `integrated.patch` to a fresh clone reproduced the tree
the fresh sandbox verified (`5b249cf`, `ee80834`, `12a8efa`, `deefbc0`,
`c407b0d`, `bb4faaa` and `da6eaf8`).

Most of a task's time is the boundary, not the model. The worker's median was
10-13 seconds of a 45-58-second task. The rest is gateway readiness, sandbox
creation, the denial and cancellation tests (which stop and restart the VM),
the Shield-down refusal and the restart before export. The lone integrated
verification sandbox was created in 13.2-13.8 seconds. The medians were
22.1-22.6 seconds with 6 sandboxes at once and 18.6-24.9 with 8. Wall time was
20-27% of the summed task time. The second `7925bff` run took 374 seconds
because one worker waited out four HTTP 429 responses (below).

Seven tasks failed, all on the SambaNova lane. Six failed `sandbox_verify` after
two attempts. `csvline` in the first run raised `SyntaxError` both times.
`interval` failed in all five `fixture-both-lanes` runs the same way: its only
edit, identical each time, added a docstring to `merge` and changed no code, and
the repair attempt edited nothing. The three tests that fail before any edit
still failed, and `merge([(8, 10), (1, 3), (2, 6)])` still returned
`[(8, 10)]`. `interval` passed on Cerebras all seven times, and `csvline` passed
on SambaNova in the other six runs. Of the 127 tasks that passed, 125 passed on
their first attempt. The repair budget rescued two, both `humanize` on Cerebras,
in the second `fixture-12` run and the second `7925bff` run. The failed states
were kept for inspection.

The seventh, `duration` in the first `7925bff` run, never got an answer.
OpenCode opens a session with two requests. Shield passed the first, and then
the TLS link between the sandbox and Shield's middleware failed
(`SSLV3_ALERT_BAD_RECORD_MAC`, then `TSI_DATA_CORRUPTED`). The sandbox refused
the second with `403 middleware_failed` and logged a middleware-failure finding.
The worker stopped on that error after 2.3 seconds, before the first was
answered. The middleware forks a Shield process
for every request and response while gRPC serves TLS on other threads. By
default grpcio 1.84.0 hooks every fork; twice in the 300 milliseconds before the
corruption it logged that other threads were calling into gRPC and skipped its
fork handlers. The forked child only runs Shield and never uses gRPC, so the
middleware now starts with `GRPC_ENABLE_FORK_SUPPORT=0`. A stress test on the
host drove the real middleware and Shield with 300 request and response cycles
from 4 threads, each cycle on a new TLS connection. With the default, 8 of 12
runs logged corruption and 11 of their 3,600 cycles failed. With the setting,
none of 12 did, and each ran faster than its paired default run (22.5 seconds
against 26.1 on average). At 20 cycles on 2 threads, neither setting corrupted
any of 30 runs. TLS and the token checks are unchanged.

All 1,009 model requests crossed Shield with the pinned model and provider
policy, and none were blocked. The lane's pinned provider completed 987 with
HTTP 200, and no successful response came from another provider. HTTP 429 came
back 15 times, 11 on Cerebras and 4 on SambaNova, and Shield delivered each as
a provider error with no fallback. After the three in the first two runs,
OpenCode sent its next request about 60 seconds later, and those tasks had the
three slowest workers of those runs (66-71 seconds). In the second `7925bff`
run, `roman` on Cerebras got four, and its worker ran 249 seconds. Seven
requests have no response record and were not blocked. Each was one of the two
requests OpenCode opens when a session starts. Four got HTTP 200 headers but no
completed response. Three got nothing: `duration`'s two (above) and one in the
first run, whose task passed and whose logs were not kept. Task mode plants no
secrets, so nothing was masked.

The tool-less director made 51 rulings, and none failed. The 22 in the first
four runs mostly preferred the smaller change, and eight of them were checked
by reading the patches (the 29 in the `7925bff` runs were not):

- `hexcolor`: the dropped patch checks digits with `int(x, 16)`, which accepts
  a sign or a space: `parse_hex("#-12345")` returns `(-1, 35, 69)`. This holds,
  and the tests do not cover it.
- `csvline`, twice: the landed patch keeps a quote in the middle of a field
  literally, as Python's `csv` module does, and the dropped one opens quoting
  there and can swallow commas. This holds, with the lanes swapped between the
  two runs, and the tests do not cover it.
- `semver`: the dropped patch's leading-zero check runs on `str(int(...))` and
  can never fire. This holds.
- `roman`, twice on the same SambaNova patch: it refuses inputs the original
  accepted. This holds: `4.0` is now refused.
- `humanize`: the dropped patch adds non-ASCII characters (a non-breaking
  hyphen, a narrow no-break space and an arrow). This holds.
- `camel`: it kept the leading underscore of `_fooBar`, although the prompt
  says the result never starts with one. This was a judgment call.

A ruling chooses among candidates that already passed their own tests; it is
not verification.

Repeats show where two runs of one task part. Each task ran five or seven
times on its lane with the same prompt: 352 pairs of the same task on the same
lane. Of these, 27 involved a repair attempt and 20 opened with different
requests, which leaves 305 compared step by step. OpenCode gives every tool
call a random ID and sends it back in the next request, so no two repeats sent
the same request bytes after their first tool call. Apart from those IDs,
whenever two workers had seen the same history (prompt, tool calls and tool
outputs), they gave the same answer: 1,197 of 1,197 steps, on both lanes.
Every split started outside the model. In 138 pairs it was the first `glob`,
whose file list OpenCode returns in the order ripgrep's parallel walk finds the
files. In 11 it was a test run whose output differed only in its numbers, such
as timings. The 156 pairs that saw the same tool outputs throughout produced
byte-identical patches, and 25 of the 149 that split still converged on the
same patch. Failures repeat too: `interval` on SambaNova made the same edit in
all five runs.

`7925bff` assumed OpenCode lists `glob` results newest first, and gave each
payload file its own modification time. OpenCode does not sort them: in the
three runs built from it, the first `glob` still split 31 of 59 compared pairs,
so it was reverted. The order has to come from ripgrep itself (`--sort=path` in
the sandbox image). This is an observation on one fixture, not a determinism
test; the lanes stay separate.

Limits:

- Only text edits to existing regular files land: no new, deleted or renamed
  files, mode changes or binaries.
- There is one winner per conflict group; overlapping patches are never merged.
- The director sees each patch truncated to 2,000 characters and the worker's
  unverified report.
- A change that breaks another landed task through a different file is caught
  only by the integrated verification. That failure fails the run; Captain does
  not retry without the culprit.
- The integrated tree has no ref, so `git gc` may prune it; the patch is the
  durable result.
- These are 134 tasks on one small fixture and one host. They exercise the
  harness at 6 and 8 sandboxes at once; they are not a task-success rate or a
  provider ranking.

## Boundary

| Component | Location and authority |
| --- | --- |
| Controller | Host; creates only a disposable fixture repository and private run state |
| Worker and tests | OpenShell sandbox; fresh OpenCode process, no shared host server |
| NIM credential | OpenShell's encrypted credential store; workload receives a placeholder |
| Shield | Host middleware; authenticates gateway/supervisor JWTs over verified TLS, calls Captain's existing redaction engine |
| Network | Only approved binaries may POST to the selected provider's chat-completions path; all other destinations are denied |
| Files | `/sandbox` and runtime scratch paths are writable; system paths have explicit read access; `/outside` is excluded |
| Landing | Only the bounded text edit to the fixture's existing `slugify.py` is accepted |

Middleware runs **before credential injection**. It decodes JSON before masking
strings, preserving integer precision and catching Unicode-escaped secrets in
tool output. Masking remains enabled even if `CAPTAIN_REDACT=off` is inherited.
Malformed, compressed, oversized, wrong-model or out-of-scope requests are
refused. Redactor or audit-write failures also deny the request.

The middleware masks outbound requests and restores identities in complete JSON
responses. For OpenCode's streaming requests, it explicitly requests
`stream: false` from NIM, removes `stream_options`, restores the complete JSON,
and returns equivalent SSE content/tool, finish, usage and `[DONE]` events to
OpenCode. Tool arguments are decoded before restoration and encoded again, so
quotes in an original identity do not corrupt the tool's JSON. Arguments that do
not decode to a JSON object are delivered as text and never regain secrets;
OpenCode reports them to the model as an invalid tool call instead of the run
failing. Secret placeholders in prose, reasoning and error responses remain
masked.
Only structured arguments for a tool offered in the paired request can regain
secrets that Shield actually masked in that request. A placeholder alone never
authorizes a vault lookup. Invented handles, handles from a different request
or sandbox, and unoffered tools are refused. Replacement walks parsed argument
values and keys, rejects key collisions and bounds expansion before emitting
JSON. The worker receives no partial answer before the complete response is
checked.

Both response input and replacement must fit 4 MiB. OpenShell bounds whole-body
collection after response headers to two minutes; the worker still has its
overall ten-minute deadline. Unsupported, compressed, partial, `no-transform`,
malformed or oversized responses are refused, as are restoration or audit-write
failures. There is no fallback to uninspected responses. Provider error statuses
keep their JSON format rather than being converted into successful SSE events.

The request/response association is keyed by authenticated sandbox and request
IDs, limited to 128 pending entries, and expires after ten minutes. Each entry
holds at most 1,024 observed secret handles and their original values. This
per-request authorization map exists only in middleware memory and is consumed
at response preflight. Shield's existing owner-only vault remains on disk.
The Go adapter returns this private mapping separately from the outbound model
body. It is never copied into audit records or sent to the provider. Missing,
replayed or expired associations refuse response delivery. Restarting middleware
during inference loses this transient association and fails closed; it does not
resume an in-flight model call. Completed-worker artifact recovery is unchanged.

The fixture uses synthetic secrets, a synthetic operator identity, and paths
under `/sandbox`. The live gates require a response restoration audit event and
the original synthetic identity in the worker's final answer. The tool-secret
gate additionally requires an audited restoration and a tool output containing
the SHA-256 of the synthetic credential. Final answers must not contain that
credential. Tool arguments and worker transcripts are private local artifacts:
restored values can appear there, just as they can in the files the worker reads.
Normal filesystem and network enforcement still applies to every tool call.
General repository snapshots, task routing and broader qualification remain
outside this fixture pilot.

OpenShell 0.1.2 excludes `text/event-stream` and `multipart/x-mixed-replace` from
`WHOLE_BODY_BYTES`, even for responses within the size cap. Its `STREAM_BYTES`
mode forbids holding input across units. A native SSE-buffering attempt was
therefore refused at preflight, with no changes landed. The JSON-to-SSE adapter
uses the existing supported response modes; low-latency streaming restoration
remains unresolved.

No prompts, bodies or credentials are written to the middleware audit. It
records request/sandbox IDs, masking counts, hashes, and synthetic-canary
observations. The gateway retains its own policy/network logs. The private
state directory also contains a credential store, TLS keys and Shield vault;
do not publish that directory. The checked-in result is sanitized.

## Run

Prerequisites: Python 3.12+, Go matching this repository, `uv`, `curl`, Git and
Docker. Supported bootstrap platforms are Apple Silicon macOS and Linux
arm64/x86_64. A Linux worker image is used on either host. Ports are dedicated
to the pilot; the gateway uses client-certificate authentication and Shield
requires signed, audience-checked extension tokens. Telemetry is disabled.

From the repository root:

```sh
pilot_state=$(mktemp -d /tmp/cc-openshell.XXXXXX)
python3 examples/openshell-pilot/prepare.py --state "$pilot_state"
"$pilot_state/venv/bin/python" examples/openshell-pilot/pilot.py --state "$pilot_state"
```

`NVIDIA_API_KEY` must already be in the controller's environment. It is passed
only to the provider-registration process, never through command arguments,
the snapshot, image build or worker environment. The selected model is fixed
to `z-ai/glm-5.3-flash`; there is no provider fallback. This makes a real NIM
request only after the runtime and pre-worker checks pass.

`prepare.py` verifies the pinned archive hashes, installs hash-locked Python
wheels into the private virtual environment, generates protocol bindings from
two pinned NVIDIA proto files, builds the Shield adapter, and builds the worker
image. It does not run downloaded install scripts or modify the host's PATH.
The image's Debian packages are installed by apt from its signed repositories;
the base image and OpenCode binary are pinned in the Dockerfile and artifact lock.
The image also includes a pinned Debian `ripgrep` package. OpenCode otherwise
tries to download that tool on its first glob or content search, which the
network policy denies. Both searches run through OpenCode inside the sandbox
before the first model request; missing tools or incorrect results stop the run.

For the MicroVM backend, pass `--runtime vm` to both commands, and pass the
locally built driver to `prepare.py --vm-driver`. The bootstrap signs that
local binary with the macOS hypervisor entitlement. This does not grant the
workload additional filesystem or network access.

Use a short state path under `/tmp`: macOS's default `$TMPDIR` path can exceed
the MicroVM driver's Unix socket limit. The pilot checks this before starting
services and reports a shorter-path remedy.

Sandbox environment settings are supplied through OpenShell's `--env` API.
The VM backend does not inherit the worker image's Dockerfile `ENV` values.
The explicit settings select the NIM configuration, disable external model
catalog/plugin updates, and prevent Python bytecode from entering the diff.

All work runs in the foreground. Worker execution has a 10-minute deadline;
SIGINT and SIGTERM request sandbox cleanup. The controller shuts down its services before
returning. A completed run deletes its sandbox. An unsuccessful worker retains
its sandbox workspace and requests a stop; inspect `cleanup.log` if that stop
cannot be confirmed. A failed startup has no worker artifacts and is deleted.

Each state directory has one controller owner. Both preparation and execution
acquire a nonblocking OS file lock before reading checkpoints or starting
services. A competing invocation exits without modifying the active run.
The lock releases when its owner exits, including after a crash; do not delete
`controller.lock` to override an active owner. Preparation refuses a directory
that already contains a checkpoint. Invalid start/resume requests leave the
previous report and checkpoint unchanged.

After exporting, the first controller stops its services and releases ownership
before starting the recovery controller. Only the new owner may update the
checkpoint or perform cleanup. This prevents a second resume from interfering
with the first controller's gateway, middleware or landing operation.

## Acceptance gates

1. Qualify the runtime kernel, exercise OpenCode's glob and content search without
   a model call, and verify the unmodified fixture has three failing tests.
   This proves the worker tools function under policy and the task is not vacuous.
2. Require explicit permission errors for reads and writes under `/outside`.
   The canary is world-readable and its directory world-writable in the image,
   so ordinary Unix permissions cannot account for those denials.
3. Require explicit refusals for an unrelated network destination and an
   unapproved NVIDIA API path. A timeout is not a passing denial test.
4. Time out a remote parent with a sleeping child, require a successful
   `sandbox stop`, then verify the recovered VM has a new boot identity.
   OpenShell 0.1.2's exec timeout alone leaves a descendant alive. The controller
   therefore cancels the whole sandbox on remote or local transport deadlines;
   it never treats disconnection as cancellation.
5. Run one OpenCode worker; require the unchanged fixture tests to pass. Require
   audit observations that Shield masked the synthetic secret from tool output
   and restored the synthetic operator identity in the worker's final answer.
   Require a request-scoped tool-secret restoration, the correct synthetic
   credential hash in tool output, and no raw credential in final answers.
6. Stop Shield and require the next otherwise valid model request to be refused.
7. Flush the completed snapshot and export with `sync`, save the pending-export
   checkpoint, stop the first controller's services, and
   launch a fresh controller process against the same gateway database. Restart
   the sandbox and rerun tests before downloading the diff.
8. Reject changed target HEAD/worktree, altered tests, extra files, symlinks,
   mode changes, binary diffs or oversized exports. Check and apply the patch
   against the starting revision in an isolated Git index, then compare the
   resulting bytes with the exported file before changing the landing file.
   Save the validated hashes durably and replace the file atomically.

The controlled restart point is after successful worker completion, diff
capture against the recorded starting commit, and a successful guest `sync`,
before download. VM stop is abrupt: unflushed guest writes can be lost. It does not claim mid-inference resumption. A pending
export can be resumed manually without submitting another model task:

```sh
"$pilot_state/venv/bin/python" examples/openshell-pilot/pilot.py --state "$pilot_state" --resume
```

Recovery also covers interruption immediately before or after the local file
replacement. The saved `landing_pending` checkpoint pins the patch, original
file and expected result. Resume accepts either the unchanged original or the
exact recorded result, while refusing changed exports, staged changes, other
files, mode changes or subsequent edits. It does not submit another model task
or restart the worker. A completed landing can be verified again with the same
command; after confirmed sandbox deletion this needs neither OpenShell services
nor an API key. Runtime cleanup is still required for a retained sandbox.

The report records gateway readiness, sandbox creation, worker duration and
sandbox restart duration separately, plus attempts and successful tasks. A
single passing task would establish a functional smoke test, not a reliability
estimate. Expansion remains gated on all live checks passing.

## Artifacts and tests

`report.json` contains verdicts and timings. `checkpoint.json` pins the sandbox
name, base revision and snapshot/test hashes. `snapshot.tar`, `result.patch`,
`recovered-slugify.py`, the disposable `landing/` repository, worker output,
test logs, Shield audit and gateway/sandbox logs remain in the private state.
The report returns `inconclusive` for infrastructure errors or worker/transport
deadlines and `fail` for an observed acceptance failure. Expired workers retain
their captured output and elapsed time, stop the sandbox, and skip verification
and landing; tool-reported test success does not count as completion. The process
exits nonzero for either verdict.

```sh
go test ./examples/openshell-pilot/shield
PYTHONPATH="$pilot_state/generated" PILOT_SHIELD_BIN="$pilot_state/shield" \
  "$pilot_state/venv/bin/python" -m unittest discover \
  -s examples/openshell-pilot -p 'test_*.py' -v
```

Adapter tests cover cancellation on both remote and local deadlines, real Captain masking, malformed input, credential identity,
JWT expiry/audience/signature, protocol negotiation, closed request/response scope,
redactor/audit failure, patch validation, checkpoint persistence and a
middleware started without gRPC fork handlers. These are not substitutes for
live OpenShell enforcement tests.

Landing regressions cover mismatched exports without modifying the checkout,
interruptions on either side of atomic replacement, repeated completion in a
fresh process, and preservation of user edits. Controller regressions cover
competing entrypoints, lock release after exceptions and process death, unsafe
lock files, and preparation over an existing checkpoint. CI runs the controller,
landing and diff tests on Linux and macOS without model calls or OpenShell.
Worker regressions cover missing or broken search tools, refusal to submit a
model task after a failed gate, and deadline verdicts with retained partial output.
Response regressions cover completion-to-SSE conversion, separate choices/tool
calls and reasoning, escaped identities in tool arguments, legacy function calls,
large integers, preserved secret placeholders, input/output caps, request pairing,
provider errors, and fail-closed service or audit errors. Tool-secret regressions
cover forged handles after vault population, cross-request and cross-sandbox
refusal, offered-tool restrictions, JSON escaping, colliding keys, bounded
expansion, and keeping secrets out of prose, reasoning, errors and audits.
The standard-library
response tests also run in CI.

Task-mode regressions cover spec validation and bounds, patch scope, snapshot
pinning and refused sources, protected files, baseline expectations, worker-tool
visibility, Shield mediation on the pinned model and provider, the repair loop,
recovery export and verify mode. They also check that every example team task
is valid and fails at the base revision; these tests run in CI. Captain's side,
`go test ./pkg/captaincode -run 'OpenShell|ShellJoin|ToolLessClaude'`, drives a
fake pilot through landing, conflicts with and without a director, a failing
integrated tree, cancellation and refused setups.
Python lint runs with
`ruff check examples/openshell-pilot`; Go checks use `go vet ./...` and the
Shield test command above.

## Optional dependencies

The repository's `go.mod` and `go.sum` are unchanged. Everything below is used
only by this example and is pinned in `artifacts.lock.json`, `requirements.txt`
or the Dockerfile:

- NVIDIA OpenShell CLI, gateway, prover and runtime images: 0.1.2 (Apache-2.0).
- OpenCode: 1.18.32 (MIT), downloaded as its platform binary without npm scripts.
- Python image: 3.12 slim Bookworm, pinned by image digest; Git, curl and CA
  certificates are installed inside that image.
- ripgrep: Debian Bookworm `13.0.0-4+b2`, from the signed Debian archive,
  maintained by Debian Rust Maintainers (MIT/Unlicense; completion files BSD-3-Clause).
- Python wheels: grpcio 1.84.0, grpcio-tools 1.84.0, protobuf 7.36.2,
  PyJWT 2.15.1, cryptography 50.0.1, cffi 2.1.1, pycparser 3.0,
  setuptools 84.0.0 and typing-extensions 4.16.0.

Package names and publisher/source metadata were checked on PyPI and npm.
The wheels use Apache-2.0, BSD, MIT or PSF licenses. NVIDIA's proto files retain
their Apache-2.0 notices in the downloaded run directory.

Upstream references are pinned to
[OpenShell v0.1.2](https://github.com/NVIDIA/OpenShell/tree/v0.1.2):
[support requirements](https://github.com/NVIDIA/OpenShell/blob/v0.1.2/docs/about/support-matrix.mdx),
[middleware](https://github.com/NVIDIA/OpenShell/blob/v0.1.2/docs/extensibility/supervisor-middleware/index.mdx),
and [extension authentication](https://github.com/NVIDIA/OpenShell/blob/v0.1.2/docs/extensibility/overview.mdx).
