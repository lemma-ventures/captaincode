import datetime
import fcntl
import hashlib
import json
import math
import os
import platform
import re
import secrets
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
CATALOG_FILE = HERE / "catalog.json"
QUALIFIED_FILE = HERE / "qualified.json"
EVIDENCE_DIR = HERE / "results" / "qualify"
# A qualification ages out: a provider can change what serves a route.
QUALIFY_DAYS = 30
# A profile qualifies on three fixture runs in a row, all passing, each with
# the one repair a real task gets: one sample is too noisy to gate on, and a
# run without the repair tests a stricter setting than tasks use.
QUALIFY_RUNS = 3
QUALIFY_REPAIRS = 1
# The OpenShell release the pilot pins; a report and a qualification name it.
OPENSHELL_VERSION = json.loads((HERE / "artifacts.lock.json").read_text())["openshell"]
# A random value made once per machine. It tells two machines apart without
# a hostname, user name, serial number or MAC address.
MACHINE_ID_FILE = Path.home() / ".captaincode" / "machine-id"
MACHINE_ID = re.compile(r"[0-9a-f]{16}")
MACHINE_FIELDS = ("os", "os_version", "arch", "cpu_model", "memory_gib", "openshell_version",
                  "vm_driver_sha256", "machine_id")
SHA256 = re.compile(r"[0-9a-f]{64}")

OPENROUTER = {
    "model": "openai/gpt-oss-120b", "host": "openrouter.ai", "base_path": "/api/v1",
    "key_env": "OPENROUTER_API_KEY", "file": "openrouter.yaml", "type": "captain-openrouter-pilot", "output": 16384,
}
LANES = {
    "cerebras": ("cerebras", "Cerebras"), "sambanova": ("sambanova", "SambaNova"),
    "together": ("together", "Together"), "deepinfra": ("deepinfra/bf16", "DeepInfra"),
    "crusoe": ("crusoe/bf16", "Crusoe"), "parasail": ("parasail/fp4", "Parasail"),
}
# A strict dollar cap prices each lane at a ceiling, in USD per million prompt
# and completion tokens. Shield sends it as OpenRouter's max_price, so a lane
# that costs more is refused before it generates, and reserves against it.
# List prices on 2 October 2026: Cerebras 0.35/0.75, SambaNova 0.14/0.95,
# Together 0.15/0.60, DeepInfra 0.037/0.17, Crusoe 0.05/0.25, Parasail 0.10/0.75.
CEILINGS = {
    "cerebras": (0.45, 0.95), "sambanova": (0.18, 1.20), "together": (0.19, 0.75),
    "deepinfra": (0.05, 0.22), "crusoe": (0.07, 0.32), "parasail": (0.13, 0.95),
}
# Template tokens a provider adds around the forwarded messages; gpt-oss adds
# about 70. Byte-level tokenizers emit at most one token per body byte.
PROMPT_MARGIN = 4096
# The hand-kept profiles: NIM's GLM 5.3 Flash and gpt-oss-120b on six lanes.
KEPT = {
    "nim": {
        "model": "z-ai/glm-5.3-flash", "host": "integrate.api.nvidia.com", "base_path": "/v1",
        "key_env": "NVIDIA_API_KEY", "file": "nvidia.yaml", "type": "captain-nim-pilot", "output": 4096,
    },
    **{name: dict(OPENROUTER, route=route, served_by=served_by, ceiling=CEILINGS[name], source="kept")
       for name, (route, served_by) in LANES.items()},
}
KEPT["nim"]["source"] = "kept"

NAME = re.compile(r"[a-z][a-z0-9-]{0,63}")
MODEL = re.compile(r"[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._:-]*")
ROUTE = re.compile(r"[a-z0-9][a-z0-9.-]*(/[a-z0-9][a-z0-9.-]*){0,3}")
SERVED_BY = re.compile(r"[A-Za-z0-9][A-Za-z0-9 ._()&-]{0,63}")


def route_provider(route):
    return route.split("/")[0]


def duplicates_kept(entry):
    """A generated profile that names a kept lane's model and provider is that
    lane: cerebras/fp16 is the kept cerebras route under its variant name."""
    for kept in KEPT.values():
        if kept.get("route") is None or kept["model"] != entry["model"]:
            continue
        if route_provider(kept["route"]) == route_provider(entry["route"]) and (
                "/" not in kept["route"] or kept["route"] == entry["route"]):
            return True
    return False


def load_catalog(path=None):
    """Read the registry-generated profiles (captain openshell profiles).

    The catalog names a model and a route only; host, key and policy come from
    OPENROUTER, so an edited catalog cannot point Shield at another host. A
    malformed entry fails the whole load: Shield must not run on a guess.
    """
    path = Path(path or CATALOG_FILE)
    if not path.exists():
        return {}
    catalog = json.loads(path.read_text())
    if type(catalog) is not dict or catalog.get("schema") != 1 or type(catalog.get("profiles")) is not dict:
        raise ValueError("catalog.json: unsupported catalog")
    loaded = {}
    for name, entry in sorted(catalog["profiles"].items()):
        if type(name) is not str or not NAME.fullmatch(name) or type(entry) is not dict:
            raise ValueError("catalog.json: invalid profile name")
        ceiling = entry.get("ceiling")
        valid = (type(entry.get("model")) is str and MODEL.fullmatch(entry["model"])
                 and type(entry.get("route")) is str and ROUTE.fullmatch(entry["route"])
                 and type(entry.get("served_by")) is str and SERVED_BY.fullmatch(entry["served_by"])
                 and type(entry.get("output")) is int and 1 <= entry["output"] <= OPENROUTER["output"]
                 and type(entry.get("context")) is int and 32768 <= entry["context"] <= 4_194_304
                 and type(entry.get("leg")) is str and NAME.fullmatch(entry["leg"])
                 and entry.get("tier", "cheap") in ("cheap", "frontier")
                 and type(ceiling) is list and len(ceiling) == 2
                 and all(type(v) in (int, float) and math.isfinite(v) and 0 <= v < 1000 for v in ceiling))
        if not valid:
            raise ValueError("catalog.json: invalid profile " + name)
        if name in KEPT or duplicates_kept(entry):
            continue
        loaded[name] = dict(OPENROUTER, model=entry["model"], route=entry["route"], served_by=entry["served_by"],
                            output=entry["output"], context=entry["context"], ceiling=tuple(ceiling),
                            leg=entry["leg"], source="registry", **({"tier": entry["tier"]} if "tier" in entry else {}))
    return loaded


PROFILES = {**KEPT, **load_catalog()}


def profile(name):
    if name not in PROFILES:
        raise ValueError("unsupported inference profile")
    return dict(PROFILES[name])


def identity(name):
    """What a qualification vouches for: the model, where it is served and
    with which key. A ceiling or token limit can change without requalifying."""
    selected = profile(name)
    fields = {key: selected.get(key) for key in ("model", "host", "base_path", "key_env", "route", "served_by")}
    return hashlib.sha256(json.dumps(fields, sort_keys=True).encode()).hexdigest()


def machine_id(create=True):
    """This machine's id from MACHINE_ID_FILE. With create, a missing file is
    made with a new random id (mode 0600); without it, a missing file is None."""
    path = Path(MACHINE_ID_FILE)
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except FileNotFoundError:
        if not create:
            return None
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        try:
            descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
        except FileExistsError:
            return machine_id(create=False)
        try:
            value = secrets.token_hex(8)
            os.write(descriptor, (value + "\n").encode())
        finally:
            os.close(descriptor)
        return value
    try:
        value = os.read(descriptor, 64).decode("ascii", "replace").strip()
    finally:
        os.close(descriptor)
    if not MACHINE_ID.fullmatch(value):
        raise ValueError(f"{path}: not a machine id; remove the file to make a new one")
    return value


def driver_sha256(driver):
    """The SHA-256 of a VM driver binary, or None when there is no driver."""
    if driver is None or not Path(driver).is_file():
        return None
    return hashlib.sha256(Path(driver).read_bytes()).hexdigest()


def _sysctl(name):
    try:
        result = subprocess.run(["/usr/sbin/sysctl", "-n", name], capture_output=True, timeout=5, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return None
    return result.stdout.decode("utf-8", "replace").strip() if result.returncode == 0 else None


def _cpu_model():
    model = None
    if sys.platform == "darwin":
        model = _sysctl("machdep.cpu.brand_string")
    elif Path("/proc/cpuinfo").is_file():
        for line in Path("/proc/cpuinfo").read_text(errors="replace").splitlines():
            if line.startswith("model name"):
                model = line.partition(":")[2]
                break
    model = re.sub(r"[^A-Za-z0-9 ()@.,_+-]", "", (model or platform.processor() or "").strip())[:96]
    return model or None


def _memory_gib():
    try:
        total = os.sysconf("SC_PAGE_SIZE") * os.sysconf("SC_PHYS_PAGES")
    except (OSError, ValueError, AttributeError):
        total = int(_sysctl("hw.memsize") or 0) if sys.platform == "darwin" else 0
    return round(total / 2**30) if total > 0 else None


def machine_profile(driver=None, openshell=None, create=True):
    """What a fixture result depends on outside the pilot: the host and its
    OpenShell build. No hostname, user name, serial number or MAC address."""
    if sys.platform == "darwin":
        os_name, os_version = "macos", platform.mac_ver()[0]
    else:
        os_name, os_version = platform.system().lower(), platform.release()
    return {"os": os_name, "os_version": os_version or None, "arch": platform.machine() or None,
            "cpu_model": _cpu_model(), "memory_gib": _memory_gib(),
            "openshell_version": openshell or OPENSHELL_VERSION, "vm_driver_sha256": driver_sha256(driver),
            "machine_id": machine_id(create=create)}


def selection_machine(driver=None, openshell=None):
    """The part of the machine profile that selection compares. It never
    creates the machine id."""
    return {"openshell_version": openshell or OPENSHELL_VERSION, "vm_driver_sha256": driver_sha256(driver),
            "machine_id": machine_id(create=False)}


def valid_machine(machine):
    return (type(machine) is dict and set(machine) == set(MACHINE_FIELDS)
            and type(machine["openshell_version"]) is str and machine["openshell_version"] != ""
            and (machine["vm_driver_sha256"] is None or (type(machine["vm_driver_sha256"]) is str
                                                         and SHA256.fullmatch(machine["vm_driver_sha256"])))
            and type(machine["machine_id"]) is str and MACHINE_ID.fullmatch(machine["machine_id"]) is not None)


def read_qualified(path=None):
    path = Path(path or QUALIFIED_FILE)
    if not path.exists():
        return {}
    value = json.loads(path.read_text())
    if type(value) is not dict or value.get("schema") != 1 or type(value.get("profiles")) is not dict:
        raise ValueError("qualified.json: unsupported record")
    return value["profiles"]


def qualification(name, checks, now=None, path=None, machine=None):
    """Whether a task may select this profile, and why not.

    machine is selection_machine() for this machine. A record that carries a
    machine profile selects only with the same OpenShell version and VM driver:
    the sandbox denial checks depend on that driver. A record from another
    machine with the same driver still selects and says so. A record written
    before machine profiles existed selects until it ages out, marked legacy.
    machine None leaves the driver unchecked (a listing with no runtime)."""
    if name not in PROFILES:
        return False, "unsupported inference profile"
    record = read_qualified(path).get(name)
    if type(record) is not dict:
        return False, "not qualified: run the pilot fixture on it (captain openshell qualify)"
    if record.get("identity") != identity(name):
        return False, "qualified for another model or route; qualify it again"
    if sorted(record.get("checks") or []) != sorted(checks):
        return False, "qualified against another check set; qualify it again"
    if record.get("repair_attempts") != QUALIFY_REPAIRS or len(record.get("runs") or []) != QUALIFY_RUNS:
        return False, f"qualified under an older rule; qualify it again ({QUALIFY_RUNS} runs with repair)"
    try:
        at = datetime.datetime.fromisoformat(record["qualified_at"])
    except (KeyError, TypeError, ValueError):
        return False, "qualification has no valid date"
    now = now or datetime.datetime.now(datetime.timezone.utc)
    if at.tzinfo is None or not datetime.timedelta(0) <= now - at <= datetime.timedelta(days=QUALIFY_DAYS):
        return False, f"qualification is older than {QUALIFY_DAYS} days; qualify it again"
    status = "qualified " + at.date().isoformat()
    recorded = record.get("machine")
    if recorded is None:
        return True, status + ", legacy: no machine profile"
    if not valid_machine(recorded):
        return False, "qualification has an invalid machine profile; qualify it again"
    if machine is None:
        return True, status + ", driver not checked"
    if (recorded["openshell_version"] != machine.get("openshell_version")
            or recorded["vm_driver_sha256"] != machine.get("vm_driver_sha256")):
        return False, "qualified with another OpenShell version or VM driver; qualify it again on this machine"
    if recorded["machine_id"] != machine.get("machine_id"):
        return True, status + " on another machine (" + recorded["machine_id"] + ")"
    return True, status


def require_qualified(name, checks, machine=None):
    allowed, reason = qualification(name, checks, machine=machine)
    if not allowed:
        raise ValueError(f"profile {name} cannot run tasks: {reason}")


def record_qualification(name, reports, checks, path=None, evidence=None, now=None):
    """Record a qualification: QUALIFY_RUNS distinct fixture runs, each with
    verdict pass, every check passed, and the repair budget tasks get. Any
    other set records nothing. The reports are kept under results/qualify so
    their digests can be rechecked."""
    if type(reports) is not list or len(reports) != QUALIFY_RUNS:
        raise ValueError(f"a qualification needs exactly {QUALIFY_RUNS} fixture runs")
    blobs = []
    for report in reports:
        results = report.get("checks") or {}
        if report.get("verdict") != "pass" or report.get("inference") != name or sorted(results) != sorted(checks) or any(
                type(results[check]) is not dict or results[check].get("verdict") != "pass" for check in checks):
            raise ValueError("only fixture runs that passed every check qualify a profile")
        if report.get("max_worker_attempts") != 1 + QUALIFY_REPAIRS:
            raise ValueError(f"a qualifying run needs the task repair budget ({QUALIFY_REPAIRS})")
        if not valid_machine(report.get("machine")):
            raise ValueError("a qualifying run needs a machine profile")
        blobs.append((json.dumps(report, indent=2, sort_keys=True) + "\n").encode())
    if len({hashlib.sha256(data).digest() for data in blobs}) != len(blobs):
        raise ValueError("the same fixture run cannot count twice")
    machine = reports[0]["machine"]
    if any({key: report["machine"][key] for key in ("openshell_version", "vm_driver_sha256", "machine_id")}
           != {key: machine[key] for key in ("openshell_version", "vm_driver_sha256", "machine_id")}
           for report in reports):
        raise ValueError("the qualifying runs come from different machines or VM drivers")
    now = now or datetime.datetime.now(datetime.timezone.utc)
    path, evidence = Path(path or QUALIFIED_FILE), Path(evidence or EVIDENCE_DIR)
    evidence.mkdir(parents=True, exist_ok=True)
    runs = []
    for index, data in enumerate(blobs, 1):
        kept = evidence / f"{now.strftime('%Y-%m-%dT%H%M%SZ')}-{name}-{index}.json"
        kept.write_bytes(data)
        runs.append({"report": str(kept.relative_to(path.parent)) if kept.is_relative_to(path.parent) else str(kept),
                     "report_sha256": hashlib.sha256(data).hexdigest()})
    record = {"identity": identity(name), "model": PROFILES[name]["model"], "route": PROFILES[name].get("route"),
              "checks": sorted(checks), "qualified_at": now.isoformat(timespec="seconds"),
              "repair_attempts": QUALIFY_REPAIRS, "runs": runs,
              "compute_driver": reports[0].get("compute_driver"), "openshell": reports[0].get("openshell"),
              "machine": machine}
    descriptor = os.open(path.parent / ".qualified.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o644)
    try:
        fcntl.flock(descriptor, fcntl.LOCK_EX)
        profiles = read_qualified(path)
        profiles[name] = record
        temporary = path.with_suffix(".json.tmp")
        temporary.write_text(json.dumps({"schema": 1, "profiles": dict(sorted(profiles.items()))}, indent=2) + "\n")
        temporary.replace(path)
    finally:
        os.close(descriptor)
    return record


def pin_request(body, name, capped=False):
    selected = profile(name)
    if capped and "ceiling" not in selected:
        raise ValueError("a strict cost cap needs a priced lane")
    if "route" in selected:
        if body.get("model") != selected["model"] or any(key in body for key in ("models", "route")):
            raise ValueError("request does not name the pinned model")
        provider = {
            "only": [selected["route"]], "order": [selected["route"]], "allow_fallbacks": False,
            "data_collection": "deny", "zdr": True,
        }
        if capped:
            # No per-request fee: the reservation prices tokens only.
            provider["max_price"] = {"prompt": selected["ceiling"][0], "completion": selected["ceiling"][1], "request": 0}
        body = dict(body, temperature=0, provider=provider)
        maximum = body.get("max_tokens", selected["output"])
        if type(maximum) is not int or maximum < 1:
            raise ValueError("invalid token limit")
        if "max_completion_tokens" in body:
            raise ValueError("alternate token limit is not supported")
        body["max_tokens"] = min(maximum, selected["output"])
    return body


def reservation(body, max_tokens, name):
    """The most one forwarded request can cost, in USD: prompt tokens bounded by
    the body's bytes plus the template margin, completion tokens by the clamped
    max_tokens, both at the lane's ceilings."""
    prompt, completion = profile(name)["ceiling"]
    return ((len(body) + PROMPT_MARGIN) * prompt + max_tokens * completion) / 1_000_000


def canonical_calls(body):
    """Rename tool-call IDs to call_1, call_2... in order of first use.

    Workers and providers mint random IDs, so two runs with the same history
    would otherwise send different bytes from the second request on. Renaming
    is one-to-one within the request, so each result stays paired with its call.
    """
    if "messages" not in body:
        return body, 0
    if type(body["messages"]) is not list:
        raise ValueError("messages must be a list")
    names = {}

    def rename(value):
        if type(value) is not str:
            raise ValueError("tool call IDs must be strings")
        return names.setdefault(value, f"call_{len(names) + 1}")

    messages = []
    for message in body["messages"]:
        if type(message) is dict and message.get("tool_calls") is not None:
            calls = message["tool_calls"]
            if type(calls) is not list or any(type(call) is not dict for call in calls):
                raise ValueError("tool calls must be a list of objects")
            message = dict(message, tool_calls=[dict(call, id=rename(call["id"])) if "id" in call else call
                                                for call in calls])
        if type(message) is dict and "tool_call_id" in message:
            message = dict(message, tool_call_id=rename(message["tool_call_id"]))
        messages.append(message)
    return dict(body, messages=messages), len(names)


def response_provider(data, name, status):
    selected = profile(name)
    if "served_by" not in selected or not 200 <= status < 300:
        return None
    value = json.loads(data)
    if value.get("model") != selected["model"] or value.get("provider") != selected["served_by"]:
        raise ValueError("response does not confirm the pinned provider and model")
    return selected["served_by"]


def main(argv=None):
    """List every profile and whether a task may select it, or with
    `record <profile> <report.json>...` record a qualification. With
    `--prepared <runtime>`, the listing checks that runtime's VM driver."""
    from pilot import CHECKS
    if argv and argv[0] == "record":
        if len(argv) < 3:
            print("usage: profiles.py record <profile> <report.json>...", file=sys.stderr)
            return 2
        try:
            reports = [json.loads(Path(report).read_text()) for report in argv[2:]]
            record = record_qualification(argv[1], reports, CHECKS)
        except (OSError, ValueError) as error:
            print(f"profile {argv[1]} not qualified: {error}", file=sys.stderr)
            return 1
        print(json.dumps(record, indent=2))
        return 0
    machine = None
    if argv and "--prepared" in argv:
        index = argv.index("--prepared")
        if index + 1 >= len(argv):
            print("usage: profiles.py [--json] [--prepared <runtime>]", file=sys.stderr)
            return 2
        machine = selection_machine(Path(argv[index + 1]) / "bin" / "openshell-driver-vm")
    rows = []
    for name, selected in sorted(PROFILES.items(), key=lambda item: (item[1].get("source") != "kept", item[0])):
        allowed, reason = qualification(name, CHECKS, machine=machine)
        rows.append({"profile": name, "source": selected.get("source"), "model": selected["model"],
                     "route": selected.get("route"), "selectable": allowed, "legacy": "legacy" in reason,
                     "status": reason})
    if argv and "--json" in argv:
        print(json.dumps(rows, indent=2))
    else:
        for row in rows:
            print(f"{'yes' if row['selectable'] else 'no ':3} {row['profile']:44} {row['source']:8} "
                  f"{row['model']:30} {row['route'] or '-':28} {row['status']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
