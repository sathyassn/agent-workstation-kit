#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import io
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/install-machine-identity.py"
SPEC = importlib.util.spec_from_file_location("machine_identity", SCRIPT)
assert SPEC and SPEC.loader
machine_identity = importlib.util.module_from_spec(SPEC)
sys.modules["machine_identity"] = machine_identity
SPEC.loader.exec_module(machine_identity)


class MachineIdentityTests(unittest.TestCase):
    def command(self, display_name: str) -> list[str]:
        return [
            str(SCRIPT),
            "--hostname", "ac-ws-001",
            "--display-name", display_name,
            "--uuid", "11111111-1111-4111-8111-111111111111",
            "--asset-tag", "AC-1",
            "--namespace", "ac",
            "--platform", "linux",
            "--role", "agent-workstation",
        ]

    def apply_command(self, display_name: str = "Atlas") -> list[str]:
        return [
            "install-machine-identity.py",
            *self.command(display_name)[1:],
            "--confirm-recovery-tested",
            "--connection-context", "local-console",
            "--apply",
        ]

    def test_preview_renders_non_secret_identity_without_mutation(self) -> None:
        result = subprocess.run(self.command("Atlas"), check=True, capture_output=True, text=True)
        self.assertIn('display_name = "Atlas"', result.stdout)
        self.assertIn("PREVIEW only", result.stdout)

    def test_control_character_in_display_name_is_rejected(self) -> None:
        result = subprocess.run(self.command("Atlas\n"), check=False, capture_output=True, text=True)
        self.assertEqual(2, result.returncode)
        self.assertIn("--display-name", result.stderr)

    def test_non_v4_uuid_is_rejected(self) -> None:
        command = self.command("Atlas")
        command[command.index("--uuid") + 1] = "not-a-uuid"
        result = subprocess.run(command, check=False, capture_output=True, text=True)
        self.assertEqual(2, result.returncode)
        self.assertIn("--uuid", result.stderr)

    def test_deceptive_and_noncanonical_display_names_are_rejected(self) -> None:
        for value in ("-Atlas", "Atlas ", "Atlas  North", "At\u200blas", "At\u202elas", "Cafe\u0301", "Аtlas"):
            with self.subTest(value=value):
                result = subprocess.run(self.command(value), check=False, capture_output=True, text=True)
                self.assertEqual(2, result.returncode)

    def test_plain_identity_values_cannot_be_parsed_as_options(self) -> None:
        argv = ["install-machine-identity.py", *self.command("Atlas")[1:]]
        index = argv.index("--asset-tag")
        argv[index:index + 2] = ["--asset-tag=--apply"]
        with mock.patch.object(sys, "argv", argv), self.assertRaises(SystemExit) as raised:
            machine_identity.main()
        self.assertEqual(2, raised.exception.code)

    def test_namespace_must_match_hostname(self) -> None:
        command = self.command("Atlas")
        command[command.index("--namespace") + 1] = "zz"
        result = subprocess.run(command, check=False, capture_output=True, text=True)
        self.assertEqual(2, result.returncode)
        self.assertIn("hostname prefix", result.stderr)

    def test_apply_root_and_platform_gates_precede_mutation(self) -> None:
        argv = self.apply_command()
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(machine_identity.os, "geteuid", return_value=501), \
             mock.patch.object(machine_identity, "atomic_install") as install:
            self.assertEqual(2, machine_identity.main())
            install.assert_not_called()
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(machine_identity.os, "geteuid", return_value=0), \
             mock.patch.object(machine_identity, "actual_platform", return_value="macos"), \
             mock.patch.object(machine_identity, "atomic_install") as install:
            self.assertEqual(2, machine_identity.main())
            install.assert_not_called()

    def test_apply_requires_recovery_and_connection_context_in_installer(self) -> None:
        argv = ["install-machine-identity.py", *self.command("Atlas")[1:], "--apply"]
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(machine_identity.os, "geteuid", return_value=0), \
             mock.patch.object(machine_identity, "actual_platform", return_value="linux"), \
             mock.patch.object(machine_identity, "atomic_install") as install:
            self.assertEqual(2, machine_identity.main())
            install.assert_not_called()

    def test_tailscale_connection_context_verifies_reported_peer(self) -> None:
        success = subprocess.CompletedProcess([], 0, "user@example.com\n", "")
        with mock.patch.dict(machine_identity.os.environ, {"SSH_CONNECTION": "100.64.0.10 50000 100.64.0.20 22"}, clear=True), \
             mock.patch.object(machine_identity.subprocess, "run", return_value=success) as run:
            self.assertTrue(machine_identity.connection_context_is_valid("tailscale-ssh", "100.64.0.10"))
            run.assert_called_once_with(
                ["tailscale", "whois", "100.64.0.10"],
                check=False,
                text=True,
                capture_output=True,
            )
            self.assertFalse(machine_identity.connection_context_is_valid("tailscale-ssh", "100.64.0.11"))

    def test_local_console_context_rejects_observable_ssh_session(self) -> None:
        with mock.patch.dict(machine_identity.os.environ, {"SSH_CONNECTION": "100.64.0.10 50000 100.64.0.20 22"}, clear=True):
            self.assertFalse(machine_identity.connection_context_is_valid("local-console", None))

    def test_local_console_rejects_remote_ancestor_after_sudo_strips_environment(self) -> None:
        with mock.patch.dict(machine_identity.os.environ, {}, clear=True), \
             mock.patch.object(machine_identity, "remote_login_ancestor_detected", return_value=True):
            self.assertFalse(machine_identity.connection_context_is_valid("local-console", None))

    def test_local_console_fails_closed_when_ancestry_cannot_be_inspected(self) -> None:
        with mock.patch.dict(machine_identity.os.environ, {}, clear=True), \
             mock.patch.object(machine_identity, "remote_login_ancestor_detected", return_value=None):
            self.assertFalse(machine_identity.connection_context_is_valid("local-console", None))

    def test_local_console_accepts_inspected_non_remote_ancestry(self) -> None:
        with mock.patch.dict(machine_identity.os.environ, {}, clear=True), \
             mock.patch.object(machine_identity, "remote_login_ancestor_detected", return_value=False):
            self.assertTrue(machine_identity.connection_context_is_valid("local-console", None))

    def test_atomic_install_hardens_directory_and_file(self) -> None:
        with self.subTest("permissions"):
            with tempfile.TemporaryDirectory() as directory:
                target = Path(directory) / "identity" / "identity.toml"
                with mock.patch.object(machine_identity.os, "chown") as chown:
                    machine_identity.atomic_install(target, "schema_version = 1\n")
                self.assertEqual(0o755, target.parent.stat().st_mode & 0o777)
                self.assertEqual(0o644, target.stat().st_mode & 0o777)
                self.assertEqual("schema_version = 1\n", target.read_text())
                self.assertGreaterEqual(chown.call_count, 2)
                self.assertIn(mock.call(target.parent, 0, 0), chown.call_args_list)
                self.assertTrue(any(call.args[1:] == (0, 0) and Path(call.args[0]).parent == target.parent for call in chown.call_args_list))

    def test_missing_platform_command_fails_before_install(self) -> None:
        argv = self.apply_command()
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(machine_identity.os, "geteuid", return_value=0), \
             mock.patch.object(machine_identity, "actual_platform", return_value="linux"), \
             mock.patch.object(machine_identity.shutil, "which", return_value=None), \
             mock.patch.object(machine_identity, "atomic_install") as install:
            self.assertEqual(2, machine_identity.main())
            install.assert_not_called()

    def test_manifest_survives_os_naming_failure(self) -> None:
        argv = self.apply_command()
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "identity" / "identity.toml"
            with mock.patch.object(sys, "argv", argv), \
                 mock.patch.dict(machine_identity.TARGETS, {"linux": target}), \
                 mock.patch.object(machine_identity.os, "geteuid", return_value=0), \
                 mock.patch.object(machine_identity.os, "chown"), \
                 mock.patch.object(machine_identity, "actual_platform", return_value="linux"), \
                 mock.patch.object(machine_identity.shutil, "which", return_value="/usr/bin/hostnamectl"), \
                 mock.patch.object(machine_identity, "linux_hosts_conflicts", return_value=None), \
                 mock.patch.object(machine_identity, "set_os_names", side_effect=subprocess.CalledProcessError(1, ["hostnamectl"])):
                self.assertEqual(1, machine_identity.main())
            self.assertTrue(target.is_file())
            self.assertIn('display_name = "Atlas"', target.read_text(encoding="utf-8"))

    def test_install_failure_does_not_claim_a_record_exists(self) -> None:
        argv = self.apply_command()
        stderr = io.StringIO()
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(machine_identity.os, "geteuid", return_value=0), \
             mock.patch.object(machine_identity, "actual_platform", return_value="linux"), \
             mock.patch.object(machine_identity.shutil, "which", return_value="/usr/bin/hostnamectl"), \
             mock.patch.object(machine_identity, "linux_hosts_conflicts", return_value=None), \
             mock.patch.object(machine_identity, "atomic_install", side_effect=OSError("read-only filesystem")), \
             mock.patch.object(machine_identity, "set_os_names") as set_names, redirect_stderr(stderr):
            self.assertEqual(1, machine_identity.main())
            set_names.assert_not_called()
        self.assertIn("record was not installed", stderr.getvalue())
        self.assertIn("no OS naming command was attempted", stderr.getvalue())

    def test_linux_hosts_conflict_is_detected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            hosts = Path(directory) / "hosts"
            hosts.write_text("127.0.1.1 old-host old-host.local\n", encoding="utf-8")
            with mock.patch.object(machine_identity.socket, "gethostname", return_value="old-host"):
                self.assertEqual("old-host", machine_identity.linux_hosts_conflicts("ac-ws-001", hosts))

    def test_linux_hosts_conflict_blocks_apply_before_install(self) -> None:
        argv = self.apply_command()
        stderr = io.StringIO()
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(machine_identity.os, "geteuid", return_value=0), \
             mock.patch.object(machine_identity, "actual_platform", return_value="linux"), \
             mock.patch.object(machine_identity.shutil, "which", return_value="/usr/bin/hostnamectl"), \
             mock.patch.object(machine_identity, "linux_hosts_conflicts", return_value="old-host"), \
             mock.patch.object(machine_identity, "atomic_install") as install, redirect_stderr(stderr):
            self.assertEqual(2, machine_identity.main())
            install.assert_not_called()
        self.assertIn("old-host", stderr.getvalue())

    def test_linux_sets_static_transient_and_pretty_names(self) -> None:
        args = mock.Mock(platform="linux", hostname="ac-ws-001", display_name="Atlas")
        with mock.patch.object(machine_identity.subprocess, "run") as run:
            machine_identity.set_os_names(args)
        self.assertEqual(
            [
                mock.call(["hostnamectl", "set-hostname", "ac-ws-001", "--static"], check=True),
                mock.call(["hostnamectl", "set-hostname", "ac-ws-001", "--transient"], check=True),
                mock.call(["hostnamectl", "set-hostname", "Atlas", "--pretty"], check=True),
            ],
            run.call_args_list,
        )

    def test_linux_resolution_accepts_local_and_rejects_foreign_addresses(self) -> None:
        local = subprocess.CompletedProcess([], 0, "192.0.2.10 ac-ws-001\n", "")
        foreign = subprocess.CompletedProcess([], 0, "203.0.113.77 ac-ws-001.example\n", "")
        interfaces = subprocess.CompletedProcess(
            [], 0, "2: eth0    inet 192.0.2.10/24 brd 192.0.2.255 scope global eth0\n", ""
        )
        with mock.patch.object(machine_identity.subprocess, "run", side_effect=(local, interfaces)):
            self.assertTrue(machine_identity.linux_hostname_resolves_locally("ac-ws-001"))
        with mock.patch.object(machine_identity.subprocess, "run", side_effect=(foreign, interfaces)):
            self.assertFalse(machine_identity.linux_hostname_resolves_locally("ac-ws-001"))

    def test_post_rename_resolution_failure_returns_recovery_status(self) -> None:
        argv = self.apply_command()
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "identity" / "identity.toml"
            with mock.patch.object(sys, "argv", argv), \
                 mock.patch.dict(machine_identity.TARGETS, {"linux": target}), \
                 mock.patch.object(machine_identity.os, "geteuid", return_value=0), \
                 mock.patch.object(machine_identity.os, "chown"), \
                 mock.patch.object(machine_identity, "actual_platform", return_value="linux"), \
                 mock.patch.object(machine_identity.shutil, "which", return_value="/usr/bin/tool"), \
                 mock.patch.object(machine_identity, "linux_hosts_conflicts", return_value=None), \
                 mock.patch.object(machine_identity, "set_os_names"), \
                 mock.patch.object(machine_identity, "linux_hostname_resolves_locally", return_value=False):
                self.assertEqual(1, machine_identity.main())
            self.assertTrue(target.is_file())


if __name__ == "__main__":
    unittest.main()
