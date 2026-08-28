#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/start-linux-pilot.py"
SPEC = importlib.util.spec_from_file_location("start_linux_pilot", SCRIPT)
assert SPEC and SPEC.loader
start_linux_pilot = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = start_linux_pilot
SPEC.loader.exec_module(start_linux_pilot)


class StartLinuxPilotTests(unittest.TestCase):
    def test_os_release_parser_handles_quotes_and_comments(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "os-release"
            path.write_text('# comment\nID=ubuntu\nVERSION_ID="24.04"\n', encoding="utf-8")
            self.assertEqual(
                {"ID": "ubuntu", "VERSION_ID": "24.04"},
                start_linux_pilot.read_os_release(path),
            )

    def test_root_execution_is_rejected(self) -> None:
        check = start_linux_pilot.execution_user_check(0)
        self.assertEqual("FAIL", check.status)

    def test_unexpected_ubuntu_release_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "os-release"
            path.write_text('ID=ubuntu\nVERSION_ID="26.04"\n', encoding="utf-8")
            check = start_linux_pilot.operating_system_check("24.04", path)
        self.assertEqual("FAIL", check.status)
        self.assertIn("expected Ubuntu 24.04", check.detail)

    def test_profile_must_be_a_direct_child_of_machines(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines").mkdir()
            valid, error = start_linux_pilot.resolve_profile(fleet, Path("machines/ac-ws-001.toml"))
            self.assertIsNone(error)
            self.assertEqual((fleet / "machines/ac-ws-001.toml").resolve(), valid)
            for value in (Path("outside.toml"), Path("machines/pilot/ac-ws-001.toml"), Path("../escape.toml")):
                with self.subTest(value=value):
                    resolved, error = start_linux_pilot.resolve_profile(fleet, value)
                    self.assertIsNone(resolved)
                    self.assertIsNotNone(error)

    def test_symlinked_profile_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines").mkdir()
            target = fleet / "machines/ac-ws-001.toml"
            target.write_text('state = "draft"\n', encoding="utf-8")
            link = fleet / "machines/ac-ws-002.toml"
            link.symlink_to(target.name)
            resolved, error = start_linux_pilot.resolve_profile(
                fleet, Path("machines/ac-ws-002.toml")
            )
        self.assertIsNone(resolved)
        self.assertIn("symlink", error or "")

    def test_missing_profile_is_a_next_step_not_a_false_pass(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines").mkdir()
            (fleet / "kit.lock").write_text(
                (ROOT / "VERSION").read_text(encoding="utf-8"), encoding="utf-8"
            )
            checks = start_linux_pilot.fleet_checks(fleet, Path("machines/ac-ws-001.toml"))
            self.assertIn("PASS", {check.status for check in checks})
            self.assertIn("NEXT", {check.status for check in checks})
            self.assertNotIn("FAIL", {check.status for check in checks})

    def test_mismatched_kit_lock_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines").mkdir()
            (fleet / "kit.lock").write_text("0.0.0\n", encoding="utf-8")
            checks = start_linux_pilot.fleet_checks(fleet, None)
            self.assertEqual("FAIL", checks[0].status)

    def test_approved_fleet_failure_without_output_has_safe_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory).resolve()
            machines = fleet / "machines"
            machines.mkdir()
            profile = machines / "ac-ws-001.toml"
            profile.write_text(
                'state = "approved"\n[machine]\nplatform = "linux"\n',
                encoding="utf-8",
            )
            (fleet / "kit.lock").write_text(
                (ROOT / "VERSION").read_text(encoding="utf-8"), encoding="utf-8"
            )
            original_run = start_linux_pilot.run
            calls = 0

            def fake_run(command: list[str], **_: object) -> subprocess.CompletedProcess[str]:
                nonlocal calls
                calls += 1
                return subprocess.CompletedProcess(command, 0 if calls == 1 else 1, "", "")

            start_linux_pilot.run = fake_run
            try:
                checks = start_linux_pilot.fleet_checks(fleet, Path("machines/ac-ws-001.toml"))
            finally:
                start_linux_pilot.run = original_run
            self.assertEqual("ready validation failed", checks[-1].detail)

    def test_non_linux_profile_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory).resolve()
            (fleet / "machines").mkdir()
            (fleet / "kit.lock").write_text(
                (ROOT / "VERSION").read_text(encoding="utf-8"), encoding="utf-8"
            )
            profile = fleet / "machines/ac-mac-001.toml"
            profile.write_text(
                'state = "approved"\n[machine]\nplatform = "macos"\n',
                encoding="utf-8",
            )
            success = subprocess.CompletedProcess(["validate"], 0, "", "")
            with mock.patch.object(start_linux_pilot, "run", return_value=success):
                checks = start_linux_pilot.fleet_checks(
                    fleet, Path("machines/ac-mac-001.toml")
                )
        self.assertEqual("FAIL", checks[-1].status)
        self.assertIn("must be linux", checks[-1].detail)

    def test_missing_preflight_is_reported_without_exception(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            missing = Path(directory) / "preflight.sh"
            check = start_linux_pilot.preflight_check(missing)
            self.assertEqual("FAIL", check.status)
            self.assertIn("missing executable", check.detail)

    def test_private_fleet_symlink_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fleet = root / "fleet"
            fleet.mkdir()
            link = root / "fleet-link"
            link.symlink_to(fleet, target_is_directory=True)
            checks = start_linux_pilot.fleet_checks(link.absolute(), None)
            self.assertEqual("FAIL", checks[0].status)

    def test_dirty_toolkit_is_rejected(self) -> None:
        dirty = subprocess.CompletedProcess(["git"], 0, " M README.md\n", "")
        with mock.patch.object(
            start_linux_pilot.shutil, "which", return_value="/usr/bin/git"
        ), mock.patch.object(start_linux_pilot, "run", return_value=dirty):
            check = start_linux_pilot.repository_check()
        self.assertEqual("FAIL", check.status)
        self.assertIn("not clean", check.detail)

    def test_codex_missing_is_rejected(self) -> None:
        with mock.patch.object(start_linux_pilot.shutil, "which", return_value=None):
            check = start_linux_pilot.codex_check()
        self.assertEqual("FAIL", check.status)

    def test_failed_repository_suite_is_rejected(self) -> None:
        failed = subprocess.CompletedProcess(["make", "check"], 2)
        with mock.patch.object(
            start_linux_pilot.shutil, "which", return_value="/usr/bin/make"
        ), mock.patch.object(start_linux_pilot.subprocess, "run", return_value=failed):
            check = start_linux_pilot.repository_suite_check()
        self.assertEqual("FAIL", check.status)

    def test_private_profile_must_be_committed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory).resolve()
            machines = fleet / "machines"
            machines.mkdir()
            profile = machines / "ac-ws-001.toml"
            profile.write_text('state = "approved"\n', encoding="utf-8")
            responses = [
                subprocess.CompletedProcess(["git", "status"], 0, "", ""),
                subprocess.CompletedProcess(["git", "rev-parse"], 0, "abc123\n", ""),
                subprocess.CompletedProcess(["git", "ls-files"], 1, "", "not tracked"),
            ]
            with mock.patch.object(
                start_linux_pilot.shutil, "which", return_value="/usr/bin/git"
            ), mock.patch.object(start_linux_pilot, "run", side_effect=responses):
                check = start_linux_pilot.fleet_repository_check(
                    fleet, Path("machines/ac-ws-001.toml")
                )
        self.assertEqual("FAIL", check.status)
        self.assertIn("not committed", check.detail)


if __name__ == "__main__":
    unittest.main()
