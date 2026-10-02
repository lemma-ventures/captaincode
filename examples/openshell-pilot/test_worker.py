import argparse
import hashlib
import json
import shutil
import subprocess
import sys
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
                self.result(json.dumps([{"entry": {"path": "slugify.py"}, "text": "def slugify(text):\n"}]).encode()),
                self.result(b"/usr/local/bin/rg\n")]

    def test_tools_are_exercised_through_opencode_without_model_calls(self):
        with patch.object(self.instance, "remote", side_effect=self.tool_results()) as remote:
            self.instance.check_worker_tools()
        self.assertEqual(self.instance.report["checks"]["worker_tools"]["verdict"], "pass")
        self.assertEqual(self.instance.report["worker_attempts"], 0)
        self.assertEqual([call.args[4:7] for call in remote.call_args_list[:2]],
                         [("debug", "rg", "files"), ("debug", "rg", "search")])
        self.assertEqual(remote.call_args_list[2].args, ("sh", "-c", "command -v rg"))
        self.assertEqual((self.instance.state / "worker-files.log").read_bytes(), self.tool_results()[0].stdout)

    def test_missing_search_tool_or_invalid_results_stop_before_worker(self):
        valid = self.tool_results()
        for results in [[self.result(b"ripgrep execution failed", 1), valid[1], valid[2]],
                        [self.result(b"unrelated.py\n"), valid[1], valid[2]],
                        [self.result(b"test_slugify.py\nslugify.py\n"), valid[1], valid[2]],
                        [valid[0], self.result(b"ripgrep execution failed", 1), valid[2]],
                        [valid[0], self.result(b"not JSON"), valid[2]],
                        [valid[0], self.result(b"[]"), valid[2]],
                        [valid[0], self.result(b'[{"entry":{"path":"elsewhere.py"},"text":"def slugify"}]'), valid[2]],
                        [valid[0], valid[1], self.result(b"/usr/bin/rg\n")],
                        [valid[0], valid[1], self.result(b"", 1)]]:
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

    def test_repair_requires_real_failure_and_preserves_first_attempt(self):
        self.instance.repair_attempts = 1
        repo = self.instance.state / "fixture"
        shutil.copytree(pilot.HERE / "fixture", repo)
        self.instance.checkpoint["tests_sha256"] = hashlib.sha256((repo / "test_slugify.py").read_bytes()).hexdigest()
        prompts = []

        def remote(*args, **kwargs):
            if args[0] == "opencode":
                prompts.append(args[-1])
                if len(prompts) == 2:
                    (repo / "slugify.py").write_text("import re\ndef slugify(text):\n    return re.sub(r'[^a-z0-9]+', '-', text.lower()).strip('-')\n")
                return self.result(b'{"type":"text","part":{"text":"claims success"}}\n')
            return subprocess.run([sys.executable, *args[1:]], cwd=repo,
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=kwargs.get("check", True))

        with patch.object(self.instance, "remote", side_effect=remote):
            self.instance.edit_and_verify()
        self.assertEqual(len(prompts), 2)
        self.assertIn("FAILED (failures=3)", prompts[1])
        self.assertEqual([row["verdict"] for row in self.instance.report["attempts"]], ["fail", "pass"])
        self.assertEqual(self.instance.report["worker_attempts"], 2)
        self.assertEqual(self.instance.report["task_successes"], 0)
        self.assertEqual(self.instance.checkpoint["phase"], "worker_started")
        self.assertIn(b"FAILED", (self.instance.state / "sandbox-tests-1.log").read_bytes())
        self.assertIn(b"OK", (self.instance.state / "sandbox-tests-2.log").read_bytes())
        for row in self.instance.report["attempts"]:
            self.assertEqual(row["worker_sha256"], hashlib.sha256((self.instance.state / f'worker-{row["attempt"]}.jsonl').read_bytes()).hexdigest())

    def test_repair_is_opt_in_bounded_and_not_triggered_by_inconclusive_checks(self):
        self.instance.checkpoint["tests_sha256"] = "0" * 64
        for budget, code, calls in [(0, 1, 3), (1, 1, 6), (1, 124, 3), (1, 2, 3)]:
            self.instance.repair_attempts = budget
            results = [self.result(b"claim\n"), self.result(), self.result(b"FAILED", code)] * 2
            with (self.subTest(budget=budget, code=code),
                  patch.object(self.instance, "remote", side_effect=results) as remote,
                  self.assertRaises((AssertionError, RuntimeError))):
                self.instance.edit_and_verify()
            self.assertEqual(remote.call_count, calls)

    def test_changed_tests_do_not_trigger_repair(self):
        self.instance.repair_attempts = 1
        self.instance.checkpoint["tests_sha256"] = "0" * 64
        with (patch.object(self.instance, "remote", side_effect=[self.result(), subprocess.CalledProcessError(1, "hash check")]) as remote,
              self.assertRaises(subprocess.CalledProcessError)):
            self.instance.edit_and_verify()
        self.assertEqual(remote.call_count, 2)
        self.assertEqual(self.instance.report["worker_attempts"], 1)

    def test_exhausted_budget_does_not_restart_a_fresh_deadline(self):
        self.instance.repair_attempts = 1
        self.instance.checkpoint["tests_sha256"] = "0" * 64
        with (patch.object(self.instance, "remote", side_effect=[self.result(), self.result(), self.result(b"FAILED", 1)]) as remote,
              patch.object(pilot.time, "monotonic", side_effect=[0, 0, 0, 600, 601, 601]),
              self.assertRaisesRegex(RuntimeError, "deadline exhausted")):
            self.instance.edit_and_verify()
        self.assertEqual(remote.call_count, 3)
        self.assertEqual(self.instance.report["worker_attempts"], 1)

    def test_repair_budget_is_pinned_across_recovery(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            first = pilot.Pilot(state, runtime="vm", repair_attempts=1)
            first.save()
            self.assertEqual(pilot.Pilot(state).repair_attempts, 1)
            with self.assertRaisesRegex(RuntimeError, "repair budget"):
                pilot.Pilot(state, repair_attempts=0)
        for value in [-1, 2, True, "1"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                pilot.Pilot(self.instance.state, repair_attempts=value)
            self.instance.checkpoint["repair_attempts"] = value
            self.instance.save()
            with self.subTest(saved=value), self.assertRaisesRegex(ValueError, "checkpoint repair budget"):
                pilot.Pilot(self.instance.state)


if __name__ == "__main__":
    unittest.main()
