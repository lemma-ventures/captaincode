import contextlib
import datetime
import getpass
import hashlib
import io
import json
import os
import socket
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pilot
import profiles
from profiles import (
    CEILINGS,
    LANES,
    PROFILES,
    PROMPT_MARGIN,
    canonical_calls,
    pin_request,
    profile,
    reservation,
    response_provider,
)


class ProfileTests(unittest.TestCase):
    def test_provider_controls_cannot_be_weakened_by_worker(self):
        request = {"model": profile("cerebras")["model"], "temperature": 1,
                   "provider": {"allow_fallbacks": True, "order": ["other"], "zdr": False}}
        pinned = pin_request(request, "cerebras")
        self.assertEqual(pinned["provider"], {"only": ["cerebras"], "order": ["cerebras"],
                         "allow_fallbacks": False, "zdr": True, "data_collection": "deny"})
        self.assertEqual(pinned["temperature"], 0)
        self.assertEqual(request["temperature"], 1)
        self.assertEqual(pinned["max_tokens"], 16384)
        self.assertEqual(pin_request(dict(request, max_tokens=999999), "cerebras")["max_tokens"], 16384)
        for limit in [0, -1, True, "100"]:
            with self.subTest(limit=limit), self.assertRaises(ValueError):
                pin_request(dict(request, max_tokens=limit), "cerebras")
        with self.assertRaises(ValueError):
            pin_request(dict(request, max_completion_tokens=999999), "cerebras")

    def test_tool_call_ids_are_renamed_in_first_use_order(self):
        def history(first, second):
            return {"model": "m", "messages": [
                {"role": "user", "content": "go"},
                {"role": "assistant", "content": None, "tool_calls": [
                    {"id": first, "type": "function", "function": {"name": "glob", "arguments": "{}"}},
                    {"id": second, "type": "function", "function": {"name": "read", "arguments": "{}"}}]},
                {"role": "tool", "tool_call_id": second, "content": "b"},
                {"role": "tool", "tool_call_id": first, "content": "a"}]}
        original = history("call_x9Qa", "toolu_77")
        renamed, count = canonical_calls(original)
        self.assertEqual(count, 2)
        self.assertEqual(json.dumps(renamed), json.dumps(canonical_calls(history("call_2", "call_1"))[0]))
        self.assertEqual([call["id"] for call in renamed["messages"][1]["tool_calls"]], ["call_1", "call_2"])
        self.assertEqual([message["tool_call_id"] for message in renamed["messages"][2:]], ["call_2", "call_1"])
        self.assertEqual(original["messages"][1]["tool_calls"][0]["id"], "call_x9Qa")
        self.assertEqual(canonical_calls(renamed), (renamed, 2))

    def test_reused_call_ids_stay_equal_and_bodies_without_calls_are_unchanged(self):
        call = {"id": "functions.read:0", "type": "function", "function": {"name": "read", "arguments": "{}"}}
        reused = {"messages": [{"role": "assistant", "tool_calls": [call]},
                               {"role": "tool", "tool_call_id": "functions.read:0", "content": "a"},
                               {"role": "assistant", "tool_calls": [call]},
                               {"role": "tool", "tool_call_id": "functions.read:0", "content": "b"}]}
        renamed, count = canonical_calls(reused)
        self.assertEqual(count, 1)
        self.assertEqual({message.get("tool_call_id") or message["tool_calls"][0]["id"]
                          for message in renamed["messages"]}, {"call_1"})
        plain = {"model": "m", "messages": [{"role": "user", "content": "go"},
                                            {"role": "assistant", "content": "done", "tool_calls": None}]}
        self.assertEqual(canonical_calls(plain), (plain, 0))
        self.assertEqual(canonical_calls({"model": "m"}), ({"model": "m"}, 0))

    def test_malformed_tool_calls_are_refused(self):
        for messages in [{"role": "user"}, [{"role": "assistant", "tool_calls": {"id": "a"}}],
                         [{"role": "assistant", "tool_calls": ["a"]}],
                         [{"role": "assistant", "tool_calls": [{"id": 7, "type": "function"}]}],
                         [{"role": "assistant", "tool_calls": [{"id": None, "type": "function"}]}],
                         [{"role": "tool", "tool_call_id": ["a"], "content": "x"}]]:
            with self.subTest(messages=messages), self.assertRaises(ValueError):
                canonical_calls({"model": "m", "messages": messages})

    def test_alternate_model_paths_and_unconfirmed_serving_tuple_are_refused(self):
        for request in [{"model": "other"}, {"model": profile("cerebras")["model"], "models": []},
                        {"model": profile("cerebras")["model"], "route": "fallback"}]:
            with self.subTest(request=request), self.assertRaises(ValueError):
                pin_request(request, "cerebras")
        for response in [{}, {"model": profile("cerebras")["model"], "provider": "Other"},
                         {"model": "other", "provider": "Cerebras"}]:
            with self.subTest(response=response), self.assertRaises(ValueError):
                response_provider(json.dumps(response), "cerebras", 200)
        self.assertEqual(response_provider(json.dumps({"model": profile("cerebras")["model"],
                         "provider": "Cerebras"}), "cerebras", 200), "Cerebras")
        self.assertIsNone(response_provider(b'{"error":{}}', "cerebras", 503))

    def test_each_lane_pins_one_provider_behind_the_same_openshell_endpoint(self):
        lanes = {name: profile(name) for name in profiles.KEPT if name != "nim"}
        self.assertEqual({lane["route"] for lane in lanes.values()},
                         {"cerebras", "sambanova", "together", "deepinfra/bf16", "crusoe/bf16", "parasail/fp4"})
        for name, lane in lanes.items():
            with self.subTest(lane=name):
                self.assertEqual([lane[key] for key in ("model", "host", "key_env", "file", "type")],
                                 ["openai/gpt-oss-120b", "openrouter.ai", "OPENROUTER_API_KEY",
                                  "openrouter.yaml", "captain-openrouter-pilot"])
                pinned = pin_request({"model": lane["model"], "provider": {"only": ["cerebras", "groq"]}}, name)
                self.assertEqual(pinned["provider"], {"only": [lane["route"]], "order": [lane["route"]],
                                 "allow_fallbacks": False, "zdr": True, "data_collection": "deny"})
                served = json.dumps({"model": lane["model"], "provider": lane["served_by"]})
                self.assertEqual(response_provider(served, name, 200), lane["served_by"])
                for other in lanes.values():
                    if other["served_by"] != lane["served_by"]:
                        with self.assertRaises(ValueError):
                            response_provider(json.dumps({"model": lane["model"], "provider": other["served_by"]}),
                                              name, 200)
        self.assertEqual(pin_request({"model": "any", "temperature": 1}, "nim"), {"model": "any", "temperature": 1})
        self.assertIsNone(response_provider(b"not json", "nim", 200))
        with self.assertRaises(ValueError):
            pin_request({}, "unlisted")

    def test_strict_cap_pins_price_ceilings_and_reserves_the_worst_case(self):
        self.assertEqual(CEILINGS.keys(), LANES.keys())
        request = {"model": profile("cerebras")["model"], "max_tokens": 1024,
                   "provider": {"max_price": {"prompt": 99, "completion": 99}}}
        uncapped = pin_request(request, "cerebras")
        self.assertNotIn("max_price", uncapped["provider"])
        capped = pin_request(request, "cerebras", capped=True)
        self.assertEqual(capped["provider"], dict(uncapped["provider"], max_price={"prompt": 0.45, "completion": 0.95, "request": 0}))
        self.assertEqual(json.dumps(pin_request(request, "cerebras")), json.dumps(uncapped))
        with self.assertRaisesRegex(ValueError, "priced lane"):
            pin_request({"model": "any"}, "nim", capped=True)
        body = b"x" * 1000
        self.assertAlmostEqual(reservation(body, 1024, "cerebras"), ((1000 + PROMPT_MARGIN) * 0.45 + 1024 * 0.95) / 1e6)
        # A live 2 October probe: a 295-byte body capped at 32 completion
        # tokens came back as 74 prompt and 32 completion tokens, $0.0000499.
        self.assertGreater(reservation(b"x" * 295, 32, "cerebras"), 4.99e-05)
        self.assertGreaterEqual(PROMPT_MARGIN + 295, 74)

    def test_strict_cap_is_pinned_with_the_checkpoint_and_needs_a_priced_lane(self):
        with tempfile.TemporaryDirectory() as temporary:
            state = Path(temporary) / "capped"
            instance = pilot.Pilot(state, runtime="vm", inference="cerebras", max_cost_usd=0.25)
            instance.save()
            self.assertEqual(json.loads((state / "checkpoint.json").read_text())["max_cost_usd"], 0.25)
            self.assertEqual((instance.max_cost_usd, instance.report["cost_limit_usd"]), (0.25, 0.25))
            self.assertEqual(pilot.Pilot(state).max_cost_usd, 0.25)
            with self.assertRaisesRegex(RuntimeError, "cannot change cost cap"):
                pilot.Pilot(state, max_cost_usd=0.5)
            plain = pilot.Pilot(Path(temporary) / "plain", runtime="vm", inference="cerebras")
            self.assertIsNone(plain.max_cost_usd)
            self.assertNotIn("max_cost_usd", plain.checkpoint)
            with self.assertRaisesRegex(RuntimeError, "cannot change cost cap"):
                pilot.Pilot(plain.state, max_cost_usd=0.25)
            with self.assertRaisesRegex(ValueError, "priced profile"):
                pilot.Pilot(Path(temporary) / "nim", runtime="vm", inference="nim", max_cost_usd=0.25)
            for value in [-1, 1000, float("nan"), True, "0.25"]:
                with self.subTest(value=value), self.assertRaises(ValueError):
                    pilot.Pilot(Path(temporary) / "bad", runtime="vm", inference="cerebras", max_cost_usd=value)
            args = pilot.arguments().parse_args(["--state", str(state), "--max-cost-usd", "0.25"])
            self.assertEqual(args.max_cost_usd, 0.25)

    def test_lane_sandbox_uses_the_shared_provider_type_without_exposing_the_key(self):
        with tempfile.TemporaryDirectory() as temporary:
            instance = pilot.Pilot(Path(temporary), runtime="vm", inference="sambanova")
            failed = subprocess.CompletedProcess([], 1, b"")
            with (patch.dict(os.environ, {"OPENROUTER_API_KEY": "test-placeholder"}),
                  patch.object(instance, "cli_run", return_value=failed) as cli,
                  self.assertRaisesRegex(RuntimeError, "sandbox creation failed")):
                instance.create_sandbox()
            calls = [call.args for call in cli.call_args_list]
            self.assertEqual(calls[0][-1], pilot.HERE / "openrouter.yaml")
            self.assertEqual(calls[1][calls[1].index("--type") + 1], "captain-openrouter-pilot")
            self.assertEqual(cli.call_args_list[1].kwargs["env"]["OPENROUTER_API_KEY"], "test-placeholder")
            self.assertFalse(any("test-placeholder" in str(arg) for args in calls for arg in args))
            policy = (instance.state / "policy.yaml").read_text()
            self.assertIn("openrouter.ai", policy)
            self.assertNotIn("integrate.api.nvidia.com", policy)

    def test_resume_pins_profile_and_snapshot_has_only_placeholder_credentials(self):
        with tempfile.TemporaryDirectory() as temporary:
            state = Path(temporary)
            instance = pilot.Pilot(state, runtime="vm", inference="cerebras")
            instance.prepare_snapshot()
            config = json.loads((state / "payload/opencode.json").read_text())
            self.assertEqual(config["model"], "pilot/openai/gpt-oss-120b")
            self.assertEqual(config["provider"]["pilot"]["options"], {
                "baseURL": "https://openrouter.ai/api/v1", "apiKey": "{env:OPENROUTER_API_KEY}"})
            restored = pilot.Pilot(state)
            self.assertEqual(restored.inference, "cerebras")
            self.assertEqual(restored.runtime, "vm")
            before = (state / "checkpoint.json").read_bytes()
            with self.assertRaisesRegex(RuntimeError, "cannot change inference"):
                pilot.Pilot(state, inference="nim")
            self.assertEqual((state / "checkpoint.json").read_bytes(), before)


def catalog_entry(**overrides):
    entry = {"leg": "glm", "model": "z-ai/glm-5.3", "route": "z-ai/fp8", "served_by": "Z.AI",
             "output": 16384, "context": 1048576, "ceiling": [0.75, 2.5]}
    entry.update(overrides)
    return entry


DRIVER = "d" * 64
MACHINE_A = "0123456789abcdef"


def machine(driver=DRIVER, machine_id=MACHINE_A, openshell="0.1.2"):
    return {"os": "macos", "os_version": "15.6", "arch": "arm64", "cpu_model": "Apple M3 Max", "memory_gib": 128,
            "openshell_version": openshell, "vm_driver_sha256": driver, "machine_id": machine_id}


def selecting(driver=DRIVER, machine_id=MACHINE_A, openshell="0.1.2"):
    return {"openshell_version": openshell, "vm_driver_sha256": driver, "machine_id": machine_id}


def passing_report(name, run=1):
    return {"verdict": "pass", "inference": name, "openshell": "0.1.2", "compute_driver": "vm", "run": run,
            "max_worker_attempts": 1 + profiles.QUALIFY_REPAIRS, "machine": machine(),
            "checks": {check: {"verdict": "pass", "detail": ""} for check in pilot.CHECKS}}


def passing_runs(name):
    return [passing_report(name, run) for run in range(1, profiles.QUALIFY_RUNS + 1)]


class CatalogTests(unittest.TestCase):
    def write(self, temporary, profiles_, schema=1):
        path = Path(temporary) / "catalog.json"
        path.write_text(json.dumps({"schema": schema, "profiles": profiles_}))
        return path

    def test_kept_profiles_stay(self):
        self.assertEqual(profiles.KEPT["nim"]["model"], "z-ai/glm-5.3-flash")
        self.assertEqual(set(LANES) | {"nim"}, set(profiles.KEPT))
        for name in profiles.KEPT:
            self.assertEqual(PROFILES[name], profiles.KEPT[name])
            self.assertEqual(PROFILES[name]["source"], "kept")

    def test_generated_profiles_reach_only_openrouter(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = self.write(temporary, {"glm-z-ai-fp8": catalog_entry(host="evil.example", key_env="NVIDIA_API_KEY",
                                                                         base_path="/x", file="nvidia.yaml")})
            loaded = profiles.load_catalog(path)["glm-z-ai-fp8"]
        self.assertEqual((loaded["host"], loaded["base_path"], loaded["key_env"], loaded["file"], loaded["type"]),
                         ("openrouter.ai", "/api/v1", "OPENROUTER_API_KEY", "openrouter.yaml", "captain-openrouter-pilot"))
        self.assertEqual((loaded["model"], loaded["route"], loaded["served_by"], loaded["ceiling"], loaded["context"]),
                         ("z-ai/glm-5.3", "z-ai/fp8", "Z.AI", (0.75, 2.5), 1048576))
        self.assertEqual(loaded["source"], "registry")

    def test_malformed_catalog_fails_closed(self):
        bad = [{"Bad Name": catalog_entry()}, {"x": catalog_entry(route="z-ai/fp8?x=1")},
               {"x": catalog_entry(model="../etc")}, {"x": catalog_entry(output=999999)},
               {"x": catalog_entry(ceiling=[float("nan"), 1])}, {"x": catalog_entry(ceiling=[1])},
               {"x": catalog_entry(served_by="")}, {"x": catalog_entry(context=1024)},
               {"x": catalog_entry(tier="max")}, {"x": "not an object"}]
        with tempfile.TemporaryDirectory() as temporary:
            for entries in bad:
                with self.subTest(entries=entries), self.assertRaises(ValueError):
                    profiles.load_catalog(self.write(temporary, entries))
            with self.assertRaises(ValueError):
                profiles.load_catalog(self.write(temporary, {}, schema=2))
            self.assertEqual(profiles.load_catalog(Path(temporary) / "missing.json"), {})

    def test_kept_lanes_are_not_duplicated(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = self.write(temporary, {
                "cerebras": catalog_entry(),
                "gpt-oss-cerebras-fp16": catalog_entry(leg="gpt-oss", model="openai/gpt-oss-120b", route="cerebras/fp16"),
                "gpt-oss-deepinfra-bf16": catalog_entry(leg="gpt-oss", model="openai/gpt-oss-120b", route="deepinfra/bf16"),
                "gpt-oss-deepinfra-turbo": catalog_entry(leg="gpt-oss", model="openai/gpt-oss-120b", route="deepinfra/turbo"),
                "gpt-oss-groq": catalog_entry(leg="gpt-oss", model="openai/gpt-oss-120b", route="groq")})
            self.assertEqual(set(profiles.load_catalog(path)), {"gpt-oss-deepinfra-turbo", "gpt-oss-groq"})


class QualificationTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        for name, value in [("QUALIFIED_FILE", self.root / "qualified.json"), ("EVIDENCE_DIR", self.root / "results/qualify"),
                            ("MACHINE_ID_FILE", self.root / "home/.captaincode/machine-id")]:
            patcher = patch.object(profiles, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_unqualified_profiles_cannot_run_tasks(self):
        allowed, reason = profiles.qualification("cerebras", pilot.CHECKS)
        self.assertFalse(allowed)
        self.assertIn("not qualified", reason)
        with self.assertRaisesRegex(ValueError, "profile cerebras cannot run tasks"):
            profiles.require_qualified("cerebras", pilot.CHECKS)
        self.assertFalse(profiles.qualification("unknown", pilot.CHECKS)[0])

    def test_only_three_full_passes_with_the_task_repair_qualify(self):
        partial = passing_report("cerebras", 3)
        partial["checks"]["network_denied"]["verdict"] = "inconclusive"
        missing = passing_report("cerebras", 3)
        del missing["checks"]["diff_landed"]
        no_repair = dict(passing_report("cerebras", 3), max_worker_attempts=1)
        first_two = passing_runs("cerebras")[:2]
        for third in [dict(passing_report("cerebras", 3), verdict="fail"), partial, missing, no_repair,
                      passing_report("sambanova", 3), passing_report("cerebras", 1)]:
            with self.subTest(third=third), self.assertRaises(ValueError):
                profiles.record_qualification("cerebras", first_two + [third], pilot.CHECKS)
        for runs in [first_two, passing_runs("cerebras") + [passing_report("cerebras", 4)], passing_report("cerebras")]:
            with self.subTest(runs=runs), self.assertRaisesRegex(ValueError, "exactly 3"):
                profiles.record_qualification("cerebras", runs, pilot.CHECKS)
        self.assertFalse(profiles.QUALIFIED_FILE.exists())

    def test_a_recorded_qualification_holds_until_it_ages_or_the_profile_changes(self):
        now = datetime.datetime(2026, 10, 2, 12, tzinfo=datetime.timezone.utc)
        record = profiles.record_qualification("cerebras", passing_runs("cerebras"), pilot.CHECKS, now=now)
        self.assertEqual(len(record["runs"]), 3)
        for run in record["runs"]:
            kept = self.root / run["report"]
            self.assertEqual(run["report_sha256"], hashlib.sha256(kept.read_bytes()).hexdigest())
        self.assertEqual(record["identity"], profiles.identity("cerebras"))
        self.assertEqual(record["repair_attempts"], 1)
        self.assertTrue(profiles.qualification("cerebras", pilot.CHECKS, now=now + datetime.timedelta(days=30))[0])
        self.assertFalse(profiles.qualification("sambanova", pilot.CHECKS, now=now)[0])
        stale = profiles.qualification("cerebras", pilot.CHECKS, now=now + datetime.timedelta(days=31))
        self.assertEqual(stale, (False, "qualification is older than 30 days; qualify it again"))
        self.assertIn("another check set", profiles.qualification("cerebras", pilot.CHECKS[:-1], now=now)[1])
        moved = dict(PROFILES, cerebras=dict(PROFILES["cerebras"], route="cerebras/fp8"))
        with patch.object(profiles, "PROFILES", moved):
            self.assertIn("another model or route", profiles.qualification("cerebras", pilot.CHECKS, now=now)[1])
        repriced = dict(PROFILES, cerebras=dict(PROFILES["cerebras"], ceiling=(9, 9)))
        with patch.object(profiles, "PROFILES", repriced):
            self.assertTrue(profiles.qualification("cerebras", pilot.CHECKS, now=now)[0])

    def test_machine_profile_has_the_fields_and_no_host_or_user_name(self):
        driver = self.root / "openshell-driver-vm"
        driver.write_bytes(b"driver")
        profile_ = profiles.machine_profile(driver)
        self.assertEqual(set(profile_), set(profiles.MACHINE_FIELDS))
        self.assertEqual(profile_["vm_driver_sha256"], hashlib.sha256(b"driver").hexdigest())
        self.assertEqual(profile_["openshell_version"], profiles.OPENSHELL_VERSION)
        self.assertTrue(profiles.valid_machine(profile_))
        self.assertIsNone(profiles.machine_profile(None)["vm_driver_sha256"])
        report = pilot.Pilot(self.root / "fixture", runtime="vm", inference="cerebras").report
        report["machine"] = profile_
        text = json.dumps(report)
        for private in [socket.gethostname(), socket.gethostname().split(".")[0], getpass.getuser()]:
            if len(private) > 2:
                self.assertNotIn(private, text)

    def test_machine_id_is_random_private_and_made_once(self):
        self.assertIsNone(profiles.machine_id(create=False))
        self.assertIsNone(profiles.selection_machine()["machine_id"])
        self.assertFalse(profiles.MACHINE_ID_FILE.exists())
        first = profiles.machine_id()
        self.assertRegex(first, r"^[0-9a-f]{16}$")
        self.assertEqual(stat.S_IMODE(profiles.MACHINE_ID_FILE.stat().st_mode), 0o600)
        self.assertEqual(profiles.machine_id(), first)
        self.assertEqual(profiles.machine_id(create=False), first)
        profiles.MACHINE_ID_FILE.write_text("not-an-id\n")
        with self.assertRaisesRegex(ValueError, "not a machine id"):
            profiles.machine_id()

    def test_selection_needs_the_same_driver_and_openshell_version(self):
        now = datetime.datetime(2026, 10, 7, 12, tzinfo=datetime.timezone.utc)
        record = profiles.record_qualification("cerebras", passing_runs("cerebras"), pilot.CHECKS, now=now)
        self.assertEqual(record["machine"], machine())
        same = profiles.qualification("cerebras", pilot.CHECKS, now=now, machine=selecting())
        self.assertEqual(same, (True, "qualified 2026-10-07"))
        other = profiles.qualification("cerebras", pilot.CHECKS, now=now, machine=selecting(machine_id="fedcba9876543210"))
        self.assertEqual(other, (True, "qualified 2026-10-07 on another machine (0123456789abcdef)"))
        self.assertTrue(profiles.qualification("cerebras", pilot.CHECKS, now=now, machine=selecting(machine_id=None))[0])
        for mismatch in [selecting(driver="e" * 64), selecting(driver=None), selecting(openshell="0.1.3")]:
            with self.subTest(mismatch=mismatch):
                allowed, reason = profiles.qualification("cerebras", pilot.CHECKS, now=now, machine=mismatch)
                self.assertFalse(allowed)
                self.assertIn("another OpenShell version or VM driver", reason)
                with self.assertRaisesRegex(ValueError, "cannot run tasks"):
                    profiles.require_qualified("cerebras", pilot.CHECKS, machine=mismatch)
        self.assertIn("driver not checked", profiles.qualification("cerebras", pilot.CHECKS, now=now)[1])

    def test_a_record_without_a_machine_profile_is_legacy_until_it_ages_out(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        record = profiles.record_qualification("cerebras", passing_runs("cerebras"), pilot.CHECKS, now=now)
        del record["machine"]
        profiles.QUALIFIED_FILE.write_text(json.dumps({"schema": 1, "profiles": {"cerebras": record}}))
        allowed, reason = profiles.qualification("cerebras", pilot.CHECKS, now=now, machine=selecting(driver="e" * 64))
        self.assertTrue(allowed)
        self.assertIn("legacy", reason)
        expired = profiles.qualification("cerebras", pilot.CHECKS, now=now + datetime.timedelta(days=31),
                                         machine=selecting())
        self.assertFalse(expired[0])
        with contextlib.redirect_stdout(io.StringIO()) as listing:
            self.assertEqual(profiles.main(["--json"]), 0)
        row = next(row for row in json.loads(listing.getvalue()) if row["profile"] == "cerebras")
        self.assertTrue(row["selectable"])
        self.assertTrue(row["legacy"])

    def test_runs_without_one_machine_profile_do_not_qualify(self):
        no_profile = passing_runs("cerebras")
        del no_profile[2]["machine"]
        other_driver = passing_runs("cerebras")
        other_driver[1]["machine"] = machine(driver="e" * 64)
        other_machine = passing_runs("cerebras")
        other_machine[0]["machine"] = machine(machine_id="fedcba9876543210")
        bad_id = passing_runs("cerebras")
        for report in bad_id:
            report["machine"] = machine(machine_id="host-name")
        for runs, message in [(no_profile, "machine profile"), (other_driver, "different machines"),
                              (other_machine, "different machines"), (bad_id, "machine profile")]:
            with self.subTest(message=message), self.assertRaisesRegex(ValueError, message):
                profiles.record_qualification("cerebras", runs, pilot.CHECKS)
        self.assertFalse(profiles.QUALIFIED_FILE.exists())

    def test_task_mode_selects_only_with_the_qualified_driver(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        runs = passing_runs("cerebras")
        driver_bytes = b"qualified driver"
        for report in runs:
            report["machine"] = machine(driver=hashlib.sha256(driver_bytes).hexdigest())
        profiles.record_qualification("cerebras", runs, pilot.CHECKS, now=now)

        class Gated(pilot.Pilot):
            task_mode = True
        for name, content, allowed in [("same", driver_bytes, True), ("other", b"another driver", False)]:
            with self.subTest(driver=name):
                state = self.root / name
                (state / "bin").mkdir(parents=True)
                (state / "bin/openshell-driver-vm").write_bytes(content)
                if allowed:
                    Gated(state, runtime="vm", inference="cerebras")
                    self.assertTrue((state / "checkpoint.json").exists())
                else:
                    with self.assertRaisesRegex(ValueError, "another OpenShell version or VM driver"):
                        Gated(state, runtime="vm", inference="cerebras")
                    self.assertFalse((state / "checkpoint.json").exists())
        self.assertFalse(profiles.MACHINE_ID_FILE.exists(), "selection must not create the machine id")

    def test_a_single_run_record_from_the_older_rule_lapses(self):
        now = datetime.datetime(2026, 10, 2, 12, tzinfo=datetime.timezone.utc)
        profiles.QUALIFIED_FILE.write_text(json.dumps({"schema": 1, "profiles": {"cerebras": {
            "identity": profiles.identity("cerebras"), "checks": sorted(pilot.CHECKS),
            "qualified_at": now.isoformat(), "report": "results/qualify/x.json", "report_sha256": "0" * 64}}}))
        allowed, reason = profiles.qualification("cerebras", pilot.CHECKS, now=now)
        self.assertFalse(allowed)
        self.assertIn("older rule", reason)

    def test_record_command_counts_three_reports(self):
        paths = []
        for report in passing_runs("cerebras"):
            path = self.root / f"report-{report['run']}.json"
            path.write_text(json.dumps(report))
            paths.append(str(path))
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(profiles.main(["record", "cerebras"] + paths[:2]), 1)
            self.assertFalse(profiles.qualification("cerebras", pilot.CHECKS)[0])
            self.assertEqual(profiles.main(["record", "cerebras"] + paths), 0)
        self.assertTrue(profiles.qualification("cerebras", pilot.CHECKS)[0])

    def test_task_mode_refuses_an_unqualified_profile_before_writing_state(self):
        class Gated(pilot.Pilot):
            task_mode = True
        state = self.root / "state"
        with self.assertRaisesRegex(ValueError, "cannot run tasks"):
            Gated(state, runtime="vm", inference="cerebras")
        self.assertFalse((state / "checkpoint.json").exists())
        pilot.Pilot(self.root / "fixture", runtime="vm", inference="cerebras")

    def test_fixture_run_with_qualify_gets_the_task_repair_and_records_nothing(self):
        repairs = []

        def passed(instance):
            repairs.append(instance.repair_attempts)
            instance.report.update(passing_report("cerebras"))
        args = pilot.arguments().parse_args(["--state", str(self.root / "fixture"), "--runtime", "vm",
                                             "--profile", "cerebras", "--qualify"])
        steps = ["preflight", "start_services", "prepare_snapshot", "create_sandbox", "acceptance_checks", "cleanup"]
        with contextlib_exit_stack(steps) as _, patch.object(pilot.Pilot, "run_worker", autospec=True, side_effect=passed), \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(pilot.main(args), 0)
        self.assertEqual(repairs, [profiles.QUALIFY_REPAIRS])
        self.assertFalse(profiles.QUALIFIED_FILE.exists())

    def test_qualify_refuses_another_repair_budget(self):
        args = pilot.arguments().parse_args(["--state", str(self.root / "s"), "--qualify", "--repair-attempts", "0"])
        with self.assertRaisesRegex(RuntimeError, "task repair budget"):
            pilot.run_controller(args, build=lambda a: self.fail("built a pilot"))

    def test_qualify_needs_a_fresh_fixture_state(self):
        args = pilot.arguments().parse_args(["--state", str(self.root / "s"), "--resume", "--qualify"])
        with self.assertRaisesRegex(RuntimeError, "fresh state"):
            pilot.run_controller(args, build=lambda a: pilot.Pilot(a.state, runtime="vm", inference="cerebras"))


def contextlib_exit_stack(names):
    stack = contextlib.ExitStack()
    for name in names:
        stack.enter_context(patch.object(pilot.Pilot, name, autospec=True))
    return stack


if __name__ == "__main__":
    unittest.main()
