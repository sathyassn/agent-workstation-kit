#!/usr/bin/env python3

from __future__ import annotations

import argparse
import copy
import importlib.util
import io
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

    def test_optional_package_selections_are_validated_and_rendered(self) -> None:
        profile = self.make_ready(self.load_work())
        profile["tooling"]["apt_packages"] = ["ripgrep", "sqlite3=3.0"]
        self.assertEqual([], fleetctl.validate_profile(profile, ready=True))
        phase = fleetctl.phase_for(profile, "base", apply=False, recovery=False)
        self.assertIn("--apt", phase.command)
        self.assertIn("sqlite3=3.0", phase.command)
        for bad in (["--force"], ["https://example.invalid/package"], ["ripgrep", "ripgrep"]):
            profile["tooling"]["apt_packages"] = bad
            self.assertIn("tooling.apt_packages", {issue.path for issue in fleetctl.validate_profile(profile, ready=True)})

    def test_mac_account_and_workload_phases_now_have_commands(self) -> None:
        profile = fleetctl.load_profile(ROOT / "config/profiles/macos-work.example.toml")
        profile["accounts"]["macos_provisioning"] = "local"
        accounts = fleetctl.phase_for(profile, "accounts", apply=False, recovery=False)
        self.assertEqual("setup-accounts-macos.sh", Path(accounts.command[0]).name)
        self.assertIn("local", accounts.command)
        workload = fleetctl.phase_for(profile, "workloads", apply=False, recovery=False)
        self.assertEqual("check-project-workloads.sh", Path(workload.command[0]).name)
        self.assertFalse(workload.privileged)
        profile["tooling"]["apt_packages"] = ["ripgrep"]
        self.assertIn("tooling.apt_packages", {issue.path for issue in fleetctl.validate_profile(profile, ready=False)})

    def test_examples_are_valid_drafts(self) -> None:
        examples = sorted((ROOT / "config/profiles").glob("*.example.toml"))
        self.assertGreaterEqual(len(examples), 3)
        for path in examples:
            self.assertEqual([], fleetctl.validate_profile(fleetctl.load_profile(path), ready=False), path)

    def test_guided_setup_renders_platform_specific_draft_without_writing(self) -> None:
        for template, guide in (
            ("work.example.toml", "day-zero-linux.md"),
            ("macos-work.example.toml", "day-zero-macos.md"),
        ):
            with self.subTest(template=template), tempfile.TemporaryDirectory() as directory:
                fleet = Path(directory)
                (fleet / "kit.lock").write_text(fleetctl.VERSION, encoding="utf-8")
                content = (ROOT / "config/profiles" / template).read_text(encoding="utf-8")
                profile = fleetctl.load_profile(ROOT / "config/profiles" / template)
                hostname = profile["machine"]["hostname"]
                target = fleet / "machines" / f"{hostname}.toml"
                target.parent.mkdir()
                target.write_text(content, encoding="utf-8")
                command = [
                    sys.executable, str(ROOT / "scripts/fleetctl.py"),
                    "--fleet-root", directory, "setup", str(target), "--handoff-draft",
                ]
                result = subprocess.run(command, check=True, capture_output=True, text=True)
                self.assertTrue(result.stdout.startswith(f"# {hostname} "))
                self.assertIn(guide, result.stdout)
                self.assertFalse((fleet / "handoffs").exists())
                wrong_path = target.with_name("wrong.toml")
                wrong_path.write_text(content, encoding="utf-8")
                command[-2] = str(wrong_path)
                result = subprocess.run(command, capture_output=True, text=True)
                self.assertEqual(2, result.returncode)
                self.assertIn(str(target.relative_to(fleet)), result.stderr)

    def readiness_fixture(self, directory: str, template: str = "work.example.toml"):
        fleet = Path(directory)
        profile = fleetctl.load_profile(ROOT / "config/profiles" / template)
        hostname = profile["machine"]["hostname"]
        target = fleet / "machines" / f"{hostname}.toml"
        target.parent.mkdir()
        target.write_text((ROOT / "config/profiles" / template).read_text(encoding="utf-8"), encoding="utf-8")
        (fleet / "handoffs").mkdir()
        (fleet / "handoffs" / f"{hostname}-bootstrap.md").write_text("# reviewed\n", encoding="utf-8")
        args = argparse.Namespace(
            fleet_root=fleet, handoff_draft=False, expected_ubuntu=None,
            kit_revision=None, fleet_revision=None, execution_context="host",
        )
        return args, target, profile

    def run_readiness(self, args, target, profile, *, platform="linux", euid=1000,
                      committed=0, launcher_code=0):
        """Run readiness with host facts stubbed; return (exit code, launcher argv)."""
        launched = []

        def run(command, **kwargs):
            if command[:2] == ["git", "-C"] and "cat-file" in command:
                return subprocess.CompletedProcess(command, committed)
            launched.append(command)
            return subprocess.CompletedProcess(command, launcher_code)

        with mock.patch.object(fleetctl, "actual_platform", return_value=platform), \
                mock.patch.object(fleetctl.os, "geteuid", return_value=euid), \
                mock.patch.object(fleetctl, "setup_revision", return_value="a" * 40), \
                mock.patch.object(fleetctl.subprocess, "run", side_effect=run), \
                mock.patch("sys.stdout"):
            code = fleetctl.guided_setup(args, target, profile)
        return code, launched

    def test_readiness_forwards_context_release_and_exit_code(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            args, target, profile = self.readiness_fixture(directory)
            args.expected_ubuntu = "26.04"
            code, launched = self.run_readiness(args, target, profile, launcher_code=1)
            self.assertEqual(1, code)
            self.assertEqual(1, len(launched))
            command = launched[0]
            self.assertEqual("start-linux-pilot.py", Path(command[1]).name)
            self.assertEqual("host", command[command.index("--execution-context") + 1])
            self.assertEqual("26.04", command[command.index("--expected-ubuntu") + 1])
            args.execution_context = "unknown"
            _, launched = self.run_readiness(args, target, profile)
            self.assertEqual("unknown", launched[0][launched[0].index("--execution-context") + 1])

    def test_readiness_refuses_wrong_host_root_and_unreviewed_handoff(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            args, target, profile = self.readiness_fixture(directory)
            handoff = target.parents[1] / "handoffs" / f"{profile['machine']['hostname']}-bootstrap.md"
            for kwargs, message in (
                ({"platform": "macos"}, "selected linux workstation"),
                ({"euid": 0}, "non-root"),
                ({"committed": 1}, "commit the reviewed handoff"),
            ):
                with self.subTest(**kwargs), self.assertRaisesRegex(ValueError, message):
                    self.run_readiness(args, target, profile, **kwargs)
            handoff.unlink()
            with self.assertRaisesRegex(ValueError, "review and commit"):
                self.run_readiness(args, target, profile)
            handoff.symlink_to(target)
            with self.assertRaisesRegex(ValueError, "review and commit"):
                self.run_readiness(args, target, profile)

    def test_ubuntu_release_is_refused_for_macos_profiles(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            args, target, profile = self.readiness_fixture(directory, "macos-work.example.toml")
            args.expected_ubuntu = "24.04"
            with self.assertRaisesRegex(ValueError, "Linux profiles"):
                self.run_readiness(args, target, profile, platform="macos")

    def test_setup_options_are_validated_before_any_check(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            base = [sys.executable, str(ROOT / "scripts/fleetctl.py"), "--fleet-root", directory,
                    "setup", str(fleet / "machines/x.toml")]
            for extra, message in (
                (["--expected-ubuntu", "24.4"], "YY.MM"),
                (["--kit-revision", "abc"], "full lowercase"),
                (["--handoff-draft", "--fleet-revision", "a" * 40], "readiness only"),
                (["--execution-context", "laptop"], "invalid choice"),
            ):
                with self.subTest(extra=extra):
                    result = subprocess.run([*base, *extra], capture_output=True, text=True)
                    self.assertEqual(2, result.returncode)
                    self.assertIn(message, result.stderr)

    def test_setup_refuses_a_fleet_root_inside_the_public_kit(self) -> None:
        result = subprocess.run(
            [sys.executable, str(ROOT / "scripts/fleetctl.py"), "--fleet-root", str(ROOT / "templates/private-fleet"),
             "setup", "machines/acme-ws-001.toml", "--handoff-draft"],
            capture_output=True, text=True)
        self.assertEqual(2, result.returncode)
        self.assertIn("outside every public toolkit worktree", result.stderr)

    def test_every_fleet_entry_point_refuses_a_fleet_inside_the_kit(self) -> None:
        inside = str(ROOT / "templates/private-fleet")
        commands = (
            [str(ROOT / "scripts/fleetctl.py"), "--fleet-root", inside, "init", "machines/acme-ws-009.toml",
             "--context", "work", "--namespace", "acme", "--hostname", "acme-ws-009", "--platform", "linux",
             "--human", "alice", "--admin", "admin-01"],
            [str(ROOT / "scripts/start-linux-pilot.py"), "--fleet-root", inside, "--profile", "machines/x.toml"],
            [str(ROOT / "scripts/start-macos-pilot.py"), "--fleet-root", inside, "--profile", "machines/x.toml"],
        )
        for command in commands:
            with self.subTest(script=Path(command[0]).name):
                result = subprocess.run([sys.executable, *command], capture_output=True, text=True)
                self.assertEqual(2, result.returncode, result.stdout + result.stderr)
                self.assertIn("outside every public toolkit worktree", result.stderr)
        self.assertFalse((ROOT / "templates/private-fleet/machines/acme-ws-009.toml").exists())

    def test_handoff_draft_leaves_no_bytecode_in_the_kit(self) -> None:
        import shutil
        with tempfile.TemporaryDirectory() as directory:
            kit = Path(directory) / "kit"
            shutil.copytree(ROOT, kit, ignore=shutil.ignore_patterns(".git", "__pycache__", ".worktrees"))
            fleet = Path(directory) / "fleet"
            (fleet / "machines").mkdir(parents=True)
            (fleet / "kit.lock").write_text(fleetctl.VERSION, encoding="utf-8")
            profile = fleetctl.load_profile(ROOT / "config/profiles/work.example.toml")
            target = fleet / "machines" / f"{profile['machine']['hostname']}.toml"
            target.write_text((ROOT / "config/profiles/work.example.toml").read_text(encoding="utf-8"), encoding="utf-8")
            result = subprocess.run([sys.executable, str(kit / "scripts/fleetctl.py"), "--fleet-root", str(fleet),
                                     "setup", str(target), "--handoff-draft"], capture_output=True, text=True)
            self.assertEqual(0, result.returncode, result.stderr)
            self.assertEqual([], [str(path) for path in kit.rglob("__pycache__")])

    def test_draft_command_states_the_host_context(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            args, target, profile = self.readiness_fixture(directory)
            args.handoff_draft = True
            with mock.patch("sys.stdout", new_callable=__import__("io").StringIO) as output:
                self.assertEqual(0, fleetctl.guided_setup(args, target, profile))
            self.assertIn("--execution-context host", output.getvalue())

    def test_setup_revision_rejects_staged_subdirectory_and_wrong_pin(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            git = ["git", "-C", directory]
            subprocess.run([*git, "init", "-q"], check=True)
            subprocess.run(
                [*git, "-c", "user.name=Test", "-c", "user.email=test@example.com",
                 "commit", "--allow-empty", "-qm", "Fixture"],
                check=True,
            )
            revision = fleetctl.setup_revision(source, "source", None)
            self.assertEqual(revision, fleetctl.setup_revision(source, "source", revision))
            with self.assertRaisesRegex(ValueError, "expected"):
                fleetctl.setup_revision(source, "source", "0" * len(revision))
            staged = source / "staged"
            staged.mkdir()
            with self.assertRaisesRegex(ValueError, "use the reviewed source checkout"):
                fleetctl.setup_revision(staged, "source", None)
            (source / "local.txt").touch()
            with self.assertRaisesRegex(ValueError, "review local changes"):
                fleetctl.setup_revision(source, "source", revision)

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

    def test_resources_optional_overrides_are_validated_and_passed_only_when_set(self) -> None:
        profile = self.make_ready(self.load_work())
        default_phase = fleetctl.phase_for(profile, "resources", apply=False, recovery=False)
        assert default_phase.command
        for flag in ("--reserve-fixed-gib", "--reserve-cores", "--cpu-weight", "--memory-reserve-percent"):
            self.assertNotIn(flag, default_phase.command)

        profile["resources"]["reserve_fixed_gib"] = 8.5
        profile["resources"]["reserve_cores"] = 0
        profile["resources"]["cpu_weight"] = 75
        self.assertEqual([], fleetctl.validate_profile(profile, ready=True))
        override_phase = fleetctl.phase_for(profile, "resources", apply=False, recovery=False)
        assert override_phase.command
        self.assertEqual("8.5", override_phase.command[override_phase.command.index("--reserve-fixed-gib") + 1])
        self.assertEqual("0", override_phase.command[override_phase.command.index("--reserve-cores") + 1])
        self.assertEqual("75", override_phase.command[override_phase.command.index("--cpu-weight") + 1])
        self.assertNotIn("--memory-reserve-percent", override_phase.command)

        percent_profile = self.make_ready(self.load_work())
        percent_profile["resources"]["os_memory_reserve_percent"] = 15
        self.assertEqual([], fleetctl.validate_profile(percent_profile, ready=True))
        percent_phase = fleetctl.phase_for(percent_profile, "resources", apply=False, recovery=False)
        assert percent_phase.command
        self.assertEqual("15", percent_phase.command[percent_phase.command.index("--memory-reserve-percent") + 1])

        conflict = self.make_ready(self.load_work())
        conflict["resources"]["reserve_fixed_gib"] = 8
        conflict["resources"]["os_memory_reserve_percent"] = 10
        self.assertIn("resources.reserve_fixed_gib", {i.path for i in fleetctl.validate_profile(conflict, ready=True)})

        for key, bad_values in (
            ("reserve_fixed_gib", (0, -2, True, "8", float("inf"), float("nan"), 1_000_000, 18446744073709551666)),
            ("reserve_cores", (-1, 1.5, True, "2", 1025, 18446744073709551666)),
            ("cpu_weight", (0, 10001, -5, 50.5, False, "50")),
        ):
            for bad in bad_values:
                invalid = self.make_ready(self.load_work())
                invalid["resources"][key] = bad
                with self.subTest(key=key, value=bad):
                    self.assertIn(f"resources.{key}", {i.path for i in fleetctl.validate_profile(invalid, ready=True)})

    def test_example_profiles_name_the_role_defaults_and_call_other_numbers_overrides(self) -> None:
        for name in ("work", "personal", "macos-work"):
            text = (ROOT / f"config/profiles/{name}.example.toml").read_text(encoding="utf-8")
            line = next(line for line in text.splitlines() if line.startswith("# reserve_fixed_gib"))
            with self.subTest(profile=name):
                self.assertIn("8 GiB desktop", line)
                self.assertIn("2 GiB headless", line)
                self.assertIn("any other number is an override", line)
                self.assertNotIn("6 desktop", line)
                weight = next(line for line in text.splitlines() if line.startswith("# cpu_weight"))
                self.assertIn("override for agent.slice", weight)

    def test_profile_migration_keeps_sizing_overrides_and_exempts_only_the_reserve_rename(self) -> None:
        spec = importlib.util.spec_from_file_location("check_profile_migration", ROOT / "scripts/check-profile-migration.py")
        assert spec and spec.loader
        migration = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(migration)
        old = fleetctl.load_profile(ROOT / "tests/fixtures/schema2-work.toml")
        new = fleetctl.load_profile(ROOT / "config/profiles/work.example.toml")
        self.assertEqual([], migration.check_migration(old, new))
        for key, value in (("reserve_fixed_gib", 6), ("reserve_cores", 2), ("cpu_weight", 75)):
            changed = copy.deepcopy(new)
            changed["resources"][key] = value
            with self.subTest(key=key):
                self.assertIn("resources changed during schema migration", migration.check_migration(old, changed))
        renamed = copy.deepcopy(new)
        renamed["resources"].pop("os_memory_reserve_gib", None)
        renamed["resources"]["os_memory_reserve_percent"] = 15
        self.assertEqual([], migration.check_migration(old, renamed))

    def test_run_apply_exits_2_and_prints_executor_commands(self) -> None:
        import shlex

        def parse_printed_commands(output: str) -> tuple[list[list[str]], list[list[str]]]:
            plan_cmds: list[list[str]] = []
            run_cmds: list[list[str]] = []
            for raw_line in output.splitlines():
                stripped = raw_line.strip()
                if stripped.startswith("scripts/setup-workstation.sh plan "):
                    plan_cmds.append(shlex.split(stripped))
                elif stripped.startswith((
                    "scripts/setup-workstation.sh run ",
                    "sudo /bin/bash /opt/agent-workstation-kit/scripts/setup-workstation.sh run ",
                )):
                    run_cmds.append(shlex.split(stripped))
            return plan_cmds, run_cmds

        def parse_plan_flags(argv: list[str]) -> argparse.Namespace:
            self.assertEqual(["scripts/setup-workstation.sh", "plan"], argv[:2])
            parser = argparse.ArgumentParser()
            parser.add_argument("--fleet-root", required=True)
            parser.add_argument("--profile", required=True)
            parser.add_argument("--export-plan", required=True)
            parser.add_argument("--confirm-recovery-tested", action="store_true")
            parser.add_argument("--connection-context", choices=("local-console", "tailscale-ssh"))
            parser.add_argument("--ssh-source-ip")
            return parser.parse_args(argv[2:])

        def parse_run_flags(argv: list[str]) -> tuple[bool, argparse.Namespace]:
            if argv[:4] == [
                "sudo", "/bin/bash",
                "/opt/agent-workstation-kit/scripts/setup-workstation.sh", "run",
            ]:
                is_root = True
                rest = argv[4:]
            else:
                self.assertEqual(["scripts/setup-workstation.sh", "run"], argv[:2])
                is_root = False
                rest = argv[2:]
            parser = argparse.ArgumentParser()
            parser.add_argument("--plan-file", required=True)
            parser.add_argument("--scope", choices=("packages", "machine", "agent", "resources", "audit"), required=True)
            parser.add_argument("--apply", action="store_true")
            parser.add_argument("--approve-plan")
            parser.add_argument("--confirm-target")
            parser.add_argument("--confirm-recovery-tested", action="store_true")
            parser.add_argument("--execution-context", choices=("host", "sandbox", "unknown"))
            parser.add_argument("--journal")
            return is_root, parser.parse_args(rest)

        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            (fleet / "machines").mkdir()
            (fleet / "kit.lock").write_text(fleetctl.VERSION + "\n", encoding="utf-8")
            linux_profile_path = fleet / "machines/ac-ws-001.toml"
            macos_profile_path = fleet / "machines/ac-mac-001.toml"
            linux_profile_path.write_text(
                (ROOT / "config/profiles/work.example.toml").read_text(encoding="utf-8"),
                encoding="utf-8",
            )
            macos_profile_path.write_text(
                (ROOT / "config/profiles/macos-work.example.toml").read_text(encoding="utf-8"),
                encoding="utf-8",
            )
            linux_profile = fleetctl.load_profile(linux_profile_path)
            macos_profile = fleetctl.load_profile(macos_profile_path)

            cases = (
                ("linux", linux_profile_path, linux_profile, "base", [], "packages", True),
                ("linux", linux_profile_path, linux_profile, "accounts", [], "machine", True),
                (
                    "linux", linux_profile_path, linux_profile, "identity",
                    ["--confirm-recovery-tested", "--connection-context", "local-console"],
                    "machine", True,
                ),
                (
                    "linux", linux_profile_path, linux_profile, "remote-hardening",
                    ["--confirm-recovery-tested", "--connection-context", "tailscale-ssh"],
                    "machine", True,
                ),
                (
                    "linux", linux_profile_path, linux_profile, "remote-hardening",
                    ["--confirm-recovery-tested", "--connection-context", "tailscale-ssh", "--ssh-source-ip", "100.64.0.10"],
                    "machine", True,
                ),
                ("linux", linux_profile_path, linux_profile, "agentctl", [], "machine", True),
                ("linux", linux_profile_path, linux_profile, "workloads", [], "machine", True),
                ("linux", linux_profile_path, linux_profile, "shell", [], "agent", False),
                ("linux", linux_profile_path, linux_profile, "audit", [], "audit", True),
                ("macos", macos_profile_path, macos_profile, "base", [], "packages", False),
                ("macos", macos_profile_path, macos_profile, "identity", [], "machine", True),
                ("macos", macos_profile_path, macos_profile, "workloads", [], "agent", False),
            )
            for platform, profile_path, profile, phase_name, extra, expected_scope, expected_root in cases:
                with self.subTest(platform=platform, phase=phase_name, extra=extra):
                    result = subprocess.run(
                        [
                            sys.executable, str(ROOT / "scripts/fleetctl.py"),
                            "--fleet-root", str(fleet),
                            "run", str(profile_path.relative_to(fleet)), phase_name, "--apply", *extra,
                        ],
                        capture_output=True, text=True, check=False,
                    )
                    self.assertEqual(2, result.returncode, result.stdout + result.stderr)
                    plan_cmds, run_cmds = parse_printed_commands(result.stderr)
                    self.assertGreaterEqual(len(plan_cmds), 1, result.stderr)
                    parsed_plans = [parse_plan_flags(cmd) for cmd in plan_cmds]
                    for parsed_plan in parsed_plans:
                        self.assertEqual(str(fleet), parsed_plan.fleet_root)
                        self.assertEqual(str(profile_path.relative_to(fleet)), parsed_plan.profile)
                        self.assertTrue(parsed_plan.confirm_recovery_tested)
                        self.assertIn(parsed_plan.connection_context, ("local-console", "tailscale-ssh"))
                        # Verify the parsed plan flags generate valid apply arguments for identity/remote-hardening.
                        id_phase = fleetctl.phase_for(
                            profile, "identity", apply=True,
                            recovery=parsed_plan.confirm_recovery_tested,
                            connection_context=parsed_plan.connection_context,
                            ssh_source_ip=parsed_plan.ssh_source_ip,
                        )
                        assert id_phase.command
                        self.assertIn("--confirm-recovery-tested", id_phase.command)
                        self.assertIn("--connection-context", id_phase.command)
                        self.assertEqual(
                            parsed_plan.connection_context,
                            id_phase.command[id_phase.command.index("--connection-context") + 1],
                        )
                        if parsed_plan.connection_context == "tailscale-ssh":
                            self.assertTrue(parsed_plan.ssh_source_ip)
                            self.assertIn("--ssh-source-ip", id_phase.command)
                            self.assertEqual(
                                parsed_plan.ssh_source_ip,
                                id_phase.command[id_phase.command.index("--ssh-source-ip") + 1],
                            )
                            if platform == "linux":
                                rem_phase = fleetctl.phase_for(
                                    profile, "remote-hardening", apply=True,
                                    recovery=parsed_plan.confirm_recovery_tested,
                                    connection_context=parsed_plan.connection_context,
                                    ssh_source_ip=parsed_plan.ssh_source_ip,
                                )
                                assert rem_phase.command
                                self.assertIn("--confirm-recovery-tested", rem_phase.command)
                                self.assertEqual(
                                    parsed_plan.ssh_source_ip,
                                    rem_phase.command[rem_phase.command.index("--ssh-source-ip") + 1],
                                )
                    if "--connection-context" not in extra:
                        contexts = {p.connection_context for p in parsed_plans}
                        self.assertEqual({"local-console", "tailscale-ssh"}, contexts)
                    elif "tailscale-ssh" in extra:
                        self.assertEqual(["tailscale-ssh"], [p.connection_context for p in parsed_plans])
                        expected_peer = "100.64.0.10" if "--ssh-source-ip" in extra else "SSH_SOURCE_IP"
                        self.assertEqual(expected_peer, parsed_plans[0].ssh_source_ip)

                    self.assertEqual(2, len(run_cmds), result.stderr)
                    preview_is_root, preview_args = parse_run_flags(run_cmds[0])
                    apply_is_root, apply_args = parse_run_flags(run_cmds[1])
                    self.assertEqual(expected_root, preview_is_root)
                    self.assertEqual(expected_root, apply_is_root)
                    self.assertFalse(preview_args.apply)
                    self.assertTrue(apply_args.apply)
                    self.assertEqual(expected_scope, preview_args.scope)
                    self.assertEqual(expected_scope, apply_args.scope)
                    self.assertEqual(profile["machine"]["hostname"], apply_args.confirm_target)
                    self.assertEqual("host", apply_args.execution_context)
                    self.assertTrue(apply_args.approve_plan)
                    self.assertTrue(apply_args.journal)
                    if expected_root:
                        self.assertEqual("/opt/agent-workstation-fleet/plans/execution-plan.json", preview_args.plan_file)
                        self.assertEqual("/opt/agent-workstation-fleet/plans/execution-plan.json", apply_args.plan_file)
                        self.assertTrue(apply_args.confirm_recovery_tested)
                    else:
                        self.assertFalse(apply_args.confirm_recovery_tested)

            # macOS agentctl is Linux-only: print the Linux-only error and no apply command.
            mac_agentctl = subprocess.run(
                [
                    sys.executable, str(ROOT / "scripts/fleetctl.py"),
                    "--fleet-root", str(fleet),
                    "run", str(macos_profile_path.relative_to(fleet)), "agentctl", "--apply",
                ],
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(2, mac_agentctl.returncode)
            self.assertIn("agentctl is Linux-only", mac_agentctl.stderr)
            self.assertNotIn("setup-workstation.sh", mac_agentctl.stderr)
            self.assertNotIn("--apply", mac_agentctl.stderr)

    def test_run_read_only_audit_still_runs_without_apply(self) -> None:
        profile = self.make_ready(self.load_work())
        argv = ["fleetctl.py", "run", "ignored.toml", "audit"]
        completed = subprocess.CompletedProcess([], 0)
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(fleetctl, "load_profile", return_value=profile), \
             mock.patch.object(fleetctl, "actual_platform", return_value="linux"), \
             mock.patch.object(fleetctl, "audit_machine_identity", return_value=0) as id_audit, \
             mock.patch.object(fleetctl, "audit_declared_accounts", return_value=0) as acct_audit, \
             mock.patch.object(fleetctl.subprocess, "run", return_value=completed) as run, \
             mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(0, fleetctl.main())
            id_audit.assert_called_once_with(profile)
            acct_audit.assert_called_once_with(profile)
            run.assert_called_once()
            self.assertNotIn("--apply", run.call_args.args[0])


if __name__ == "__main__":
    unittest.main()
