# OpenShell pilot

Experimental acceptance harness for one NIM-backed OpenCode worker, one Git
snapshot, one OpenShell sandbox, and one edit, test, export and landing cycle.
It operates on the small `fixture/` repository, not your checkout. It does not
change Captain's routing, scope declarations, configuration or running brain.

## Current result

**Four bounded MicroVM runs passed with a locally patched OpenShell driver;
the latest passes all 16 checks. Expansion remains gated.**

The [worker search result](results/2026-09-30-search.json) on 30 September 2026
completed **1 task from 1 worker attempt**, with zero tool errors. OpenCode's
glob and content search now work with networking disabled and under the live
MicroVM policy. The image includes `ripgrep`; previously, OpenCode tried to
download it and the network policy correctly denied the request. This removes
that tool failure, without establishing that provider latency is resolved.

| Latest measurement | Seconds |
| --- | ---: |
| Gateway readiness | 0.856 |
| Sandbox creation, prepared image | 13.678 |
| Worker edit and test | 145.684 |
| Recovery gateway readiness | 0.725 |
| Sandbox restart | 2.246 |

File and network denials, cancellation, outbound Shield masking and refusal
when Shield is down, restart recovery and exact diff landing all passed. The
completed sandbox was deleted and the pilot services exited. The sanitized
report records the image and harness hashes alongside the driver hash.

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

## Boundary

| Component | Location and authority |
| --- | --- |
| Controller | Host; creates only a disposable fixture repository and private run state |
| Worker and tests | OpenShell sandbox; fresh OpenCode process, no shared host server |
| NIM credential | OpenShell's encrypted credential store; workload receives a placeholder |
| Shield | Host middleware; authenticates gateway/supervisor JWTs over verified TLS, calls Captain's existing redaction engine |
| Network | Only approved binaries may POST to NVIDIA's chat-completions path; all other destinations are denied |
| Files | `/sandbox` and runtime scratch paths are writable; system paths have explicit read access; `/outside` is excluded |
| Landing | Only the bounded text edit to the fixture's existing `slugify.py` is accepted |

Middleware runs **before credential injection**. It decodes JSON before masking
strings, preserving integer precision and catching Unicode-escaped secrets in
tool output. Masking remains enabled even if `CAPTAIN_REDACT=off` is inherited.
Malformed, compressed, oversized, wrong-model or out-of-scope requests are
refused. Redactor or audit-write failures also deny the request.

The middleware only implements outbound request masking. It does not yet
provide Captain's response identity restoration or tool-argument secret
restoration. The fixture uses synthetic secrets and paths under `/sandbox`.
This is not a claim that the complete Shield contract works in OpenShell.
OpenShell 0.1.2's streamed response contract forbids holding input across body
units, while identity stand-ins can span both transport chunks and model
deltas. Restoring each chunk independently would corrupt those split values;
that boundary remains unresolved before general repository use.

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
   an audit observation that Shield masked the synthetic secret from tool output.
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
JWT expiry/audience/signature, protocol negotiation, closed request scope,
redactor/audit failure, patch validation and checkpoint persistence. These are
not substitutes for live OpenShell enforcement tests.

Landing regressions cover mismatched exports without modifying the checkout,
interruptions on either side of atomic replacement, repeated completion in a
fresh process, and preservation of user edits. Controller regressions cover
competing entrypoints, lock release after exceptions and process death, unsafe
lock files, and preparation over an existing checkpoint. CI runs the controller,
landing and diff tests on Linux and macOS without model calls or OpenShell.
Worker regressions cover missing or broken search tools, refusal to submit a
model task after a failed gate, and deadline verdicts with retained partial output.
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
