#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/start-macos-pilot.py"
SPEC = importlib.util.spec_from_file_location("start_macos_pilot", SCRIPT)
assert SPEC and SPEC.loader
start_macos_pilot = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = start_macos_pilot
SPEC.loader.exec_module(start_macos_pilot)


class StartMacosPilotTests(unittest.TestCase):
    @staticmethod
    def _validated_profile_checks(platform: str) -> list[object]:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines").mkdir()
            (fleet / "kit.lock").write_text(
                (ROOT / "VERSION").read_text(encoding="utf-8"), encoding="utf-8"
            )
            profile = fleet / "machines/acme-mac-001.toml"
            profile.write_text(
                "schema_version = 3\n"
                "[machine]\n"
                f'platform = "{platform}"\n',
                encoding="utf-8",
            )
            clean = start_macos_pilot.Check("PASS", "private fleet revision", "clean")

            def successful_run(command: list[str], **_: object) -> subprocess.CompletedProcess[str]:
                return subprocess.CompletedProcess(command, 0, "ok\n", "")

            with mock.patch.object(
                start_macos_pilot,
                "clean_git_checkout_check",
                return_value=clean,
            ), mock.patch.object(start_macos_pilot, "run", side_effect=successful_run):
                return start_macos_pilot.fleet_checks(
                    fleet, Path("machines/acme-mac-001.toml")
                )

    def test_non_macos_platform_is_rejected(self) -> None:
        check = start_macos_pilot.operating_system_check("linux")
        self.assertEqual("FAIL", check.status)
        self.assertIn("macOS required", check.detail)

    def test_macos_version_is_reported(self) -> None:
        result = subprocess.CompletedProcess(["sw_vers"], 0, "26.0\n", "")
        check = start_macos_pilot.operating_system_check(
            "darwin", runner=lambda _: result
        )
        self.assertEqual("PASS", check.status)
        self.assertEqual("macOS 26.0", check.detail)

    def test_root_execution_is_rejected(self) -> None:
        self.assertEqual("FAIL", start_macos_pilot.execution_user_check(0).status)

    def test_sensitive_command_output_can_be_suppressed(self) -> None:
        result = subprocess.CompletedProcess(["codex"], 0, "user@example.com\n", "")
        with mock.patch.object(
            start_macos_pilot.shutil, "which", return_value="/bin/codex"
        ), mock.patch.object(start_macos_pilot, "run", return_value=result):
            check = start_macos_pilot.command_check(
                "Codex authentication",
                ["codex", "login", "status"],
                include_output=False,
            )
        self.assertEqual("PASS", check.status)
        self.assertEqual("check passed", check.detail)
        self.assertNotIn("@", check.detail)

    def test_repository_suite_uses_current_python(self) -> None:
        result = subprocess.CompletedProcess(["make"], 0, "", "")
        with mock.patch.object(start_macos_pilot.shutil, "which", return_value="/usr/bin/make"), \
             mock.patch.object(start_macos_pilot.subprocess, "run", return_value=result) as run:
            check = start_macos_pilot.repository_suite_check()
        self.assertEqual("PASS", check.status)
        self.assertEqual(sys.executable, run.call_args.kwargs["env"]["PYTHON"])

    def test_fleet_root_symlink_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fleet = root / "fleet"
            fleet.mkdir()
            link = root / "fleet-link"
            link.symlink_to(fleet, target_is_directory=True)
            checks = start_macos_pilot.fleet_checks(link.absolute(), None)
        self.assertEqual("FAIL", checks[0].status)

    def test_profile_must_be_committed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines").mkdir()
            (fleet / "kit.lock").write_text(
                (ROOT / "VERSION").read_text(encoding="utf-8"), encoding="utf-8"
            )
            (fleet / "machines/acme-mac-001.toml").write_text(
                'schema_version = 3\n', encoding="utf-8"
            )
            clean = start_macos_pilot.Check("PASS", "private fleet revision", "clean")
            with mock.patch.object(
                start_macos_pilot,
                "clean_git_checkout_check",
                return_value=clean,
            ), mock.patch.object(
                start_macos_pilot,
                "run",
                return_value=subprocess.CompletedProcess(["git"], 1, "", "not tracked"),
            ):
                checks = start_macos_pilot.fleet_checks(
                    fleet, Path("machines/acme-mac-001.toml")
                )
        self.assertEqual("FAIL", checks[-1].status)
        self.assertIn("not committed", checks[-1].detail)

    def test_nested_profile_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines/pilot").mkdir(parents=True)
            (fleet / "kit.lock").write_text(
                (ROOT / "VERSION").read_text(encoding="utf-8"), encoding="utf-8"
            )
            clean = start_macos_pilot.Check("PASS", "private fleet revision", "clean")
            with mock.patch.object(
                start_macos_pilot,
                "clean_git_checkout_check",
                return_value=clean,
            ):
                checks = start_macos_pilot.fleet_checks(
                    fleet, Path("machines/pilot/acme-mac-001.toml")
                )
        self.assertEqual("FAIL", checks[-1].status)

    def test_symlinked_profile_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines").mkdir()
            (fleet / "kit.lock").write_text(
                (ROOT / "VERSION").read_text(encoding="utf-8"), encoding="utf-8"
            )
            target = fleet / "machines/acme-mac-001.toml"
            target.write_text(
                'schema_version = 3\n[machine]\nplatform = "macos"\n',
                encoding="utf-8",
            )
            (fleet / "machines/acme-mac-002.toml").symlink_to(target.name)
            clean = start_macos_pilot.Check("PASS", "private fleet revision", "clean")
            with mock.patch.object(
                start_macos_pilot,
                "clean_git_checkout_check",
                return_value=clean,
            ):
                checks = start_macos_pilot.fleet_checks(
                    fleet, Path("machines/acme-mac-002.toml")
                )
        self.assertEqual("FAIL", checks[-1].status)
        self.assertIn("must not be a symlink", checks[-1].detail)

    def test_macos_profile_is_accepted(self) -> None:
        checks = self._validated_profile_checks("macos")
        self.assertEqual("PASS", checks[-1].status)
        self.assertIn("valid macOS profile", checks[-1].detail)

    def test_profile_validation_uses_current_python(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines").mkdir()
            (fleet / "kit.lock").write_text(
                (ROOT / "VERSION").read_text(encoding="utf-8"), encoding="utf-8"
            )
            profile = fleet / "machines/acme-mac-001.toml"
            profile.write_text(
                'schema_version = 3\n[machine]\nplatform = "macos"\n',
                encoding="utf-8",
            )
            clean = start_macos_pilot.Check("PASS", "private fleet revision", "clean")
            commands: list[list[str]] = []

            def successful_run(command: list[str], **_: object) -> subprocess.CompletedProcess[str]:
                commands.append(command)
                return subprocess.CompletedProcess(command, 0, "ok\n", "")

            with mock.patch.object(
                start_macos_pilot, "clean_git_checkout_check", return_value=clean
            ), mock.patch.object(start_macos_pilot, "run", side_effect=successful_run):
                start_macos_pilot.fleet_checks(
                    fleet, Path("machines/acme-mac-001.toml")
                )
        validation = next(command for command in commands if "validate" in command)
        self.assertEqual(sys.executable, validation[0])

    def test_non_macos_profile_is_rejected(self) -> None:
        checks = self._validated_profile_checks("linux")
        self.assertEqual("FAIL", checks[-1].status)
        self.assertIn("machine.platform must be macos", checks[-1].detail)


if __name__ == "__main__":
    unittest.main()
