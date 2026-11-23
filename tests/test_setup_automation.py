from __future__ import annotations
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts/lib"))
import package_setup


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / filename)
    value = importlib.util.module_from_spec(spec)
    sys.modules[name] = value
    spec.loader.exec_module(value)
    return value


execution = module("execute_setup", "execute-setup.py")
remote = module("verify_remote_access", "verify-remote-access.py")


class PackageAutomationTests(unittest.TestCase):
    def test_preview_does_not_mutate_and_repeat_run_skips_installed(self):
        packages = {"apt": ["git", "jq"]}
        with mock.patch.object(package_setup.sys, "platform", "linux"), mock.patch.object(package_setup, "inventory", return_value={"apt": {"git": "1.0"}}), mock.patch.object(package_setup, "run") as run, mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(0, package_setup.provision("linux", packages))
            run.assert_not_called()
        with mock.patch.object(package_setup.sys, "platform", "linux"), mock.patch.object(package_setup.os, "geteuid", return_value=0), mock.patch.object(package_setup, "require_staged_root"), mock.patch.object(package_setup, "inventory", side_effect=[{"apt": {"git": "1.0"}}, {"apt": {"git": "1.0", "jq": "2.0"}}]), mock.patch.object(package_setup, "run", return_value=subprocess.CompletedProcess([], 0)) as run, mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(0, package_setup.provision("linux", packages, apply=True))
            self.assertEqual(["apt-get", "install", "-y", "jq"], run.call_args_list[1].args[0])
        with mock.patch.object(package_setup.sys, "platform", "linux"), mock.patch.object(package_setup.os, "geteuid", return_value=0), mock.patch.object(package_setup, "require_staged_root"), mock.patch.object(package_setup, "inventory", return_value={"apt": {"git": "1.0", "jq": "2.0"}}), mock.patch.object(package_setup, "run") as run, mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(0, package_setup.provision("linux", packages, apply=True))
            run.assert_not_called()

    def test_success_exit_without_installed_package_is_a_failure(self):
        with mock.patch.object(package_setup.sys, "platform", "linux"), mock.patch.object(package_setup.os, "geteuid", return_value=0), mock.patch.object(package_setup, "require_staged_root"), mock.patch.object(package_setup, "inventory", return_value={"apt": {}}), mock.patch.object(package_setup, "run", return_value=subprocess.CompletedProcess([], 0)), mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(1, package_setup.provision("linux", {"apt": ["jq"]}, apply=True))

    def test_pinned_existing_version_conflict_never_upgrades(self):
        with self.assertRaisesRegex(ValueError, "separately"):
            package_setup.missing_packages({"apt": ["git=2.0"]}, {"apt": {"git": "1.0"}})

    def test_macos_casks_only_install_missing_and_never_update(self):
        packages = {"formulae": ["git"], "casks": ["orbstack"]}
        with mock.patch.object(package_setup.sys, "platform", "darwin"), mock.patch.object(package_setup.os, "geteuid", return_value=501), mock.patch.object(package_setup, "inventory", side_effect=[{"formulae": {"git": "2.0"}, "casks": {}}, {"formulae": {"git": "2.0"}, "casks": {"orbstack": "1.0"}}]), mock.patch.object(package_setup, "run", return_value=subprocess.CompletedProcess([], 0)) as run, mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(0, package_setup.provision("macos", packages, apply=True))
            self.assertEqual(["brew", "install", "--cask", "orbstack"], run.call_args.args[0])
            self.assertEqual(1, run.call_count)

    def test_package_names_cannot_inject_options_urls_or_taps(self):
        for value in ("--force", "https://example.invalid/tool", "tap/tools/package", "pkg;id", "pkg\nother"):
            self.assertFalse(package_setup.valid_package(value, "formulae"))
            self.assertFalse(package_setup.valid_package(value, "apt"))


class ExecutionAutomationTests(unittest.TestCase):
    def plan(self):
        return {"profile": {"state": "approved", "machine": {"hostname": "lab-ws-001", "platform": "linux"}, "accounts": {"agent": "agent-01"}}, "user_python": sys.executable,
                "source_files": {"scripts/bootstrap-linux.sh": "one", "scripts/install-user-tooling.sh": "two", "docs/example.md": "three"},
                "phases": [{"name": "base", "owner": "root", "command": ["KIT/scripts/bootstrap-linux.sh", "--apply"]}, {"name": "user-tooling", "owner": "agent", "command": ["KIT/scripts/install-user-tooling.sh", "--agents", "--apply"]}]}

    def test_failure_persists_and_resume_runs_only_incomplete_phase(self):
        plan = self.plan()
        journal = {}
        first_run = mock.Mock(side_effect=[subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], 7)])
        verifier = mock.Mock(return_value=True)
        with tempfile.TemporaryDirectory() as directory, mock.patch("sys.stdout", new=io.StringIO()):
            journal_path = Path(directory) / "run.json"
            journal["_journal_path"] = str(journal_path)
            self.assertEqual(1, execution.execute(plan, plan["phases"], journal, runner=first_run, verifier=verifier, binding="host-a"))
            saved = json.loads(journal_path.read_text())
            self.assertEqual("verified", saved["executions"]["base"]["status"])
            self.assertEqual("failed", saved["executions"]["user-tooling"]["status"])
            retry = mock.Mock(return_value=subprocess.CompletedProcess([], 0))
            self.assertEqual(0, execution.execute(plan, plan["phases"], saved, runner=retry, verifier=verifier, binding="host-a"))
            self.assertEqual(2, retry.call_count)  # preview and apply only the incomplete phase
            self.assertTrue(all("install-user-tooling.sh" in call.args[0][1] for call in retry.call_args_list))
            repeat = mock.Mock()
            self.assertEqual(0, execution.execute(plan, plan["phases"], saved, runner=repeat, verifier=verifier, binding="host-a"))
            repeat.assert_not_called()

    def test_phases_inherit_the_held_journal_lock(self):
        plan = self.plan()
        runner = mock.Mock(return_value=subprocess.CompletedProcess([], 0))
        with tempfile.TemporaryDirectory() as directory, mock.patch("sys.stdout", new=io.StringIO()):
            with execution.journal_lock(Path(directory) / "run.json"):
                held = tuple(execution.HELD_LOCKS)
                execution.execute(plan, plan["phases"][:1], {}, runner=runner, verifier=lambda *_: True, binding="host-a")
        self.assertEqual(1, len(held))
        self.assertEqual(2, runner.call_count)  # preview and apply
        self.assertTrue(all(call.kwargs["pass_fds"] == held for call in runner.call_args_list))
        self.assertEqual([], execution.HELD_LOCKS)

    def test_lock_outlives_a_killed_executor_until_its_phase_exits(self):
        # The executor takes the lock, starts a long phase with the inherited
        # descriptor, then dies without cleanup (as with kill -9).
        script = (
            "import importlib.util, os, subprocess, sys\n"
            "from pathlib import Path\n"
            "spec = importlib.util.spec_from_file_location('ex', sys.argv[1])\n"
            "ex = importlib.util.module_from_spec(spec); spec.loader.exec_module(ex)\n"
            "with ex.journal_lock(Path(sys.argv[2])):\n"
            "    phase = subprocess.Popen(['sleep', '30'], pass_fds=tuple(ex.HELD_LOCKS),\n"
            "                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)\n"
            "    print(phase.pid, flush=True)\n"
            "    os._exit(0)\n"
        )
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "run.json"
            executor = subprocess.run([sys.executable, "-c", script, str(ROOT / "scripts/execute-setup.py"), str(journal)],
                                      capture_output=True, text=True, check=True, timeout=30)
            phase = int(executor.stdout.split()[0])
            try:
                with self.assertRaisesRegex(ValueError, "left running, holds this account's journal; .*lsof .*run.json.lock"):
                    with execution.journal_lock(journal):
                        pass
            finally:
                os.kill(phase, 9)
            for _ in range(50):
                try:
                    os.kill(phase, 0)
                except ProcessLookupError:
                    break
                __import__("time").sleep(0.1)
            with execution.journal_lock(journal):
                pass  # free again once the orphaned phase has exited

    def test_live_drift_invalidates_receipt_even_when_input_matches(self):
        plan = self.plan()
        phase = plan["phases"][0]
        journal = {}
        runner = mock.Mock(return_value=subprocess.CompletedProcess([], 0))
        with mock.patch("sys.stdout", new=io.StringIO()):
            execution.execute(plan, [phase], journal, runner=runner, verifier=lambda *_: True, binding="host-a")
            runner.reset_mock()
            execution.execute(plan, [phase], journal, runner=runner, verifier=mock.Mock(side_effect=[False, True]), binding="host-a")
        self.assertEqual(2, runner.call_count)

    def test_failed_preview_never_applies_or_runs_later_phases(self):
        plan = self.plan()
        runner = mock.Mock(return_value=subprocess.CompletedProcess([], 8))
        with mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(1, execution.execute(plan, plan["phases"], {}, runner=runner, verifier=lambda *_: True, binding="host-a"))
        self.assertEqual(1, runner.call_count)
        self.assertNotIn("--apply", runner.call_args.args[0])

    def test_manual_phase_is_a_gate_not_false_success(self):
        phase = {"name": "resources", "command": None, "note": "human policy"}
        with mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(3, execution.execute(self.plan(), [phase], {}, runner=mock.Mock(), binding="host-a"))

    def test_unrelated_docs_change_preserves_receipt_but_host_change_does_not(self):
        plan = self.plan()
        phase = plan["phases"][0]
        key = execution.phase_input(plan, phase, "host-a", "alice")
        plan["source_files"]["docs/example.md"] = "changed"
        self.assertEqual(key, execution.phase_input(plan, phase, "host-a", "alice"))
        self.assertNotEqual(key, execution.phase_input(plan, phase, "host-b", "alice"))
        plan["source_files"]["scripts/bootstrap-linux.sh"] = "changed"
        self.assertNotEqual(key, execution.phase_input(plan, phase, "host-a", "alice"))

    def test_source_tampering_and_path_escape_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "script"
            source.write_text("reviewed")
            plan = {"source_files": {"script": execution.file_digest(source)}}
            execution.verify_source(plan, root)
            source.write_text("changed")
            with self.assertRaisesRegex(ValueError, "source changed"):
                execution.verify_source(plan, root)
            with self.assertRaises(ValueError):
                execution.verify_source({"source_files": {"../outside": "digest"}}, root)
        with self.assertRaisesRegex(ValueError, "approved kit script"):
            execution.command_for({"name": "base", "command": ["/bin/sh", "-c", "arbitrary"]})
        with self.assertRaisesRegex(ValueError, "invalid phase command"):
            execution.command_for({"name": "base", "command": []})

    def test_secure_journal_preserves_existing_file_and_rejects_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            path = parent / "run.json"
            execution.prepare_journal(path)
            execution.write_journal(path, {"executions": {}})
            self.assertEqual(0o600, path.stat().st_mode & 0o777)
            link = parent / "link.json"
            link.symlink_to(path)
            with self.assertRaises(ValueError):
                execution.prepare_journal(link)

    def test_macos_host_lock_uses_application_support_run_dir(self):
        self.assertEqual(
            Path("/Library/Application Support/agent-workstation-kit/run"),
            execution.MACOS_LOCK_ROOT / "agent-workstation-kit" / "run",
        )
        if sys.platform == "darwin":
            # Proves the real macOS parent chain passes secure_root_path, unlike /var/run.
            execution.secure_root_path(execution.MACOS_LOCK_ROOT)

    def test_macos_host_apply_lock_creates_root_owned_run_dir_and_serializes(self):
        original_lstat = Path.lstat
        original_fstat = os.fstat

        def root_stat_result(result):
            return os.stat_result((
                result.st_mode, result.st_ino, result.st_dev, result.st_nlink,
                0, 0, result.st_size, result.st_atime, result.st_mtime, result.st_ctime,
            ))

        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory).resolve()

            def fake_lstat(path, *args, **kwargs):
                return root_stat_result(original_lstat(path, *args, **kwargs))

            def fake_fstat(fd):
                return root_stat_result(original_fstat(fd))

            with mock.patch.object(execution.sys, "platform", "darwin"), \
                 mock.patch.object(execution.os, "geteuid", return_value=0), \
                 mock.patch.object(execution, "MACOS_LOCK_ROOT", base), \
                 mock.patch.object(Path, "lstat", fake_lstat), \
                 mock.patch.object(execution.os, "fstat", fake_fstat):
                with execution.host_apply_lock():
                    top = base / "agent-workstation-kit"
                    run_dir = top / "run"
                    lock_file = run_dir / "privileged-setup.lock"
                    self.assertTrue(top.is_dir())
                    self.assertEqual(0o755, top.lstat().st_mode & 0o777)
                    self.assertTrue(run_dir.is_dir())
                    self.assertEqual(0o755, run_dir.lstat().st_mode & 0o777)
                    self.assertTrue(lock_file.is_file())
                    self.assertEqual(0o600, lock_file.lstat().st_mode & 0o777)
                    with self.assertRaisesRegex(ValueError, "host's privileged setup lock"):
                        with execution.host_apply_lock():
                            self.fail("concurrent macOS host lock must not succeed")

    def test_macos_host_apply_lock_refuses_unsafe_existing_directories(self):
        original_lstat = Path.lstat

        def root_stat_result(result, uid=0):
            return os.stat_result((
                result.st_mode, result.st_ino, result.st_dev, result.st_nlink,
                uid, 0, result.st_size, result.st_atime, result.st_mtime, result.st_ctime,
            ))

        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory).resolve()

            # Group-writable existing parent directory is refused.
            top = base / "agent-workstation-kit"
            top.mkdir(mode=0o775)
            top.chmod(0o775)
            with mock.patch.object(execution.sys, "platform", "darwin"), \
                 mock.patch.object(execution.os, "geteuid", return_value=0), \
                 mock.patch.object(execution, "MACOS_LOCK_ROOT", base), \
                 mock.patch.object(Path, "lstat", lambda p, *a, **k: root_stat_result(original_lstat(p, *a, **k))):
                with self.assertRaisesRegex(ValueError, "root-owned and not group/world writable"):
                    with execution.host_apply_lock():
                        pass
            self.assertFalse((top / "run").exists())

            # Non-root-owned existing parent directory is refused.
            top.chmod(0o755)
            with mock.patch.object(execution.sys, "platform", "darwin"), \
                 mock.patch.object(execution.os, "geteuid", return_value=0), \
                 mock.patch.object(execution, "MACOS_LOCK_ROOT", base), \
                 mock.patch.object(Path, "lstat", lambda p, *a, **k: root_stat_result(original_lstat(p, *a, **k), uid=501 if p == top else 0)):
                with self.assertRaisesRegex(ValueError, "root-owned and not group/world writable"):
                    with execution.host_apply_lock():
                        pass
            self.assertFalse((top / "run").exists())

            # Symlinked run directory is refused.
            target = base / "other"
            target.mkdir(mode=0o755)
            (top / "run").symlink_to(target)
            with mock.patch.object(execution.sys, "platform", "darwin"), \
                 mock.patch.object(execution.os, "geteuid", return_value=0), \
                 mock.patch.object(execution, "MACOS_LOCK_ROOT", base), \
                 mock.patch.object(Path, "lstat", lambda p, *a, **k: root_stat_result(original_lstat(p, *a, **k))):
                with self.assertRaisesRegex(ValueError, "regular directory|root-owned"):
                    with execution.host_apply_lock():
                        pass
            self.assertFalse((target / "privileged-setup.lock").exists())


class RemoteAutomationTests(unittest.TestCase):
    def test_tcp_failure_stops_before_any_authentication(self):
        with mock.patch.object(remote.socket, "create_connection", side_effect=OSError("denied")), mock.patch.object(remote, "invoke") as invoke:
            results = remote.check_remote("lab-mac-001", [5900], "alice", transfer=True)
        invoke.assert_not_called()
        self.assertTrue(all(result["status"] == "fail" for result in results))

    def test_authenticated_sftp_roundtrip_and_independent_reconnect(self):
        commands = []
        def invoke(command):
            commands.append(command)
            if command[0] == "sftp":
                batch = Path(command[command.index("-b") + 1]).read_text().splitlines()
                import shlex
                upload = Path(shlex.split(batch[0])[1])
                download = Path(shlex.split(batch[1])[2])
                download.write_bytes(upload.read_bytes())
                return subprocess.CompletedProcess(command, 0, "", "")
            return subprocess.CompletedProcess(command, 0, "alice\n" if command[-1] == "id -un" else "", "")
        with mock.patch.object(remote.socket, "create_connection", return_value=mock.MagicMock()), mock.patch.object(remote, "invoke", side_effect=invoke):
            results = remote.check_remote("lab-mac-001", [5900], "alice", transfer=True)
        self.assertTrue(all(result["status"] == "pass" for result in results))
        self.assertEqual(2, sum(command[-1] == "id -un" for command in commands))
        self.assertTrue(any(command[0] == "sftp" for command in commands))
        self.assertTrue(all("ControlPath=none" in command for command in commands))

    def test_wrong_named_user_does_not_transfer(self):
        with mock.patch.object(remote.socket, "create_connection", return_value=mock.MagicMock()), mock.patch.object(remote, "invoke", return_value=subprocess.CompletedProcess([], 0, "other-user\n", "")) as invoke:
            results = remote.check_remote("lab-mac-001", [], "alice", transfer=True)
        self.assertEqual(1, invoke.call_count)
        self.assertEqual("fail", results[-1]["status"])

    def test_preview_never_connects_or_authenticates(self):
        with mock.patch.object(sys, "argv", ["verify-remote-access.py", "--host", "lab-mac-001", "--ssh-user", "alice", "--sftp-roundtrip"]), mock.patch.object(remote, "check_remote") as check, mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(0, remote.main())
            check.assert_not_called()

    def test_hosts_cannot_embed_credentials_options_or_pairing_links(self):
        for value in ("-oProxyCommand=bad", "orca://pair?secret", "alice@host", "host;command", "host/path"):
            self.assertFalse(remote.valid_host(value))


class MacShellCompatibilityTests(unittest.TestCase):
    def test_shell_baseline_preview_with_system_bash_preserves_home(self):
        with tempfile.TemporaryDirectory() as directory:
            env = os.environ.copy()
            env["HOME"] = directory
            result = subprocess.run(["/bin/bash", str(ROOT / "scripts/install-shell-baseline.sh"), "--antidote-ref", "v1.9.10"], env=env, text=True, capture_output=True)
            self.assertEqual(0, result.returncode, result.stderr)
            self.assertIn("git clone", result.stdout)
            self.assertEqual([], list(Path(directory).iterdir()))

    def test_local_accounts_preview_and_managed_gate_use_no_vendor_auth(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            for name, script in {"uname": "echo Darwin", "dscl": "exit 1", "dseditgroup": "exit 1"}.items():
                path = folder / name
                path.write_text("#!/bin/sh\n" + script + "\n")
                path.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = str(folder) + ":" + env["PATH"]
            command = ["/bin/bash", str(ROOT / "scripts/setup-accounts-macos.sh"), "--agent", "agent-01", "--human", "alice", "--admin", "admin-01"]
            local = subprocess.run([*command, "--management", "local"], env=env, capture_output=True, text=True)
            self.assertEqual(0, local.returncode, local.stderr)
            self.assertIn("sysadminctl", local.stdout)
            self.assertIn("-password -", local.stdout)
            managed = subprocess.run([*command, "--management", "managed"], env=env, capture_output=True, text=True)
            self.assertNotEqual(0, managed.returncode)
            self.assertNotIn("RUN:", managed.stdout)


mise_installer = module("install_mise", "install-mise.py")


class MiseInstallerTests(unittest.TestCase):
    def test_os_and_architecture_are_explicit(self):
        self.assertEqual("mise-v2026.10.4-linux-arm64", mise_installer.asset_name("2026.10.4", "Linux", "aarch64"))
        self.assertEqual("mise-v2026.10.4-macos-x64", mise_installer.asset_name("2026.10.4", "Darwin", "x86_64"))
        with self.assertRaises(ValueError):
            mise_installer.asset_name("latest", "Linux", "x86_64")

    def test_preview_never_downloads(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(mise_installer.Path, "home", return_value=Path(directory)), mock.patch.object(mise_installer.shutil, "which", return_value=None), mock.patch.object(mise_installer.urllib.request, "urlopen") as download, mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(0, mise_installer.install("2026.10.4", "0" * 64))
            download.assert_not_called()
            self.assertEqual([], list(Path(directory).iterdir()))

    def test_checksum_failure_never_executes_or_installs_download(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(mise_installer.Path, "home", return_value=Path(directory)), mock.patch.object(mise_installer.shutil, "which", return_value=None), mock.patch.object(mise_installer.urllib.request, "urlopen") as download, mock.patch.object(mise_installer.subprocess, "run") as run, mock.patch.object(mise_installer.platform, "system", return_value="Linux"), mock.patch("sys.stdout", new=io.StringIO()):
            download.return_value.__enter__.return_value.read.side_effect = [b"not approved", b""]
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                mise_installer.install("2026.10.4", "0" * 64, apply=True)
            run.assert_not_called()
            self.assertFalse((Path(directory) / ".local/bin/mise").exists())

    def test_existing_different_binary_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(mise_installer.Path, "home", return_value=Path(directory)):
            target = Path(directory) / ".local/bin/mise"
            target.parent.mkdir(parents=True)
            target.write_bytes(b"user-owned original")
            with self.assertRaisesRegex(ValueError, "no automatic replacement"):
                mise_installer.install("2026.10.4", "0" * 64, apply=True)
            self.assertEqual(b"user-owned original", target.read_bytes())


class ExecutionJourneyTests(unittest.TestCase):
    def test_real_process_preview_apply_resume_and_drift_in_disposable_kit(self):
        if os.geteuid() == 0 or sys.platform != "darwin":
            self.skipTest("fixture exercises the native Mac non-root package-owner path; Linux root gates use isolated unit tests")
        import shutil
        import pwd
        with tempfile.TemporaryDirectory(prefix="setup-journey-") as directory:
            folder = Path(directory)
            kit = folder / "kit"
            scripts = kit / "scripts"
            (scripts / "lib").mkdir(parents=True)
            shutil.copyfile(ROOT / "scripts/execute-setup.py", scripts / "execute-setup.py")
            shutil.copyfile(ROOT / "scripts/lib/readiness.py", scripts / "lib/readiness.py")
            shutil.copyfile(ROOT / "scripts/lib/terminal_ui.py", scripts / "lib/terminal_ui.py")
            shutil.copyfile(ROOT / "scripts/lib/setup_scopes.py", scripts / "lib/setup_scopes.py")
            shutil.copyfile(ROOT / "scripts/lib/path_safety.py", scripts / "lib/path_safety.py")
            # The executed fixture installer writes only inside this disposable kit.
            install = scripts / "bootstrap-macos.sh"
            install.write_text('#!/bin/bash\nroot=$(cd "$(dirname "$0")/.." && pwd)\nif [[ "${1:-}" == --apply ]]; then printf "applied\\n" >> "$root/actions.txt"; touch "$root/installed"; else echo fixture-preview; fi\n')
            (scripts / "lib/phase_verification.py").write_text('def verify_phase(plan, name, root):\n    return (root / "installed").is_file()\ndef resource_values(profile, total):\n    return {}\n')
            source_files = {str(path.relative_to(kit)): execution.file_digest(path) for path in kit.rglob("*") if path.is_file()}
            platform = "macos" if sys.platform == "darwin" else "linux"
            phase = {"name": "base", "owner": "package-owner", "command": ["KIT/scripts/bootstrap-macos.sh", "--apply"], "note": "disposable fixture only"}
            plan = {"schema": 1, "profile": {"state": "approved", "machine": {"hostname": "lab-mac-001", "platform": platform}, "accounts": {"agent": pwd.getpwuid(os.geteuid()).pw_name}}, "phases": [phase], "source_files": source_files, "user_python": sys.executable}
            plan_file = folder / "plan.json"
            plan_file.write_text(json.dumps(plan))
            state_dir = folder / "state"
            state_dir.mkdir(mode=0o700)
            journal = state_dir / "run.json"
            command = [sys.executable, str(scripts / "execute-setup.py"), "--plan-file", str(plan_file), "--scope", "packages", "--journal", str(journal)]
            env = os.environ.copy()
            for key in ("CODEX_SANDBOX", "SANDBOX_EXECUTION_ID", "container"):
                env.pop(key, None)
            preview = subprocess.run(command, env=env, text=True, capture_output=True)
            self.assertEqual(0, preview.returncode, preview.stderr)
            self.assertFalse(journal.exists())
            self.assertFalse((kit / "installed").exists())
            approval = next(line.partition(": ")[2] for line in preview.stdout.splitlines() if line.startswith("Execution plan ID:"))
            apply = [*command, "--apply", "--approve-plan", approval, "--confirm-target", "lab-mac-001", "--execution-context", "host"]
            rejected = subprocess.run([*command, "--apply", "--approve-plan", "wrong", "--confirm-target", "lab-mac-001", "--execution-context", "host"], env=env, text=True, capture_output=True)
            self.assertNotEqual(0, rejected.returncode)
            self.assertFalse((kit / "installed").exists())
            first = subprocess.run(apply, env=env, text=True, capture_output=True)
            self.assertEqual(0, first.returncode, first.stderr)
            self.assertEqual("applied\n", (kit / "actions.txt").read_text())
            self.assertEqual("verified", json.loads(journal.read_text())["executions"]["base"]["status"])
            again = subprocess.run(apply, env=env, text=True, capture_output=True)
            self.assertEqual(0, again.returncode, again.stderr)
            self.assertIn("SKIP base", again.stdout)
            self.assertEqual("applied\n", (kit / "actions.txt").read_text())
            (kit / "installed").unlink()
            repaired = subprocess.run(apply, env=env, text=True, capture_output=True)
            self.assertEqual(0, repaired.returncode, repaired.stderr)
            self.assertIn("DRIFT base", repaired.stdout)
            self.assertEqual("applied\napplied\n", (kit / "actions.txt").read_text())
            install.write_text(install.read_text() + "# changed after approval\n")
            changed = subprocess.run(apply, env=env, text=True, capture_output=True)
            self.assertNotEqual(0, changed.returncode)
            self.assertEqual("applied\napplied\n", (kit / "actions.txt").read_text())


class SSHInstallGuardTests(unittest.TestCase):
    def test_fresh_openssh_is_guarded_before_apt_and_checked_after(self):
        events = []
        def run(command, **kwargs):
            events.append(command)
            return subprocess.CompletedProcess(command, 0)
        def guard(**kwargs):
            events.append(["guard", kwargs["apply"]])
        with mock.patch.object(package_setup.sys, "platform", "linux"), mock.patch.object(package_setup.os, "geteuid", return_value=0), mock.patch.object(package_setup, "require_staged_root"), mock.patch.object(package_setup, "inventory", side_effect=[{"apt": {}}, {"apt": {"openssh-server": "1.0"}}]), mock.patch.object(package_setup, "guard_new_ssh_install", side_effect=guard), mock.patch.object(package_setup, "run", side_effect=run), mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(0, package_setup.provision("linux", {"apt": ["openssh-server"]}, apply=True))
        self.assertEqual(["guard", True], events[0])
        self.assertEqual("apt-get", events[1][0])

    def test_active_or_managed_ssh_is_not_masked_implicitly(self):
        guard = mock.Mock()
        guard.exists.return_value = False
        guard.is_symlink.return_value = False
        with mock.patch.object(package_setup, "SSH_GUARD", guard), mock.patch.object(package_setup, "run", return_value=subprocess.CompletedProcess([], 0)) as run:
            with self.assertRaisesRegex(ValueError, "active SSH"):
                package_setup.guard_new_ssh_install(apply=True)
            self.assertTrue(all(not call.kwargs.get("mutate") for call in run.call_args_list))

    def test_guard_release_requires_active_deny_by_default_firewall(self):
        guard = mock.Mock()
        guard.exists.return_value = True
        defaults = mock.Mock()
        defaults.read_text.return_value = 'DEFAULT_INPUT_POLICY="ACCEPT"\nDEFAULT_FORWARD_POLICY="DROP"\n'
        with mock.patch.object(package_setup, "SSH_GUARD", guard), mock.patch.object(package_setup, "_guard_record"), mock.patch.object(package_setup.os, "geteuid", return_value=0), mock.patch.object(package_setup, "require_staged_root"), mock.patch.object(package_setup, "Path", return_value=defaults), mock.patch.object(package_setup, "run", return_value=subprocess.CompletedProcess([], 0, "Status: active\n", "")) as run:
            with self.assertRaisesRegex(ValueError, "deny-by-default"):
                package_setup.release_ssh_guard(apply=True)
            self.assertEqual(1, run.call_count)
            guard.unlink.assert_not_called()


class MacWorkloadJourneyTests(unittest.TestCase):
    def test_native_shell_workload_commands_are_previewed_then_run_in_fixture(self):
        if os.geteuid() == 0:
            self.skipTest("workload helper intentionally rejects root")
        import shutil
        with tempfile.TemporaryDirectory(prefix="mac-workload-") as directory:
            folder = Path(directory)
            scripts = folder / "kit/scripts"
            (scripts / "lib").mkdir(parents=True)
            shutil.copyfile(ROOT / "scripts/lib/common.sh", scripts / "lib/common.sh")
            app = folder / "OrbStack.app"
            app.mkdir()
            helper = scripts / "check-project-workloads.sh"
            helper.write_text((ROOT / "scripts/check-project-workloads.sh").read_text().replace("'/Applications/OrbStack.app'", repr(str(app))))
            binaries = folder / "bin"
            binaries.mkdir()
            events = folder / "events.txt"
            for name, body in {
                "uname": "echo Darwin",
                "id": 'if [ "${1:-}" = -un ]; then echo agent-01; else echo 501; fi',
                "docker": 'echo "docker $*" >> "$EVENTS"',
                "node": 'echo "node $*" >> "$EVENTS"',
            }.items():
                file = binaries / name
                file.write_text("#!/bin/sh\n" + body + "\n")
                file.chmod(0o755)
            project = folder / "project"
            (project / "node_modules/.bin").mkdir(parents=True)
            playwright = project / "node_modules/.bin/playwright"
            playwright.write_text('#!/bin/sh\necho "playwright $*" >> "$EVENTS"\n')
            playwright.chmod(0o755)
            (project / "package-lock.json").write_text("{}\n")
            env = os.environ.copy()
            env.update(PATH=str(binaries) + ":" + env["PATH"], EVENTS=str(events))
            command = ["/bin/bash", str(helper), "--agent", "agent-01", "--project", str(project), "--container-image", "example.invalid/test@sha256:" + "0" * 64]
            preview = subprocess.run(command, env=env, text=True, capture_output=True)
            self.assertEqual(0, preview.returncode, preview.stderr)
            self.assertFalse(events.exists())
            for timeout in ("-1", "301", "999999999999999999999999"):
                invalid = subprocess.run([*command, "--engine-timeout", timeout], env=env, text=True, capture_output=True)
                self.assertNotEqual(0, invalid.returncode)
                self.assertIn("Engine timeout must be", invalid.stderr)
            leading_zero = subprocess.run([*command, "--engine-timeout", "008"], env=env, text=True, capture_output=True)
            self.assertEqual(0, leading_zero.returncode, leading_zero.stderr)
            self.assertFalse(events.exists())
            applied = subprocess.run([*command, "--apply"], env=env, text=True, capture_output=True)
            self.assertEqual(0, applied.returncode, applied.stderr)
            ran = events.read_text()
            self.assertIn("docker --context orbstack info", ran)
            self.assertIn("playwright install chromium", ran)
            self.assertIn("node -e", ran)
            self.assertIn("--network none --read-only --cap-drop ALL", ran)
            self.assertNotIn("context use", ran)
            events.unlink()
            (project / "package-lock.json").unlink()
            unlocked = subprocess.run([*command, "--apply"], env=env, text=True, capture_output=True)
            self.assertNotEqual(0, unlocked.returncode)
            self.assertFalse(events.exists())


class HomebrewInventoryRegressionTests(unittest.TestCase):
    def test_unrelated_removed_cask_does_not_block_selected_packages(self):
        calls = []
        def run(command, **kwargs):
            calls.append(command)
            if command == ["brew", "list", "--formula"]:
                return subprocess.CompletedProcess(command, 0, "git\n", "")
            if command == ["brew", "list", "--formula", "--versions", "git"]:
                return subprocess.CompletedProcess(command, 0, "git 2.0\n", "")
            if command == ["brew", "list", "--cask"]:
                return subprocess.CompletedProcess(command, 0, "powershell\ngoogle-chrome\n", "")
            if command == ["brew", "list", "--cask", "--versions", "google-chrome"]:
                return subprocess.CompletedProcess(command, 0, "google-chrome 1.0\n", "")
            return subprocess.CompletedProcess(command, 1, "", "unrelated removed cask")
        with mock.patch.object(package_setup, "run", side_effect=run):
            installed = package_setup.inventory("macos", {"formulae": ["git"], "casks": ["google-chrome", "orbstack"]})
        self.assertEqual({"git": "2.0"}, installed["formulae"])
        self.assertEqual({"google-chrome": "1.0"}, installed["casks"])
        self.assertFalse(any("powershell" in command for command in calls))


class ResumeEdgeCaseTests(unittest.TestCase):
    def test_interrupted_apply_never_records_success_or_starts_later_phase(self):
        base = ExecutionAutomationTests()
        plan = base.plan()
        journal = {}
        runner = mock.Mock(side_effect=[subprocess.CompletedProcess([], 0), KeyboardInterrupt])
        with mock.patch("sys.stdout", new=io.StringIO()):
            with self.assertRaises(KeyboardInterrupt):
                execution.execute(plan, plan["phases"], journal, runner=runner, verifier=lambda *_: True, binding="fixture-host")
        self.assertEqual(2, runner.call_count)
        self.assertEqual({}, journal["executions"])

    def test_duplicate_execution_lock_has_actionable_error(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "run.json"
            with mock.patch.object(execution.fcntl, "flock", side_effect=BlockingIOError):
                with self.assertRaisesRegex(ValueError, "another setup process"):
                    with execution.journal_lock(path):
                        self.fail("lock must not be acquired")

    def test_service_identity_is_never_silently_omitted_from_linux_account_plan(self):
        import importlib.util
        spec = importlib.util.spec_from_file_location("service_fleetctl", ROOT / "scripts/fleetctl.py")
        fleet = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = fleet
        spec.loader.exec_module(fleet)
        profile = fleet.load_profile(ROOT / "config/profiles/work.example.toml")
        profile["accounts"]["services"] = ["svc-backup"]
        phase = fleet.phase_for(profile, "accounts", apply=False, recovery=False)
        self.assertIn("--service", phase.command)
        self.assertIn("svc-backup", phase.command)
        profile["accounts"]["humans"].append("svc-backup")
        self.assertIn("accounts.services", {issue.path for issue in fleet.validate_profile(profile, ready=False)})


class ScriptArgumentSafetyTests(unittest.TestCase):
    def test_plugin_ref_cannot_be_treated_as_a_git_option(self):
        with tempfile.TemporaryDirectory() as directory:
            env = os.environ.copy()
            env["HOME"] = directory
            result = subprocess.run(["/bin/bash", str(ROOT / "scripts/install-shell-baseline.sh"), "--antidote-ref", "--all"], env=env, text=True, capture_output=True)
            self.assertNotEqual(0, result.returncode)
            self.assertNotIn("RUN:", result.stdout)
            self.assertEqual([], list(Path(directory).iterdir()))


class SupportedImageTests(unittest.TestCase):
    def test_non_ubuntu_or_unqualified_release_stops_before_apply(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "os-release"
            for text, accepted in (('ID=ubuntu\nVERSION_ID="24.04"\n', True), ('ID=ubuntu\nVERSION_ID="26.04"\n', True), ('ID=fedora\nVERSION_ID="44"\n', False), ('ID=ubuntu\nVERSION_ID="22.04"\n', False)):
                path.write_text(text)
                if accepted:
                    execution.require_supported_linux(path)
                else:
                    with self.assertRaisesRegex(ValueError, "qualification"):
                        execution.require_supported_linux(path)


class LinuxProjectWorkloadTests(unittest.TestCase):
    def test_same_entry_helper_checks_rootless_podman_and_rejects_rootful_engine(self):
        if os.geteuid() == 0:
            self.skipTest("project workloads intentionally reject root")
        import shutil
        with tempfile.TemporaryDirectory(prefix="linux-project-workload-") as directory:
            folder = Path(directory)
            scripts = folder / "scripts"
            (scripts / "lib").mkdir(parents=True)
            shutil.copyfile(ROOT / "scripts/lib/common.sh", scripts / "lib/common.sh")
            shutil.copyfile(ROOT / "scripts/check-project-workloads.sh", scripts / "check-project-workloads.sh")
            binaries = folder / "bin"
            binaries.mkdir()
            events = folder / "events.txt"
            for name, content in {
                "uname": "echo Linux",
                "id": 'if [ "${1:-}" = -un ]; then echo agent-01; else echo 1000; fi',
                "podman": 'echo "podman $*" >> "$EVENTS"; if [ "${1:-}" = --remote=false ]; then shift; fi; if [ "${1:-}" = info ]; then echo "$ROOTLESS_MODE"; fi',
            }.items():
                path = binaries / name
                path.write_text("#!/bin/sh\n" + content + "\n")
                path.chmod(0o755)
            env = os.environ.copy()
            env.update(PATH=str(binaries) + ":" + env["PATH"], EVENTS=str(events), ROOTLESS_MODE="true")
            command = ["/bin/bash", str(scripts / "check-project-workloads.sh"), "--agent", "agent-01", "--container-image", "example.invalid/test@sha256:" + "0" * 64]
            preview = subprocess.run(command, env=env, text=True, capture_output=True)
            self.assertEqual(0, preview.returncode, preview.stderr)
            self.assertFalse(events.exists())
            for timeout in ("-1", "301", "999999999999999999999999"):
                invalid = subprocess.run([*command, "--engine-timeout", timeout], env=env, text=True, capture_output=True)
                self.assertNotEqual(0, invalid.returncode)
                self.assertIn("Engine timeout must be", invalid.stderr)
            leading_zero = subprocess.run([*command, "--engine-timeout", "008"], env=env, text=True, capture_output=True)
            self.assertEqual(0, leading_zero.returncode, leading_zero.stderr)
            self.assertFalse(events.exists())
            applied = subprocess.run([*command, "--apply"], env=env, text=True, capture_output=True)
            self.assertEqual(0, applied.returncode, applied.stderr)
            self.assertIn("podman --remote=false run --pull=missing", events.read_text())
            self.assertIn("--network none --read-only --cap-drop ALL", events.read_text())
            events.unlink()
            env["ROOTLESS_MODE"] = "false"
            rejected = subprocess.run([*command, "--apply"], env=env, text=True, capture_output=True)
            self.assertNotEqual(0, rejected.returncode)
            self.assertNotIn("podman --remote=false run", events.read_text())
