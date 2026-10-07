# Second-machine runbook

This runbook repeats the OpenShell evidence on a second machine. Every live
pass so far used the public fixture on one Apple Silicon Mac. The goal is one
3-of-3 qualification and one real task on another machine, with reports that
you can compare with the first machine.

The selection rule is in [README.md](README.md#profiles-from-the-registry-and-qualification):
a qualification selects only with the same OpenShell version and VM driver
hash. A clone brings the first machine's `qualified.json`. Those records
select on the second machine only if its prepared driver has the same hash.

## 1. Prepare the runtime

Use a short state path under `/tmp`. Use the same Captain commit as the first
machine.

```sh
git -C <captaincode> rev-parse HEAD           # write this down
python3 examples/openshell-pilot/prepare.py --state /tmp/cc-prep --runtime vm --vm-driver <driver>
shasum -a 256 /tmp/cc-prep/bin/openshell-driver-vm
python3 examples/openshell-pilot/profiles.py --prepared /tmp/cc-prep
```

- `<driver>` is the local VM driver build with the journal-recovery patch
  (OpenShell #3940). Copy the same unsigned binary from the first machine to
  test "same driver, another machine". Build it again on this machine to test
  "another driver".
- `prepare.py` signs the driver on macOS. The hash is of the signed file.
- The listing shows, for each record, `legacy`, `on another machine (<id>)`, or
  refused for another OpenShell version or VM driver.

## 2. Qualify one profile, 3 of 3

```sh
captain openshell qualify --pilot examples/openshell-pilot --prepared /tmp/cc-prep --profile cerebras
```

- This sends real model requests: three fixture runs, each with one repair.
- The first report creates `~/.captaincode/machine-id` on this machine.
- A pass writes the record to `qualified.json` with this machine's profile, and
  keeps the three reports under `results/qualify/`.
- If a run fails, write down the pass count and the failed checks. Do not
  retry until it passes.

## 3. Run one real task

Choose a small public repository and a task with a test command. Do not use a
private repository or real credentials.

```sh
export CAPTAIN_OPENSHELL_PILOT=<captaincode>/examples/openshell-pilot
export CAPTAIN_OPENSHELL_PREPARED=/tmp/cc-prep
export CAPTAIN_OPENSHELL_PROFILE=cerebras
cd <public-repository>
CAPTAIN_STRICT=1 CAPTAIN_MAX_COST=0.50 captain with openshell '<task>'
captain task inspect <task-id>
```

Set `CAPTAIN_OPENSHELL_ALLOWED` and `CAPTAIN_OPENSHELL_VERIFY`, or accept the
confirm line that Captain proposes. The patch is exported, not applied.

## 4. Bring the reports back

Copy these files to the first machine:

- the three `results/qualify/*.json` reports and the new `qualified.json` record;
- the task's `report.json` from its pilot state, and the run directory that
  `captain task inspect` names.

Do not copy `~/.captaincode/machine-id`. Each machine keeps its own id.

## 5. Compare

| What | Where in the report | First machine | Second machine |
|---|---|---|---|
| Qualification pass count | `captain openshell qualify` output | 3 of 3 | |
| Checks failed, if any | `checks.<name>.verdict` | none | |
| Sandbox start time | `timings_seconds.sandbox_create` | | |
| Worker time | `timings_seconds.worker` | | |
| Worker attempts | `worker_attempts` | | |
| Shield spend | `shield.cost_usd`, `shield.budget.committed_usd` | | |
| Machine | `machine.os`, `machine.arch`, `machine.cpu_model`, `machine.memory_gib` | | |
| Driver | `machine.vm_driver_sha256`, `vm_driver.version` | | |

Fill the first-machine column from its `results/qualify/` reports. A
difference in a denial check (`filesystem_denied`, `network_denied`,
`nim_path_denied`) is a driver or kernel difference. Report it before you
change any check.
