"""Fixture tests for live postcondition parsing; no host state is read."""
from __future__ import annotations
import pwd
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts/lib"))
import phase_verification as pv  # noqa: E402

HARDENED = """permitrootlogin no
passwordauthentication no
kbdinteractiveauthentication no
allowusers admin-01
allowusers alice
allowusers bob
denyusers agent-01
"""


class SshdTests(unittest.TestCase):
    def test_each_allowusers_line_is_kept(self):
        self.assertEqual(pv.sshd_effective(HARDENED)["allowusers"], ["admin-01", "alice", "bob"])

    def test_multiple_ssh_users_verify(self):
        self.assertEqual(pv.sshd_problems(HARDENED, ["alice", "bob", "admin-01"], "agent-01"), [])

    def test_missing_or_extra_user_fails(self):
        self.assertIn("sshd AllowUsers does not exactly match accounts.ssh_users",
                      pv.sshd_problems(HARDENED, ["alice", "admin-01"], "agent-01"))
        self.assertTrue(pv.sshd_problems(HARDENED, ["alice", "bob", "admin-01", "carol"], "agent-01"))

    def test_agent_must_be_denied(self):
        self.assertIn("sshd DenyUsers does not include the agent account",
                      pv.sshd_problems(HARDENED.replace("denyusers agent-01\n", ""), ["alice", "bob", "admin-01"], "agent-01"))

    def test_password_login_fails(self):
        text = HARDENED.replace("passwordauthentication no", "passwordauthentication yes")
        self.assertIn("sshd passwordauthentication is not no", pv.sshd_problems(text, ["alice", "bob", "admin-01"], "agent-01"))


class AccountTests(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.TemporaryDirectory()
        self.addCleanup(self.home.cleanup)
        self.profile = {
            "machine": {"platform": "macos"},
            "accounts": {"agent": "agent-01", "humans": ["alice"], "admins": ["admin-01"],
                         "services": [], "operators": [], "viewers": []},
        }
        uid = Path(self.home.name).stat().st_uid
        self.shells = {"agent-01": "/bin/zsh", "alice": "/bin/bash", "admin-01": "/bin/bash"}
        self.groups = {"agent-01": "agent-01", "alice": "alice staff", "admin-01": "admin-01 admin"}

        def getpwnam(name):
            if name not in self.shells:
                raise KeyError(name)
            return pwd.struct_passwd((name, "x", uid, uid, "", self.home.name, self.shells[name]))

        def probe(command, env=None):
            return subprocess.CompletedProcess(command, 0, self.groups[command[-1]], "")

        patches = (mock.patch.object(pv.pwd, "getpwnam", getpwnam), mock.patch.object(pv, "probe", probe))
        for patch in patches:
            patch.start()
            self.addCleanup(patch.stop)

    def test_existing_bash_humans_and_admins_pass(self):
        self.assertEqual(pv.account_problems(self.profile), [])

    def test_agent_without_zsh_fails_with_reason(self):
        self.shells["agent-01"] = "/bin/bash"
        self.assertEqual(pv.account_problems(self.profile), ["agent-01 (agent) does not use zsh"])

    def test_missing_account_and_admin_drift_are_reported(self):
        del self.shells["alice"]
        self.groups["admin-01"] = "admin-01"
        problems = pv.account_problems(self.profile)
        self.assertIn("alice does not exist", problems)
        self.assertIn("admin-01 is not in admin", problems)

    def test_agent_in_docker_group_fails(self):
        self.groups["agent-01"] = "agent-01 docker"
        self.assertIn("agent-01 is in the root-equivalent docker group", pv.account_problems(self.profile))


if __name__ == "__main__":
    unittest.main()
