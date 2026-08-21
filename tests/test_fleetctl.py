#!/usr/bin/env python3

from __future__ import annotations

import copy
import importlib.util
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("fleetctl", ROOT / "scripts/fleetctl.py")
assert SPEC and SPEC.loader
fleetctl = importlib.util.module_from_spec(SPEC)
sys.modules["fleetctl"] = fleetctl
SPEC.loader.exec_module(fleetctl)


class FleetProfileTests(unittest.TestCase):
    def load_work(self) -> dict:
        return fleetctl.load_profile(ROOT / "config/profiles/work.example.toml")

    def make_ready(self, profile: dict) -> dict:
        result = copy.deepcopy(profile)
        result["state"] = "approved"
        result["remote"]["tailscale_tailnet"] = "organization.example"
        result["remote"]["kvm"] = "glinet-comet-rm1"
        result["remote"]["desktop_lock_mode"] = "dedicated-shared"
        result["tooling"]["gws"] = "skip"
        result["tooling"]["secrets_provider"] = "organization-vault"
        result["tooling"]["antidote_ref"] = "v1.9.10"
        result["security"]["endpoint_management"] = "mdm-and-edr"
        result["backup"]["target"] = "corporate-backup"
        result["maintenance"]["update_window"] = "Sunday 02:00-04:00"
        result["maintenance"]["owner"] = "platform-team"
        return result

    def test_examples_are_valid_drafts(self) -> None:
        examples = sorted((ROOT / "config/profiles").glob("*.example.toml"))
        self.assertGreaterEqual(len(examples), 3)
        for path in examples:
            profile = fleetctl.load_profile(path)
            self.assertEqual([], fleetctl.validate_profile(profile, ready=False))

    def test_cli_version_matches_version_file(self) -> None:
        result = subprocess.run(
            [str(ROOT / "scripts/fleetctl.py"), "--version"],
            check=True,
            capture_output=True,
            text=True,
        )
        expected = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
        self.assertEqual(f"fleetctl.py {expected}", result.stdout.strip())

    def test_missing_version_file_has_safe_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual(
                "0.0.0+unknown",
                fleetctl.load_version(Path(directory)),
            )

    def test_draft_cannot_be_applied(self) -> None:
        profile = self.load_work()
        profile["remote"]["kvm"] = "ask"
        issues = fleetctl.validate_profile(profile, ready=True)
        paths = {issue.path for issue in issues}
        self.assertIn("state", paths)
        self.assertIn("tooling.gws", paths)
        self.assertIn("remote.desktop_lock_mode", paths)
        self.assertIn("remote.kvm", paths)

    def test_resolved_work_profile_is_ready(self) -> None:
        profile = self.make_ready(self.load_work())
        self.assertEqual([], fleetctl.validate_profile(profile, ready=True))

    def test_unknown_keys_are_rejected(self) -> None:
        profile = self.load_work()
        profile["accounts"]["superuser"] = "root"
        issues = fleetctl.validate_profile(profile, ready=False)
        self.assertIn("accounts.superuser", {issue.path for issue in issues})

    def test_personal_identity_cannot_be_shared(self) -> None:
        profile = self.load_work()
        profile["profile"] = "personal"
        profile["model_auth"]["codex"] = "named-human"
        issues = fleetctl.validate_profile(profile, ready=False)
        self.assertIn("model_auth.codex", {issue.path for issue in issues})

    def test_account_phase_uses_argv_not_shell_text(self) -> None:
        profile = self.make_ready(self.load_work())
        phase = fleetctl.phase_for(profile, "accounts", apply=True, recovery=False)
        assert phase.command
        self.assertEqual("--apply", phase.command[-1])
        self.assertIn("--operator", phase.command)
        self.assertNotIn("sudo", phase.command)

    def test_shell_metacharacter_in_account_is_rejected(self) -> None:
        profile = self.load_work()
        profile["accounts"]["humans"] = ["alice;id"]
        issues = fleetctl.validate_profile(profile, ready=False)
        self.assertIn("accounts.humans", {issue.path for issue in issues})

    def test_malformed_types_return_issues_instead_of_crashing(self) -> None:
        profile = self.load_work()
        profile["machine"]["platform"] = ["linux"]
        profile["tooling"]["gws"] = ["install"]
        issues = fleetctl.validate_profile(profile, ready=False)
        paths = {issue.path for issue in issues}
        self.assertIn("machine.platform", paths)
        self.assertIn("tooling.gws", paths)

    def test_daily_and_admin_accounts_cannot_overlap(self) -> None:
        profile = self.load_work()
        profile["accounts"]["admins"] = ["alice"]
        issues = fleetctl.validate_profile(profile, ready=False)
        self.assertIn("accounts", {issue.path for issue in issues})

    def test_required_agent_tooling_cannot_be_disabled(self) -> None:
        profile = self.load_work()
        profile["tooling"]["install_agents"] = False
        issues = fleetctl.validate_profile(profile, ready=False)
        self.assertIn("tooling.install_agents", {issue.path for issue in issues})

    def test_ssh_recovery_requires_a_declared_admin(self) -> None:
        profile = self.load_work()
        profile["accounts"]["ssh_users"] = profile["accounts"]["humans"]
        issues = fleetctl.validate_profile(profile, ready=False)
        self.assertIn("accounts.ssh_users", {issue.path for issue in issues})

    def test_timezone_must_be_a_real_iana_zone(self) -> None:
        profile = self.load_work()
        profile["maintenance"]["timezone"] = "Toronto-ish"
        issues = fleetctl.validate_profile(profile, ready=False)
        self.assertIn("maintenance.timezone", {issue.path for issue in issues})

    def test_timezone_path_does_not_escape_validation(self) -> None:
        profile = self.load_work()
        profile["maintenance"]["timezone"] = "/etc/localtime"
        issues = fleetctl.validate_profile(profile, ready=False)
        self.assertIn("maintenance.timezone", {issue.path for issue in issues})

    def test_agentctl_target_uses_machine_id(self) -> None:
        profile = self.make_ready(self.load_work())
        phase = fleetctl.phase_for(profile, "agentctl", apply=False, recovery=False)
        assert phase.command
        self.assertEqual(profile["machine"]["id"], phase.command[phase.command.index("--target") + 1])

    def test_run_rejects_platform_mismatch_before_subprocess(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = ["fleetctl.py", "run", "ignored.toml", "base"]
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(fleetctl, "load_profile", return_value=profile), \
             mock.patch.object(fleetctl, "actual_platform", return_value="macos"), \
             mock.patch.object(fleetctl.subprocess, "run") as run:
            self.assertEqual(2, fleetctl.main())
            run.assert_not_called()

    def test_user_phase_rejects_non_agent_account(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = ["fleetctl.py", "run", "ignored.toml", "shell"]
        fake_user = type("User", (), {"pw_name": "alice"})()
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(fleetctl, "load_profile", return_value=profile), \
             mock.patch.object(fleetctl, "actual_platform", return_value="linux"), \
             mock.patch.object(fleetctl.pwd, "getpwuid", return_value=fake_user), \
             mock.patch.object(fleetctl.subprocess, "run") as run:
            self.assertEqual(2, fleetctl.main())
            run.assert_not_called()

    def test_remote_apply_requires_recovery_confirmation(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = ["fleetctl.py", "run", "ignored.toml", "remote-hardening", "--apply"]
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(fleetctl, "load_profile", return_value=profile), \
             mock.patch.object(fleetctl, "actual_platform", return_value="linux"), \
             mock.patch.object(fleetctl.subprocess, "run") as run:
            self.assertEqual(2, fleetctl.main())
            run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
