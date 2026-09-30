import json
import tempfile
import unittest
from pathlib import Path

import pilot
from profiles import pin_request, profile, response_provider


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
