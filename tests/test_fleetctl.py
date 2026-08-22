#!/usr/bin/env python3

from __future__ import annotations

import copy
import importlib.util
import subprocess
import sys
import tempfile
import unittest
import uuid
from pathlib import Path
from unittest import mock


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
        result["machine"]["asset_tag"] = "AC-10001"
        result["remote"]["tailscale_tailnet"] = "organization.example"
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
            self.assertEqual([], fleetctl.validate_profile(fleetctl.load_profile(path), ready=False), path)

    def test_cli_version_matches_version_file(self) -> None:
        result = subprocess.run([str(ROOT / "scripts/fleetctl.py"), "--version"], check=True, capture_output=True, text=True)
        self.assertEqual(f"fleetctl.py {(ROOT / 'VERSION').read_text().strip()}", result.stdout.strip())

    def test_missing_version_file_has_safe_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual("0.0.0+unknown", fleetctl.load_version(Path(directory)))

    def test_draft_cannot_be_applied_but_deferred_kvm_can(self) -> None:
        issues = fleetctl.validate_profile(self.load_work(), ready=True)
        paths = {issue.path for issue in issues}
        self.assertIn("state", paths)
        self.assertIn("machine.asset_tag", paths)
        self.assertNotIn("remote.kvm", paths)

    def test_resolved_work_profile_is_ready(self) -> None:
        self.assertEqual([], fleetctl.validate_profile(self.make_ready(self.load_work()), ready=True))

    def test_unknown_keys_are_rejected(self) -> None:
        profile = self.load_work()
        profile["accounts"]["superuser"] = "root"
        self.assertIn("accounts.superuser", {i.path for i in fleetctl.validate_profile(profile, ready=False)})

    def test_hostname_namespace_and_class_are_enforced(self) -> None:
        profile = self.load_work()
        profile["machine"]["hostname"] = "ss-mac-001"
        self.assertIn("machine.hostname", {i.path for i in fleetctl.validate_profile(profile, ready=False)})

    def test_uuid_must_be_canonical_v4(self) -> None:
        profile = self.load_work()
        profile["machine"]["uuid"] = "not-a-uuid"
        self.assertIn("machine.uuid", {i.path for i in fleetctl.validate_profile(profile, ready=False)})

    def test_admin_assignments_are_complete_and_one_to_one(self) -> None:
        profile = self.load_work()
        profile["accounts"]["admin_assignments"] = {"admin-01": "alice"}
        self.assertIn("accounts.admin_assignments", {i.path for i in fleetctl.validate_profile(profile, ready=False)})

    def test_personal_identity_cannot_be_shared(self) -> None:
        profile = self.load_work()
        profile["profile"] = profile["deployment"]["context"] = "personal"
        profile["deployment"]["ownership"] = "individual"
        profile["model_auth"]["codex"] = "named-human"
        self.assertIn("model_auth.codex", {i.path for i in fleetctl.validate_profile(profile, ready=False)})

    def test_account_phase_uses_argv_not_shell_text(self) -> None:
        phase = fleetctl.phase_for(self.make_ready(self.load_work()), "accounts", apply=True, recovery=False)
        assert phase.command
        self.assertEqual("--apply", phase.command[-1])
        self.assertNotIn("sudo", phase.command)

    def test_shell_metacharacter_is_rejected(self) -> None:
        profile = self.load_work()
        profile["accounts"]["humans"] = ["alice;id"]
        self.assertIn("accounts.humans", {i.path for i in fleetctl.validate_profile(profile, ready=False)})

    def test_malformed_types_return_issues(self) -> None:
        profile = self.load_work()
        profile["machine"]["platform"] = ["linux"]
        profile["tooling"]["gws"] = ["install"]
        paths = {i.path for i in fleetctl.validate_profile(profile, ready=False)}
        self.assertIn("machine.platform", paths)
        self.assertIn("tooling.gws", paths)

    def test_agentctl_target_uses_hostname(self) -> None:
        profile = self.make_ready(self.load_work())
        phase = fleetctl.phase_for(profile, "agentctl", apply=False, recovery=False)
        assert phase.command
        self.assertEqual(profile["machine"]["hostname"], phase.command[phase.command.index("--target") + 1])

    def test_init_generates_valid_unique_uuid_without_overwrite(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "ac-ws-042.toml"
            argv = ["fleetctl.py", "init", str(output), "--context", "work", "--namespace", "ac", "--hostname", "ac-ws-042", "--platform", "linux", "--human", "alice"]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(0, fleetctl.main())
            profile = fleetctl.load_profile(output)
            self.assertEqual(4, uuid.UUID(profile["machine"]["uuid"]).version)
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())

    def test_init_rejects_more_admins_than_humans_before_write(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "ac-ws-043.toml"
            argv = ["fleetctl.py", "init", str(output), "--context", "work", "--namespace", "ac", "--hostname", "ac-ws-043", "--platform", "linux", "--human", "alice", "--admin", "admin-01", "--admin", "admin-02"]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse(output.exists())

    def test_external_fleet_requires_matching_kit_lock(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            argv = ["fleetctl.py", "--fleet-root", str(root), "validate", "machines/ac-ws-001.toml"]
            with mock.patch.object(sys, "argv", argv), mock.patch.object(fleetctl, "load_profile") as load:
                self.assertEqual(2, fleetctl.main())
                load.assert_not_called()

    def test_runner_strips_inherited_apply_environment(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = ["fleetctl.py", "run", "ignored.toml", "base"]
        completed = subprocess.CompletedProcess([], 0)
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.dict(fleetctl.os.environ, {"APPLY_CHANGES": "true"}), \
             mock.patch.object(fleetctl, "load_profile", return_value=profile), \
             mock.patch.object(fleetctl, "actual_platform", return_value="linux"), \
             mock.patch.object(fleetctl.subprocess, "run", return_value=completed) as run:
            self.assertEqual(0, fleetctl.main())
            self.assertNotIn("APPLY_CHANGES", run.call_args.kwargs["env"])

    def test_run_rejects_platform_mismatch_before_subprocess(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = ["fleetctl.py", "run", "ignored.toml", "base"]
        with mock.patch.object(sys, "argv", argv), mock.patch.object(fleetctl, "load_profile", return_value=profile), mock.patch.object(fleetctl, "actual_platform", return_value="macos"), mock.patch.object(fleetctl.subprocess, "run") as run:
            self.assertEqual(2, fleetctl.main())
            run.assert_not_called()

    def test_user_phase_rejects_non_agent_account(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = ["fleetctl.py", "run", "ignored.toml", "shell"]
        fake_user = type("User", (), {"pw_name": "alice"})()
        with mock.patch.object(sys, "argv", argv), mock.patch.object(fleetctl, "load_profile", return_value=profile), mock.patch.object(fleetctl, "actual_platform", return_value="linux"), mock.patch.object(fleetctl.pwd, "getpwuid", return_value=fake_user), mock.patch.object(fleetctl.subprocess, "run") as run:
            self.assertEqual(2, fleetctl.main())
            run.assert_not_called()

    def test_remote_apply_requires_recovery_confirmation(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = ["fleetctl.py", "run", "ignored.toml", "remote-hardening", "--apply"]
        with mock.patch.object(sys, "argv", argv), mock.patch.object(fleetctl, "load_profile", return_value=profile), mock.patch.object(fleetctl, "actual_platform", return_value="linux"), mock.patch.object(fleetctl.subprocess, "run") as run:
            self.assertEqual(2, fleetctl.main())
            run.assert_not_called()

    def test_remote_apply_requires_explicit_connection_context(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = [
            "fleetctl.py", "run", "ignored.toml", "remote-hardening",
            "--apply", "--confirm-recovery-tested",
        ]
        with mock.patch.object(sys, "argv", argv), mock.patch.object(fleetctl, "load_profile", return_value=profile), mock.patch.object(fleetctl, "actual_platform", return_value="linux"), mock.patch.object(fleetctl.subprocess, "run") as run:
            self.assertEqual(2, fleetctl.main())
            run.assert_not_called()

    def test_remote_apply_passes_declared_tailscale_peer(self) -> None:
        profile = self.make_ready(self.load_work())
        phase = fleetctl.phase_for(
            profile,
            "remote-hardening",
            apply=True,
            recovery=True,
            connection_context="tailscale-ssh",
            ssh_source_ip="100.64.0.10",
        )
        assert phase.command
        self.assertIn("--confirm-recovery-tested", phase.command)
        self.assertEqual(
            "100.64.0.10",
            phase.command[phase.command.index("--ssh-source-ip") + 1],
        )


if __name__ == "__main__":
    unittest.main()
