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


class ConditionalSshdTests(unittest.TestCase):
    def fake_sshd(self, unsafe_user=None, failing_user=None):
        calls = []

        def run(command, env=None):
            calls.append(command)
            spec = command[-1]
            user = spec.split(",")[0].removeprefix("user=")
            if user == failing_user:
                return subprocess.CompletedProcess(command, 255, "", "bad match spec")
            text = HARDENED
            if user == unsafe_user:
                # What `sshd -T -C` prints when `Match User x` re-enables passwords.
                text = text.replace("passwordauthentication no", "passwordauthentication yes")
            return subprocess.CompletedProcess(command, 0, text, "")
        return run, calls

    def test_every_user_and_address_is_evaluated_with_match_rules(self):
        run, calls = self.fake_sshd()
        self.assertEqual(pv.sshd_conditional_problems(["alice", "agent-01"], run), [])
        self.assertEqual(len(calls), 4)
        self.assertTrue(all(call[:3] == ["sshd", "-T", "-C"] for call in calls))
        self.assertIn("user=alice,host=localhost,addr=100.64.0.1", [call[3] for call in calls])

    def test_match_block_reenabling_passwords_fails(self):
        run, _ = self.fake_sshd(unsafe_user="alice")
        problems = pv.sshd_conditional_problems(["alice", "bob"], run)
        self.assertIn("sshd passwordauthentication is not no for alice from 127.0.0.1", problems)
        self.assertFalse(any("bob" in problem for problem in problems))

    def test_unreadable_conditional_policy_fails_closed(self):
        run, _ = self.fake_sshd(failing_user="bob")
        self.assertIn("cannot read effective sshd policy for bob from 127.0.0.1",
                      pv.sshd_conditional_problems(["bob"], run))


class SudoGrantTests(unittest.TestCase):
    def fake_sudo(self, outputs):
        def run(command, env=None):
            self.assertEqual(command[:4], ["sudo", "-n", "-l", "-U"])
            self.assertEqual(env["LC_ALL"], "C")
            code, text = outputs[command[-1]]
            return subprocess.CompletedProcess(command, code, text, "")
        return run

    def test_no_grant_passes(self):
        run = self.fake_sudo({"agent-01": (1, "User agent-01 is not allowed to run sudo on host.\n")})
        self.assertEqual(pv.sudo_grant_problems(["agent-01"], run), [])

    def test_direct_sudoers_grant_fails(self):
        grant = "User agent-01 may run the following commands on host:\n    (ALL) NOPASSWD: ALL\n"
        run = self.fake_sudo({"agent-01": (0, grant)})
        self.assertEqual(pv.sudo_grant_problems(["agent-01"], run), ["agent-01 has a sudoers command grant"])

    def test_unknown_or_denied_query_fails_closed(self):
        run = self.fake_sudo({"svc": (1, "sudo: a password is required\n")})
        self.assertEqual(pv.sudo_grant_problems(["svc"], run),
                         ["cannot prove that svc has no sudoers command grant"])


UFW = ROOT / "tests/fixtures/ufw"


class UfwStatusTests(unittest.TestCase):
    def test_plain_status_without_direction_verifies(self):
        status = (UFW / "status-plain.txt").read_text()
        for rule in ("22/tcp", "4000/tcp", "4000/udp"):
            self.assertTrue(pv.ufw_allows_in(status, rule, "tailscale0"), rule)

    def test_verbose_status_with_in_direction_verifies(self):
        status = (UFW / "status-verbose.txt").read_text()
        for rule in ("22/tcp", "4000/tcp", "4000/udp"):
            self.assertTrue(pv.ufw_allows_in(status, rule, "tailscale0"), rule)

    def test_outgoing_routed_unscoped_and_deny_rules_do_not_count(self):
        status = (UFW / "status-not-inbound.txt").read_text()
        for rule in ("22/tcp", "4000/tcp", "4000/udp", "6768/tcp"):
            self.assertFalse(pv.ufw_allows_in(status, rule, "tailscale0"), rule)

    def test_other_interface_does_not_count(self):
        status = (UFW / "status-plain.txt").read_text()
        self.assertFalse(pv.ufw_allows_in(status, "22/tcp", "eth0"))


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
