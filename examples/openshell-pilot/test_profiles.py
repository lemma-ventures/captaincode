import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pilot
from profiles import PROFILES, pin_request, profile, response_provider


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
