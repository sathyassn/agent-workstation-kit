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


class SshdMatchOverrideTests(unittest.TestCase):
    def config(self, folder, main, drop_in=None):
        root = Path(folder)
        (root / "sshd_config.d").mkdir()
        (root / "sshd_config").write_text(main.replace("DROPINS", str(root / "sshd_config.d") + "/*.conf"))
        if drop_in is not None:
            (root / "sshd_config.d/50-local.conf").write_text(drop_in)
        return root / "sshd_config"

    def test_kit_style_config_without_match_passes(self):
        with tempfile.TemporaryDirectory() as folder:
            config = self.config(folder, "Include DROPINS\nPasswordAuthentication no\n",
                                 "AllowUsers alice\nPasswordAuthentication no\n")
            self.assertEqual(pv.sshd_match_problems(config), [])

    def test_match_that_sshd_test_mode_cannot_see_fails_closed(self):
        # `sshd -T -C` never matches LocalPort without lport=, so only this scan sees it.
        with tempfile.TemporaryDirectory() as folder:
            config = self.config(folder, "Include DROPINS\n",
                                 "Match LocalPort 2222\n    PasswordAuthentication yes\n")
            problems = pv.sshd_match_problems(config)
            self.assertEqual(1, len(problems))
            self.assertIn("sets passwordauthentication", problems[0])

    def test_match_with_unrelated_keywords_passes(self):
        with tempfile.TemporaryDirectory() as folder:
            config = self.config(folder, "Include DROPINS\nMatch User alice\n    X11Forwarding no\n")
            self.assertEqual(pv.sshd_match_problems(config), [])

    def test_match_opened_in_an_include_covers_later_lines(self):
        with tempfile.TemporaryDirectory() as folder:
            config = self.config(folder, "Include DROPINS\nPermitRootLogin yes\n",
                                 "Match Group staff\n    X11Forwarding no\n")
            self.assertIn("sets permitrootlogin", pv.sshd_match_problems(config)[0])

    def test_deprecated_alias_and_quoted_include_are_scanned(self):
        with tempfile.TemporaryDirectory() as folder:
            main = 'Include "DROPINS"\n'
            config = self.config(folder, main, "Match LocalPort 22\n    ChallengeResponseAuthentication yes\n")
            self.assertIn("sets challengeresponseauthentication", pv.sshd_match_problems(config)[0])

    def test_include_inside_match_and_unreadable_config_fail_closed(self):
        with tempfile.TemporaryDirectory() as folder:
            config = self.config(folder, "Match Address 100.101.0.0/16\n    Include DROPINS\n",
                                 "KbdInteractiveAuthentication=yes\n")
            self.assertIn("sets kbdinteractiveauthentication", pv.sshd_match_problems(config)[0])
        self.assertEqual(pv.sshd_match_problems(Path("/nonexistent/sshd_config")),
                         ["cannot read sshd configuration /nonexistent/sshd_config"])


class VerifierWiringTests(unittest.TestCase):
    """Removing a check from verify_phase or accounts_match must fail here."""

    def plan(self):
        profile = {"machine": {"platform": "linux"}, "remote": {"nomachine_port": 4000},
                   "accounts": {"agent": "agent-01", "humans": ["alice"], "admins": ["admin-01"],
                                "services": ["svc"], "operators": [], "viewers": [], "ssh_users": ["alice", "admin-01"]}}
        return {"profile": profile, "phases": [{"name": "remote-hardening", "command": ["KIT/x"]}]}

    def test_remote_hardening_runs_conditional_and_match_checks(self):
        with mock.patch.object(pv, "probe", return_value=subprocess.CompletedProcess([], 0, HARDENED.replace("allowusers bob\n", ""), "")), \
                mock.patch.object(pv, "sshd_conditional_problems", return_value=["conditional"]) as conditional, \
                mock.patch.object(pv, "sshd_match_problems", return_value=["match"]) as match, \
                mock.patch("sys.stdout") as output:
            self.assertFalse(pv.verify_phase(self.plan(), "remote-hardening", ROOT))
        conditional.assert_called_once_with(["alice", "admin-01", "agent-01"])
        match.assert_called_once_with()

    def test_accounts_include_the_sudo_grant_check(self):
        profile = self.plan()["profile"]
        with mock.patch.object(pv, "account_problems", return_value=[]), \
                mock.patch.object(pv, "sudo_grant_problems", return_value=["agent-01 has a sudoers command grant"]) as sudo, \
                mock.patch("sys.stdout"):
            self.assertFalse(pv.accounts_match(profile))
        sudo.assert_called_once_with(["agent-01", "svc"])


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


GIB = 1024 * 1024
COMMAND = ["apply-resource-policy-linux.sh", "--agent", "agent-01"]


def receipt_text(inputs=None, readings=None, values=None, complete=True, states=None):
    """A version 2 receipt as apply writes it for 64 GiB desktop, 8 GiB swap, 16 cores."""
    lines = ["version\t2", "agent\tagent-01"]
    base_inputs = pv.resource_receipt_inputs(pv.resource_options(COMMAND))
    base_inputs.update(inputs or {})
    lines += ["input\t%s\t%s" % item for item in base_inputs.items()]
    base_readings = {"ram_kib": str(64 * GIB), "swap_kib": str(8 * GIB), "zram_kib": "0", "zram_planned_kib": "0", "cpu_cores": "16", "role": "desktop"}
    base_readings.update(readings or {})
    lines += ["reading\t%s\t%s" % item for item in base_readings.items()]
    base_values = {"reserve_kib": "9730785", "memory_max_kib": "57378079", "memory_high_kib": "51640272", "swap_max_kib": "2097152",
                   "cpu_weight": "50", "cpu_quota_percent": "1400", "tasks_max": "8192", "oomd_pressure_percent": "60",
                   "swappiness": "10", "zram_percent": "0"}
    base_values.update(values or {})
    lines += ["value\t%s\t%s" % item for item in base_values.items()]
    lines += ["begin\t1\tfile\t/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf\tcreated\t-\t_etc_systemd_user_agent.slice.d_50-agent-workstation-kit.conf", "done\t1"]
    if states is not None:
        lines += ["state\t" + state for state in states]
    elif complete:
        lines.append("state\tcomplete")
    return "\n".join(lines) + "\n"


class ResourcePolicyTests(unittest.TestCase):
    """The resources verifier mirrors apply-resource-policy-linux.sh."""

    def receipt(self, **kwargs):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = Path(directory.name) / "receipt.tsv"
        path.write_text(receipt_text(**kwargs))
        return pv.read_resource_receipt(path)

    def test_cpu_weight_lives_on_the_parent_agent_slice_dropin(self):
        self.assertEqual(Path("/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf"), pv.AGENT_PARENT_SLICE)
        values = pv.resource_values({"resources": {}}, 64 * GIB, COMMAND + ["--cpu-weight", "75"], swap_total_kib=8 * GIB, cpu_cores=16, desktop=True)
        self.assertNotIn("CPUWeight", pv.agent_slice_text("agent-01", values, True))
        parent = pv.agent_parent_slice_text("agent-01", values)
        self.assertIn("[Unit]\nConditionUser=agent-01\n", parent)
        self.assertNotIn("Description=", parent)
        self.assertTrue(parent.endswith("[Slice]\nCPUWeight=75\n"))

    def test_migrating_receipt_is_incomplete(self):
        with self.assertRaises(ValueError):
            pv.receipt_resource_values(self.receipt(states=["complete", "migrating"]), {"resources": {}}, COMMAND, "agent-01")
        values = pv.receipt_resource_values(self.receipt(states=["complete", "migrating", "complete"]), {"resources": {}}, COMMAND, "agent-01")
        self.assertEqual("57378079K", values["MemoryMax"])

    def test_measure_is_informational(self):
        plain = pv.resource_values({"resources": {}}, 60 * GIB, COMMAND, swap_total_kib=8 * GIB, cpu_cores=16, desktop=True)
        measured = pv.resource_values({"resources": {}}, 60 * GIB, COMMAND + ["--measure"], swap_total_kib=8 * GIB, cpu_cores=16, desktop=True)
        self.assertEqual(plain, measured)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, gib in (("init.scope", 1), ("system.slice", 3), ("user.slice", 22),
                              ("user.slice/user-1001.slice/user@1001.service/agent.slice", 20)):
                (root / name).mkdir(parents=True, exist_ok=True)
                (root / name / "memory.current").write_text(str(gib << 30) + "\n")
            # 1 + 3 + 22 - 20 = 6 GiB in use outside agent work; 1.5x = 9 GiB.
            self.assertEqual(9 * GIB, pv.measured_non_agent_kib(1001, root))

    def test_integer_flags_and_profile_fields_are_checked_before_arithmetic(self):
        for flag in ("--cpu-weight", "--reserve-cores", "--memory-reserve-percent", "--memory-high-percent", "--memory-max-percent",
                     "--swap-max-percent", "--oomd-pressure-percent", "--zram-percent", "--swappiness", "--reserve-fixed-gib"):
            for bad in ("18446744073709551666", "-1", "abc", "08", ""):
                with self.subTest(flag=flag, value=bad), self.assertRaises(ValueError):
                    pv.resource_options(COMMAND + [flag, bad])
        for field in ("reserve_cores", "cpu_weight", "os_memory_reserve_percent", "reserve_fixed_gib"):
            for bad in (18446744073709551666, -1, "abc", "08", True):
                with self.subTest(field=field, value=bad), self.assertRaises(ValueError):
                    pv.resource_options(COMMAND, {"resources": {field: bad}})
        options = pv.resource_options(COMMAND + ["--reserve-fixed-gib", "6.50", "--cpu-weight", "75"], {"resources": {"reserve_cores": 2}})
        self.assertEqual(("6.5", 75, 2), (options["--reserve-fixed-gib"], options["--cpu-weight"], options["--reserve-cores"]))
        self.assertEqual("8", pv.resource_options(COMMAND, {"resources": {"reserve_fixed_gib": 8.0}})["--reserve-fixed-gib"])

    def test_ceiling_override_keeps_the_reserve_unless_allowed(self):
        # 64 GiB desktop: the formula reserve is 9.3 GiB, a 90% ceiling leaves 6.4 GiB.
        with self.assertRaisesRegex(ValueError, "less than the reserve"):
            pv.resource_values({"resources": {}}, 64 * GIB, COMMAND + ["--memory-max-percent", "90"], swap_total_kib=0, cpu_cores=16, desktop=True)
        # 16 GiB headless: a 95% ceiling leaves 0.8 GiB, below 2 GiB.
        with self.assertRaisesRegex(ValueError, "2 GiB"):
            pv.resource_values({"resources": {}}, 16 * GIB, COMMAND + ["--memory-max-percent", "95"], swap_total_kib=0, cpu_cores=16, desktop=False)
        allowed = pv.resource_values({"resources": {}}, 64 * GIB, COMMAND + ["--memory-max-percent", "90", "--allow-small-reserve"],
                                     swap_total_kib=0, cpu_cores=16, desktop=True)
        self.assertEqual("60397977K", allowed["MemoryMax"])

    def test_planned_zram_counts_toward_the_swap_total_and_replaces_active_zram(self):
        # No existing zram device: 8 GiB disk + 16 GiB planned zram = 24 GiB -> 6 GiB.
        no_dev = pv.resource_values({"resources": {}}, 64 * GIB, COMMAND + ["--zram-percent", "25"],
                                    swap_total_kib=8 * GIB, active_zram_kib=0, cpu_cores=16, desktop=True)
        self.assertEqual("6291456K", no_dev["MemorySwapMax"])
        # Existing 4 GiB zram resized up to 16 GiB: 8 GiB disk + 16 GiB planned = 24 GiB -> 6 GiB (not 7 GiB or 3 GiB).
        resized_up = pv.resource_values({"resources": {}}, 64 * GIB, COMMAND + ["--zram-percent", "25"],
                                        swap_total_kib=12 * GIB, active_zram_kib=4 * GIB, cpu_cores=16, desktop=True)
        self.assertEqual("6291456K", resized_up["MemorySwapMax"])
        # Existing 28 GiB zram resized down to 16 GiB: 4 GiB disk + 16 GiB planned = 20 GiB -> 5 GiB (not 8 GiB).
        resized_down = pv.resource_values({"resources": {}}, 64 * GIB, COMMAND + ["--zram-percent", "25"],
                                          swap_total_kib=32 * GIB, active_zram_kib=28 * GIB, cpu_cores=16, desktop=True)
        self.assertEqual("5242880K", resized_down["MemorySwapMax"])
        # Receipt derivation mirrors zram resize up and down and rejects zram_kib > swap_kib.
        up_receipt = self.receipt(
            inputs={"zram_percent": "25"},
            readings={"swap_kib": str(12 * GIB), "zram_kib": str(4 * GIB), "zram_planned_kib": str(16 * GIB)},
            values={"swap_max_kib": str(6 * GIB), "zram_percent": "25"},
        )
        self.assertEqual("6291456K", pv.receipt_resource_values(up_receipt, {"resources": {}}, COMMAND + ["--zram-percent", "25"], "agent-01")["MemorySwapMax"])
        down_receipt = self.receipt(
            inputs={"zram_percent": "25"},
            readings={"swap_kib": str(32 * GIB), "zram_kib": str(28 * GIB), "zram_planned_kib": str(16 * GIB)},
            values={"swap_max_kib": str(5 * GIB), "zram_percent": "25"},
        )
        self.assertEqual("5242880K", pv.receipt_resource_values(down_receipt, {"resources": {}}, COMMAND + ["--zram-percent", "25"], "agent-01")["MemorySwapMax"])
        with self.assertRaises(ValueError):
            pv.receipt_resource_values(
                self.receipt(readings={"swap_kib": str(4 * GIB), "zram_kib": str(8 * GIB)}),
                {"resources": {}}, COMMAND, "agent-01",
            )
        with tempfile.TemporaryDirectory() as directory:
            swaps = Path(directory) / "swaps"
            swaps.write_text("Filename\tType\tSize\tUsed\tPriority\n/swap.img\tfile\t8388608\t0\t-2\n/dev/zram0\tpartition\t4194304\t0\t100\n")
            self.assertEqual(12 * GIB, pv.host_swap_total_kib(swaps))
            self.assertEqual(4 * GIB, pv.host_zram_swap_kib(swaps))

    def test_values_come_from_the_receipt_not_the_host(self):
        with mock.patch.object(pv, "host_swap_total_kib", side_effect=AssertionError("re-read swap")), \
                mock.patch.object(pv, "host_cpu_cores", side_effect=AssertionError("re-read cores")), \
                mock.patch.object(pv, "host_desktop_present", side_effect=AssertionError("re-read role")):
            values = pv.receipt_resource_values(self.receipt(), {"resources": {}}, COMMAND, "agent-01")
        self.assertEqual(("57378079K", "2097152K", "1400%"), (values["MemoryMax"], values["MemorySwapMax"], values["CPUQuota"]))

    def test_receipt_that_does_not_match_the_plan_fails(self):
        cases = (
            ("incomplete", {"complete": False}, COMMAND, "agent-01"),
            ("other agent", {}, COMMAND, "agent-02"),
            ("inputs differ from the plan", {}, COMMAND + ["--cpu-weight", "75"], "agent-01"),
            ("value differs from its readings", {"values": {"memory_max_kib": "60000000"}}, COMMAND, "agent-01"),
            ("unreadable reading", {"readings": {"ram_kib": "18446744073709551666"}}, COMMAND, "agent-01"),
        )
        for label, receipt, command, agent in cases:
            with self.subTest(label), self.assertRaises(ValueError):
                pv.receipt_resource_values(self.receipt(**receipt), {"resources": {}}, command, agent)

    def test_systemd_timespan_parser(self):
        valid = (
            ("1min", 60_000_000),
            ("2min 4s", 124_000_000),
            ("14s", 14_000_000),
            ("500ms", 500_000),
            ("250us", 250),
            ("1h 30min", 5_400_000_000),
            ("1d 2h", 93_600_000_000),
            ("1w", 604_800_000_000),
            ("infinity", None),
        )
        for text, expected in valid:
            with self.subTest(text=text):
                self.assertEqual(expected, pv.parse_systemd_timespan_usec(text))
        for bad in ("", "   ", "14", "14sec", "1min bogus", "-5s", "50%", "1.5us", "min", "infinity 1s"):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                pv.parse_systemd_timespan_usec(bad)

    def test_effective_values_and_cpu_delegation_are_checked(self):
        values = pv.resource_values({"resources": {}}, 64 * GIB, COMMAND, swap_total_kib=8 * GIB, cpu_cores=16, desktop=True)
        live = {
            "agent-work.slice": "MemoryHigh=%d\nMemoryMax=%d\nMemorySwapMax=%d\nTasksMax=8192\nCPUQuotaPerSecUSec=14s\n"
                                % (51640272 * 1024, 57378079 * 1024, 2097152 * 1024),
            "agent.slice": "CPUWeight=50\n",
        }

        def run(delegate):
            def fake(command, **_kwargs):
                if command[:2] == ["systemctl", "show"]:
                    return subprocess.CompletedProcess(command, 0, delegate + "\n", "")
                return subprocess.CompletedProcess(command, 0, live[command[4]], "")
            return fake

        with tempfile.TemporaryDirectory() as directory:
            controllers = Path(directory) / "cgroup.controllers"
            controllers.write_text("cpuset cpu io memory pids\n")
            self.assertEqual([], pv.effective_resource_problems("agent-01", 1001, values, run("cpu memory pids"), controllers))
            # Systemd timespan formats: 1min (6000%), 2min 4s (12400%), 14s (1400%), 500ms (50%).
            for quota, formatted in (("6000%", "1min"), ("12400%", "2min 4s"), ("1400%", "14s"), ("50%", "500ms")):
                with self.subTest(quota=quota, formatted=formatted):
                    case_values = {**values, "CPUQuota": quota}
                    live["agent-work.slice"] = (
                        "MemoryHigh=%d\nMemoryMax=%d\nMemorySwapMax=%d\nTasksMax=8192\nCPUQuotaPerSecUSec=%s\n"
                        % (51640272 * 1024, 57378079 * 1024, 2097152 * 1024, formatted)
                    )
                    self.assertEqual([], pv.effective_resource_problems("agent-01", 1001, case_values, run("cpu memory pids"), controllers))
            # infinity means no quota; malformed values fail closed.
            for bad_quota in ("infinity", "14bogus", ""):
                with self.subTest(bad_quota=bad_quota):
                    live["agent-work.slice"] = (
                        "MemoryHigh=%d\nMemoryMax=%d\nMemorySwapMax=%d\nTasksMax=8192\nCPUQuotaPerSecUSec=%s\n"
                        % (51640272 * 1024, 57378079 * 1024, 2097152 * 1024, bad_quota)
                    )
                    problems = pv.effective_resource_problems("agent-01", 1001, values, run("cpu memory pids"), controllers)
                    self.assertTrue(any("CPUQuotaPerSecUSec" in problem for problem in problems), problems)
            live["agent-work.slice"] = (
                "MemoryHigh=%d\nMemoryMax=%d\nMemorySwapMax=%d\nTasksMax=8192\nCPUQuotaPerSecUSec=14s\n"
                % (51640272 * 1024, 57378079 * 1024, 2097152 * 1024)
            )
            self.assertIn("user@1001.service does not delegate the cpu controller",
                          pv.effective_resource_problems("agent-01", 1001, values, run("memory pids"), controllers))
            live["agent.slice"] = "CPUWeight=100\n"
            self.assertIn("agent.slice effective CPUWeight is 100, expected 50",
                          pv.effective_resource_problems("agent-01", 1001, values, run("cpu memory pids"), controllers))
            controllers.write_text("cpuset io memory pids\n")
            self.assertIn("the cgroup v2 cpu controller is not available",
                          pv.effective_resource_problems("agent-01", 1001, values, run("cpu memory pids"), controllers))


if __name__ == "__main__":
    unittest.main()
