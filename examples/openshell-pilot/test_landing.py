import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pilot


class LandingTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.instance = pilot.Pilot(Path(self.directory.name), runtime="vm")
        self.instance.prepare_snapshot()
        self.repo = self.instance.state / "landing"
        self.target = self.repo / "slugify.py"
        self.before = self.target.read_bytes()
        self.after = b"def slugify(text):\n    return text.strip().lower()\n"
        self.target.write_bytes(self.after)
        diff = pilot.command(["git", "diff", "--binary", "--no-renames", "HEAD"], cwd=self.repo).stdout
        self.target.write_bytes(self.before)
        (self.instance.state / "result.patch").write_bytes(diff)
        (self.instance.state / "recovered-slugify.py").write_bytes(self.after)
        self.instance.save("export_pending")

    def recover(self, instance=None):
        instance = instance or self.instance
        result = subprocess.CompletedProcess([], 0, b"Ran 4 tests\n")
        with (patch.object(instance, "cli_run", return_value=result) as cli,
              patch.object(instance, "remote", return_value=result)):
            instance.recover_and_land()
        return cli

    def interrupt_after_landing(self):
        check = self.instance.check

        def interrupt(name, passed, detail=""):
            if name == "diff_landed":
                raise RuntimeError("interrupted after landing")
            return check(name, passed, detail)

        with (patch.object(self.instance, "check", side_effect=interrupt),
              self.assertRaisesRegex(RuntimeError, "interrupted after landing")):
            self.recover()
        self.assertEqual(self.target.read_bytes(), self.after)

    def test_mismatched_export_never_changes_landing_file(self):
        (self.instance.state / "recovered-slugify.py").write_bytes(b"unrelated export\n")
        with self.assertRaises((RuntimeError, AssertionError)):
            self.recover()
        self.assertEqual(self.target.read_bytes(), self.before)
        self.assertEqual(pilot.command(["git", "status", "--porcelain"], cwd=self.repo).stdout, b"")

    def test_resume_after_landing_uses_local_checkpoint_without_reapplying(self):
        self.interrupt_after_landing()
        recovered = pilot.Pilot(self.instance.state)
        cli = self.recover(recovered)
        cli.assert_not_called()
        self.assertEqual(recovered.checkpoint["phase"], "complete")
        self.assertEqual(recovered.report["task_successes"], 1)
        self.assertEqual(self.target.read_bytes(), self.after)

    def test_completed_landing_is_idempotent(self):
        self.recover()
        recovered = pilot.Pilot(self.instance.state)
        cli = self.recover(recovered)
        cli.assert_not_called()
        self.assertEqual(recovered.report["task_successes"], 1)
        self.assertEqual(self.target.read_bytes(), self.after)

    def test_completed_resume_runs_in_a_fresh_process_without_runtime_or_key(self):
        self.recover()
        self.instance.checkpoint["sandbox_deleted"] = True
        self.instance.save()
        environment = {key: value for key, value in os.environ.items() if key != "NVIDIA_API_KEY"}
        result = pilot.command([sys.executable, pilot.__file__, "--state", self.instance.state, "--resume"],
                               env=environment)
        self.assertIn(b'"verdict": "pass"', result.stdout)
        report = json.loads((self.instance.state / "report.json").read_text())
        self.assertEqual(report["task_successes"], 1)
        self.assertEqual(self.target.read_bytes(), self.after)

    def test_resume_before_atomic_replace_finishes_saved_landing(self):
        replace = os.replace

        def interrupt(source, destination):
            if Path(destination) == self.target:
                raise RuntimeError("interrupted before landing")
            return replace(source, destination)

        with (patch("pilot.os.replace", side_effect=interrupt),
              self.assertRaisesRegex(RuntimeError, "interrupted before landing")):
            self.recover()
        self.assertEqual(self.target.read_bytes(), self.before)
        recovered = pilot.Pilot(self.instance.state)
        self.assertEqual(recovered.checkpoint["phase"], "landing_pending")
        self.recover(recovered).assert_not_called()
        self.assertEqual(self.target.read_bytes(), self.after)

    def test_resume_refuses_changed_head(self):
        self.interrupt_after_landing()
        pilot.command(["git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                       "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
                       "commit", "--allow-empty", "--no-verify", "-qm", "new head"], cwd=self.repo)
        with self.assertRaisesRegex(RuntimeError, "landing HEAD"):
            self.recover(pilot.Pilot(self.instance.state))

    def test_resume_refuses_mode_changes_and_untracked_files(self):
        self.interrupt_after_landing()
        self.target.chmod(0o644)
        with self.assertRaisesRegex(RuntimeError, "mode changed"):
            self.recover(pilot.Pilot(self.instance.state))
        self.target.chmod(0o600)
        (self.repo / "extra.py").write_text("new user file\n")
        with self.assertRaisesRegex(RuntimeError, "landing worktree"):
            self.recover(pilot.Pilot(self.instance.state))

    def test_resume_refuses_changed_export_and_keeps_landed_file(self):
        self.interrupt_after_landing()
        (self.instance.state / "result.patch").write_bytes(b"tampered\n")
        with self.assertRaises(RuntimeError):
            self.recover(pilot.Pilot(self.instance.state))
        self.assertEqual(self.target.read_bytes(), self.after)

    def test_resume_preserves_subsequent_user_edit(self):
        self.interrupt_after_landing()
        self.target.write_bytes(b"user edit\n")
        with self.assertRaisesRegex(RuntimeError, "landing"):
            self.recover(pilot.Pilot(self.instance.state))
        self.assertEqual(self.target.read_bytes(), b"user edit\n")

    def test_matching_preexisting_edit_is_not_adopted(self):
        self.target.write_bytes(self.after)
        with self.assertRaisesRegex(RuntimeError, "landing"):
            self.recover()
        self.assertNotEqual(self.instance.checkpoint["phase"], "complete")

    def test_resume_checks_other_files_and_index(self):
        self.interrupt_after_landing()
        (self.repo / "test_slugify.py").write_bytes(b"changed tests\n")
        with self.assertRaisesRegex(RuntimeError, "landing"):
            self.recover(pilot.Pilot(self.instance.state))
        pilot.command(["git", "restore", "test_slugify.py"], cwd=self.repo)
        pilot.command(["git", "add", "slugify.py"], cwd=self.repo)
        with self.assertRaisesRegex(RuntimeError, "landing"):
            self.recover(pilot.Pilot(self.instance.state))

    def test_export_symlink_is_refused(self):
        exported = self.instance.state / "recovered-slugify.py"
        exported.unlink()
        exported.symlink_to(self.target)
        with self.assertRaisesRegex(RuntimeError, "regular file"):
            self.recover()
        self.assertEqual(self.target.read_bytes(), self.before)


if __name__ == "__main__":
    unittest.main()
