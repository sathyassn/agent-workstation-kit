"""Root package changes run only from the staged copy; no host state is changed."""
from __future__ import annotations
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts/lib"))
import package_setup  # noqa: E402
import path_safety  # noqa: E402


class StagedRootTests(unittest.TestCase):
    def test_checkout_outside_opt_is_refused(self):
        with self.assertRaisesRegex(ValueError, "staged copy"):
            path_safety.require_staged_root(ROOT)

    def test_user_owned_tree_at_the_staged_path_is_refused(self):
        with tempfile.TemporaryDirectory() as folder:
            staged = Path(folder) / "kit"
            (staged / "config").mkdir(parents=True)
            (staged / "config/packages.toml").write_text("")
            with mock.patch.object(path_safety, "STAGED_ROOT", staged):
                with self.assertRaisesRegex(ValueError, "not root-controlled"):
                    path_safety.require_staged_root(staged)

    def test_root_apt_apply_from_a_checkout_is_refused_before_any_command(self):
        calls = []
        with mock.patch.object(package_setup.sys, "platform", "linux"), \
                mock.patch.object(package_setup.os, "geteuid", return_value=0), \
                mock.patch.object(package_setup, "run", side_effect=lambda *a, **k: calls.append(a)), \
                mock.patch.object(package_setup, "inventory", side_effect=AssertionError("inventory ran")):
            with self.assertRaisesRegex(ValueError, "staged copy"):
                package_setup.provision("linux", {"apt": ["jq"]}, apply=True)
        self.assertEqual(calls, [])


class SshGuardRollbackTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        # The guard record path's parent is not writable, so the record write fails.
        self.guard = Path(self.folder.name) / "missing" / "ssh-guard.json"
        self.commands = []

    def guard_install(self, unmask_code):
        def run(command, mutate=False):
            self.commands.append(command)
            code = unmask_code if command[:2] == ["systemctl", "unmask"] else 0
            if command[:3] == ["systemctl", "is-active", "--quiet"]:
                code = 3
            return subprocess.CompletedProcess(command, code, "", "")
        parent = mock.MagicMock()
        parent.is_symlink.return_value = False
        parent.lstat.return_value = mock.Mock(st_uid=0, st_mode=0o755)
        units = Path(self.folder.name) / "units"
        real_path = package_setup.Path
        with mock.patch.object(package_setup, "SSH_GUARD", self.guard), \
                mock.patch.object(package_setup, "run", side_effect=run), \
                mock.patch.object(package_setup, "Path", side_effect=lambda *a: units.joinpath(*a[1:]) if a and a[0] == "/etc/systemd/system" else real_path(*a)), \
                mock.patch.object(type(self.guard), "parent", new_callable=mock.PropertyMock, return_value=parent):
            package_setup.guard_new_ssh_install(apply=True)

    def test_failed_record_removes_the_masks(self):
        with self.assertRaisesRegex(ValueError, "masks were removed"):
            self.guard_install(unmask_code=0)
        self.assertIn(["systemctl", "unmask", *package_setup.SSH_UNITS], self.commands)

    def test_failed_unmask_is_reported_with_the_manual_command(self):
        with self.assertRaisesRegex(ValueError, "Remove the masks by hand"):
            self.guard_install(unmask_code=1)


if __name__ == "__main__":
    unittest.main()
