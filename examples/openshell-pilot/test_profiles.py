import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pilot
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
        lanes = {name: profile(name) for name in PROFILES if name != "nim"}
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


if __name__ == "__main__":
    unittest.main()
