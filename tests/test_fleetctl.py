#!/usr/bin/env python3

from __future__ import annotations

import argparse
import copy
import importlib.util
import os
import stat
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
        result["collaboration"].update(
            atlassian_site="company.atlassian.net",
            atlassian_identity="service-account",
            atlassian_principal="acagentdev",
            atlassian_mcp_auth="service-account-api-key",
        )
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

    def test_schema_two_profile_is_rejected_for_explicit_migration(self) -> None:
        profile = self.load_work()
        profile["schema_version"] = 2
        self.assertIn("schema_version", {i.path for i in fleetctl.validate_profile(profile, ready=False)})

    def test_display_name_must_be_trimmed_printable_and_bounded(self) -> None:
        for value in (" Atlas", "Atlas ", "Atlas  North", "Atlas\n"):
            profile = self.load_work()
            profile["machine"]["display_name"] = value
            with self.subTest(value=value):
                self.assertIn("machine.display_name", {i.path for i in fleetctl.validate_profile(profile, ready=False)})

    def test_display_name_rejects_deceptive_or_noncanonical_unicode(self) -> None:
        for value in ("At\u200blas", "At\u202elas", "Cafe\u0301", "Аtlas", "-Atlas"):
            profile = self.load_work()
            profile["machine"]["display_name"] = value
            with self.subTest(value=value):
                self.assertIn("machine.display_name", {i.path for i in fleetctl.validate_profile(profile, ready=False)})

    def test_privileged_identity_values_cannot_start_with_options(self) -> None:
        for key, value in (("asset_tag", "--apply"), ("role", "-x"), ("hardware_profile", "--help")):
            profile = self.make_ready(self.load_work())
            profile["machine"][key] = value
            with self.subTest(key=key):
                self.assertIn(f"machine.{key}", {i.path for i in fleetctl.validate_profile(profile, ready=True)})

    def test_enabled_provider_requires_a_non_url_principal(self) -> None:
        for value in (
            "none", "https://example.invalid/path", "ghp_1234567890",
            "glptt-example", "sk-example", "xai-example",
        ):
            profile = self.load_work()
            profile["source_control"]["github_principal"] = value
            with self.subTest(value=value):
                self.assertIn(
                    "source_control.github_principal",
                    {i.path for i in fleetctl.validate_profile(profile, ready=False)},
                )

    def test_named_human_atlassian_principal_can_be_an_email(self) -> None:
        profile = self.make_ready(self.load_work())
        profile["profile"] = profile["deployment"]["context"] = "personal"
        profile["deployment"]["ownership"] = "individual"
        profile["accounts"]["humans"] = ["alice"]
        profile["accounts"]["operators"] = ["alice"]
        profile["accounts"]["admins"] = ["admin-01"]
        profile["accounts"]["admin_assignments"] = {"admin-01": "alice"}
        profile["accounts"]["ssh_users"] = ["alice", "admin-01"]
        profile["collaboration"].update(
            atlassian_identity="named-human",
            atlassian_principal="alice@example.com",
            atlassian_mcp_auth="oauth-2.1",
        )
        self.assertNotIn(
            "collaboration.atlassian_principal",
            {i.path for i in fleetctl.validate_profile(profile, ready=True)},
        )

    def test_multi_operator_profile_rejects_named_human_atlassian_identity(self) -> None:
        profile = self.make_ready(self.load_work())
        profile["profile"] = profile["deployment"]["context"] = "personal"
        profile["deployment"]["ownership"] = "individual"
        profile["collaboration"].update(
            atlassian_identity="named-human",
            atlassian_principal="alice@example.com",
            atlassian_mcp_auth="oauth-2.1",
        )
        self.assertIn(
            "collaboration.atlassian_identity",
            {i.path for i in fleetctl.validate_profile(profile, ready=True)},
        )

    def test_atlassian_service_account_rejects_hyphenated_principal(self) -> None:
        profile = self.make_ready(self.load_work())
        profile["collaboration"].update(
            atlassian_identity="service-account",
            atlassian_principal="ac-agent-dev",
            atlassian_mcp_auth="service-account-api-key",
        )
        self.assertIn(
            "collaboration.atlassian_principal",
            {i.path for i in fleetctl.validate_profile(profile, ready=True)},
        )

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

    def test_identity_phase_uses_display_name_and_stable_identifiers(self) -> None:
        profile = self.make_ready(self.load_work())
        phase = fleetctl.phase_for(
            profile,
            "identity",
            apply=True,
            recovery=True,
            connection_context="local-console",
        )
        assert phase.command
        self.assertEqual(profile["machine"]["display_name"], phase.command[phase.command.index("--display-name") + 1])
        self.assertEqual(profile["machine"]["uuid"], phase.command[phase.command.index("--uuid") + 1])
        self.assertIn("--confirm-recovery-tested", phase.command)
        self.assertIn("--connection-context", phase.command)
        self.assertEqual("--apply", phase.command[-1])

    def test_init_generates_valid_unique_uuid_without_overwrite(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "ac-ws-042.toml"
            argv = ["fleetctl.py", "init", str(output), "--context", "work", "--namespace", "ac", "--hostname", "ac-ws-042", "--platform", "linux", "--human", "alice"]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(0, fleetctl.main())
            profile = fleetctl.load_profile(output)
            self.assertEqual(4, uuid.UUID(profile["machine"]["uuid"]).version)
            self.assertEqual("ac-ws-042", profile["machine"]["display_name"])
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())

    def test_init_rejects_more_admins_than_humans_before_write(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "ac-ws-043.toml"
            argv = ["fleetctl.py", "init", str(output), "--context", "work", "--namespace", "ac", "--hostname", "ac-ws-043", "--platform", "linux", "--human", "alice", "--admin", "admin-01", "--admin", "admin-02"]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse(output.exists())

    def test_init_rejects_invalid_display_name_before_write(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "ac-ws-044.toml"
            argv = [
                "fleetctl.py", "init", str(output), "--context", "work",
                "--namespace", "ac", "--hostname", "ac-ws-044",
                "--display-name", "Atlas\nInjected", "--platform", "linux",
                "--human", "alice",
            ]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse(output.exists())

    def test_init_rejects_duplicate_assigned_name_before_write(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            existing_path = root / "ac-ws-001.toml"
            existing_path.write_text(fleetctl.render_profile(argparse.Namespace(
                context="work", namespace="ac", hostname="ac-ws-001", display_name="Atlas",
                platform="linux", hardware_profile="generic", asset_tag="ask",
                human=["alice"], admin=["admin-01"], agent="agent-01",
            )), encoding="utf-8")
            output = root / "ac-ws-002.toml"
            argv = [
                "fleetctl.py", "init", str(output), "--context", "work",
                "--namespace", "ac", "--hostname", "ac-ws-002",
                "--display-name", "atlas", "--platform", "linux", "--human", "alice",
            ]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse(output.exists())

    def test_init_rejects_duplicate_hostname_even_with_a_different_filename(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            existing_path = root / "wrong-filename.toml"
            existing_path.write_text(fleetctl.render_profile(argparse.Namespace(
                context="work", namespace="ac", hostname="ac-ws-001", display_name="Atlas",
                platform="linux", hardware_profile="generic", asset_tag="ask",
                human=["alice"], admin=["admin-01"], agent="agent-01",
            )), encoding="utf-8")
            output = root / "ac-ws-002.toml"
            argv = [
                "fleetctl.py", "init", str(output), "--context", "work",
                "--namespace", "ac", "--hostname", "ac-ws-001",
                "--display-name", "Beacon", "--platform", "linux", "--human", "alice",
            ]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse(output.exists())

    def test_init_fails_closed_on_malformed_sibling_identity(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "broken.toml").write_text(
                'schema_version = 3\n[machine]\nhostname = "ac-ws-001"\ndisplay_name = 42\n',
                encoding="utf-8",
            )
            output = root / "ac-ws-002.toml"
            argv = [
                "fleetctl.py", "init", str(output), "--context", "work",
                "--namespace", "ac", "--hostname", "ac-ws-002",
                "--display-name", "Beacon", "--platform", "linux", "--human", "alice",
            ]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse(output.exists())

    def test_init_fails_closed_on_nested_profile(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            nested = root / "pilot"
            nested.mkdir()
            (nested / "ac-ws-001.toml").write_text("schema_version = 2\n", encoding="utf-8")
            output = root / "ac-ws-002.toml"
            argv = [
                "fleetctl.py", "init", str(output), "--context", "work",
                "--namespace", "ac", "--hostname", "ac-ws-002",
                "--display-name", "Beacon", "--platform", "linux", "--human", "alice",
            ]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse(output.exists())

    def test_init_with_fleet_root_resolves_output_and_checks_all_machine_names(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            machines = root / "machines"
            machines.mkdir()
            (root / "kit.lock").write_text(fleetctl.VERSION + "\n", encoding="utf-8")
            existing = machines / "ac-ws-001.toml"
            existing.write_text(fleetctl.render_profile(argparse.Namespace(
                context="work", namespace="ac", hostname="ac-ws-001", display_name="Atlas",
                platform="linux", hardware_profile="generic", asset_tag="ask",
                human=["alice"], admin=["admin-01"], agent="agent-01",
            )), encoding="utf-8")
            argv = [
                "fleetctl.py", "--fleet-root", str(root), "init", "machines/ac-ws-002.toml",
                "--context", "work", "--namespace", "ac", "--hostname", "ac-ws-002",
                "--display-name", "ATLAS", "--platform", "linux", "--human", "alice",
            ]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse((machines / "ac-ws-002.toml").exists())

    def test_init_with_fleet_root_rejects_retired_hostname(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "machines").mkdir()
            (root / "kit.lock").write_text(fleetctl.VERSION + "\n", encoding="utf-8")
            (root / "retired-hostnames.txt").write_text("ac-ws-002\n", encoding="utf-8")
            argv = [
                "fleetctl.py", "--fleet-root", str(root), "init", "machines/ac-ws-002.toml",
                "--context", "work", "--namespace", "ac", "--hostname", "ac-ws-002",
                "--display-name", "Beacon", "--platform", "linux", "--human", "alice",
            ]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse((root / "machines/ac-ws-002.toml").exists())

    def test_init_in_machines_directory_infers_retirement_ledger(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            machines = root / "machines"
            machines.mkdir()
            (root / "retired-hostnames.txt").write_text("ac-ws-002\n", encoding="utf-8")
            output = machines / "ac-ws-002.toml"
            argv = [
                "fleetctl.py", "init", str(output), "--context", "work",
                "--namespace", "ac", "--hostname", "ac-ws-002",
                "--display-name", "Beacon", "--platform", "linux", "--human", "alice",
            ]
            with mock.patch.object(sys, "argv", argv):
                self.assertEqual(2, fleetctl.main())
            self.assertFalse(output.exists())

    def test_allocation_lock_rejects_symlink(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "target"
            target.write_text("do not modify", encoding="utf-8")
            lock = root / ".fleetctl-identity.lock"
            lock.symlink_to(target)
            with self.assertRaises(OSError), fleetctl.fleet_identity_lock(lock):
                pass
            self.assertEqual("do not modify", target.read_text(encoding="utf-8"))

    def test_live_machine_names_handles_missing_platform_command(self) -> None:
        with mock.patch.object(fleetctl.subprocess, "run", side_effect=FileNotFoundError), \
             mock.patch.object(fleetctl.socket, "gethostname", return_value="fallback-host"):
            self.assertEqual(("fallback-host", None, "fallback-host"), fleetctl.live_machine_names("linux"))

    def test_identity_audit_accepts_exact_hardened_record(self) -> None:
        profile = self.make_ready(self.load_work())
        machine, deployment = profile["machine"], profile["deployment"]
        content = "\n".join(
            [
                "schema_version = 1",
                "",
                "[identity]",
                *(f'{key} = "{value}"' for key, value in {
                    "hostname": machine["hostname"],
                    "display_name": machine["display_name"],
                    "uuid": machine["uuid"],
                    "asset_tag": machine["asset_tag"],
                    "namespace": deployment["namespace"],
                    "platform": machine["platform"],
                    "role": machine["role"],
                }.items()),
                "",
            ]
        )
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "identity" / "identity.toml"
            target.parent.mkdir(mode=0o755)
            target.write_text(content, encoding="utf-8")
            target.chmod(0o644)
            original_stat = Path.stat

            def root_owned(path: Path, *args, **kwargs) -> os.stat_result:
                result = original_stat(path, *args, **kwargs)
                mode = result.st_mode
                if path == target:
                    mode = stat.S_IFREG | 0o644
                elif path == target.parent:
                    mode = stat.S_IFDIR | 0o755
                return os.stat_result((mode, result.st_ino, result.st_dev, result.st_nlink, 0, 0, result.st_size, result.st_atime, result.st_mtime, result.st_ctime))

            with mock.patch.object(fleetctl, "local_identity_path", return_value=target), \
                 mock.patch.object(fleetctl, "live_machine_names", return_value=(machine["hostname"], machine["display_name"], machine["hostname"])), \
                 mock.patch.object(fleetctl, "linux_hostname_resolves", return_value=True), \
                 mock.patch.object(Path, "stat", root_owned):
                self.assertEqual(0, fleetctl.audit_machine_identity(profile))

    def test_linux_identity_audit_rejects_runtime_or_resolution_drift(self) -> None:
        profile = self.make_ready(self.load_work())
        with tempfile.TemporaryDirectory() as directory:
            missing = Path(directory) / "identity.toml"
            with mock.patch.object(fleetctl, "local_identity_path", return_value=missing), \
                 mock.patch.object(fleetctl, "live_machine_names", return_value=(profile["machine"]["hostname"], profile["machine"]["display_name"], "old-host")), \
                 mock.patch.object(fleetctl, "linux_hostname_resolves", return_value=False):
                self.assertGreaterEqual(fleetctl.audit_machine_identity(profile), 2)

    def test_identity_audit_rejects_missing_or_symlinked_record(self) -> None:
        profile = self.make_ready(self.load_work())
        with tempfile.TemporaryDirectory() as directory:
            missing = Path(directory) / "identity" / "identity.toml"
            with mock.patch.object(fleetctl, "local_identity_path", return_value=missing), \
                 mock.patch.object(fleetctl, "live_machine_names", return_value=(profile["machine"]["hostname"], profile["machine"]["display_name"], None)):
                self.assertGreater(fleetctl.audit_machine_identity(profile), 0)
            missing.parent.mkdir()
            target = Path(directory) / "elsewhere.toml"
            target.write_text("schema_version = 1\n", encoding="utf-8")
            missing.symlink_to(target)
            with mock.patch.object(fleetctl, "local_identity_path", return_value=missing), \
                 mock.patch.object(fleetctl, "live_machine_names", return_value=(profile["machine"]["hostname"], profile["machine"]["display_name"], None)):
                self.assertGreater(fleetctl.audit_machine_identity(profile), 0)

    def test_macos_identity_audit_requires_localhostname(self) -> None:
        profile = self.make_ready(self.load_work())
        profile["machine"]["platform"] = "macos"
        profile["machine"]["os_family"] = "macos"
        profile["machine"]["hostname"] = "ac-mac-001"
        with tempfile.TemporaryDirectory() as directory:
            missing = Path(directory) / "identity.toml"
            with mock.patch.object(fleetctl, "local_identity_path", return_value=missing), \
                 mock.patch.object(fleetctl, "live_machine_names", return_value=("ac-mac-001", profile["machine"]["display_name"], "wrong-local")):
                self.assertGreaterEqual(fleetctl.audit_machine_identity(profile), 1)

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

    def test_identity_apply_requires_recovery_confirmation(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = ["fleetctl.py", "run", "ignored.toml", "identity", "--apply"]
        with mock.patch.object(sys, "argv", argv), mock.patch.object(fleetctl, "load_profile", return_value=profile), mock.patch.object(fleetctl, "actual_platform", return_value="linux"), mock.patch.object(fleetctl.subprocess, "run") as run:
            self.assertEqual(2, fleetctl.main())
            run.assert_not_called()

    def test_identity_apply_requires_explicit_connection_context(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = [
            "fleetctl.py", "run", "ignored.toml", "identity",
            "--apply", "--confirm-recovery-tested",
        ]
        with mock.patch.object(sys, "argv", argv), mock.patch.object(fleetctl, "load_profile", return_value=profile), mock.patch.object(fleetctl, "actual_platform", return_value="linux"), mock.patch.object(fleetctl.subprocess, "run") as run:
            self.assertEqual(2, fleetctl.main())
            run.assert_not_called()

    def test_linux_hostname_resolution_must_return_a_local_address(self) -> None:
        getent_local = subprocess.CompletedProcess([], 0, "192.0.2.10 ac-ws-001\n", "")
        ip_local = subprocess.CompletedProcess([], 0, "2: eth0    inet 192.0.2.10/24 brd 192.0.2.255 scope global eth0\n", "")
        with mock.patch.object(fleetctl.subprocess, "run", side_effect=(getent_local, ip_local)):
            self.assertTrue(fleetctl.linux_hostname_resolves("ac-ws-001"))

        getent_foreign = subprocess.CompletedProcess([], 0, "203.0.113.77 ac-ws-001.example\n", "")
        with mock.patch.object(fleetctl.subprocess, "run", side_effect=(getent_foreign, ip_local)):
            self.assertFalse(fleetctl.linux_hostname_resolves("ac-ws-001"))

        getent_loopback = subprocess.CompletedProcess([], 0, "127.0.1.1 ac-ws-001\n", "")
        with mock.patch.object(fleetctl.subprocess, "run", side_effect=(getent_loopback, ip_local)):
            self.assertTrue(fleetctl.linux_hostname_resolves("ac-ws-001"))

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
