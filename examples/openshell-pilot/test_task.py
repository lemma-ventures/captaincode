import argparse
import contextlib
import hashlib
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pilot
import profiles
import task

BROKEN = "def double(value):\n    return value + 1\n"
FIXED = "def double(value):\n    return value * 2\n"
TESTS = ("import unittest\nfrom calc import double\n\n\nclass DoubleTests(unittest.TestCase):\n"
         "    def test_double(self):\n        self.assertEqual(double(3), 6)\n")


def git(repo, *args, env=None):
    return subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false",
                           "-c", "core.hooksPath=/dev/null", *args], cwd=repo, env=env, check=True,
                          capture_output=True).stdout.decode().strip()


def make_repo(root, files=None):
    repo = Path(root) / "source"
    repo.mkdir()
    for name, text in (files or {"calc.py": BROKEN, "test_calc.py": TESTS, "notes/readme.md": "calc\n"}).items():
        (repo / name).parent.mkdir(parents=True, exist_ok=True)
        (repo / name).write_text(text)
    git(repo, "init", "-q")
    git(repo, "add", "-A")
    git(repo, "commit", "-qm", "base")
    return repo, git(repo, "rev-parse", "HEAD")


def spec(repo, revision, **overrides):
    value = {"schema": 1, "id": "double-1", "repo": str(repo), "revision": revision, "prompt": "Fix double().",
             "verify": ["python", "-m", "unittest", "-q", "test_calc"], "allowed": ["calc.py"], "protected": ["test_calc.py"]}
    value.update(overrides)
    return value


def result(output=b"", code=0):
    return subprocess.CompletedProcess([], code, output)


class ValidationTests(unittest.TestCase):
    def test_valid_spec_gets_defaults_and_is_idempotent(self):
        value = task.validate_task(spec("/repo", "a" * 40))
        self.assertEqual((value["mode"], value["baseline"], value["deadline_seconds"], value["verify_seconds"]), ("edit", "fail", 600, 120))
        self.assertEqual(task.validate_task(value), value)
        self.assertEqual(task.validate_task(json.loads(json.dumps(value))), value)
        verify = task.validate_task({"schema": 1, "mode": "verify", "id": "run.verify", "repo": "/repo", "revision": "b" * 64,
                                     "verify": ["python", "-m", "unittest"]})
        self.assertEqual((verify["prompt"], verify["allowed"], verify["protected"], verify["baseline"]), ("", [], [], "pass"))
        self.assertEqual(task.validate_task(verify), verify)

    def test_invalid_specs_are_rejected(self):
        base = spec("/repo", "a" * 40)
        cases = [{"extra": 1}, {"schema": 2}, {"mode": "land"}, {"id": ""}, {"id": "-lead"}, {"id": "a/b"}, {"id": "x" * 65},
                 {"repo": "relative"}, {"repo": "/re\0po"}, {"revision": "HEAD"}, {"revision": "a" * 39}, {"revision": "A" * 40},
                 {"prompt": ""}, {"prompt": "x" * 16385}, {"prompt": "a\0b"}, {"verify": []}, {"verify": "python -m unittest"},
                 {"verify": ["python", ""]}, {"verify": ["x"] * 33}, {"verify": ["a\0"]}, {"allowed": []},
                 {"allowed": ["calc.py", "calc.py"]}, {"protected": ["calc.py"]}, {"baseline": "sometimes"},
                 {"deadline_seconds": 59}, {"deadline_seconds": True}, {"verify_seconds": 901}, {"verify_seconds": "60"}]
        for path in ["../calc.py", "/etc/passwd", "a//b", "./calc.py", "a/", ".", ".git/config", "src/.GIT/hooks/pre-commit",
                     "a\\b", "a\nb", "", "x" * 256, 7]:
            cases.append({"allowed": [path]})
        for overrides in cases:
            with self.subTest(overrides=overrides), self.assertRaises(ValueError):
                task.validate_task(dict(base, **overrides))
        for extra in [{"prompt": "change things"}, {"allowed": ["calc.py"]}, {"protected": ["test_calc.py"]}, {"baseline": "fail"}]:
            with self.subTest(verify=extra), self.assertRaises(ValueError):
                task.validate_task({"schema": 1, "mode": "verify", "id": "v", "repo": "/repo", "revision": "a" * 40,
                                    "verify": ["true"], **extra})

    def test_spec_file_is_bounded_and_not_followed_through_links(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "task.json"
            path.write_text(json.dumps(spec("/repo", "a" * 40)))
            self.assertEqual(task.load_task(path)["id"], "double-1")
            link = Path(directory) / "link.json"
            link.symlink_to(path)
            with self.assertRaises(OSError):
                task.load_task(link)
            path.write_text(" " * (task.SPEC_LIMIT + 1))
            with self.assertRaisesRegex(RuntimeError, "bounded"):
                task.load_task(path)


class PatchTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.repo, self.revision = make_repo(self.root)
        self.index_env = dict(os.environ, GIT_INDEX_FILE=str(self.root / "index"))
        git(self.repo, "read-tree", self.revision, env=self.index_env)

    def diff(self, change):
        work = self.root / "work"
        shutil.copytree(self.repo, work)
        change(work)
        git(work, "add", "-N", ".")
        return subprocess.run(["git", "diff", "--binary", "--no-renames", self.revision], cwd=work, check=True,
                              stdout=subprocess.PIPE).stdout

    def test_allowed_text_edit_passes(self):
        patch = self.diff(lambda work: (work / "calc.py").write_text(FIXED))
        self.assertEqual(task.validate_task_patch(patch, self.repo, ["calc.py"], self.index_env), ["calc.py"])

    def test_scope_mode_and_file_set_changes_are_rejected(self):
        changes = {
            "outside scope": lambda work: (work / "test_calc.py").write_text(TESTS + "# weakened\n"),
            "new file": lambda work: (work / "helper.py").write_text("x = 1\n"),
            "deleted file": lambda work: (work / "calc.py").unlink(),
            "mode change": lambda work: (work / "calc.py").chmod(0o755),
            "binary": lambda work: (work / "calc.py").write_bytes(b"\0\1\2binary"),
            "symlink": lambda work: ((work / "calc.py").unlink(), (work / "calc.py").symlink_to("/etc/passwd")),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                shutil.rmtree(self.root / "work", ignore_errors=True)
                patch = self.diff(change)
                with self.assertRaises(RuntimeError):
                    task.validate_task_patch(patch, self.repo, ["calc.py"], self.index_env)

    def test_stale_or_oversized_patches_are_rejected(self):
        patch = self.diff(lambda work: (work / "calc.py").write_text(FIXED))
        stale = patch.replace(b"+ 1", b"+ 7")
        with self.assertRaises(RuntimeError):
            task.validate_task_patch(stale, self.repo, ["calc.py"], self.index_env)
        with self.assertRaisesRegex(RuntimeError, "limit"):
            task.validate_task_patch(b"x" * (task.PATCH_LIMIT + 1), self.repo, ["calc.py"], self.index_env)


class TaskPilotTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.repo, self.revision = make_repo(self.root)
        self.spec_path = self.root / "task.json"
        self.spec_path.write_text(json.dumps(spec(self.repo, self.revision, deadline_seconds=300, verify_seconds=30)))
        self.state = self.root / "state"

    def build(self, **kwargs):
        return task.TaskPilot(self.state, task=kwargs.pop("task", self.spec_path), runtime="vm", **kwargs)

    def test_checkpoint_pins_the_task_and_mode(self):
        instance = self.build()
        self.assertEqual(instance.checkpoint["task"]["id"], "double-1")
        self.assertEqual((instance.worker_deadline, instance.resume_timeout), (300, 270))
        self.assertEqual(list(instance.report["checks"]), task.TASK_CHECKS)
        self.assertEqual((instance.report["mode"], instance.report["task_id"]), ("task", "double-1"))
        instance.save()
        resumed = task.TaskPilot(self.state)
        self.assertEqual(resumed.task, instance.task)
        other = self.root / "other.json"
        other.write_text(json.dumps(spec(self.repo, self.revision, prompt="Something else.")))
        with self.assertRaisesRegex(RuntimeError, "cannot change the task"):
            task.TaskPilot(self.state, task=other)
        with self.assertRaisesRegex(RuntimeError, "task run"):
            pilot.Pilot(self.state)

    def test_new_state_needs_a_task_and_fixture_state_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, "needs --task"):
            task.TaskPilot(self.state)
        self.assertFalse((self.state / "checkpoint.json").exists())
        fixture = self.root / "fixture-state"
        pilot.Pilot(fixture, runtime="vm").save()
        with self.assertRaisesRegex(RuntimeError, "fixture run"):
            task.TaskPilot(fixture, task=self.spec_path)

    def test_snapshot_is_the_pinned_revision_not_the_working_tree(self):
        (self.repo / "calc.py").write_text("uncommitted = True\n")
        (self.repo / "untracked.py").write_text("x = 1\n")
        instance = self.build()
        instance.prepare_snapshot()
        landing, payload = self.state / "landing", self.state / "payload"
        self.assertEqual((landing / "calc.py").read_text(), BROKEN)
        self.assertEqual((payload / "repo/calc.py").read_text(), BROKEN)
        self.assertFalse((payload / "repo/untracked.py").exists())
        self.assertFalse((payload / "canary.txt").exists())
        self.assertEqual((payload / "repo/notes/readme.md").read_text(), "calc\n")
        self.assertEqual(instance.checkpoint["tree"], git(self.repo, "rev-parse", self.revision + "^{tree}"))
        self.assertEqual(instance.checkpoint["protected_sha256"], {"test_calc.py": hashlib.sha256(TESTS.encode()).hexdigest()})
        self.assertEqual(json.loads((payload / "opencode.json").read_text())["model"], "pilot/" + instance.model)
        self.assertEqual(instance.checkpoint["phase"], "snapshot")

    def test_snapshot_refuses_unusable_sources(self):
        (self.repo / "link.py").symlink_to("calc.py")
        git(self.repo, "add", "link.py")
        git(self.repo, "commit", "-qm", "link")
        linked = git(self.repo, "rev-parse", "HEAD")
        subdir = self.repo / "notes"
        cases = {"missing path": spec(self.repo, self.revision, allowed=["absent.py"]),
                 "symlink path": spec(self.repo, linked, allowed=["link.py"]),
                 "not top level": spec(subdir, self.revision, allowed=["calc.py"]),
                 "tree in edit mode": spec(self.repo, git(self.repo, "rev-parse", self.revision + "^{tree}"))}
        for name, value in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as state:
                path = Path(state) / "task.json"
                path.write_text(json.dumps(value))
                with self.assertRaises(RuntimeError):
                    task.TaskPilot(Path(state) / "s", task=path, runtime="vm").prepare_snapshot()

    def test_export_ignored_content_fails_the_tree_check(self):
        (self.repo / ".gitattributes").write_text("notes/** export-ignore\n")
        git(self.repo, "add", ".gitattributes")
        git(self.repo, "commit", "-qm", "attributes")
        self.spec_path.write_text(json.dumps(spec(self.repo, git(self.repo, "rev-parse", "HEAD"))))
        with self.assertRaisesRegex(RuntimeError, "does not reproduce"):
            self.build().prepare_snapshot()

    def test_prompt_states_scope_and_verification(self):
        prompt = self.build().worker_prompt()
        self.assertTrue(prompt.startswith("Fix double()."))
        self.assertIn("change only calc.py", prompt)
        self.assertIn("Never modify test_calc.py", prompt)
        self.assertIn("python -m unittest -q test_calc", prompt)

    def test_repair_loop_runs_task_verification(self):
        instance = self.build(repair_attempts=1)
        instance.prepare_snapshot()
        work = self.state / "payload/repo"
        prompts = []

        def remote(*args, **kwargs):
            if args[0] == "opencode":
                prompts.append(args[-1])
                if len(prompts) == 2:
                    (work / "calc.py").write_text(FIXED)
                return result(b'{"type":"text","part":{"text":"done"}}\n')
            return subprocess.run([sys.executable, "-B", *args[1:]], cwd=work, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                  timeout=kwargs.get("timeout"), check=False)

        with patch.object(instance, "remote", side_effect=remote):
            instance.edit_and_verify()
        self.assertEqual(len(prompts), 2)
        self.assertIn("Independent verification rejected", prompts[1])
        self.assertIn("AssertionError: 4 != 6", prompts[1])
        self.assertEqual([row["verdict"] for row in instance.report["attempts"]], ["fail", "pass"])
        self.assertEqual(instance.report["checks"]["protected_unchanged"]["verdict"], "pass")
        self.assertEqual(instance.report["checks"]["sandbox_verify"]["verdict"], "pass")
        self.assertIn(b"OK", (self.state / "verify-2.log").read_bytes())

    def test_protected_file_change_fails_before_verification(self):
        instance = self.build()
        instance.prepare_snapshot()
        work = self.state / "payload/repo"

        def remote(*args, **kwargs):
            if args[0] == "opencode":
                (work / "test_calc.py").write_text("import unittest\n")
                return result()
            return subprocess.run([sys.executable, "-B", *args[1:]], cwd=work, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)

        with patch.object(instance, "remote", side_effect=remote), self.assertRaisesRegex(AssertionError, "protected_unchanged"):
            instance.edit_and_verify()
        self.assertEqual(instance.report["checks"]["sandbox_verify"]["verdict"], "inconclusive")

    def test_baseline_expectation_and_deadline(self):
        instance = self.build()
        for code, expected, passed in [(1, "fail", True), (0, "fail", False), (0, "pass", True), (1, "pass", False), (1, "any", True)]:
            instance.task["baseline"] = expected
            with self.subTest(code=code, expected=expected), patch.object(instance, "remote", return_value=result(b"log", code)):
                if passed:
                    instance.check_baseline()
                else:
                    with self.assertRaisesRegex(AssertionError, "baseline"):
                        instance.check_baseline()
        with patch.object(instance, "remote", return_value=result(b"", 124)), self.assertRaisesRegex(RuntimeError, "deadline"):
            instance.check_baseline()

    def test_worker_tools_must_see_every_named_file(self):
        instance = self.build()
        for output, code, rg, passed in [(b"calc.py\nnotes/readme.md\ntest_calc.py\n", 0, b"/usr/local/bin/rg\n", True),
                                         (b"calc.py\ntest_calc.py\nnotes/readme.md\n", 0, b"/usr/local/bin/rg\n", False),
                                         (b"calc.py\nnotes/readme.md\ntest_calc.py\n", 0, b"/usr/bin/rg\n", False),
                                         (b"calc.py\n", 0, b"/usr/local/bin/rg\n", False),
                                         (b"calc.py\ntest_calc.py\n", 1, b"/usr/local/bin/rg\n", False)]:
            with (self.subTest(output=output, code=code, rg=rg),
                  patch.object(instance, "remote", side_effect=[result(output, code), result(rg)]) as remote):
                if passed:
                    instance.check_worker_tools()
                else:
                    with self.assertRaisesRegex(AssertionError, "worker_tools"):
                        instance.check_worker_tools()
                self.assertEqual(remote.call_args_list[1].args, ("sh", "-c", "command -v rg"))

    def audit(self, rows):
        with (self.state / "shield-audit.jsonl").open("w") as stream:
            for row in rows:
                stream.write(json.dumps(row) + "\n")

    def test_shield_mediation_requires_pinned_model_and_provider(self):
        model = "openai/gpt-oss-120b"
        request = {"model": model, "body_sha256": "0" * 64, "secrets": 1, "identities": 2}
        response = {"phase": "response", "provider": "Cerebras", "identities": 0}
        cases = [([request, response], True), ([request], False), ([dict(request, model="other"), response], False),
                 ([request, dict(response, provider="SambaNova")], False), ([], False)]
        for number, (rows, passed) in enumerate(cases):
            instance = task.TaskPilot(self.root / f"lane-{number}", task=self.spec_path, runtime="vm", inference="cerebras")
            self.state = instance.state
            self.audit(rows)
            with (self.subTest(rows=rows), patch.object(instance, "edit_and_verify", return_value=result(b'{"type":"text","part":{"text":"fixed"}}\n')),
                  patch.object(instance, "seal_and_export") as seal):
                if passed:
                    instance.run_worker()
                    seal.assert_called_once()
                    self.assertEqual(instance.report["shield"], {"requests": 1, "responses": 1, "blocked": 0, "secrets_masked": 1,
                                                                 "identities_masked": 2, "served_by": ["Cerebras"]})
                    self.assertEqual((instance.state / "answer.txt").read_text(), "fixed")
                else:
                    with self.assertRaisesRegex(AssertionError, "shield_mediated"):
                        instance.run_worker()
                    seal.assert_not_called()

    def recover(self, instance, change, verify_code=0):
        work = self.root / "sandbox-repo"
        shutil.copytree(self.state / "payload/repo", work)
        git(work, "init", "-q")
        git(work, "add", "-A", "-f", ".")
        git(work, "commit", "-qm", "snapshot")
        revision = git(work, "rev-parse", "HEAD")
        change(work)
        git(work, "add", "-N", ".")
        exported = subprocess.run(["git", "diff", "--binary", "--no-renames", revision], cwd=work, check=True, stdout=subprocess.PIPE).stdout

        def cli_run(*args, **kwargs):
            if args[:2] == ("sandbox", "download"):
                Path(args[4]).write_bytes(exported)
            return result()

        instance.save("export_pending")
        with (patch.object(instance, "cli_run", side_effect=cli_run),
              patch.object(instance, "remote", return_value=result(b"Ran 1 test\nOK\n", verify_code))):
            instance.recover_and_land()
        return exported

    def test_recovery_exports_a_scoped_verified_patch(self):
        instance = self.build()
        instance.prepare_snapshot()
        exported = self.recover(instance, lambda work: (work / "calc.py").write_text(FIXED))
        export = instance.report["export"]
        self.assertEqual(export["changed_files"], ["calc.py"])
        self.assertEqual(export["patch_sha256"], hashlib.sha256(exported).hexdigest())
        self.assertEqual((export["base_revision"], export["verify"]["exit_code"]), (self.revision, 0))
        self.assertEqual((instance.report["verdict"], instance.checkpoint["phase"]), ("pass", "complete"))
        self.assertEqual(json.loads((self.state / "checkpoint.json").read_text())["phase"], "complete")

    def test_recovery_rejects_scope_violations_and_failed_reverification(self):
        for name, change, code, check in [
                ("outside scope", lambda work: (work / "notes/readme.md").write_text("edited\n"), 0, "diff_scope"),
                ("new file", lambda work: (work / "extra.py").write_text("x = 1\n"), 0, "diff_scope"),
                ("restart verification", lambda work: (work / "calc.py").write_text(FIXED), 1, "restart_recovery")]:
            with self.subTest(name=name):
                shutil.rmtree(self.state, ignore_errors=True)
                shutil.rmtree(self.root / "sandbox-repo", ignore_errors=True)
                instance = self.build()
                instance.prepare_snapshot()
                with self.assertRaisesRegex(AssertionError, check):
                    self.recover(instance, change, code)
                self.assertEqual(instance.report["verdict"], "inconclusive")
                self.assertNotIn("export", instance.report)

    def test_verify_mode_runs_no_worker(self):
        tree = git(self.repo, "rev-parse", self.revision + "^{tree}")
        self.spec_path.write_text(json.dumps({"schema": 1, "mode": "verify", "id": "integrated", "repo": str(self.repo),
                                              "revision": tree, "verify": ["python", "-m", "unittest"]}))
        instance = self.build()
        self.assertEqual(list(instance.report["checks"]), task.VERIFY_CHECKS)
        instance.prepare_snapshot()
        self.assertEqual(instance.checkpoint["tree"], tree)
        with patch.object(instance, "remote") as remote:
            instance.run_worker()
        remote.assert_not_called()
        self.assertEqual((instance.report["verdict"], instance.checkpoint["phase"]), ("pass", "complete"))

    def test_verify_mode_starts_no_recovery_process(self):
        tree = git(self.repo, "rev-parse", self.revision + "^{tree}")
        self.spec_path.write_text(json.dumps({"schema": 1, "mode": "verify", "id": "integrated", "repo": str(self.repo),
                                              "revision": tree, "verify": ["python", "-m", "unittest"]}))
        instance = self.build()
        args = argparse.Namespace(state=self.state, resume=False)
        with (patch.object(instance, "preflight"), patch.object(instance, "start_services"),
              patch.object(instance, "create_sandbox"), patch.object(instance, "acceptance_checks"),
              patch.object(instance, "cleanup") as cleanup):
            returned, resume = pilot.run_controller(args, build=lambda _: instance)
        cleanup.assert_called_once()
        self.assertIs(returned, instance)
        self.assertFalse(resume)
        self.assertEqual((instance.report["verdict"], instance.checkpoint["phase"]), ("pass", "complete"))
        self.assertNotIn("checkpoint_reloaded", instance.report["checks"])

    def test_edit_mode_hands_a_pending_export_to_recovery(self):
        instance = self.build()
        args = argparse.Namespace(state=self.state, resume=False)
        with (patch.object(instance, "preflight"), patch.object(instance, "start_services"),
              patch.object(instance, "prepare_snapshot"), patch.object(instance, "create_sandbox"),
              patch.object(instance, "acceptance_checks"), patch.object(instance, "cleanup"),
              patch.object(instance, "run_worker", side_effect=lambda: instance.save("export_pending"))):
            _, resume = pilot.run_controller(args, build=lambda _: instance)
        self.assertTrue(resume)

    def test_command_line_builds_a_task_pilot(self):
        args = argparse.Namespace(state=self.state, task=self.spec_path, runtime="vm", profile="cerebras", repair_attempts=1)
        instance = task.build(args)
        self.assertIsInstance(instance, task.TaskPilot)
        self.assertEqual((instance.inference, instance.repair_attempts), ("cerebras", 1))


class ResumeHandoffTests(unittest.TestCase):
    """The --resume child owns a gateway and a sandbox, so a deadline or an
    interrupt in the parent must reach it as SIGTERM, never as SIGKILL."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.script = self.root / "child.py"

    def handoff(self, source, timeout):
        self.script.write_text(source)
        stdout, stderr = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            code = pilot.recover_in_new_process(str(self.script), argparse.Namespace(state=self.root, resume_timeout=timeout))
        return code, stdout.getvalue(), stderr.getvalue()

    def test_deadline_asks_the_recovery_process_to_clean_up(self):
        code, stdout, stderr = self.handoff(
            "import signal, sys, time\nfrom pathlib import Path\n"
            "def cleanup(signum, frame):\n"
            "    (Path(sys.argv[2]) / 'cleaned').write_text(' '.join(sys.argv[1:]))\n"
            "    sys.exit(3)\n"
            "signal.signal(signal.SIGTERM, cleanup)\n"
            "print('started', flush=True)\ntime.sleep(60)\n", timeout=3)
        self.assertEqual(code, 1)
        self.assertEqual((self.root / "cleaned").read_text(), f"--state {self.root} --resume")
        self.assertIn("started", stdout)
        self.assertIn("recovery timed out; the recovery process was asked to clean up (exit 3)", stderr)

    def test_recovery_output_and_exit_code_pass_through(self):
        code, stdout, stderr = self.handoff("import sys\nprint('{\"verdict\": \"fail\"}')\nsys.exit(1)\n", timeout=30)
        self.assertEqual((code, stdout, stderr), (1, '{"verdict": "fail"}\n', ""))
        code, stdout, _ = self.handoff("print('{\"verdict\": \"pass\"}')\n", timeout=30)
        self.assertEqual((code, stdout), (0, '{"verdict": "pass"}\n'))


class TeamFixtureTests(unittest.TestCase):
    def test_every_team_task_is_a_valid_task_that_starts_failing(self):
        team = Path(__file__).resolve().parent / "team"
        repo = team / "repo"
        files = {path.name for path in repo.iterdir() if path.is_file()}
        specs = sorted(team.glob("*.json"))
        self.assertTrue(specs)
        baselines = {}
        for spec_file in specs:
            named = set()
            for entry in json.loads(spec_file.read_text())["tasks"]:
                with self.subTest(team=spec_file.name, task=entry["id"]):
                    self.assertIn(entry["profile"], profiles.PROFILES)
                    # Captain passes the profile and repair budget as flags, not spec fields.
                    spec = {key: value for key, value in entry.items() if key not in ("profile", "repair_attempts")}
                    spec = task.validate_task(dict(spec, schema=1, repo=str(repo), revision="0" * 40))
                    named.update(spec["allowed"] + spec["protected"])
                    verify = (sys.executable if spec["verify"][0] == "python" else spec["verify"][0], *spec["verify"][1:])
                    if verify not in baselines:
                        baselines[verify] = subprocess.run(verify, cwd=repo, env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"),
                                                           capture_output=True, timeout=60, check=False)
                    self.assertEqual(spec["baseline"], "fail")
                    self.assertNotEqual(baselines[verify].returncode, 0, baselines[verify].stderr.decode(errors="replace"))
            self.assertEqual(named, files, spec_file.name)


if __name__ == "__main__":
    unittest.main()
