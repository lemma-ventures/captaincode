import argparse
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pilot


class WorkerTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.instance = pilot.Pilot(Path(self.directory.name), runtime="vm")

    def result(self, output=b"", code=0):
        return subprocess.CompletedProcess([], code, output)

    def tool_results(self):
        return [self.result(b"slugify.py\ntest_slugify.py\n"),
                self.result(json.dumps([{"entry": {"path": "slugify.py"}, "text": "def slugify(text):\n"}]).encode())]

    def test_tools_are_exercised_through_opencode_without_model_calls(self):
        with patch.object(self.instance, "remote", side_effect=self.tool_results()) as remote:
            self.instance.check_worker_tools()
        self.assertEqual(self.instance.report["checks"]["worker_tools"]["verdict"], "pass")
        self.assertEqual(self.instance.report["worker_attempts"], 0)
        self.assertEqual([call.args[4:7] for call in remote.call_args_list],
                         [("debug", "rg", "files"), ("debug", "rg", "search")])
        self.assertEqual((self.instance.state / "worker-files.log").read_bytes(), self.tool_results()[0].stdout)

    def test_missing_search_tool_or_invalid_results_stop_before_worker(self):
        valid = self.tool_results()
        for results in [[self.result(b"ripgrep execution failed", 1), valid[1]],
                        [self.result(b"unrelated.py\n"), valid[1]],
                        [valid[0], self.result(b"ripgrep execution failed", 1)],
                        [valid[0], self.result(b"not JSON")],
                        [valid[0], self.result(b"[]")],
                        [valid[0], self.result(b'[{"entry":{"path":"elsewhere.py"},"text":"def slugify"}]')]]:
            with (self.subTest(results=results), patch.object(self.instance, "remote", side_effect=results),
                  self.assertRaisesRegex(AssertionError, "worker_tools")):
                self.instance.check_worker_tools()
            self.assertEqual(self.instance.report["worker_attempts"], 0)
            self.assertEqual(self.instance.report["checks"]["worker_tools"]["verdict"], "fail")

    def test_controller_does_not_submit_worker_after_tool_gate_failure(self):
        args = argparse.Namespace(state=self.instance.state, runtime="vm", resume=False)
        with (patch.object(pilot, "Pilot", return_value=self.instance),
              patch.object(self.instance, "preflight"), patch.object(self.instance, "start_services"),
              patch.object(self.instance, "prepare_snapshot"), patch.object(self.instance, "create_sandbox"),
              patch.object(self.instance, "acceptance_checks", side_effect=AssertionError("worker_tools")),
              patch.object(self.instance, "run_worker") as worker):
            instance, resume = pilot.run_controller(args)
        worker.assert_not_called()
        self.assertFalse(resume)
        self.assertEqual(instance.report["verdict"], "fail")

    def test_worker_deadlines_are_inconclusive_and_preserve_output(self):
        output = b'{"type":"tool_use"}\n'
        for deadline in [self.result(output, 124), subprocess.TimeoutExpired([], 615, output=output)]:
            with self.subTest(deadline=deadline), patch.object(self.instance, "remote") as remote:
                if isinstance(deadline, Exception):
                    remote.side_effect = deadline
                else:
                    remote.return_value = deadline
                with self.assertRaisesRegex(RuntimeError, "deadline"):
                    self.instance.run_worker()
                remote.assert_called_once()
                self.assertEqual(self.instance.report["checks"]["worker_exit"]["verdict"], "inconclusive")
                self.assertIn("worker", self.instance.report["timings_seconds"])
                self.assertEqual((self.instance.state / "worker.jsonl").read_bytes(), output)
                self.assertEqual(self.instance.report["task_successes"], 0)
                self.assertEqual(self.instance.checkpoint["phase"], "worker_started")

    def test_observed_worker_error_remains_failure(self):
        with (patch.object(self.instance, "remote", return_value=self.result(b"failed", 1)),
              self.assertRaisesRegex(AssertionError, "worker_exit")):
            self.instance.run_worker()
        self.assertEqual(self.instance.report["checks"]["worker_exit"]["verdict"], "fail")


if __name__ == "__main__":
    unittest.main()
