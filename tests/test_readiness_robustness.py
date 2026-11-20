"""Readiness entry points fail clearly when their environment is broken."""
from __future__ import annotations
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / filename)
    value = importlib.util.module_from_spec(spec)
    sys.modules[name] = value  # dataclasses look the module up while loading
    spec.loader.exec_module(value)
    return value


linux_pilot = module("start_linux_pilot_robustness", "start-linux-pilot.py")


class BrokenGitTests(unittest.TestCase):
    """`git worktree list` failing must give ERROR and exit 2, not a traceback."""

    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.stubs = Path(folder.name) / "bin"
        self.stubs.mkdir()
        real_git = subprocess.run(["sh", "-c", "command -v git"], capture_output=True, text=True, check=True).stdout.strip()
        (self.stubs / "git").write_text(
            "#!/bin/sh\n"
            'for argument; do [ "$argument" = worktree ] && { echo "fatal: broken" >&2; exit 128; }; done\n'
            f'exec "{real_git}" "$@"\n')
        (self.stubs / "git").chmod(0o755)
        self.fleet = Path(folder.name) / "fleet"
        (self.fleet / "machines").mkdir(parents=True)

    def run_entry(self, *arguments):
        environment = {**os.environ, "PATH": str(self.stubs) + os.pathsep + os.environ["PATH"]}
        return subprocess.run([sys.executable, *arguments], capture_output=True, text=True, env=environment, timeout=60)

    def test_every_fleet_entry_point_reports_the_failure(self):
        commands = (
            [str(ROOT / "scripts/fleetctl.py"), "--fleet-root", str(self.fleet), "setup", "machines/acme-ws-001.toml", "--handoff-draft"],
            [str(ROOT / "scripts/fleetctl.py"), "--fleet-root", str(self.fleet), "init", "machines/acme-ws-009.toml",
             "--context", "work", "--namespace", "acme", "--hostname", "acme-ws-009", "--platform", "linux",
             "--human", "alice", "--admin", "admin-01"],
            [str(ROOT / "scripts/start-linux-pilot.py"), "--fleet-root", str(self.fleet), "--profile", "machines/x.toml"],
            [str(ROOT / "scripts/start-macos-pilot.py"), "--fleet-root", str(self.fleet), "--profile", "machines/x.toml"],
        )
        for command in commands:
            with self.subTest(script=Path(command[0]).name, action=command[3] if len(command) > 3 else ""):
                result = self.run_entry(*command)
                self.assertEqual(2, result.returncode, result.stdout + result.stderr)
                self.assertIn("ERROR fleet root: cannot establish public worktree boundaries", result.stderr)
                self.assertNotIn("Traceback", result.stderr)
        self.assertFalse((self.fleet / "machines/acme-ws-009.toml").exists())


class MacHandoffInterpreterTests(unittest.TestCase):
    def test_macos_draft_uses_the_day_zero_python(self):
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory)
            version = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
            (fleet / "kit.lock").write_text(version, encoding="utf-8")
            template = ROOT / "config/profiles/macos-work.example.toml"
            hostname = next(line.split('"')[1] for line in template.read_text().splitlines() if line.startswith("hostname"))
            target = fleet / "machines" / f"{hostname}.toml"
            target.parent.mkdir()
            target.write_text(template.read_text(encoding="utf-8"), encoding="utf-8")
            result = subprocess.run([sys.executable, str(ROOT / "scripts/fleetctl.py"), "--fleet-root", directory,
                                     "setup", str(target), "--handoff-draft"], capture_output=True, text=True, check=True)
            self.assertIn('"$PYTHON_BIN" scripts/fleetctl.py --fleet-root "$FLEET_ROOT" setup', result.stdout)
            self.assertNotIn("\npython3 scripts/fleetctl.py", result.stdout)
            self.assertIn("PYTHON_BIN", result.stdout.split("```sh")[0])


class BashGateTests(unittest.TestCase):
    """`make bash-check` passes only for Bash 4 or newer."""

    def gate(self, shell):
        return subprocess.run(["make", "--no-print-directory", "-C", str(ROOT), "bash-check", "BASH_BIN=" + shell],
                              capture_output=True, text=True, timeout=60)

    def fake_bash(self, folder, prints):
        path = Path(folder) / ("bash-" + (prints or "empty"))
        path.write_text("#!/bin/sh\n" + (f"echo {prints}\n" if prints else "") + "exit 0\n")
        path.chmod(0o755)
        return str(path)

    def test_non_bash_and_old_or_silent_bash_fail(self):
        with tempfile.TemporaryDirectory() as folder:
            for shell in ("/bin/sh", self.fake_bash(folder, ""), self.fake_bash(folder, "3"), str(Path(folder) / "missing")):
                with self.subTest(shell=shell):
                    self.assertNotEqual(0, self.gate(shell).returncode)
            if Path("/bin/zsh").exists():
                self.assertNotEqual(0, self.gate("/bin/zsh").returncode)

    def test_modern_bash_passes(self):
        with tempfile.TemporaryDirectory() as folder:
            self.assertEqual(0, self.gate(self.fake_bash(folder, "5")).returncode)


class LinuxBytecodeTests(unittest.TestCase):
    def test_linux_readiness_sends_bytecode_to_a_temporary_prefix(self):
        seen = {}

        def run(command, **kwargs):
            seen.update(kwargs["env"])
            seen["command"] = command
            return subprocess.CompletedProcess(command, 0)

        with mock.patch.object(linux_pilot.shutil, "which", return_value="/usr/bin/make"), \
                mock.patch.object(linux_pilot.subprocess, "run", side_effect=run):
            check = linux_pilot.repository_suite_check()
        self.assertEqual("PASS", check.status)
        self.assertEqual(["make", "check"], seen["command"])
        self.assertEqual("1", seen["PYTHONDONTWRITEBYTECODE"])
        self.assertFalse(Path(seen["PYTHONPYCACHEPREFIX"]).is_relative_to(ROOT))


class SymlinkedFleetRootTests(unittest.TestCase):
    def test_plan_refuses_a_symlinked_fleet_root_like_fleetctl_setup(self):
        with tempfile.TemporaryDirectory() as directory:
            real = Path(directory) / "fleet"
            real.mkdir()
            link = Path(directory) / "fleet-link"
            link.symlink_to(real)
            result = subprocess.run([sys.executable, str(ROOT / "scripts/setup-workstation.py"),
                                     "--fleet-root", str(link), "--profile", "machines/x.toml"],
                                    capture_output=True, text=True, timeout=60)
            self.assertEqual(2, result.returncode)
            self.assertIn("must not be a symlink", result.stderr)


if __name__ == "__main__":
    unittest.main()
