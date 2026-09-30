import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pilot


class PilotTests(unittest.TestCase):
    def test_network_denial_requires_matching_policy_evidence(self):
        denied = b"OCSF NET:OPEN [MED] DENIED /usr/bin/curl(0) -> example.com:443 [reason:transparent_tcp_policy_denied]"
        self.assertTrue(pilot.network_denial_confirmed(7, denied))
        for code, events in [(0, denied), (28, denied), (7, b""),
                             (7, denied.replace(b"example.com", b"other.example")),
                             (7, denied.replace(b"DENIED", b"ALLOWED")),
                             (7, b"DNS lookup failed example.com:443")]:
            self.assertFalse(pilot.network_denial_confirmed(code, events))

    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.repo = Path(self.directory.name)
        pilot.command(["git", "-c", "init.templateDir=", "init", "-q", self.repo])
        (self.repo / "slugify.py").write_text("before\n")
        (self.repo / "test_slugify.py").write_text("original tests\n")
        pilot.command(["git", "add", "."], cwd=self.repo)
        pilot.command(["git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
                       "commit", "--no-verify", "-qm", "baseline"], cwd=self.repo)

    def diff(self, filename="slugify.py"):
        (self.repo / filename).write_text("after\n")
        diff = pilot.command(["git", "diff", "--binary", "--no-renames", "HEAD"], cwd=self.repo).stdout
        (self.repo / filename).write_text("before\n" if filename == "slugify.py" else "original tests\n")
        return diff

    def test_diff_lands_exactly_and_changed_target_is_refused(self):
        diff = self.diff()
        pilot.validate_patch(diff, self.repo)
        pilot.command(["git", "apply", "-"], input=diff, cwd=self.repo)
        self.assertEqual((self.repo / "slugify.py").read_text(), "after\n")
        with self.assertRaises(RuntimeError):
            pilot.validate_patch(diff, self.repo)

    def test_out_of_scope_empty_large_binary_and_mode_changes_are_refused(self):
        allowed = self.diff()
        for diff in [self.diff("test_slugify.py"), b"", b"x" * 65537,
                     b"new mode 120000\n" + allowed, b"GIT binary patch\n" + allowed]:
            with self.subTest(size=len(diff)), self.assertRaises(RuntimeError):
                pilot.validate_patch(diff, self.repo)
        self.assertEqual((self.repo / "slugify.py").read_text(), "before\n")

    def test_checkpoint_survives_reopen(self):
        path = self.repo / "checkpoint.json"
        pilot.write_json(path, {"phase": "export_pending", "revision": "abc"})
        self.assertEqual(json.loads(path.read_text())["phase"], "export_pending")
        self.assertFalse(path.with_suffix(".json.tmp").exists())

    def test_remote_deadlines_require_confirmed_sandbox_stop(self):
        instance = pilot.Pilot(self.repo / "run", runtime="vm")
        success = subprocess.CompletedProcess([], 0, b"")
        expired = subprocess.CompletedProcess([], 124, b"")
        with patch.object(instance, "cli_run", side_effect=[expired, success]) as run:
            self.assertEqual(instance.remote("sleep", "600", timeout=3, check=False).returncode, 124)
        self.assertEqual(run.call_args_list[1].args, ("sandbox", "stop", instance.checkpoint["name"]))
        self.assertTrue(instance.checkpoint.pop("stopped_after_timeout"))
        with (patch.object(instance, "cli_run", side_effect=[subprocess.TimeoutExpired([], 3), success]),
              self.assertRaises(subprocess.TimeoutExpired)):
            instance.remote("sleep", "600", timeout=3, check=False)
        self.assertTrue(instance.checkpoint.pop("stopped_after_timeout"))
        with (patch.object(instance, "cli_run", side_effect=[expired, RuntimeError("stop failed")]),
              self.assertRaisesRegex(RuntimeError, "stop failed")):
            instance.remote("sleep", "600", timeout=3, check=False)
        self.assertNotIn("stopped_after_timeout", instance.checkpoint)


    def test_runtime_is_explicit_and_survives_recovery(self):
        with patch("pilot.host_address", return_value="192.0.2.1"):
            instance = pilot.Pilot(self.repo / "vm", runtime="vm")
            self.assertEqual(instance.checkpoint["host"], "127.0.0.1")
            self.assertEqual(instance.runtime, "vm")
            recovered = pilot.Pilot(instance.state)
            self.assertEqual(recovered.runtime, "vm")
            with self.assertRaisesRegex(RuntimeError, "runtime"):
                pilot.Pilot(instance.state, runtime="docker")
            docker = pilot.Pilot(self.repo / "docker")
            self.assertEqual(docker.runtime, "docker")
            self.assertEqual(docker.checkpoint["host"], "192.0.2.1")

    def test_vm_configuration_keeps_tls_and_private_state(self):
        import tomllib

        instance = pilot.Pilot(self.repo / "vm", runtime="vm")
        config = tomllib.loads(instance.gateway_config())
        self.assertEqual(config["openshell"]["gateway"]["compute_driver"], "vm")
        self.assertTrue(config["openshell"]["gateway"]["mtls_auth"]["enabled"])
        driver = config["openshell"]["drivers"]["vm"]
        self.assertEqual(driver["state_dir"], str(instance.state / "vm"))
        self.assertEqual(driver["driver_dir"], str(instance.state / "bin"))
        self.assertTrue(driver["grpc_endpoint"].startswith("https://127.0.0.1:"))
        self.assertIn("@sha256:", driver["bootstrap_image"])

    def test_vm_rejects_long_socket_path_before_starting_services(self):
        instance = pilot.Pilot(self.repo / ("long-" * 25), runtime="vm")
        with patch("pilot.command") as run, self.assertRaisesRegex(RuntimeError, "short --state"):
            instance.preflight()
        run.assert_not_called()

    def test_middleware_runs_without_grpc_fork_handlers(self):
        instance = pilot.Pilot(self.repo / "vm", runtime="vm")
        for name in ["ca.crt", "server/tls.crt", "client/tls.crt", "client/tls.key"]:
            path = instance.state / "certs" / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("test\n")
        with patch.object(instance, "spawn", side_effect=RuntimeError("stop")) as spawn, self.assertRaisesRegex(RuntimeError, "stop"):
            instance.start_services()
        argv, log, env = spawn.call_args.args
        self.assertEqual((Path(argv[1]).name, log), ("middleware.py", "middleware.log"))
        self.assertEqual(env["GRPC_ENABLE_FORK_SUPPORT"], "0")


if __name__ == "__main__":
    unittest.main()
