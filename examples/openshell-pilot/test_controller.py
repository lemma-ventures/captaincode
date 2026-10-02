import fcntl
import os
import select
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import pilot


class ControllerTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.state = Path(self.directory.name)

    def test_competing_controller_cannot_read_or_change_checkpoint(self):
        checkpoint = self.state / "checkpoint.json"
        checkpoint.write_bytes(b"not valid JSON")
        report = self.state / "report.json"
        report.write_bytes(b"original report")
        with (self.state / "controller.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            for script, extra in [("pilot.py", ["--resume"]), ("prepare.py", [])]:
                result = pilot.command([sys.executable, pilot.HERE / script,
                                        "--state", self.state, *extra], check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(b"another controller owns this state", result.stdout)
        self.assertEqual(checkpoint.read_bytes(), b"not valid JSON")
        self.assertEqual(report.read_bytes(), b"original report")

    def test_lock_is_released_on_exception_and_has_private_permissions(self):
        with self.assertRaisesRegex(RuntimeError, "interrupted"), pilot.state_lock(self.state):
            with self.assertRaisesRegex(RuntimeError, "another controller"), pilot.state_lock(self.state):
                self.fail("two owners")
            raise RuntimeError("interrupted")
        with pilot.state_lock(self.state):
            self.assertEqual((self.state / "controller.lock").stat().st_mode & 0o777, 0o600)

    def test_lock_is_released_after_process_is_killed(self):
        script = (
            "import pathlib,sys,time; import pilot; "
            "lock=pilot.state_lock(pathlib.Path(sys.argv[1])); lock.__enter__(); "
            "print('owned', flush=True); time.sleep(60)"
        )
        process = subprocess.Popen([sys.executable, "-c", script, str(self.state)],
                                   cwd=pilot.HERE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            self.assertTrue(select.select([process.stdout], [], [], 10)[0])
            self.assertEqual(process.stdout.readline(), b"owned\n")
            with self.assertRaisesRegex(RuntimeError, "another controller"), pilot.state_lock(self.state):
                self.fail("two owners")
        finally:
            process.kill()
            process.communicate(timeout=10)
        with pilot.state_lock(self.state):
            pass

    def test_lock_rejects_symlinks_and_shared_inodes(self):
        target = self.state / "unrelated"
        target.write_bytes(b"keep")
        target.chmod(0o644)
        lock = self.state / "controller.lock"
        lock.symlink_to(target)
        with self.assertRaises(OSError), pilot.state_lock(self.state):
            self.fail("followed lock symlink")
        lock.unlink()
        os.link(target, lock)
        with self.assertRaisesRegex(RuntimeError, "regular private file"), pilot.state_lock(self.state):
            self.fail("accepted shared inode")
        self.assertEqual(target.read_bytes(), b"keep")
        self.assertEqual(target.stat().st_mode & 0o777, 0o644)

    def test_prepare_refuses_to_replace_existing_runtime_state(self):
        checkpoint = self.state / "checkpoint.json"
        checkpoint.write_bytes(b"original checkpoint")
        result = pilot.command([sys.executable, pilot.HERE / "prepare.py", "--state", self.state], check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"prepare requires fresh state", result.stdout)
        self.assertEqual(checkpoint.read_bytes(), b"original checkpoint")

    def test_invalid_entrypoint_preserves_previous_run_evidence(self):
        instance = pilot.Pilot(self.state, runtime="vm")
        for phase, extra in [("complete", []), ("worker_started", ["--resume"])]:
            instance.report["verdict"] = "pass" if phase == "complete" else "inconclusive"
            instance.checkpoint["sandbox_deleted"] = phase == "complete"
            instance.save(phase)
            before = {name: (self.state / name).read_bytes() for name in ["checkpoint.json", "report.json"]}
            result = pilot.command([sys.executable, pilot.HERE / "pilot.py", "--state", self.state, *extra], check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(before, {name: (self.state / name).read_bytes() for name in before})


if __name__ == "__main__":
    unittest.main()
