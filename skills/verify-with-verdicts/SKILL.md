---
name: verify-with-verdicts
description: Decide whether a code change works by running the repository's own checks and reporting exactly one verdict - pass, fail or inconclusive. Use after editing code and before calling a task done, when a test run hangs or hits a timeout, when tests fail and a retry or repair is being considered, when writing a regression test, when a test is flaky, and when reporting verification to a user or another agent. Covers finding the repo's check, baselines that must fail first, deadlines that kill the whole process tree, repair versus retry, flakes, and keeping tests away from live services and personal env.
license: MIT
---

# Verify with verdicts

A verification result is exactly one of three things:

- **pass**: the check ran to completion and exited 0.
- **fail**: the check ran to completion, exited non-zero, and its output names
  what failed.
- **inconclusive**: anything else. It was killed at its deadline, it could not
  start (missing toolchain, no network, no real test script), the runner
  crashed, or nothing ran.

Inconclusive is not a failure. In Captain Code, `cargo test` on a 49-crate Rust
workspace blew through its 5-minute deadline, and the harness then read the
killed run as a failing suite and paid for a repair of code nobody had shown
to be broken. It happened twice before the rules below existed. The incidents
quoted here are Captain's own.

## 1. Run the repository's own check

Use what the repository says it runs: CI config, Makefile or justfile targets,
package scripts, CONTRIBUTING. Failing that, the manifest at the root:

| Root file | Check |
|---|---|
| `go.mod` | `go test ./...` |
| `package.json` | `npm test`, if the script is real (the `npm init` placeholder exits 1 without testing anything) |
| `pyproject.toml`, `pytest.ini`, `setup.py` | `python -m pytest` |
| `Cargo.toml` | `cargo test` |
| `Makefile` with a `test` target | `make test` |

- Capture stdout and stderr together (`2>&1`). The failing assertion is often
  on stderr.
- Scope first, then widen: the package or test file you touched for fast
  feedback, then the full suite before saying pass. A scoped pass is reported
  as scoped.
- A turn that changed no files has nothing to verify. Say that instead of
  running a suite.

## 2. A new test must fail before the fix

- Run a new regression test against the untouched code first (stash the fix,
  or run it at the base commit). It must fail, and for the reason you expect.
  A test that passes on the baseline proves nothing. In Captain's eval harness,
  a fixture whose acceptance check already passes at the base revision makes
  every arm look successful without doing any work, so fixtures are checked
  against the baseline before any worker runs.
- A glob that guards files (fixtures, "must not change" lists) must match at
  least one real file. A pattern that matches nothing guards nothing and never
  fails.

## 3. Give every check a deadline that kills the whole tree

Test runners fork compilers, browsers, servers and test binaries. Killing only
the process you started leaves its children running and holding the output
pipe, so the "killed" check keeps going and your reader keeps waiting. That is
how a 5-minute budget became a wait of more than 20 minutes.

Run the check as the leader of its own process group, kill the group, and stop
waiting on pipes after a grace period:

- Shell (GNU coreutils): `timeout -k 10s 5m sh -c "$check"`. `timeout` puts the
  command in its own process group and signals the whole group; exit 124, or
  137 after the follow-up KILL, means the deadline hit. On macOS it installs as
  `gtimeout`.
- Go: `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`, a `cmd.Cancel`
  that calls `syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)`, and
  `cmd.WaitDelay = 10 * time.Second` so a grandchild holding the pipe cannot
  block `Wait`.
- Python: `subprocess.Popen(..., start_new_session=True)`, then
  `os.killpg(p.pid, signal.SIGKILL)` at the deadline.
- Node: `spawn(cmd, args, { detached: true })`, then
  `process.kill(-child.pid, 'SIGKILL')` at the deadline.

Record that the check was killed, at what deadline, and after how long. A
killed check has no verdict: `inconclusive (killed at 5m)`. If the suite cannot
finish inside the budget, run a narrower scope rather than wait longer.

## 4. Act on the verdict

- **pass**: report it with the command and the scope.
- **fail**: repair once, with the failing test names and the tail of the output
  in hand. If the repair fails too, escalate once (more reasoning effort, a
  stronger model, or a person) within a stated budget, then stop and report.
  Never loop.
- **inconclusive**: do not repair; nothing was shown to be broken. Either rerun
  once with a narrower scope or a longer deadline, or stop and say
  "verification inconclusive" with the reason. Do not round it up to pass ("it
  didn't finish, but the code looks right") or down to fail.
- Missing results, runner errors and timeouts are all inconclusive. A failure
  report names the command that failed; "tests failed" with no command is not
  evidence. Captain once treated missing results as failures and reported them
  with a blank command.
- A reviewer, human or model, can reject work whose checks passed. It cannot
  accept work whose checks failed. Keep "the checks passed" and "a person
  accepted it" as separate records.

## 5. Flaky tests

- A test that fails in the full run and might depend on timing: rerun it
  alone, a few times, and report both results ("failed in the full parallel
  run; passed alone 3/3"). Never drop the first failure from the report.
- Suites that share machine resources (ports, temp dirs, a daemon) interfere
  when they run in parallel. Say which suites ran together.
- Wait on the event, not on a status flag. A Captain test waited for a worker
  to show as running before interrupting it, but a worker shows as running
  before it can be stopped, so about one run in three the interrupt found
  nothing to stop and the suite hung. Waiting on a channel closed after the
  stop hook was attached fixed it.
- Run date logic across midnight in both UTC and local time. A Captain test
  compared UTC timestamps with local dates and failed only in the hours after
  local midnight, while UTC was still on the previous day.

## 6. Keep the run away from the machine it runs on

- Tests must not reach a live daemon, the user's real config or their home
  directory. Inject the service address, and point `HOME` and config dirs at a
  temp dir. Captain's doctor tests probed the real daemon on loopback and broke
  release packaging.
- Unset personal env pins before the run (`env -u MY_MODEL_PIN go test ./...`).
  A pass that depends on the developer's env is a local pass.
- If the code under test can run the test suite itself (a tool that verifies
  work by running tests, whose own tests run the tool), guard the recursion
  with an env marker: the inner run sees it and returns "no signal". The tests
  of that feature must clear the marker, or they misbehave when run under the
  tool. Captain hit both halves: unbounded recursion without the marker, and
  false failures when its own tests inherited it.

## 7. Capabilities are yes, no or unknown

When a check depends on what a tool supports (a flag, an output format, an API
feature):

- Record yes, no or unknown. Unknown never satisfies a hard requirement.
- Probe the real binary at the command you will call: `tool sub --help`, not
  `tool --help`. A top-level help screen can stop listing an option the
  subcommand still accepts; one CLI's top-level help dropped its JSON output
  flag while its `exec --json` kept working.

## 8. Report

```text
verdict:  pass | fail | inconclusive (<reason>)
command:  <exact command>              cwd: <dir>
env:      <changes, e.g. HOME=<tmp dir>, unset MY_MODEL_PIN>
exit:     <code | killed at <deadline>>   duration: <seconds>
scope:    <full suite | package | single test>
failing:  <test names, last ~30 lines of output>   (fail only)
reruns:   <what was rerun alone, and the results>
not run:  <checks skipped, and why>
```
