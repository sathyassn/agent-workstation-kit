#!/usr/bin/env python3
"""Tests for TSK-008: contributor identity check, CI commit identity check, and optional keyed HMAC list.

Every synthetic identity and keyed-list value is assembled from parts so this
file passes the strict public-safety self-scan without any exemptions.
"""

from __future__ import annotations

import contextlib
import datetime
import hashlib
import importlib.util
import io
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import scan_deadline


ROOT = Path(__file__).resolve().parents[1]


def _load_module(name: str, rel_path: str):
    path = ROOT / rel_path
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise FileNotFoundError(f"missing module: {rel_path}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


scan = _load_module("check_public_release_tsk008", "tests/check_public_release.py")


def join(*parts: str) -> str:
    return "".join(parts)


def git(
    root: Path,
    *args: str,
    check: bool = True,
    author_name: str = "Test Contributor",
    author_email: str = "contributor@example.invalid",
    committer_name: str | None = None,
    committer_email: str | None = None,
    extra_env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess[str]:
    env = {
        **os.environ,
        "GIT_AUTHOR_NAME": author_name,
        "GIT_AUTHOR_EMAIL": author_email,
        "GIT_COMMITTER_NAME": author_name if committer_name is None else committer_name,
        "GIT_COMMITTER_EMAIL": author_email if committer_email is None else committer_email,
    }
    if extra_env:
        env.update(extra_env)
    return subprocess.run(
        ["git", *args],
        cwd=root,
        check=check,
        capture_output=True,
        text=True,
        env=env,
    )


def run_main(
    argv: list[str],
    *,
    stdin_text: str | None = None,
    env: dict[str, str] | None = None,
) -> tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    ctx = mock.patch.dict(os.environ, env, clear=False) if env is not None else contextlib.nullcontext()
    with ctx, contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        if stdin_text is not None:
            with mock.patch.object(scan.sys, "stdin", io.StringIO(stdin_text)):
                code = scan.main(argv)
        else:
            code = scan.main(argv)
    return code, out.getvalue(), err.getvalue()


def write_scan_configs(
    root: Path,
    *,
    modes: dict[str, str] | None = None,
    exemptions: list[dict[str, object]] | None = None,
    binary: list[dict[str, object]] | None = None,
    keyed_list: dict[str, object] | None = None,
) -> None:
    tests_dir = root / "tests"
    tests_dir.mkdir(parents=True, exist_ok=True)
    (tests_dir / "public_scan_modes.json").write_text(
        json.dumps({"schema_version": 1, "modes": dict(scan.DEFAULT_MODES if modes is None else modes)}) + "\n",
        encoding="utf-8",
    )
    (tests_dir / "public_scan_exemptions.json").write_text(
        json.dumps({"schema_version": 1, "exemptions": [] if exemptions is None else exemptions}) + "\n",
        encoding="utf-8",
    )
    (tests_dir / "public_scan_binary_exceptions.json").write_text(
        json.dumps({"schema_version": 1, "binary": [] if binary is None else binary}) + "\n",
        encoding="utf-8",
    )
    if keyed_list is not None:
        (tests_dir / "public_scan_keyed_list.json").write_text(
            json.dumps(keyed_list) + "\n",
            encoding="utf-8",
        )


def _fake_pwuid(name: str, gecos: str):
    class _Pw:
        pw_name = name
        pw_gecos = gecos

    return _Pw()


def _fake_tailscale_json(node: str, dns_name: str, tailnet_name: str, magic_suffix: str) -> str:
    return json.dumps(
        {
            "BackendState": "Running",
            "MagicDNSSuffix": magic_suffix,
            "CurrentTailnet": {"Name": tailnet_name, "MagicDNSSuffix": magic_suffix},
            "Self": {"HostName": node, "DNSName": dns_name},
        }
    )


class LocalIdentityCollectionAndMatchingTests(unittest.TestCase):
    """AC-1: local identity collection on Linux & macOS, folding, redaction, and zero disk storage."""

    def _identity_mod(self):
        return _load_module("public_scan_identity_test", "tests/public_scan_identity.py")

    def test_linux_fake_identity_sources_and_folded_variants_are_caught_and_redacted(self) -> None:
        ident = self._identity_mod()
        fake_git_name = join("Zora", " ", "Vance")
        fake_git_email = join("zvance", "@", "northwind-lab", ".test")
        fake_user = join("zora", "dev")
        fake_gecos = join("Zora", " ", "K.", " ", "Vance")
        fake_host = join("nordic", "-workbox-07")
        fake_pretty = join("Nordic", " ", "Workbox", " ", "Lab")
        fake_transient = join("nordic", "-temp-07")
        fake_ts_node = join("tsnode", "-bravo")
        fake_ts_tailnet = join("tail", "77aa88", ".ts.net")
        fake_ts_dns = f"{fake_ts_node}.{fake_ts_tailnet}."
        fake_ts_org = join("northwind", "-org.github")

        def fake_run(cmd, *, cwd=None, timeout=5.0):
            if cmd[:3] == ["git", "--no-optional-locks", "config"]:
                if cmd[-1] == "user.name":
                    return subprocess.CompletedProcess(cmd, 0, fake_git_name + "\n", "")
                if cmd[-1] == "user.email":
                    return subprocess.CompletedProcess(cmd, 0, fake_git_email + "\n", "")
            if cmd == ["hostnamectl"]:
                out = (
                    f"   Static hostname: {fake_host}\n"
                    f"   Pretty hostname: {fake_pretty}\n"
                    f"Transient hostname: {fake_transient}\n"
                )
                return subprocess.CompletedProcess(cmd, 0, out, "")
            if cmd[0].endswith("tailscale") and cmd[1:] == ["status", "--json"]:
                return subprocess.CompletedProcess(
                    cmd,
                    0,
                    _fake_tailscale_json(fake_ts_node, fake_ts_dns, fake_ts_org, fake_ts_tailnet),
                    "",
                )
            raise AssertionError(f"unexpected command: {cmd}")

        profile = ident.collect_local_identity(
            ROOT,
            normalize_fn=scan.normalize_text,
            platform_name="linux",
            run_cmd=fake_run,
            get_pwuid=lambda _uid: _fake_pwuid(fake_user, f"{fake_gecos},,,"),
            get_login=lambda: fake_user,
            get_hostname=lambda: fake_host + ".lan",
            which=lambda name: "/usr/bin/tailscale" if name == "tailscale" else None,
        )
        matcher = ident.IdentityMatcher(profile.tokens)

        samples = {
            "git_name_exact": f"Author: {fake_git_name}",
            "git_name_padded_spaces": "Author: " + join("Zora", "   ", "Vance"),
            "git_name_confusable": "Author: " + join("Z", "о", "ra", " ", "V", "а", "nce"),
            "git_email_local": f"handle={join('z', 'vance')}",
            "account_user": f"owner={fake_user}",
            "account_gecos": f"name={fake_gecos}",
            "hostname_static": f"host={fake_host}",
            "hostname_zero_padded": f"host={join('nordic', '-workbox-007')}",
            "hostname_separator_swapped": f"host={join('nordic', '_workbox_07')}",
            "hostname_url_escaped": join("nordic", "%2Dworkbox%2D07"),
            "hostname_json_escaped": join("nordic", "\\u002dworkbox\\u002d07"),
            "hostname_zero_width": join("nordic", "\u200b-workbox-07"),
            "hostname_pretty": f"box={fake_pretty}",
            "hostname_transient": f"tmp={fake_transient}",
            "tailscale_node": f"peer={fake_ts_node}",
            "tailscale_org": f"org={fake_ts_org}",
            "tailscale_suffix_label": f"net={join('tail', '77aa88')}",
        }
        for label, line in samples.items():
            with self.subTest(sample=label):
                res = scan.scan_sources(
                    [scan.SourceFile("docs/note.md", (line + "\n").encode("utf-8"))],
                    identity_matcher=matcher,
                )
                self.assertTrue(
                    any(f == "docs/note.md:1: matched rule 'identity'" for f in res.failures),
                    (label, res.failures),
                )
                joined_out = "\n".join(res.failures + res.warnings).casefold()
                for secret_val in (
                    fake_git_name,
                    fake_user,
                    fake_host,
                    fake_ts_node,
                    fake_ts_org,
                    join("tail", "77aa88"),
                ):
                    self.assertNotIn(secret_val.casefold(), joined_out)

        # Leaking filename is redacted to <redacted-...> and never echoes hostname.
        path_res = scan.scan_sources(
            [scan.SourceFile(f"docs/{fake_host}.md", b"clean\n")],
            identity_matcher=matcher,
        )
        self.assertTrue(any(":0: matched rule 'identity'" in f for f in path_res.failures), path_res.failures)
        self.assertTrue(any("<redacted-" in f for f in path_res.failures), path_res.failures)
        self.assertNotIn(fake_host.casefold(), "\n".join(path_res.failures).casefold())

    def test_macos_fake_identity_sources_including_optional_hostname_unset(self) -> None:
        ident = self._identity_mod()
        fake_git_name = join("Maren", " ", "Solberg")
        fake_git_email = join("msolberg", "@", "fjord-systems", ".test")
        fake_user = join("maren", "s")
        fake_comp = join("Maren", " ", "Studio", " ", "Mac")
        fake_local_host = join("Maren", "-Studio-Mac")
        fake_explicit_host = join("maren", "-mac-host")
        fake_ts_node = join("mac", "-studio-9")
        fake_ts_tailnet = join("tail", "55cc44", ".ts.net")

        for host_set in (True, False):
            with self.subTest(scutil_hostname_set=host_set):
                def fake_run(cmd, *, cwd=None, timeout=5.0):
                    if cmd[:3] == ["git", "--no-optional-locks", "config"]:
                        return subprocess.CompletedProcess(
                            cmd,
                            0,
                            (fake_git_name if cmd[-1] == "user.name" else fake_git_email) + "\n",
                            "",
                        )
                    if cmd[:2] == ["scutil", "--get"]:
                        key = cmd[2]
                        if key == "ComputerName":
                            return subprocess.CompletedProcess(cmd, 0, fake_comp + "\n", "")
                        if key == "LocalHostName":
                            return subprocess.CompletedProcess(cmd, 0, fake_local_host + "\n", "")
                        if key == "HostName":
                            if host_set:
                                return subprocess.CompletedProcess(cmd, 0, fake_explicit_host + "\n", "")
                            return subprocess.CompletedProcess(cmd, 1, "", "HostName: not set\n")
                    if cmd[0].endswith("tailscale") and cmd[1:] == ["status", "--json"]:
                        return subprocess.CompletedProcess(
                            cmd,
                            0,
                            _fake_tailscale_json(
                                fake_ts_node,
                                f"{fake_ts_node}.{fake_ts_tailnet}.",
                                fake_ts_tailnet,
                                fake_ts_tailnet,
                            ),
                            "",
                        )
                    raise AssertionError(f"unexpected command: {cmd}")

                profile = ident.collect_local_identity(
                    ROOT,
                    normalize_fn=scan.normalize_text,
                    platform_name="darwin",
                    run_cmd=fake_run,
                    get_pwuid=lambda _uid: _fake_pwuid(fake_user, fake_git_name),
                    get_login=lambda: fake_user,
                    get_hostname=lambda: fake_local_host + ".local",
                    which=lambda name: "/opt/homebrew/bin/tailscale" if name == "tailscale" else None,
                )
                matcher = ident.IdentityMatcher(profile.tokens)
                for expected in (fake_comp, fake_local_host, fake_user, fake_ts_node):
                    res = scan.scan_sources(
                        [scan.SourceFile("docs/mac.md", f"machine: {expected}\n".encode("utf-8"))],
                        identity_matcher=matcher,
                    )
                    self.assertEqual(["docs/mac.md:1: matched rule 'identity'"], res.failures)
                res_host = scan.scan_sources(
                    [scan.SourceFile("docs/mac.md", f"fqdn: {fake_explicit_host}\n".encode("utf-8"))],
                    identity_matcher=matcher,
                )
                if host_set:
                    self.assertEqual(["docs/mac.md:1: matched rule 'identity'"], res_host.failures)
                else:
                    self.assertEqual([], res_host.failures)

    def test_no_identity_value_written_to_disk_or_output(self) -> None:
        ident = self._identity_mod()
        fake_host = join("ephemeral", "-host-9941")
        fake_user = join("ephem", "user99")
        fake_name = join("Kestrel", " ", "Nolan")
        fake_email = join("knolan", "@", "kestrel-lab", ".test")

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "docs").mkdir()
            (root / "docs/note.md").write_text(f"host is {fake_host}\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "docs: add host")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            before_files = sorted(str(p.relative_to(root)) for p in root.rglob("*") if ".git" not in p.parts)
            profile = ident.IdentityProfile(
                source_kind="local",
                tokens=ident.derive_identity_tokens(
                    [
                        ("git-name", fake_name),
                        ("git-email", fake_email),
                        ("account-user", fake_user),
                        ("hostname", fake_host),
                    ],
                    normalize_fn=scan.normalize_text,
                ),
                notes=(),
            )
            with mock.patch.object(scan, "_collect_identity_profile", return_value=profile):
                code, out, err = run_main(["--root", str(root), "--commits", head, "--identity-source", "local"])
            after_files = sorted(str(p.relative_to(root)) for p in root.rglob("*") if ".git" not in p.parts)

            self.assertEqual(1, code, out + err)
            self.assertEqual(before_files, after_files)
            self.assertIn("docs/note.md:1: matched rule 'identity'", out)
            combined = (out + err).casefold()
            for val in (fake_host, fake_user, fake_name, fake_email):
                self.assertNotIn(val.casefold(), combined)
            self.assertNotIn(scan.SCAN_VERIFIED_MARKER, out)
            self.assertNotIn(ident.IDENTITY_VERIFIED_MARKER, out)

    def test_documented_placeholders_and_short_tokens_do_not_false_positive_and_linear_time(self) -> None:
        ident = self._identity_mod()
        tokens = ident.derive_identity_tokens(
            [
                ("account-user", "alice"),
                ("hostname", "host-a"),
                ("git-email", "alice@example.com"),
                ("account-user", "ab"),  # below MIN_IDENTITY_TOKEN_LEN (3)
                ("hostname", join("realbox", "-42")),
            ],
            normalize_fn=scan.normalize_text,
        )
        self.assertNotIn("alice", tokens)
        self.assertNotIn("host-a", tokens)
        self.assertNotIn("alice@example.com", tokens)
        self.assertNotIn("ab", tokens)
        self.assertIn(join("realbox", "-42"), tokens)

        matcher = ident.IdentityMatcher(tokens)
        self.assertEqual([], matcher.detect("alice uses host-a and ab with alice@example.com"))
        self.assertEqual([join("realbox", "-42")], matcher.detect("machine=" + join("realbox", "-42")))

        # Linear-time guarantee (no ReDoS) on a million characters across 100 tokens.
        many_tokens = frozenset(f"identtoken{i:03d}" for i in range(100))
        big_matcher = ident.IdentityMatcher(many_tokens)
        big_line = ("identtoken " * (scan_deadline.LARGE_INPUT // 11)) + "clean"
        scan_deadline.assert_finishes(
            self,
            "IdentityMatcher.detect(100 tokens)",
            lambda: self.assertEqual([], big_matcher.detect(big_line)),
        )


class LocalIdentityFailClosedTests(unittest.TestCase):
    """AC-2: every required source error/timeout/empty fails closed; uninstalled tailscale is absent."""

    def _identity_mod(self):
        return _load_module("public_scan_identity_failclosed", "tests/public_scan_identity.py")

    def _base_runner(self, platform_name: str, overrides: dict[str, object]):
        def run_cmd(cmd, *, cwd=None, timeout=5.0):
            key = " ".join(cmd)
            for prefix, outcome in overrides.items():
                if key.startswith(prefix) or (prefix == "tailscale" and cmd[0].endswith("tailscale")):
                    if isinstance(outcome, Exception):
                        raise outcome
                    return outcome
            if cmd[:3] == ["git", "--no-optional-locks", "config"]:
                val = join("Zora", " ", "Vance") if cmd[-1] == "user.name" else join("zvance", "@", "northwind-lab", ".test")
                return subprocess.CompletedProcess(cmd, 0, val + "\n", "")
            if platform_name == "linux" and cmd == ["hostnamectl"]:
                return subprocess.CompletedProcess(cmd, 0, f"Static hostname: {join('nordic', '-box-1')}\n", "")
            if platform_name == "darwin" and cmd[:2] == ["scutil", "--get"]:
                if cmd[2] == "HostName":
                    return subprocess.CompletedProcess(cmd, 1, "", "HostName: not set\n")
                return subprocess.CompletedProcess(cmd, 0, join("Maren", "-Mac") + "\n", "")
            if cmd[0].endswith("tailscale"):
                return subprocess.CompletedProcess(
                    cmd,
                    0,
                    _fake_tailscale_json(
                        join("tsnode", "-1"),
                        join("tsnode", "-1.", "tail88bb", ".ts", ".net."),
                        join("tail", "88bb", ".ts", ".net"),
                        join("tail", "88bb", ".ts", ".net"),
                    ),
                    "",
                )
            raise AssertionError(f"unexpected cmd: {cmd}")

        return run_cmd

    def test_linux_and_macos_source_errors_timeouts_and_empty_values_fail_closed(self) -> None:
        ident = self._identity_mod()

        common_cases = {
            "git_name_error": ({"git --no-optional-locks config --get user.name": subprocess.CompletedProcess(["git"], 1, "", "")}, {}),
            "git_name_empty": ({"git --no-optional-locks config --get user.name": subprocess.CompletedProcess(["git"], 0, "   \n", "")}, {}),
            "git_name_timeout": ({"git --no-optional-locks config --get user.name": subprocess.TimeoutExpired(["git"], 5)}, {}),
            "git_email_error": ({"git --no-optional-locks config --get user.email": subprocess.CompletedProcess(["git"], 1, "", "")}, {}),
            "git_email_empty": ({"git --no-optional-locks config --get user.email": subprocess.CompletedProcess(["git"], 0, "\n", "")}, {}),
            "git_email_timeout": ({"git --no-optional-locks config --get user.email": subprocess.TimeoutExpired(["git"], 5)}, {}),
            "account_error": ({}, {"get_pwuid": mock.Mock(side_effect=KeyError("missing"))}),
            "account_empty": ({}, {"get_pwuid": lambda _uid: _fake_pwuid("", "Full Name")}),
            "hostname_error": ({}, {"get_hostname": mock.Mock(side_effect=OSError("no host"))}),
            "hostname_empty": ({}, {"get_hostname": lambda: "   "}),
            "tailscale_installed_error": ({"tailscale": subprocess.CompletedProcess(["tailscale"], 1, "", "daemon stopped\n")}, {}),
            "tailscale_installed_timeout": ({"tailscale": subprocess.TimeoutExpired(["tailscale"], 5)}, {}),
            "tailscale_installed_bad_json": ({"tailscale": subprocess.CompletedProcess(["tailscale"], 0, "not-json", "")}, {}),
            "tailscale_installed_empty_fields": ({"tailscale": subprocess.CompletedProcess(["tailscale"], 0, '{"Self": {}}', "")}, {}),
        }
        platform_cases = {
            "linux": {
                **common_cases,
                "hostnamectl_missing": ({"hostnamectl": FileNotFoundError("hostnamectl")}, {}),
                "hostnamectl_error": ({"hostnamectl": subprocess.CompletedProcess(["hostnamectl"], 1, "", "dbus error")}, {}),
                "hostnamectl_timeout": ({"hostnamectl": subprocess.TimeoutExpired(["hostnamectl"], 5)}, {}),
                "hostnamectl_empty": ({"hostnamectl": subprocess.CompletedProcess(["hostnamectl"], 0, "Static hostname: (none)\n", "")}, {}),
            },
            "darwin": {
                **common_cases,
                "scutil_computer_error": ({"scutil --get ComputerName": subprocess.CompletedProcess(["scutil"], 1, "", "err")}, {}),
                "scutil_computer_empty": ({"scutil --get ComputerName": subprocess.CompletedProcess(["scutil"], 0, "\n", "")}, {}),
                "scutil_computer_timeout": ({"scutil --get ComputerName": subprocess.TimeoutExpired(["scutil"], 5)}, {}),
                "scutil_localhost_error": ({"scutil --get LocalHostName": subprocess.CompletedProcess(["scutil"], 1, "", "err")}, {}),
                "scutil_localhost_empty": ({"scutil --get LocalHostName": subprocess.CompletedProcess(["scutil"], 0, "  \n", "")}, {}),
                "scutil_hostname_timeout": ({"scutil --get HostName": subprocess.TimeoutExpired(["scutil"], 5)}, {}),
                "scutil_hostname_missing_bin": ({"scutil --get HostName": FileNotFoundError("scutil")}, {}),
            },
        }

        for plat, cases in platform_cases.items():
            for name, (cmd_overrides, kw_overrides) in cases.items():
                with self.subTest(platform=plat, case=name):
                    kwargs = {
                        "normalize_fn": scan.normalize_text,
                        "platform_name": plat,
                        "run_cmd": self._base_runner(plat, cmd_overrides),
                        "get_pwuid": lambda _uid: _fake_pwuid(join("zora", "dev"), join("Zora", " ", "Vance")),
                        "get_login": lambda: join("zora", "dev"),
                        "get_hostname": lambda: join("nordic", "-box-1"),
                        "which": lambda bin_name: "/usr/bin/tailscale" if bin_name == "tailscale" else None,
                    }
                    kwargs.update(kw_overrides)
                    with self.assertRaises(ident.IdentityCollectionError):
                        ident.collect_local_identity(ROOT, **kwargs)

    def test_uninstalled_tailscale_is_reported_as_absent_and_passes(self) -> None:
        ident = self._identity_mod()
        for plat in ("linux", "darwin"):
            with self.subTest(platform=plat):
                profile = ident.collect_local_identity(
                    ROOT,
                    normalize_fn=scan.normalize_text,
                    platform_name=plat,
                    run_cmd=self._base_runner(plat, {}),
                    get_pwuid=lambda _uid: _fake_pwuid(join("zora", "dev"), join("Zora", " ", "Vance")),
                    get_login=lambda: join("zora", "dev"),
                    get_hostname=lambda: join("nordic", "-box-1"),
                    which=lambda _bin: None,
                    is_executable=lambda _path: False,
                )
                self.assertTrue(
                    any("tailscale" in note.lower() and "absent" in note.lower() for note in profile.notes),
                    profile.notes,
                )

    def test_cli_identity_collection_failure_exits_nonzero_with_no_success_markers(self) -> None:
        ident = self._identity_mod()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "README.md").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: clean")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            with mock.patch.object(
                scan,
                "_collect_identity_profile",
                side_effect=ident.IdentityCollectionError("local git identity source failed"),
            ):
                code, out, err = run_main(["--root", str(root), "--commits", head, "--identity-source", "local"])
            self.assertEqual(1, code, out + err)
            self.assertNotIn("passed", out.lower())
            self.assertNotIn(scan.SCAN_VERIFIED_MARKER, out)
            self.assertNotIn(ident.IDENTITY_VERIFIED_MARKER, out)


class CIIdentityAndTamperTests(unittest.TestCase):
    """AC-3 & AC-4: CI commit author/committer identity check, shallow refusal, markers, tamper simulation, CODEOWNERS."""

    def _identity_mod(self):
        return _load_module("public_scan_identity_ci", "tests/public_scan_identity.py")

    def test_ci_journey_commit_author_in_changed_file_fails_with_redacted_output(self) -> None:
        """AC-4: PR whose commit author name appears in a changed file fails with only file, line, rule."""
        ident = self._identity_mod()
        fake_author = join("Bram", " ", "Haldorsen")
        fake_email = join("bhaldorsen", "@", "northwind-lab", ".test")

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "tests").mkdir()
            shutil.copyfile(ROOT / "tests/check_public_release.py", root / "tests/check_public_release.py")
            shutil.copyfile(ROOT / "tests/public_scan_identity.py", root / "tests/public_scan_identity.py")
            shutil.copyfile(ROOT / "tests/public_scan_keyed.py", root / "tests/public_scan_keyed.py")
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "README.md").write_text("clean base\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: base")
            base = git(root, "rev-parse", "HEAD").stdout.strip()

            git(root, "checkout", "-q", "-b", "feat/pr-author-leak")
            (root / "docs").mkdir()
            (root / "docs/notes.md").write_text(f"Reviewed by {fake_author}\n", encoding="utf-8")
            git(root, "add", "docs/notes.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: add notes",
                author_name=fake_author,
                author_email=fake_email,
            )
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            # Switch to trusted base (exactly like public-scan.yml) and run --range --identity-source ci.
            git(root, "checkout", "-q", base)
            proc = subprocess.run(
                [
                    sys.executable,
                    "-I",
                    "-B",
                    "tests/check_public_release.py",
                    "--range",
                    base,
                    head,
                    "--identity-source",
                    "ci",
                ],
                cwd=root,
                capture_output=True,
                text=True,
                check=False,
            )
            combined = proc.stdout + proc.stderr
            self.assertEqual(1, proc.returncode, combined)
            self.assertIn("docs/notes.md:1: matched rule 'identity'", proc.stdout)
            self.assertNotIn(fake_author.casefold(), combined.casefold())
            self.assertNotIn(fake_email.casefold(), combined.casefold())
            self.assertNotIn(scan.SCAN_VERIFIED_MARKER, proc.stdout)
            self.assertNotIn(ident.IDENTITY_VERIFIED_MARKER, proc.stdout)

    def test_ci_checks_committer_identity_and_ignores_github_platform_committer(self) -> None:
        ident = self._identity_mod()
        fake_committer = join("Freja", " ", "Lindqvist")
        fake_committer_email = join("flindqvist", "@", "northwind-lab", ".test")

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "README.md").write_text("GitHub hosted repository\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: base")
            base = git(root, "rev-parse", "HEAD").stdout.strip()

            # Commit with GitHub platform committer passes even though README says "GitHub".
            (root / "docs").mkdir()
            (root / "docs/ok.md").write_text("Uses GitHub Actions\n", encoding="utf-8")
            git(root, "add", "docs/ok.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: github merge",
                author_name="Test Contributor",
                author_email="contributor@example.invalid",
                committer_name="GitHub",
                committer_email=join("noreply", "@", "github", ".com"),
            )
            mid = git(root, "rev-parse", "HEAD").stdout.strip()
            code_ok, out_ok, err_ok = run_main(["--root", str(root), "--range", base, mid, "--identity-source", "ci"])
            self.assertEqual(0, code_ok, out_ok + err_ok)
            self.assertIn(scan.SCAN_VERIFIED_MARKER, out_ok.splitlines())
            self.assertIn(ident.IDENTITY_VERIFIED_MARKER, out_ok.splitlines())

            # Commit whose real committer name appears in file fails.
            (root / "docs/ok.md").write_text(f"Committed by {fake_committer}\n", encoding="utf-8")
            git(root, "add", "docs/ok.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: committer leak",
                author_name="Test Contributor",
                author_email="contributor@example.invalid",
                committer_name=fake_committer,
                committer_email=fake_committer_email,
            )
            head = git(root, "rev-parse", "HEAD").stdout.strip()
            code_bad, out_bad, err_bad = run_main(["--root", str(root), "--range", base, head, "--identity-source", "ci"])
            self.assertEqual(1, code_bad, out_bad + err_bad)
            self.assertIn("docs/ok.md:1: matched rule 'identity'", out_bad)
            self.assertNotIn(fake_committer.casefold(), (out_bad + err_bad).casefold())

    def test_ci_fails_closed_on_shallow_incomplete_history_and_missing_commit_identity(self) -> None:
        ident = self._identity_mod()
        with tempfile.TemporaryDirectory() as directory:
            base_dir = Path(directory)
            origin = base_dir / "origin"
            shallow = base_dir / "shallow"
            git(base_dir, "init", "-q", "-b", "main", str(origin))
            write_scan_configs(origin)
            (origin / "README.md").write_text("v1\n", encoding="utf-8")
            git(origin, "add", "-A")
            git(origin, "commit", "-q", "-m", "chore: c1")
            c1 = git(origin, "rev-parse", "HEAD").stdout.strip()
            (origin / "README.md").write_text("v2\n", encoding="utf-8")
            git(origin, "add", "README.md")
            git(origin, "commit", "-q", "-m", "chore: c2")
            c2 = git(origin, "rev-parse", "HEAD").stdout.strip()

            # Shallow clone of depth=1 lacks base commit c1 -> must fail closed.
            subprocess.run(
                ["git", "clone", "-q", "--depth=1", f"file://{origin}", str(shallow)],
                check=True,
            )
            code, out, err = run_main(["--root", str(shallow), "--range", c1, c2, "--identity-source", "ci"])
            self.assertEqual(1, code, out + err)
            self.assertNotIn(scan.SCAN_VERIFIED_MARKER, out)
            self.assertNotIn(ident.IDENTITY_VERIFIED_MARKER, out)

            # Missing/empty author name in git show output -> fails closed.
            def empty_author_run(cmd, *, cwd=None, timeout=5.0):
                if "show" in cmd:
                    return subprocess.CompletedProcess(cmd, 0, "\x00a@example.invalid\x00Committer\x00c@example.invalid", "")
                return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout, check=False)

            with self.assertRaises(ident.IdentityCollectionError):
                ident.collect_ci_commit_identity(
                    origin,
                    [c2],
                    normalize_fn=scan.normalize_text,
                    base=c1,
                    head=c2,
                    run_cmd=empty_author_run,
                )

    def test_head_cannot_weaken_or_disable_identity_check_in_ci(self) -> None:
        ident = self._identity_mod()
        fake_author = join("Soren", " ", "Lindholm")
        fake_email = join("slindholm", "@", "northwind-lab", ".test")
        author_digest = scan.compute_exemption_digest("identity", fake_author)

        for scenario in ("weaken_identity_helper", "set_identity_off", "add_identity_exemption"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "tests").mkdir()
                shutil.copyfile(ROOT / "tests/check_public_release.py", root / "tests/check_public_release.py")
                shutil.copyfile(ROOT / "tests/public_scan_identity.py", root / "tests/public_scan_identity.py")
                shutil.copyfile(ROOT / "tests/public_scan_keyed.py", root / "tests/public_scan_keyed.py")
                write_scan_configs(root)
                git(root, "init", "-q", "-b", "main")
                (root / "README.md").write_text("clean\n", encoding="utf-8")
                git(root, "add", "-A")
                git(root, "commit", "-q", "-m", "chore: base")
                base = git(root, "rev-parse", "HEAD").stdout.strip()

                git(root, "checkout", "-q", "-b", "feat/tamper")
                if scenario == "weaken_identity_helper":
                    (root / "tests/public_scan_identity.py").write_text(
                        "class IdentityMatcher:\n    def __init__(self, tokens): pass\n    def detect(self, t, path=''): return []\n",
                        encoding="utf-8",
                    )
                elif scenario == "set_identity_off":
                    modes = dict(scan.DEFAULT_MODES)
                    modes["identity"] = "off"
                    write_scan_configs(root, modes=modes)
                elif scenario == "add_identity_exemption":
                    write_scan_configs(
                        root,
                        exemptions=[
                            {
                                "path": "docs/leak.md",
                                "rule": "identity",
                                "digest": author_digest,
                                "reason": "self-approved in head",
                                "approved_by": "@attacker",
                                "expires": "2027-06-01",
                            }
                        ],
                    )
                (root / "docs").mkdir()
                (root / "docs/leak.md").write_text(f"by {fake_author}\n", encoding="utf-8")
                git(root, "add", "-A")
                git(
                    root,
                    "commit",
                    "-q",
                    "-m",
                    f"feat: {scenario}",
                    author_name=fake_author,
                    author_email=fake_email,
                )
                head = git(root, "rev-parse", "HEAD").stdout.strip()

                git(root, "checkout", "-q", base)
                proc = subprocess.run(
                    [
                        sys.executable,
                        "-I",
                        "-B",
                        "tests/check_public_release.py",
                        "--range",
                        base,
                        head,
                        "--identity-source",
                        "ci",
                    ],
                    cwd=root,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(1, proc.returncode, proc.stdout + proc.stderr)
                self.assertIn("docs/leak.md:1: matched rule 'identity'", proc.stdout)
                self.assertNotIn(fake_author.casefold(), (proc.stdout + proc.stderr).casefold())
                self.assertNotIn(ident.IDENTITY_VERIFIED_MARKER, proc.stdout)

    def test_public_scan_workflow_enforces_identity_marker_and_aggregator_fails_closed(self) -> None:
        ident = self._identity_mod()
        workflow = (ROOT / ".github/workflows/public-scan.yml").read_text(encoding="utf-8")
        self.assertIn("--identity-source ci", workflow)
        self.assertIn(ident.IDENTITY_VERIFIED_MARKER, workflow)
        self.assertIn("identity_verified", workflow)
        self.assertNotIn("secrets.", workflow)

        # Extract aggregator script and verify missing identity_verified fails closed.
        lines = workflow.splitlines()
        in_result = False
        in_run = False
        run_indent = 0
        script_lines: list[str] = []
        for line in lines:
            if line.strip() == "result:":
                in_result = True
                continue
            if in_result and not in_run:
                if line.lstrip().startswith("run: |"):
                    in_run = True
                    run_indent = len(line) - len(line.lstrip(" "))
                continue
            if in_run:
                if line.strip() and (len(line) - len(line.lstrip(" "))) <= run_indent:
                    break
                script_lines.append(line[run_indent + 2 :] if len(line) >= run_indent + 2 else "")
        script = "\n".join(script_lines)
        self.assertIn("LINUX_IDENTITY_VERIFIED", script)
        self.assertIn("MACOS_IDENTITY_VERIFIED", script)

        for name, env_vars, expected_rc in (
            (
                "all_verified_passes",
                {
                    "LINUX_RESULT": "success",
                    "LINUX_VERIFIED": "true",
                    "LINUX_IDENTITY_VERIFIED": "true",
                    "MACOS_RESULT": "success",
                    "MACOS_VERIFIED": "true",
                    "MACOS_IDENTITY_VERIFIED": "true",
                },
                0,
            ),
            (
                "missing_linux_identity_fails",
                {
                    "LINUX_RESULT": "success",
                    "LINUX_VERIFIED": "true",
                    "LINUX_IDENTITY_VERIFIED": "",
                    "MACOS_RESULT": "success",
                    "MACOS_VERIFIED": "true",
                    "MACOS_IDENTITY_VERIFIED": "true",
                },
                1,
            ),
            (
                "missing_macos_identity_fails",
                {
                    "LINUX_RESULT": "success",
                    "LINUX_VERIFIED": "true",
                    "LINUX_IDENTITY_VERIFIED": "true",
                    "MACOS_RESULT": "success",
                    "MACOS_VERIFIED": "true",
                    "MACOS_IDENTITY_VERIFIED": "",
                },
                1,
            ),
        ):
            with self.subTest(case=name):
                proc = subprocess.run(
                    ["bash", "-c", script],
                    env={"PATH": os.environ.get("PATH", "/usr/bin:/bin"), **env_vars},
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(expected_rc, proc.returncode, proc.stdout + proc.stderr)


class KeyedListAndModesTests(unittest.TestCase):
    """AC-6 & AC-7: optional keyed HMAC list, modes block/warn/off, non-default reporting, exemptions."""

    SYNTHETIC_KEY_HEX = bytes(range(32)).hex()

    def _keyed_mod(self):
        return _load_module("public_scan_keyed_test", "tests/public_scan_keyed.py")

    def _identity_mod(self):
        return _load_module("public_scan_identity_modes", "tests/public_scan_identity.py")

    def test_keyed_list_is_off_by_default_and_needs_no_key(self) -> None:
        self.assertEqual("off", scan.DEFAULT_MODES["keyed-list"])
        self.assertEqual("block", scan.DEFAULT_MODES["identity"])

        repo_modes = scan.load_modes(ROOT / "tests/public_scan_modes.json")
        self.assertEqual("off", repo_modes["keyed-list"])
        self.assertEqual("block", repo_modes["identity"])

        # With default modes, running with no HMAC key or keyed list file succeeds and prints no non-default modes.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "README.md").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: clean")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            clean_env = {k: v for k, v in os.environ.items() if "HMAC" not in k}
            with mock.patch.dict(os.environ, clean_env, clear=True):
                code, out, err = run_main(["--root", str(root), "--tree", head, "--identity-source", "ci"])
            self.assertEqual(0, code, out + err)
            self.assertNotIn("Non-default detector modes:", out)

    def test_keyed_list_matches_via_hmac_fails_closed_on_missing_key_and_plaintext_entries(self) -> None:
        keyed = self._keyed_mod()
        key_bytes = bytes.fromhex(self.SYNTHETIC_KEY_HEX)
        term = join("Cobalt", " ", "Customer", " ", "Nine")
        term_digest = keyed.compute_keyed_digest(key_bytes, term, normalize_fn=scan.normalize_text)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            modes_on = dict(scan.DEFAULT_MODES)
            modes_on["keyed-list"] = "block"
            write_scan_configs(
                root,
                modes=modes_on,
                keyed_list={
                    "schema_version": 1,
                    "algorithm": "hmac-sha256",
                    "digests": [term_digest],
                },
            )
            git(root, "init", "-q", "-b", "main")
            (root / "docs").mkdir()
            (root / "docs/hit.md").write_text(f"Client: {term}\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "docs: keyed hit")
            hit_sha = git(root, "rev-parse", "HEAD").stdout.strip()

            (root / "docs/hit.md").write_text("Client: Acme Example\n", encoding="utf-8")
            git(root, "add", "docs/hit.md")
            git(root, "commit", "-q", "-m", "docs: clean")
            clean_sha = git(root, "rev-parse", "HEAD").stdout.strip()

            # 1) Missing key fails closed.
            clean_env = {k: v for k, v in os.environ.items() if "HMAC" not in k}
            with mock.patch.dict(os.environ, clean_env, clear=True):
                code_missing, out_missing, _ = run_main(
                    ["--root", str(root), "--tree", clean_sha, "--identity-source", "ci"]
                )
            self.assertEqual(1, code_missing, out_missing)
            self.assertNotIn(scan.SCAN_VERIFIED_MARKER, out_missing)

            # 2) Valid key via env var catches hit with redacted output and passes clean tree.
            with mock.patch.dict(os.environ, {**clean_env, "PUBLIC_SCAN_HMAC_KEY": self.SYNTHETIC_KEY_HEX}, clear=True):
                code_hit, out_hit, err_hit = run_main(
                    ["--root", str(root), "--tree", hit_sha, "--identity-source", "ci"]
                )
                code_clean, out_clean, err_clean = run_main(
                    ["--root", str(root), "--tree", clean_sha, "--identity-source", "ci"]
                )
            self.assertEqual(1, code_hit, out_hit + err_hit)
            self.assertIn("docs/hit.md:1: matched rule 'keyed-list'", out_hit)
            self.assertIn("Non-default detector modes:", out_hit)
            self.assertIn("- keyed-list: block", out_hit)
            self.assertNotIn(term.casefold(), (out_hit + err_hit).casefold())
            self.assertNotIn(self.SYNTHETIC_KEY_HEX, out_hit + err_hit)
            self.assertEqual(0, code_clean, out_clean + err_clean)
            self.assertIn("- keyed-list: block", out_clean)

            # 3) Untracked key file (mode 0600) works; loose perms (0644) fail closed.
            key_file = root / ".secrets/public-scan-hmac"
            key_file.parent.mkdir(parents=True, exist_ok=True)
            key_file.write_text(self.SYNTHETIC_KEY_HEX + "\n", encoding="utf-8")
            key_file.chmod(0o600)
            with mock.patch.dict(os.environ, clean_env, clear=True):
                code_file, out_file, _ = run_main(
                    ["--root", str(root), "--tree", clean_sha, "--identity-source", "ci"]
                )
            self.assertEqual(0, code_file, out_file)

            key_file.chmod(0o644)
            with mock.patch.dict(os.environ, clean_env, clear=True):
                code_loose, out_loose, _ = run_main(
                    ["--root", str(root), "--tree", clean_sha, "--identity-source", "ci"]
                )
            self.assertEqual(1, code_loose, out_loose)

            # 4) Plaintext or malformed entry in keyed-list file fails closed without echoing value.
            key_file.chmod(0o600)
            for label, bad_payload in (
                ("plaintext_entry", {"schema_version": 1, "algorithm": "hmac-sha256", "digests": [term]}),
                ("short_hex", {"schema_version": 1, "algorithm": "hmac-sha256", "digests": ["deadbeef"]}),
                ("uppercase_hex", {"schema_version": 1, "algorithm": "hmac-sha256", "digests": ["A" * 64]}),
                ("empty_digests", {"schema_version": 1, "algorithm": "hmac-sha256", "digests": []}),
                ("wrong_algorithm", {"schema_version": 1, "algorithm": "sha256", "digests": [term_digest]}),
            ):
                with self.subTest(bad_case=label):
                    bad_path = root / f"bad_{label}.json"
                    bad_path.write_text(json.dumps(bad_payload) + "\n", encoding="utf-8")
                    with self.assertRaises((scan.ScanConfigError, keyed.KeyedListConfigError)) as ctx:
                        keyed.load_keyed_list(bad_path)
                    self.assertNotIn(term.casefold(), str(ctx.exception).casefold())

    def test_identity_and_keyed_modes_and_tracked_exemptions(self) -> None:
        ident = self._identity_mod()
        keyed = self._keyed_mod()
        key_bytes = bytes.fromhex(self.SYNTHETIC_KEY_HEX)

        fake_host = join("quartz", "-node-88")
        keyed_term = join("Onyx", " ", "Project", " ", "Seven")
        keyed_digest = keyed.compute_keyed_digest(key_bytes, keyed_term, normalize_fn=scan.normalize_text)
        identity_digest = scan.compute_exemption_digest("identity", fake_host)

        ident_matcher = ident.IdentityMatcher(frozenset({fake_host}))
        keyed_matcher = keyed.KeyedListMatcher(key_bytes, frozenset({keyed_digest}), normalize_fn=scan.normalize_text)

        # Modes: block / warn / off for both identity and keyed-list.
        src = [scan.SourceFile("docs/test.md", f"host={fake_host}\nterm={keyed_term}\n".encode("utf-8"))]
        for mode in ("block", "warn", "off"):
            with self.subTest(mode=mode):
                modes = dict(scan.DEFAULT_MODES)
                modes["identity"] = mode
                modes["keyed-list"] = mode
                res = scan.scan_sources(
                    src,
                    modes=modes,
                    identity_matcher=ident_matcher,
                    keyed_matcher=keyed_matcher,
                )
                if mode == "block":
                    self.assertEqual(
                        [
                            "docs/test.md:1: matched rule 'identity'",
                            "docs/test.md:2: matched rule 'keyed-list'",
                        ],
                        res.failures,
                    )
                    self.assertEqual([], res.warnings)
                elif mode == "warn":
                    self.assertEqual([], res.failures)
                    self.assertEqual(
                        [
                            "docs/test.md:1: matched rule 'identity'",
                            "docs/test.md:2: matched rule 'keyed-list'",
                        ],
                        res.warnings,
                    )
                else:
                    self.assertEqual([], res.failures)
                    self.assertEqual([], res.warnings)

        # Non-default reporting prints identity when warn/off and keyed-list when block/warn.
        out = io.StringIO()
        custom_modes = dict(scan.DEFAULT_MODES)
        custom_modes["identity"] = "off"
        custom_modes["keyed-list"] = "warn"
        with contextlib.redirect_stdout(out):
            scan._report(
                failures=[],
                warnings=[],
                modes=custom_modes,
                exemptions=(),
                applied_exemptions=frozenset(),
                passed_message="passed",
                emit_verified_marker=True,
                emit_identity_verified_marker=False,
            )
        text = out.getvalue()
        self.assertIn("- identity: off", text)
        self.assertIn("- keyed-list: warn", text)
        self.assertNotIn(ident.IDENTITY_VERIFIED_MARKER, text)

        # Tracked exemptions: SHA-256 for identity, keyed HMAC for keyed-list.
        modes_both_block = dict(scan.DEFAULT_MODES)
        modes_both_block["identity"] = "block"
        modes_both_block["keyed-list"] = "block"
        ex_ident = scan.ExemptionRecord(
            path="docs/test.md",
            rule="identity",
            digest=identity_digest,
            reason="synthetic host fixture",
            approved_by="@owner",
            expires="2027-06-01",
        )
        ex_keyed = scan.ExemptionRecord(
            path="docs/test.md",
            rule="keyed-list",
            digest=keyed_digest,
            reason="synthetic keyed fixture",
            approved_by="@owner",
            expires="2027-06-01",
        )
        res_exempt = scan.scan_sources(
            src,
            modes=modes_both_block,
            exemptions=[ex_ident, ex_keyed],
            identity_matcher=ident_matcher,
            keyed_matcher=keyed_matcher,
        )
        self.assertEqual([], res_exempt.failures)
        self.assertIn(ex_ident.key, res_exempt.applied_exemptions)
        self.assertIn(ex_keyed.key, res_exempt.applied_exemptions)

        # Same content in another path is NOT exempted.
        other_src = [scan.SourceFile("docs/other.md", f"host={fake_host}\nterm={keyed_term}\n".encode("utf-8"))]
        res_other = scan.scan_sources(
            other_src,
            modes=modes_both_block,
            exemptions=[ex_ident, ex_keyed],
            identity_matcher=ident_matcher,
            keyed_matcher=keyed_matcher,
        )
        self.assertEqual(
            [
                "docs/other.md:1: matched rule 'identity'",
                "docs/other.md:2: matched rule 'keyed-list'",
            ],
            res_other.failures,
        )


class CommitHookDocsAndContributorJourneyTests(unittest.TestCase):
    """AC-5 (commit-hook docs) & AC-8 (contributor push journey on macOS and Linux)."""

    def test_ac5_docs_document_codeflow_managed_hooks_and_commit_time_mechanism(self) -> None:
        checklist = (ROOT / "docs/13-public-release-checklist.md").read_text(encoding="utf-8")
        low = checklist.lower()
        self.assertIn("codeflow", low)
        self.assertIn("pre-push", low)
        self.assertIn("pre-commit", low)
        self.assertIn("codeflow update", low)
        self.assertIn("identity", low)
        self.assertIn("keyed-list", low)

    def test_ac8_contributor_push_journey_on_macos_and_linux(self) -> None:
        """AC-8: adding own host name refuses push with only file/line/rule on macOS and Linux; removing passes."""
        native_plat = "darwin" if sys.platform.startswith("darwin") else "linux"
        for plat in ("darwin", "linux"):
            fake_host = join("aurora", "-", plat, "-node91")
            with self.subTest(platform=plat), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                remote = base / "remote.git"
                repo = base / "work"
                fake_bin = base / "fake-bin"
                fake_bin.mkdir()

                subprocess.run(["git", "init", "--bare", "-q", str(remote)], check=True)
                git(base, "init", "-q", "-b", "main", str(repo))
                git(repo, "config", "user.name", join("Nadia", " ", "Holm"))
                git(repo, "config", "user.email", join("nholm", "@", "northwind-lab", ".test"))

                # Provide stubbed scutil, hostnamectl, and tailscale binaries so the test
                # never depends on the host's live tailscale, scutil, or hostnamectl state.
                scutil = fake_bin / "scutil"
                scutil.write_text(
                    "#!/bin/sh\n"
                    'if [ "$1" = "--get" ] && [ "$2" = "ComputerName" ]; then echo "' + fake_host + '"; exit 0; fi\n'
                    'if [ "$1" = "--get" ] && [ "$2" = "LocalHostName" ]; then echo "' + fake_host + '"; exit 0; fi\n'
                    'if [ "$1" = "--get" ] && [ "$2" = "HostName" ]; then echo "HostName: not set" >&2; exit 1; fi\n'
                    "exit 1\n",
                    encoding="utf-8",
                )
                scutil.chmod(scutil.stat().st_mode | stat.S_IXUSR)

                hctl = fake_bin / "hostnamectl"
                hctl.write_text(
                    "#!/bin/sh\n"
                    'echo "   Static hostname: ' + fake_host + '"\n'
                    'echo "   Pretty hostname: ' + fake_host + '"\n',
                    encoding="utf-8",
                )
                hctl.chmod(hctl.stat().st_mode | stat.S_IXUSR)

                ts_payload = _fake_tailscale_json(
                    fake_host,
                    f"{fake_host}." + join("tail", "44ee", ".ts", ".net."),
                    join("tail", "44ee", ".ts", ".net"),
                    join("tail", "44ee", ".ts", ".net"),
                )
                ts_bin = fake_bin / "tailscale"
                ts_bin.write_text(
                    "#!/bin/sh\n"
                    f"printf '%s\\n' '{ts_payload}'\n",
                    encoding="utf-8",
                )
                ts_bin.chmod(ts_bin.stat().st_mode | stat.S_IXUSR)

                (repo / ".codeflow").mkdir()
                shutil.copyfile(ROOT / ".codeflow/policy.json", repo / ".codeflow/policy.json")
                shutil.copytree(ROOT / ".codeflow/git-hooks", repo / ".codeflow/git-hooks")
                (repo / "scripts").mkdir()
                shutil.copyfile(ROOT / "scripts/check_git_discipline.py", repo / "scripts/check_git_discipline.py")
                (repo / "tests").mkdir()
                shutil.copyfile(ROOT / "tests/check_public_release.py", repo / "tests/check_public_release.py")
                shutil.copyfile(ROOT / "tests/public_scan_identity.py", repo / "tests/public_scan_identity.py")
                shutil.copyfile(ROOT / "tests/public_scan_keyed.py", repo / "tests/public_scan_keyed.py")
                # Isolate account (pwd/getlogin) and socket.gethostname in the temp repo copy
                # so macOS/Linux CI runner hostnames or account names never pollute the journey.
                ident_stub = (
                    "\n\n# Test-only isolated identity source stub (temp repo copy only).\n"
                    "def _wskit_install_test_identity_stubs() -> None:\n"
                    "    import json as _json\n"
                    "    import os as _os\n"
                    "    import pwd as _pwd\n"
                    "    import socket as _socket\n"
                    "    from pathlib import Path as _Path\n"
                    "    from types import SimpleNamespace as _NS\n"
                    "    _cfg_path = _os.environ.get('_WSKIT_TEST_IDENTITY_JSON', '')\n"
                    "    if not _cfg_path:\n"
                    "        return\n"
                    "    _data = _json.loads(_Path(_cfg_path).read_text(encoding='utf-8'))\n"
                    "    _pwd.getpwuid = lambda _uid: _NS(pw_name=_data['user'], pw_gecos=_data['full_name'])\n"
                    "    _os.getlogin = lambda: _data['user']\n"
                    "    _socket.gethostname = lambda: _data['host']\n"
                    "\n"
                    "_wskit_install_test_identity_stubs()\n"
                )
                (repo / "tests/public_scan_identity.py").write_text(
                    (repo / "tests/public_scan_identity.py").read_text(encoding="utf-8") + ident_stub,
                    encoding="utf-8",
                )
                ident_json = base / "fake-identity.json"
                ident_json.write_text(
                    json.dumps(
                        {
                            "user": join("nholm", "dev"),
                            "full_name": join("Nadia", " ", "Holm"),
                            "host": fake_host,
                        }
                    )
                    + "\n",
                    encoding="utf-8",
                )
                write_scan_configs(repo)
                for req in scan.REQUIRED:
                    target = repo / req
                    if not target.exists():
                        target.parent.mkdir(parents=True, exist_ok=True)
                        target.touch()
                (repo / "VERSION").write_text("1.0.0\n", encoding="utf-8")
                (repo / "CHANGELOG.md").write_text(
                    "# Changelog\n\n## [Unreleased]\n\nPlanned as release 1.0.0.\n",
                    encoding="utf-8",
                )
                (repo / "README.md").write_text("Version 1.0.0\n", encoding="utf-8")
                (repo / "templates/private-fleet/kit.lock").write_text("1.0.0\n", encoding="utf-8")
                (repo / ".github/dependabot.yml").write_text("".join(scan.DEPENDABOT_COMMIT_POLICY), encoding="utf-8")
                if plat == native_plat:
                    (repo / "Makefile").write_text(
                        "ci-check:\n\t@python3 -I -B tests/check_public_release.py --allow-missing-license\n",
                        encoding="utf-8",
                    )
                else:
                    (repo / "Makefile").write_text(
                        "ci-check:\n"
                        "\t@python3 -I -B -c \"import importlib.util, sys; "
                        "sys.platform = '" + plat + "'; "
                        "spec = importlib.util.spec_from_file_location('cpr', 'tests/check_public_release.py'); "
                        "mod = importlib.util.module_from_spec(spec); sys.modules['cpr'] = mod; spec.loader.exec_module(mod); "
                        "raise SystemExit(mod.main(['--allow-missing-license']))\"\n",
                        encoding="utf-8",
                    )

                git(repo, "add", "-A")
                git(repo, "commit", "-q", "-m", "chore: initial repo")
                git(repo, "remote", "add", "origin", str(remote))
                git(repo, "push", "-q", "origin", "main")
                git(repo, "config", "core.hooksPath", ".codeflow/git-hooks")

                hook_env = {
                    "PATH": f"{fake_bin}:{os.environ.get('PATH', '/usr/bin:/bin')}",
                    "GIT_CONFIG_GLOBAL": "/dev/null",
                    "GIT_CONFIG_NOSYSTEM": "1",
                    "_WSKIT_TEST_IDENTITY_JSON": str(ident_json),
                }

                # Contributor adds their fake host name to a doc -> push refused.
                git(repo, "checkout", "-q", "-b", "feat/host-note")
                note = repo / "docs/machine-note.md"
                note.write_text(f"Workstation host: {fake_host}\n", encoding="utf-8")
                git(repo, "add", "docs/machine-note.md")
                git(repo, "commit", "-q", "-m", "docs: add workstation note")
                bad_commit = git(repo, "rev-parse", "HEAD").stdout.strip()

                push_fail = git(repo, "push", "origin", "feat/host-note", check=False, extra_env=hook_env)
                transcript_fail = push_fail.stdout + push_fail.stderr
                self.assertNotEqual(0, push_fail.returncode, transcript_fail)
                self.assertIn("docs/machine-note.md:1: matched rule 'identity'", transcript_fail)
                self.assertNotIn(fake_host.casefold(), transcript_fail.casefold())
                self.assertEqual("", git(remote, "for-each-ref", "refs/heads/feat/host-note").stdout.strip())

                # Also verify the --commits pre-push path directly under the target OS platform.
                commits_proc = subprocess.run(
                    [
                        sys.executable,
                        "-I",
                        "-B",
                        "-c",
                        "import importlib.util, sys; "
                        f"sys.platform = '{plat}'; "
                        "spec = importlib.util.spec_from_file_location('cpr', 'tests/check_public_release.py'); "
                        "mod = importlib.util.module_from_spec(spec); sys.modules['cpr'] = mod; "
                        "spec.loader.exec_module(mod); "
                        f"raise SystemExit(mod.main(['--commits', '{bad_commit}']))",
                    ],
                    cwd=repo,
                    env={**os.environ, **hook_env},
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(1, commits_proc.returncode, commits_proc.stdout + commits_proc.stderr)
                self.assertIn("docs/machine-note.md:1: matched rule 'identity'", commits_proc.stdout)
                self.assertNotIn(fake_host.casefold(), (commits_proc.stdout + commits_proc.stderr).casefold())

                # Contributor removes host name and replaces with placeholder -> push succeeds.
                note.write_text("Workstation host: host-a\n", encoding="utf-8")
                git(repo, "add", "docs/machine-note.md")
                git(repo, "reset", "--soft", "main")
                git(repo, "commit", "-q", "-m", "docs: use placeholder hostname")

                push_ok = git(repo, "push", "origin", "feat/host-note", check=False, extra_env=hook_env)
                transcript_ok = push_ok.stdout + push_ok.stderr
                self.assertEqual(0, push_ok.returncode, transcript_ok)
                self.assertIn("refs/heads/feat/host-note", git(remote, "for-each-ref", "refs/heads/feat/host-note").stdout)


class Round1ReviewRegressionTests(unittest.TestCase):
    """Regression tests for PR #43 round 1 cross-family review findings (H1-H3, M1-M6)."""

    SYNTHETIC_KEY_HEX = bytes(range(31, -1, -1)).hex()

    def _identity_mod(self):
        return _load_module("public_scan_identity_r1", "tests/public_scan_identity.py")

    def _keyed_mod(self):
        return _load_module("public_scan_keyed_r1", "tests/public_scan_keyed.py")

    def _repo_mod(self):
        return _load_module("check_repository_r1", "tests/check_repository.py")

    def test_h1_workflow_checks_out_pr_base_sha_and_push_before_sha(self) -> None:
        workflow = (ROOT / ".github/workflows/public-scan.yml").read_text(encoding="utf-8")
        self.assertIn("github.event.pull_request.base.sha", workflow)
        self.assertIn("github.event.before", workflow)
        # Must not unconditionally check out github.sha as the trusted scanner revision.
        self.assertNotIn("ref: ${{ github.sha }}", workflow)
        # Must fail closed if push before is all zeros on a non-root commit.
        self.assertIn("non-root", workflow.lower())

    def test_h2_workflow_requires_identity_marker_and_docs_explain_bootstrap(self) -> None:
        workflow = (ROOT / ".github/workflows/public-scan.yml").read_text(encoding="utf-8")
        self.assertIn("PUBLIC_SCAN_IDENTITY_VERIFIED=1", workflow)
        self.assertIn("LINUX_IDENTITY_VERIFIED", workflow)
        self.assertIn("MACOS_IDENTITY_VERIFIED", workflow)

        checklist = (ROOT / "docs/13-public-release-checklist.md").read_text(encoding="utf-8").lower()
        self.assertIn("bootstrap", checklist)
        self.assertIn("post-merge", checklist)
        self.assertIn("live", checklist)

    def test_h3_dependabot_pr_passes_and_ci_range_scopes_identity_to_added_content(self) -> None:
        ident = self._identity_mod()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "README.md").write_text(
                "This repository uses python and dependabot for automation.\n",
                encoding="utf-8",
            )
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: base")
            base = git(root, "rev-parse", "HEAD").stdout.strip()

            # (a) Dependabot PR commit passes cleanly even though README mentions dependabot.
            git(root, "checkout", "-q", "-b", "dependabot/pip/fixture")
            (root / "docs").mkdir()
            (root / "docs/dep.md").write_text("Bump dependency via dependabot[bot] workflow.\n", encoding="utf-8")
            git(root, "add", "docs/dep.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "build(deps): bump fixture",
                author_name="dependabot[bot]",
                author_email=join("49699333+dependabot[bot]", "@", "users.noreply.github", ".com"),
                committer_name="GitHub",
                committer_email=join("noreply", "@", "github", ".com"),
            )
            dep_head = git(root, "rev-parse", "HEAD").stdout.strip()
            code_dep, out_dep, err_dep = run_main(
                ["--root", str(root), "--range", base, dep_head, "--identity-source", "ci"]
            )
            self.assertEqual(0, code_dep, out_dep + err_dep)
            self.assertIn(ident.IDENTITY_VERIFIED_MARKER, out_dep.splitlines())

            # (b) Contributor with handle "python" editing an unrelated file passes,
            # because pre-existing README.md mentioning "python" is NOT added by the range.
            git(root, "checkout", "-q", "-b", "feat/python-author", base)
            (root / "docs").mkdir(exist_ok=True)
            (root / "docs/unrelated.md").write_text("Clean documentation update.\n", encoding="utf-8")
            git(root, "add", "docs/unrelated.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: clean update",
                author_name="python",
                author_email=join("python", "@", "northwind-lab", ".test"),
            )
            clean_head = git(root, "rev-parse", "HEAD").stdout.strip()
            code_ok, out_ok, err_ok = run_main(
                ["--root", str(root), "--range", base, clean_head, "--identity-source", "ci"]
            )
            self.assertEqual(0, code_ok, out_ok + err_ok)

            # Adding "python" in a new line in the range MUST fail.
            (root / "docs/unrelated.md").write_text("Clean documentation update.\nWritten by python.\n", encoding="utf-8")
            git(root, "add", "docs/unrelated.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: add author handle",
                author_name="python",
                author_email=join("python", "@", "northwind-lab", ".test"),
            )
            leak_line_head = git(root, "rev-parse", "HEAD").stdout.strip()
            code_line, out_line, err_line = run_main(
                ["--root", str(root), "--range", base, leak_line_head, "--identity-source", "ci"]
            )
            self.assertEqual(1, code_line, out_line + err_line)
            self.assertIn("docs/unrelated.md:2: matched rule 'identity'", out_line)

            # Adding a new file whose path contains "python" MUST fail with redacted path.
            git(root, "checkout", "-q", "-b", "feat/python-path", clean_head)
            (root / "docs/python.md").write_text("Clean body.\n", encoding="utf-8")
            git(root, "add", "docs/python.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: add file",
                author_name="python",
                author_email=join("python", "@", "northwind-lab", ".test"),
            )
            leak_path_head = git(root, "rev-parse", "HEAD").stdout.strip()
            code_path, out_path, err_path = run_main(
                ["--root", str(root), "--range", base, leak_path_head, "--identity-source", "ci"]
            )
            self.assertEqual(1, code_path, out_path + err_path)
            self.assertIn(":0: matched rule 'identity'", out_path)
            self.assertIn("<redacted-", out_path)
            self.assertNotIn("docs/python.md", out_path + err_path)

    def test_m1_metadata_and_exemption_inventory_paths_redact_identity(self) -> None:
        ident = self._identity_mod()
        fake_host = join("velvet", "-workbox-55")
        profile = ident.IdentityProfile(
            source_kind="local",
            tokens=frozenset({fake_host}),
            notes=(),
        )
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(
                root,
                exemptions=[
                    {
                        "path": f"docs/{fake_host}/note.md",
                        "rule": "identity",
                        "digest": scan.compute_exemption_digest("identity", fake_host),
                        "reason": "expired fixture",
                        "approved_by": "@owner",
                        "expires": "2020-01-01",
                    }
                ],
            )
            git(root, "init", "-q", "-b", "main")
            (root / "reports").mkdir()
            (root / f"reports/{fake_host}.txt").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-f", "-A")
            git(root, "commit", "-q", "-m", "chore: m1 fixture")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            with mock.patch.object(scan, "_collect_identity_profile", return_value=profile):
                code, out, err = run_main(["--root", str(root), "--commits", head, "--identity-source", "local"])
            combined = out + err
            self.assertEqual(1, code, combined)
            self.assertIn("<redacted-", combined)
            self.assertNotIn(fake_host, combined.casefold())

    def test_m2_wrapped_and_hyphenated_two_word_identities_are_caught(self) -> None:
        ident = self._identity_mod()
        full_name = join("Kestrel", " ", "Nolan")
        matcher = ident.IdentityMatcher(
            ident.derive_identity_tokens([("git-name", full_name)], normalize_fn=scan.normalize_text)
        )

        # Joined by a hyphen.
        hyphenated = join("Kestrel", "-", "Nolan")
        res_hyph = scan.scan_sources(
            [scan.SourceFile("docs/wrap.md", f"Author: {hyphenated}\n".encode("utf-8"))],
            identity_matcher=matcher,
        )
        self.assertEqual(["docs/wrap.md:1: matched rule 'identity'"], res_hyph.failures)
        self.assertNotIn(join("kestrel"), "\n".join(res_hyph.failures).casefold())

        # Split across Markdown line break.
        wrapped = join("Reviewed by Kestrel", "\n", "Nolan yesterday.\n")
        res_wrap = scan.scan_sources(
            [scan.SourceFile("docs/wrap.md", wrapped.encode("utf-8"))],
            identity_matcher=matcher,
        )
        self.assertEqual(["docs/wrap.md:1: matched rule 'identity'"], res_wrap.failures)

        # Split with trailing hyphen across line break.
        wrapped_hyph = join("Reviewed by Kestrel-", "\n", "Nolan yesterday.\n")
        res_wrap_hyph = scan.scan_sources(
            [scan.SourceFile("docs/wrap.md", wrapped_hyph.encode("utf-8"))],
            identity_matcher=matcher,
        )
        self.assertEqual(["docs/wrap.md:1: matched rule 'identity'"], res_wrap_hyph.failures)

    def test_m3_required_vs_optional_empty_values_and_macos_tailscale_app_path(self) -> None:
        ident = self._identity_mod()

        def ok_runner(plat: str, *, scutil_host_rc: int = 1, scutil_host_out: str = "", scutil_host_err: str = "HostName: not set\n", hostnamectl_out: str | None = None, ts_json: str | None = None):
            def run_cmd(cmd, *, cwd=None, timeout=5.0):
                if cmd[:3] == ["git", "--no-optional-locks", "config"]:
                    val = join("Zora", " ", "Vance") if cmd[-1] == "user.name" else join("zvance", "@", "northwind-lab", ".test")
                    return subprocess.CompletedProcess(cmd, 0, val + "\n", "")
                if cmd[:2] == ["scutil", "--get"]:
                    if cmd[2] == "HostName":
                        return subprocess.CompletedProcess(cmd, scutil_host_rc, scutil_host_out, scutil_host_err)
                    return subprocess.CompletedProcess(cmd, 0, join("Maren", "-Mac") + "\n", "")
                if cmd == ["hostnamectl"]:
                    out = hostnamectl_out if hostnamectl_out is not None else f"Static hostname: {join('nordic', '-box-1')}\n"
                    return subprocess.CompletedProcess(cmd, 0, out, "")
                if cmd[0].endswith(("tailscale", "Tailscale")):
                    body = ts_json if ts_json is not None else _fake_tailscale_json(
                        join("tsnode", "-1"),
                        join("tsnode", "-1.", "tail88bb", ".ts", ".net."),
                        join("tail", "88bb", ".ts", ".net"),
                        join("tail", "88bb", ".ts", ".net"),
                    )
                    return subprocess.CompletedProcess(cmd, 0, body, "")
                raise AssertionError(f"unexpected cmd: {cmd}")
            return run_cmd

        # 1) Optional empty scutil HostName with rc=0 is treated as unavailable (does not fail or add "").
        profile_empty_host = ident.collect_local_identity(
            ROOT,
            normalize_fn=scan.normalize_text,
            platform_name="darwin",
            run_cmd=ok_runner("darwin", scutil_host_rc=0, scutil_host_out="  \n", scutil_host_err=""),
            get_pwuid=lambda _uid: _fake_pwuid(join("zora", "dev"), join("Zora", " ", "Vance")),
            get_login=lambda: join("zora", "dev"),
            get_hostname=lambda: join("nordic", "-box-1"),
            which=lambda _bin: None,
            is_executable=lambda _p: False,
        )
        self.assertIn(join("zora", "dev"), profile_empty_host.tokens)
        self.assertNotIn("", profile_empty_host.tokens)

        # 2) Linux hostnamectl with only Pretty/Transient (missing Static hostname) MUST fail closed.
        with self.assertRaises(ident.IdentityCollectionError):
            ident.collect_local_identity(
                ROOT,
                normalize_fn=scan.normalize_text,
                platform_name="linux",
                run_cmd=ok_runner("linux", hostnamectl_out="Static hostname: (none)\nPretty hostname: Nice Box\n"),
                get_pwuid=lambda _uid: _fake_pwuid(join("zora", "dev"), ""),
                get_login=lambda: join("zora", "dev"),
                get_hostname=lambda: join("nordic", "-box-1"),
                which=lambda _bin: None,
            )

        # 3) Installed Tailscale JSON with only Self.HostName (missing tailnet/DNS name) MUST fail closed.
        node_only_json = json.dumps({"BackendState": "Running", "Self": {"HostName": join("tsnode", "-only")}})
        with self.assertRaises(ident.IdentityCollectionError):
            ident.collect_local_identity(
                ROOT,
                normalize_fn=scan.normalize_text,
                platform_name="linux",
                run_cmd=ok_runner("linux", ts_json=node_only_json),
                get_pwuid=lambda _uid: _fake_pwuid(join("zora", "dev"), ""),
                get_login=lambda: join("zora", "dev"),
                get_hostname=lambda: join("nordic", "-box-1"),
                which=lambda name: "/usr/bin/tailscale" if name == "tailscale" else None,
            )

        # 4) Empty optional account GECOS full name is treated as unavailable (does not fail or add empty token).
        profile_no_gecos = ident.collect_local_identity(
            ROOT,
            normalize_fn=scan.normalize_text,
            platform_name="linux",
            run_cmd=ok_runner("linux"),
            get_pwuid=lambda _uid: _fake_pwuid(join("zora", "dev"), ""),
            get_login=lambda: join("zora", "dev"),
            get_hostname=lambda: join("nordic", "-box-1"),
            which=lambda _bin: None,
        )
        self.assertIn(join("zora", "dev"), profile_no_gecos.tokens)
        self.assertNotIn("", profile_no_gecos.tokens)

        # 5) On macOS, /Applications/Tailscale.app/Contents/MacOS/Tailscale is checked when PATH lacks tailscale.
        seen_cmds: list[list[str]] = []
        base_darwin_run = ok_runner("darwin")

        def tracking_run(cmd, *, cwd=None, timeout=5.0):
            seen_cmds.append(list(cmd))
            return base_darwin_run(cmd, cwd=cwd, timeout=timeout)

        profile_app = ident.collect_local_identity(
            ROOT,
            normalize_fn=scan.normalize_text,
            platform_name="darwin",
            run_cmd=tracking_run,
            get_pwuid=lambda _uid: _fake_pwuid(join("zora", "dev"), join("Zora", " ", "Vance")),
            get_login=lambda: join("zora", "dev"),
            get_hostname=lambda: join("maren", "-mac"),
            which=lambda _bin: None,
            is_executable=lambda path: path == ident.MACOS_TAILSCALE_APP_BIN,
        )
        self.assertTrue(
            any(cmd[0] == ident.MACOS_TAILSCALE_APP_BIN for cmd in seen_cmds),
            seen_cmds,
        )
        self.assertIn(join("tsnode", "-1"), profile_app.tokens)

    def test_m4_linear_time_on_large_adversarial_inputs(self) -> None:
        ident = self._identity_mod()
        keyed = self._keyed_mod()

        # 1) Exemption approval line with a million trailing spaces.
        padded_approved = '  "approved_by": "@owner"' + (" " * scan_deadline.LARGE_INPUT) + "x"
        scan_deadline.assert_finishes(
            self,
            "identity exemption masking (trailing spaces)",
            lambda: ident._mask_public_identifier_contexts(
                padded_approved, path="tests/public_scan_exemptions.json"
            ),
        )

        # 2) Long uppercase identifier ending in a digit.
        long_upper = ("A" * scan_deadline.LARGE_INPUT) + "1"
        scan_deadline.assert_finishes(
            self,
            "keyed candidate extraction (upper+digit)",
            lambda: keyed.extract_candidates_from_normalized(long_upper),
        )

    def test_m5_keyed_list_codeowners_off_mode_validation_and_prose_matching(self) -> None:
        keyed = self._keyed_mod()
        repo_check = self._repo_mod()

        # 1) CODEOWNERS must cover tests/public_scan_keyed_list.json and check_repository enforces it.
        self.assertIn("tests/public_scan_keyed_list.json", repo_check.CODEOWNERS_REQUIRED_PATHS)
        self.assertEqual([], repo_check.codeowners_issues(ROOT))

        # 2) Plaintext entry in tests/public_scan_keyed_list.json fails even when keyed-list mode is 'off'.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(
                root,
                keyed_list={
                    "schema_version": 1,
                    "algorithm": "hmac-sha256",
                    "digests": [join("Plaintext", " ", "Customer")],
                },
            )
            git(root, "init", "-q", "-b", "main")
            (root / "README.md").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: plaintext keyed list")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            code, out, err = run_main(["--root", str(root), "--tree", head, "--identity-source", "ci"])
            self.assertEqual(1, code, out + err)
            self.assertNotIn(join("Plaintext", " ", "Customer"), out + err)
            self.assertNotIn(scan.SCAN_VERIFIED_MARKER, out)

        # 3) Anything accepted by compute_keyed_digest is matched inside prose.
        key_bytes = bytes.fromhex(self.SYNTHETIC_KEY_HEX)
        prose_values = (
            (join("client", "@", "northwind-corp", ".test"), "Reach out to {val} for onboarding."),
            (join("northwind", "/", "secret-team"), "Owned by ({val}) in the tracker."),
            (join("Alpha", " ", "Beta", " ", "Gamma", " ", "Delta"), "Project {val} launches next week."),
            (join("acct", ":", "9988-zeta"), 'Record id="{val}", status=active.'),
        )
        for val, template in prose_values:
            with self.subTest(value_prefix=val[:8]):
                digest = keyed.compute_keyed_digest(key_bytes, val, normalize_fn=scan.normalize_text)
                matcher = keyed.KeyedListMatcher(key_bytes, frozenset({digest}), normalize_fn=scan.normalize_text)
                sentence = template.format(val=val)
                hits = matcher.detect(scan.normalize_text(sentence))
                self.assertIn(digest, hits, f"digest helper value not matched in prose")

    def test_m6_tests_do_not_embed_sys_executable_and_ci_runs_linux_public_tests(self) -> None:
        import inspect

        ac8_source = inspect.getsource(
            CommitHookDocsAndContributorJourneyTests.test_ac8_contributor_push_journey_on_macos_and_linux
        )
        # test_ac8 must not write sys.executable into the scanned temp Makefile.
        self.assertNotIn("{" + "sys.executable" + "}", ac8_source)
        self.assertIn('fake_bin / "tailscale"', ac8_source)

        hardening_source = (ROOT / "tests/test_public_scan_hardening.py").read_text(encoding="utf-8")
        self.assertIn("_install_fake_identity_bin", hardening_source)

        ci_yml = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("linux-public-scan", ci_yml)
        self.assertIn("ubuntu-24.04", ci_yml)
        self.assertIn("test_public_*.py", ci_yml)

    def test_ci1_makefile_uses_ci_identity_on_github_actions_while_pre_push_enforces_local(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "tests").mkdir()
            shutil.copyfile(ROOT / "Makefile", root / "Makefile")
            shutil.copyfile(ROOT / "tests/check_public_release.py", root / "tests/check_public_release.py")
            shutil.copyfile(ROOT / "tests/public_scan_identity.py", root / "tests/public_scan_identity.py")
            shutil.copyfile(ROOT / "tests/public_scan_keyed.py", root / "tests/public_scan_keyed.py")
            write_scan_configs(root)

            # Initialize git repo with NO local/global git user.name or user.email (like GitHub Actions runners).
            clean_git_env = {
                "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                "HOME": str(root / "empty-home"),
                "GIT_CONFIG_GLOBAL": "/dev/null",
                "GIT_CONFIG_NOSYSTEM": "1",
            }
            (root / "empty-home").mkdir()
            subprocess.run(["git", "init", "-q", "-b", "main"], cwd=root, env=clean_git_env, check=True)
            for rel in scan.REQUIRED:
                target = root / rel
                if target.exists():
                    continue
                target.parent.mkdir(parents=True, exist_ok=True)
                if rel in (
                    "VERSION",
                    "CHANGELOG.md",
                    "README.md",
                    ".github/dependabot.yml",
                ):
                    shutil.copyfile(ROOT / rel, target)
                else:
                    target.write_text("Clean fixture.\n", encoding="utf-8")
            kit_lock = root / "templates/private-fleet/kit.lock"
            kit_lock.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / "templates/private-fleet/kit.lock", kit_lock)
            subprocess.run(["git", "add", "-A"], cwd=root, env=clean_git_env, check=True)
            commit_env = {
                **clean_git_env,
                "GIT_AUTHOR_NAME": join("Daria", " ", "Kestrel"),
                "GIT_AUTHOR_EMAIL": join("dkestrel", "@", "northwind-lab", ".test"),
                "GIT_COMMITTER_NAME": "GitHub",
                "GIT_COMMITTER_EMAIL": join("noreply", "@", "github", ".com"),
            }
            subprocess.run(["git", "commit", "-q", "-m", "chore: clean fixture"], cwd=root, env=commit_env, check=True)

            # (a) Under GITHUB_ACTIONS=true, make public-draft-check uses CI commit identity and succeeds.
            gh_env = {**clean_git_env, "GITHUB_ACTIONS": "true"}
            proc_ci = subprocess.run(
                ["make", f"PYTHON={sys.executable}", "public-draft-check"],
                cwd=root,
                env=gh_env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, proc_ci.returncode, proc_ci.stdout + proc_ci.stderr)
            self.assertIn(scan.SCAN_VERIFIED_MARKER, proc_ci.stdout.splitlines())
            self.assertIn(self._identity_mod().IDENTITY_VERIFIED_MARKER, proc_ci.stdout.splitlines())

            # (b) Locally (without GITHUB_ACTIONS=true), make public-draft-check still fails closed.
            proc_local = subprocess.run(
                ["make", f"PYTHON={sys.executable}", "public-draft-check"],
                cwd=root,
                env=clean_git_env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(0, proc_local.returncode, proc_local.stdout + proc_local.stderr)
            self.assertIn("identity check failed", proc_local.stdout + proc_local.stderr)

            # (c) Explicit IDENTITY_SOURCE=local overrides GITHUB_ACTIONS=true and still fails closed.
            proc_forced = subprocess.run(
                ["make", f"PYTHON={sys.executable}", "IDENTITY_SOURCE=local", "public-draft-check"],
                cwd=root,
                env=gh_env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(0, proc_forced.returncode, proc_forced.stdout + proc_forced.stderr)
            self.assertIn("identity check failed", proc_forced.stdout + proc_forced.stderr)

        # (d) pre-push must explicitly force IDENTITY_SOURCE=local and --identity-source local
        # even when GITHUB_ACTIONS=true or IDENTITY_SOURCE=ci is set in the caller environment.
        discipline = _load_module("check_git_discipline_ci1", "scripts/check_git_discipline.py")
        head_sha = git(ROOT, "rev-parse", "HEAD").stdout.strip()
        recorded_cmds: list[list[str]] = []

        def fake_run(cmd, *args, **kwargs):
            recorded_cmds.append(list(cmd))
            return subprocess.CompletedProcess(cmd, 0, "", "")

        spoof_env = {"GITHUB_ACTIONS": "true", "IDENTITY_SOURCE": "ci"}
        with mock.patch.dict(os.environ, spoof_env, clear=False):
            with mock.patch.object(discipline, "_collect_tag_objects", return_value=[]):
                with mock.patch.object(discipline, "_pushed_commits", return_value=[head_sha]):
                    with mock.patch.object(discipline.subprocess, "run", side_effect=fake_run):
                        discipline.run_pre_push(
                            io.StringIO(f"refs/heads/feat/ci1-check {head_sha} refs/heads/feat/ci1-check {'0' * 40}\n"),
                            discipline.load_policy(),
                        )
        self.assertGreaterEqual(len(recorded_cmds), 2, recorded_cmds)
        make_cmd = recorded_cmds[0]
        self.assertEqual("make", make_cmd[0])
        self.assertIn("IDENTITY_SOURCE=local", make_cmd)
        self.assertIn("ci-check", make_cmd)
        scan_cmd = recorded_cmds[1]
        self.assertIn("--identity-source", scan_cmd)
        idx = scan_cmd.index("--identity-source")
        self.assertEqual("local", scan_cmd[idx + 1])

    def test_ci2_test_suite_has_no_gitleaks_key_literal_false_positives(self) -> None:
        source = (ROOT / "tests/test_public_identity_and_keyed.py").read_text(encoding="utf-8")
        # Avoid hardcoded hex literals assigned to *_KEY* identifiers (triggers Gitleaks generic-api-key).
        self.assertIsNone(
            re.search(r"KEY[A-Z0-9_]*\s*=\s*[\"'][0-9a-fA-F]{16,}[\"']", source),
            "tests/test_public_identity_and_keyed.py must generate synthetic HMAC test keys programmatically",
        )
        gitleaks_bin = shutil.which("gitleaks")
        if gitleaks_bin is None and Path("/opt/homebrew/bin/gitleaks").is_file():
            gitleaks_bin = "/opt/homebrew/bin/gitleaks"
        if gitleaks_bin is not None:
            proc = subprocess.run(
                [gitleaks_bin, "dir", "--redact", "--no-banner", str(ROOT / "tests/test_public_identity_and_keyed.py")],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, proc.returncode, proc.stdout + proc.stderr)


class Round2ReviewRegressionTests(unittest.TestCase):
    """Regression tests for PR #45 round 2 cross-family review findings (L1-L8) + executing H1/H2 tests."""

    SYNTHETIC_KEY_BYTES = bytes(range(10, 42))

    def _identity_mod(self):
        return _load_module("public_scan_identity_r2", "tests/public_scan_identity.py")

    def _keyed_mod(self):
        return _load_module("public_scan_keyed_r2", "tests/public_scan_keyed.py")

    def _extract_workflow_step_run(self, workflow_rel: str, step_name: str) -> str:
        text = (ROOT / workflow_rel).read_text(encoding="utf-8")
        marker = f"- name: {step_name}"
        start = text.index(marker)
        run_idx = text.index("run: |", start)
        lines = text[run_idx:].splitlines()[1:]
        body: list[str] = []
        indent: int | None = None
        for line in lines:
            if not line.strip():
                body.append("")
                continue
            leading = len(line) - len(line.lstrip(" "))
            if indent is None:
                indent = leading
            if leading < indent:
                break
            body.append(line[indent:])
        return "\n".join(body) + "\n"

    def test_l1_bare_cr_does_not_shift_ci_range_identity_line_numbers(self) -> None:
        author = join("Kestrel", " ", "Nolan")
        email = join("knolan", "@", "northwind-lab", ".test")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "docs").mkdir()
            # Several bare CR characters inside line 1 MUST NOT count as extra line breaks relative to git diff hunks.
            (root / "docs/cr.txt").write_bytes(b"seg1\rseg2\rseg3\rseg4\nsecond line\n")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: base with bare CR")
            base = git(root, "rev-parse", "HEAD").stdout.strip()

            git(root, "checkout", "-q", "-b", "feat/cr-line")
            (root / "docs/cr.txt").write_bytes(
                b"seg1\rseg2\rseg3\rseg4\nsecond line\nAuthor: " + author.encode("utf-8") + b"\n"
            )
            git(root, "add", "docs/cr.txt")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: add line after bare CR",
                author_name=author,
                author_email=email,
            )
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            code, out, err = run_main(["--root", str(root), "--range", base, head, "--identity-source", "ci"])
            combined = out + err
            self.assertEqual(1, code, combined)
            self.assertIn("docs/cr.txt:3: matched rule 'identity'", combined)
            self.assertNotIn(join("kestrel"), combined.casefold())

    def test_l2_keyed_matcher_extracts_mailto_markdown_json_query_and_urls(self) -> None:
        keyed = self._keyed_mod()
        email_val = join("client", "@", "northwind-corp", ".test")
        host_val = join("secret-box", ".", "northwind", ".test")
        path_val = join("northwind", "/", "secret-team")
        query_val = join("cobalt", "-", "nine")

        cases = (
            ("markdown_mailto", email_val, f"See [Support](mailto:{email_val}) for help."),
            ("angle_mailto", email_val, f"Reply to <mailto:{email_val}> today."),
            ("contact_colon", email_val, f"Contact:{email_val}"),
            ("compact_json", email_val, f'{{"e":"{email_val}"}}'),
            ("query_param", host_val, f"host={host_val}&x=1"),
            ("url_host", host_val, f"https://{host_val}/{path_val}?ref={query_val}"),
            ("url_path", path_val, f"https://{host_val}/{path_val}?ref={query_val}"),
            ("url_query", query_val, f"https://{host_val}/{path_val}?ref={query_val}"),
        )
        for label, value, line in cases:
            with self.subTest(shape=label):
                digest = keyed.compute_keyed_digest(
                    self.SYNTHETIC_KEY_BYTES,
                    value,
                    normalize_fn=scan.normalize_text,
                )
                matcher = keyed.KeyedListMatcher(
                    self.SYNTHETIC_KEY_BYTES,
                    frozenset({digest}),
                    normalize_fn=scan.normalize_text,
                )
                hits = matcher.detect(scan.normalize_text(line))
                self.assertIn(digest, hits, f"shape '{label}' missed keyed value")

    def test_l3_journey_tests_isolate_every_live_identity_source(self) -> None:
        hardening = _load_module("test_public_scan_hardening_l3", "tests/test_public_scan_hardening.py")
        import pwd
        import socket

        live_user = pwd.getpwuid(os.getuid()).pw_name
        live_host = socket.gethostname()

        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            repo, public_remote = hardening.Round2ReviewRegressionTests()._make_pre_push_repo(base)

            # Hostile global gitconfig (would match README "Version" if leaked) + hostile PATH.
            hostile_cfg = base / "hostile.gitconfig"
            hostile_email = join("version", "@", "hostile", ".invalid")
            hostile_cfg.write_text(f"[user]\n\tname = Version\n\temail = {hostile_email}\n", encoding="utf-8")
            hostile_bin = base / "hostile-bin"
            hostile_bin.mkdir()
            for tool in ("scutil", "hostnamectl", "tailscale"):
                script = hostile_bin / tool
                script.write_text("#!/bin/sh\nexit 99\n", encoding="utf-8")
                script.chmod(script.stat().st_mode | stat.S_IXUSR)

            hook_env = hardening._install_fake_identity_bin(base, repo=repo)
            # Put hostile_bin AFTER fake_bin (so fake_bin shadows it) and set hostile GIT_CONFIG_GLOBAL
            # BEFORE merging hook_env to verify hook_env neutralizes live global git config too.
            merged_env = {
                **os.environ,
                "GIT_CONFIG_GLOBAL": str(hostile_cfg),
                "PATH": f"{hostile_bin}:{os.environ.get('PATH', '/usr/bin:/bin')}",
                **hook_env,
            }

            # Verify temp repo's collect_local_identity returns ONLY stubbed tokens, never live host/account.
            probe = subprocess.run(
                [
                    sys.executable,
                    "-I",
                    "-B",
                    "-c",
                    "import importlib.util, json, pathlib, sys; "
                    "spec = importlib.util.spec_from_file_location('psi', 'tests/public_scan_identity.py'); "
                    "mod = importlib.util.module_from_spec(spec); sys.modules['psi'] = mod; spec.loader.exec_module(mod); "
                    "prof = mod.collect_local_identity(pathlib.Path('.'), normalize_fn=lambda s: s); "
                    "print(json.dumps(sorted(prof.tokens)))",
                ],
                cwd=repo,
                env=merged_env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, probe.returncode, probe.stdout + probe.stderr)
            tokens = set(json.loads(probe.stdout))
            self.assertIn("fixture-node-01", tokens)
            if live_user.casefold() not in self._identity_mod().ALLOWED_HOME_USERS and len(live_user) >= 3:
                self.assertNotIn(live_user.casefold(), tokens)
            live_short = live_host.split(".", 1)[0].casefold()
            if live_short not in self._identity_mod().ALLOWED_HOME_USERS and len(live_short) >= 3:
                self.assertNotIn(live_short, tokens)

            # Verify a real pre-push succeeds cleanly under hostile live env.
            git(repo, "config", "core.hooksPath", ".codeflow/git-hooks")
            git(repo, "checkout", "-q", "-b", "feat/l3-clean")
            (repo / "docs").mkdir(exist_ok=True)
            (repo / "docs/l3.md").write_text("Clean note.\n", encoding="utf-8")
            git(repo, "add", "docs/l3.md")
            git(repo, "commit", "-q", "-m", "docs: clean l3 note")
            push_res = subprocess.run(
                ["git", "push", "origin", "feat/l3-clean"],
                cwd=repo,
                env=merged_env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, push_res.returncode, push_res.stdout + push_res.stderr)

    def test_l4_public_scan_workflow_handles_bootstrap_base_and_enforces_identity_when_supported(self) -> None:
        run_scan_script = self._extract_workflow_step_run(
            ".github/workflows/public-scan.yml",
            "Run deterministic privacy and identity scan on every commit",
        )
        result_script = self._extract_workflow_step_run(
            ".github/workflows/public-scan.yml",
            "Require verified deterministic privacy and identity scan on Linux and macOS",
        )

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "tests").mkdir()
            gh_out = root / "gh_output.txt"
            pinned_before = "85f4b9eccbf5fb4a62599beeb32a4cd4cd6961d4"
            base_env = {
                "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                "EVENT_NAME": "push",
                "GITHUB_REF": "refs/heads/main",
                "PUSH_BEFORE": pinned_before,
                "SCAN_MODE": "range",
                "BASE_SHA": pinned_before,
                "HEAD_SHA": "b" * 40,
                "GITHUB_OUTPUT": str(gh_out),
            }

            # (a) Pre-TSK-008 base scanner lacking --identity-source -> runs without it, emits bootstrap notice.
            (root / "tests/check_public_release.py").write_text(
                "import sys\n"
                "if '--help' in sys.argv:\n"
                "    print('usage: check_public_release.py [--range BASE HEAD] [--tree SHA]')\n"
                "    raise SystemExit(0)\n"
                "if '--identity-source' in sys.argv:\n"
                "    print('unrecognized arguments: --identity-source', file=sys.stderr)\n"
                "    raise SystemExit(2)\n"
                "print('PUBLIC_SCAN_VERIFIED=1')\n",
                encoding="utf-8",
            )
            gh_out.write_text("", encoding="utf-8")
            proc_boot = subprocess.run(
                ["bash", "-c", run_scan_script],
                cwd=root,
                env=base_env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, proc_boot.returncode, proc_boot.stdout + proc_boot.stderr)
            self.assertIn("bootstrap", (proc_boot.stdout + proc_boot.stderr).lower())
            boot_outputs = gh_out.read_text(encoding="utf-8")
            self.assertIn("verified=true", boot_outputs)
            self.assertIn("identity_verified=bootstrap", boot_outputs)

            # (b) Base scanner supporting --identity-source that omits PUBLIC_SCAN_IDENTITY_VERIFIED=1 -> FAILS CLOSED.
            (root / "tests/check_public_release.py").write_text(
                "import sys\n"
                "if '--help' in sys.argv:\n"
                "    print('usage: check_public_release.py [--identity-source {auto,local,ci}]')\n"
                "    raise SystemExit(0)\n"
                "print('PUBLIC_SCAN_VERIFIED=1')\n",
                encoding="utf-8",
            )
            gh_out.write_text("", encoding="utf-8")
            proc_missing = subprocess.run(
                ["bash", "-c", run_scan_script],
                cwd=root,
                env=base_env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(0, proc_missing.returncode, proc_missing.stdout + proc_missing.stderr)
            self.assertNotIn("identity_verified=true", gh_out.read_text(encoding="utf-8"))

            # (c) Base scanner supporting --identity-source that emits both markers -> SUCCEEDS.
            (root / "tests/check_public_release.py").write_text(
                "import sys\n"
                "if '--help' in sys.argv:\n"
                "    print('usage: check_public_release.py [--identity-source {auto,local,ci}]')\n"
                "    raise SystemExit(0)\n"
                "assert '--identity-source' in sys.argv\n"
                "print('PUBLIC_SCAN_VERIFIED=1')\n"
                "print('PUBLIC_SCAN_IDENTITY_VERIFIED=1')\n",
                encoding="utf-8",
            )
            gh_out.write_text("", encoding="utf-8")
            proc_ok = subprocess.run(
                ["bash", "-c", run_scan_script],
                cwd=root,
                env=base_env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, proc_ok.returncode, proc_ok.stdout + proc_ok.stderr)
            ok_outputs = gh_out.read_text(encoding="utf-8")
            self.assertIn("verified=true", ok_outputs)
            self.assertIn("identity_verified=true", ok_outputs)

            # (d) Result aggregator accepts matching bootstrap or matching true, rejects mismatch/missing.
            for label, linux_id, macos_id, expected_rc in (
                ("both_true", "true", "true", 0),
                ("both_bootstrap", "bootstrap", "bootstrap", 0),
                ("mismatch_true_bootstrap", "true", "bootstrap", 1),
                ("missing_macos", "true", "", 1),
            ):
                with self.subTest(result_case=label):
                    res_proc = subprocess.run(
                        ["bash", "-c", result_script],
                        env={
                            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                            "EVENT_NAME": "push",
                            "GITHUB_REF": "refs/heads/main",
                            "PUSH_BEFORE": pinned_before,
                            "LINUX_RESULT": "success",
                            "LINUX_VERIFIED": "true",
                            "LINUX_IDENTITY_VERIFIED": linux_id,
                            "MACOS_RESULT": "success",
                            "MACOS_VERIFIED": "true",
                            "MACOS_IDENTITY_VERIFIED": macos_id,
                        },
                        capture_output=True,
                        text=True,
                        check=False,
                    )
                    self.assertEqual(expected_rc, res_proc.returncode, res_proc.stdout + res_proc.stderr)

    def test_l5_unchanged_identity_lines_do_not_exhaust_finding_cap_before_scoping(self) -> None:
        author = join("Kestrel", " ", "Nolan")
        email = join("knolan", "@", "northwind-lab", ".test")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "docs").mkdir()
            # 25 pre-existing lines containing the author's name (exceeds patched MAX_FINDINGS=10).
            pre_existing = "".join(f"History entry {i}: {author}\n" for i in range(1, 26))
            (root / "docs/log.txt").write_text(pre_existing, encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: pre-existing lines")
            base = git(root, "rev-parse", "HEAD").stdout.strip()

            git(root, "checkout", "-q", "-b", "feat/added-after-many")
            (root / "docs/log.txt").write_text(pre_existing + f"New entry: {author}\n", encoding="utf-8")
            git(root, "add", "docs/log.txt")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: add one new line",
                author_name=author,
                author_email=email,
            )
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            with mock.patch.object(scan, "MAX_FINDINGS", 10):
                code, out, err = run_main(["--root", str(root), "--range", base, head, "--identity-source", "ci"])
            combined = out + err
            self.assertEqual(1, code, combined)
            self.assertIn("docs/log.txt:26: matched rule 'identity'", combined)
            self.assertNotIn("more than 10 matches", combined)
            self.assertNotIn(join("kestrel"), combined.casefold())

    def test_l6_collection_failure_redacts_tracked_paths_with_sensitive_tokens(self) -> None:
        ident = self._identity_mod()
        secret_host = join("uncollected", "-", "host77")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(
                root,
                exemptions=[
                    {
                        "path": f"docs/{secret_host}/note.md",
                        "rule": "identity",
                        "digest": scan.compute_exemption_digest("identity", secret_host),
                        "reason": "expired fixture",
                        "approved_by": "@owner",
                        "expires": "2020-01-01",
                    }
                ],
            )
            git(root, "init", "-q", "-b", "main")
            (root / "reports").mkdir()
            (root / f"reports/{secret_host}.txt").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-f", "-A")
            git(root, "commit", "-q", "-m", "chore: collection failure fixture")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            err_obj = scan._identity_mod.IdentityCollectionError("simulated local source failure")
            for mode_name, argv in (
                ("commits", ["--root", str(root), "--commits", head, "--identity-source", "local"]),
                ("worktree", ["--root", str(root), "--allow-missing-license", "--identity-source", "local"]),
            ):
                with self.subTest(mode=mode_name):
                    with mock.patch.object(scan, "_collect_identity_profile", side_effect=err_obj):
                        code, out, err = run_main(argv)
                    combined = out + err
                    self.assertEqual(1, code, combined)
                    self.assertIn("identity check failed", combined)
                    self.assertIn("<redacted-", combined)
                    self.assertNotIn(secret_host, combined.casefold())

    def test_l7_optional_empty_macos_hostname_and_gecos_are_unavailable_not_errors(self) -> None:
        ident = self._identity_mod()

        def make_darwin_runner(*, host_rc: int = 0, host_out: str = "  \n", comp_out: str = "Maren-Mac\n"):
            def run_cmd(cmd, *, cwd=None, timeout=5.0):
                if cmd[:3] == ["git", "--no-optional-locks", "config"]:
                    val = join("Zora", " ", "Vance") if cmd[-1] == "user.name" else join("zvance", "@", "northwind-lab", ".test")
                    return subprocess.CompletedProcess(cmd, 0, val + "\n", "")
                if cmd[:2] == ["scutil", "--get"]:
                    if cmd[2] == "HostName":
                        return subprocess.CompletedProcess(cmd, host_rc, host_out, "")
                    if cmd[2] == "ComputerName":
                        return subprocess.CompletedProcess(cmd, 0, comp_out, "")
                    return subprocess.CompletedProcess(cmd, 0, join("Maren", "-Mac") + "\n", "")
                raise AssertionError(f"unexpected cmd: {cmd}")
            return run_cmd

        # Optional empty scutil HostName (rc=0, whitespace/empty) + empty GECOS -> treated as unavailable, NOT error.
        profile = ident.collect_local_identity(
            ROOT,
            normalize_fn=scan.normalize_text,
            platform_name="darwin",
            run_cmd=make_darwin_runner(host_rc=0, host_out="  \n"),
            get_pwuid=lambda _uid: _fake_pwuid(join("zora", "dev"), ""),
            get_login=lambda: "",
            get_hostname=lambda: join("maren", "-mac"),
            which=lambda _bin: None,
            is_executable=lambda _p: False,
        )
        self.assertIn(join("zora", "dev"), profile.tokens)
        self.assertNotIn("", profile.tokens)

        # Required empty scutil ComputerName MUST still fail closed.
        with self.assertRaises(ident.IdentityCollectionError):
            ident.collect_local_identity(
                ROOT,
                normalize_fn=scan.normalize_text,
                platform_name="darwin",
                run_cmd=make_darwin_runner(host_rc=0, host_out="", comp_out="  \n"),
                get_pwuid=lambda _uid: _fake_pwuid(join("zora", "dev"), ""),
                get_login=lambda: "",
                get_hostname=lambda: join("maren", "-mac"),
                which=lambda _bin: None,
                is_executable=lambda _p: False,
            )

        # Docs must align with optional empty HostName treated as unavailable.
        checklist = (ROOT / "docs/13-public-release-checklist.md").read_text(encoding="utf-8").lower()
        self.assertNotIn("an empty zero-exit response or other error fails closed", checklist)

    def test_l8_ci_secret_scan_scopes_to_pr_range_ignoring_unrelated_branch_leaks(self) -> None:
        ci_yml = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        secret_job = self._extract_workflow_step_run(".github/workflows/ci.yml", "Run pinned Gitleaks")
        self.assertIn("--log-opts=", secret_job)
        self.assertIn("PR_BASE_SHA", ci_yml)
        self.assertIn("PR_HEAD_SHA", ci_yml)
        self.assertIn("PUSH_BEFORE", ci_yml)

        gitleaks_bin = shutil.which("gitleaks")
        if gitleaks_bin is None and Path("/opt/homebrew/bin/gitleaks").is_file():
            gitleaks_bin = "/opt/homebrew/bin/gitleaks"
        if gitleaks_bin is not None:
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                git(root, "init", "-q", "-b", "main")
                (root / "README.md").write_text("clean base\n", encoding="utf-8")
                git(root, "add", "README.md")
                git(root, "commit", "-q", "-m", "chore: base")
                base_sha = git(root, "rev-parse", "HEAD").stdout.strip()

                # Unrelated branch carries a synthetic GitLab token leak.
                git(root, "checkout", "-q", "-b", "unrelated-leak")
                fake_pat = join("glpat", "-", "kSaPeOD_-T0JxMi3p28B")
                (root / "leak.txt").write_text(f"token={fake_pat}\n", encoding="utf-8")
                git(root, "add", "leak.txt")
                git(root, "commit", "-q", "-m", "chore: unrelated leak")

                # Clean PR branch from base has no secret.
                git(root, "checkout", "-q", "-b", "feat/clean-pr", base_sha)
                (root / "clean.txt").write_text("clean pr change\n", encoding="utf-8")
                git(root, "add", "clean.txt")
                git(root, "commit", "-q", "-m", "docs: clean pr")
                head_sha = git(root, "rev-parse", "HEAD").stdout.strip()

                # PR-scoped gitleaks passes despite unrelated-leak branch existing in the repo.
                scoped = subprocess.run(
                    [gitleaks_bin, "git", "--redact", "--no-banner", "--exit-code", "1", f"--log-opts={base_sha}..{head_sha}", "."],
                    cwd=root,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(0, scoped.returncode, scoped.stdout + scoped.stderr)

                # Adding a leak on the PR branch itself MUST fail the PR-scoped scan.
                (root / "pr-leak.txt").write_text(f"token={fake_pat}\n", encoding="utf-8")
                git(root, "add", "pr-leak.txt")
                git(root, "commit", "-q", "-m", "docs: accidental leak")
                bad_head = git(root, "rev-parse", "HEAD").stdout.strip()
                scoped_bad = subprocess.run(
                    [gitleaks_bin, "git", "--redact", "--no-banner", "--exit-code", "1", f"--log-opts={base_sha}..{bad_head}", "."],
                    cwd=root,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(1, scoped_bad.returncode, scoped_bad.stdout + scoped_bad.stderr)

    def test_h1_executing_target_step_enforces_pr_base_and_push_before_semantics(self) -> None:
        target_script = self._extract_workflow_step_run(
            ".github/workflows/public-scan.yml",
            "Fetch commit range to scan as git data",
        )
        with tempfile.TemporaryDirectory() as directory:
            base_dir = Path(directory)
            remote = base_dir / "origin.git"
            work = base_dir / "work"
            subprocess.run(["git", "init", "--bare", "-q", str(remote)], check=True)
            git(base_dir, "init", "-q", "-b", "main", str(work))
            (work / "README.md").write_text("root\n", encoding="utf-8")
            git(work, "add", "README.md")
            git(work, "commit", "-q", "-m", "chore: root")
            c1 = git(work, "rev-parse", "HEAD").stdout.strip()

            (work / "README.md").write_text("second\n", encoding="utf-8")
            git(work, "add", "README.md")
            git(work, "commit", "-q", "-m", "chore: second")
            c2 = git(work, "rev-parse", "HEAD").stdout.strip()
            git(work, "remote", "add", "origin", str(remote))
            git(work, "push", "-q", "origin", "main")
            # Simulate PR #7 head ref on remote.
            git(work, "checkout", "-q", "-b", "feat/pr7", c1)
            (work / "pr.txt").write_text("pr change\n", encoding="utf-8")
            git(work, "add", "pr.txt")
            git(work, "commit", "-q", "-m", "docs: pr7")
            pr_head = git(work, "rev-parse", "HEAD").stdout.strip()
            git(work, "push", "-q", "origin", f"{pr_head}:refs/pull/7/head")

            local_script = target_script.replace('"https://github.com/${REPOSITORY}.git"', f'"{remote}"')
            gh_out = base_dir / "target_out.txt"

            # (a) pull_request_target checked out at BASE_SHA (c1) targeting main -> outputs mode=range, base=c1, head=pr_head.
            git(work, "checkout", "-q", c1)
            gh_out.write_text("", encoding="utf-8")
            proc_pr = subprocess.run(
                ["bash", "-c", local_script],
                cwd=work,
                env={
                    "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                    "EVENT_NAME": "pull_request_target",
                    "PR_NUMBER": "7",
                    "BASE_REF": "main",
                    "BASE_SHA": c1,
                    "HEAD_SHA": pr_head,
                    "PUSH_BEFORE": "",
                    "PUSH_HEAD": "",
                    "REPOSITORY": "example/repo",
                    "GITHUB_OUTPUT": str(gh_out),
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, proc_pr.returncode, proc_pr.stdout + proc_pr.stderr)
            pr_out = gh_out.read_text(encoding="utf-8")
            self.assertIn("mode=range", pr_out)
            self.assertIn(f"base={c1}", pr_out)
            self.assertIn(f"head={pr_head}", pr_out)

            # (b) push to main checked out at PUSH_BEFORE (c1) scanning c1..c2 -> outputs mode=range, base=c1, head=c2.
            gh_out.write_text("", encoding="utf-8")
            proc_push = subprocess.run(
                ["bash", "-c", local_script],
                cwd=work,
                env={
                    "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                    "EVENT_NAME": "push",
                    "PR_NUMBER": "",
                    "BASE_REF": "",
                    "BASE_SHA": "",
                    "HEAD_SHA": "",
                    "PUSH_BEFORE": c1,
                    "PUSH_HEAD": c2,
                    "REPOSITORY": "example/repo",
                    "GITHUB_OUTPUT": str(gh_out),
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, proc_push.returncode, proc_push.stdout + proc_push.stderr)
            push_out = gh_out.read_text(encoding="utf-8")
            self.assertIn("mode=range", push_out)
            self.assertIn(f"base={c1}", push_out)
            self.assertIn(f"head={c2}", push_out)

            # (c) push with zero PUSH_BEFORE on non-root commit c2 -> FAILS CLOSED.
            git(work, "checkout", "-q", c2)
            gh_out.write_text("", encoding="utf-8")
            proc_zero = subprocess.run(
                ["bash", "-c", local_script],
                cwd=work,
                env={
                    "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                    "EVENT_NAME": "push",
                    "PR_NUMBER": "",
                    "BASE_REF": "",
                    "BASE_SHA": "",
                    "HEAD_SHA": "",
                    "PUSH_BEFORE": "0" * 40,
                    "PUSH_HEAD": c2,
                    "REPOSITORY": "example/repo",
                    "GITHUB_OUTPUT": str(gh_out),
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(0, proc_zero.returncode, proc_zero.stdout + proc_zero.stderr)
            self.assertIn("non-root", (proc_zero.stdout + proc_zero.stderr).lower())


class Round3ReviewRegressionTests(unittest.TestCase):
    """Regression tests for PR #45 round 3 cross-family review findings (N1-N5)."""

    SYNTHETIC_KEY_BYTES = bytes(range(20, 52))
    PINNED_BOOTSTRAP_BEFORE = "85f4b9eccbf5fb4a62599beeb32a4cd4cd6961d4"

    def _keyed_mod(self):
        return _load_module("public_scan_keyed_r3", "tests/public_scan_keyed.py")

    def _extract_step(self, step_name: str) -> str:
        return Round2ReviewRegressionTests()._extract_workflow_step_run(
            ".github/workflows/public-scan.yml", step_name
        )

    def test_n1_bootstrap_is_strictly_one_time_for_pinned_main_push_only(self) -> None:
        workflow = (ROOT / ".github/workflows/public-scan.yml").read_text(encoding="utf-8")
        self.assertIn(self.PINNED_BOOTSTRAP_BEFORE, workflow)

        run_scan_script = self._extract_step("Run deterministic privacy and identity scan on every commit")
        result_script = self._extract_step(
            "Require verified deterministic privacy and identity scan on Linux and macOS"
        )

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "tests").mkdir()
            gh_out = root / "gh_output.txt"
            old_scanner = (
                "import sys\n"
                "if '--help' in sys.argv:\n"
                "    print('usage: check_public_release.py [--range BASE HEAD]')\n"
                "    raise SystemExit(0)\n"
                "if '--identity-source' in sys.argv:\n"
                "    print('unrecognized arguments: --identity-source', file=sys.stderr)\n"
                "    raise SystemExit(2)\n"
                "print('PUBLIC_SCAN_VERIFIED=1')\n"
            )
            (root / "tests/check_public_release.py").write_text(old_scanner, encoding="utf-8")

            common_env = {
                "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                "SCAN_MODE": "range",
                "BASE_SHA": self.PINNED_BOOTSTRAP_BEFORE,
                "HEAD_SHA": "b" * 40,
                "GITHUB_OUTPUT": str(gh_out),
            }

            # (a) PR into an old branch lacking --identity-source MUST FAIL CLOSED.
            gh_out.write_text("", encoding="utf-8")
            proc_pr_old = subprocess.run(
                ["bash", "-c", run_scan_script],
                cwd=root,
                env={
                    **common_env,
                    "EVENT_NAME": "pull_request_target",
                    "GITHUB_REF": "refs/heads/old-branch",
                    "PUSH_BEFORE": "",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(0, proc_pr_old.returncode, proc_pr_old.stdout + proc_pr_old.stderr)
            self.assertEqual("", gh_out.read_text(encoding="utf-8").strip())

            # (b) Push to main with a DIFFERENT before SHA MUST FAIL CLOSED.
            gh_out.write_text("", encoding="utf-8")
            proc_other_push = subprocess.run(
                ["bash", "-c", run_scan_script],
                cwd=root,
                env={
                    **common_env,
                    "EVENT_NAME": "push",
                    "GITHUB_REF": "refs/heads/main",
                    "PUSH_BEFORE": "1" * 40,
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(0, proc_other_push.returncode, proc_other_push.stdout + proc_other_push.stderr)

            # (c) Failed --help probe on a PR (or even pinned push) MUST FAIL CLOSED.
            # Fixture fails ONLY on --help; any scan invocation would succeed and write scan_ran.txt,
            # proving the --help probe guard itself stops execution.
            scan_ran = root / "scan_ran.txt"
            (root / "tests/check_public_release.py").write_text(
                "import pathlib, sys\n"
                "if '--help' in sys.argv:\n"
                "    print('broken help probe', file=sys.stderr)\n"
                "    raise SystemExit(1)\n"
                "pathlib.Path('scan_ran.txt').write_text('ran', encoding='utf-8')\n"
                "print('PUBLIC_SCAN_VERIFIED=1')\n"
                "print('PUBLIC_SCAN_IDENTITY_VERIFIED=1')\n",
                encoding="utf-8",
            )
            for label, ev_env in (
                ("pr_probe_fail", {"EVENT_NAME": "pull_request_target", "GITHUB_REF": "refs/heads/main", "PUSH_BEFORE": ""}),
                ("push_probe_fail", {"EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main", "PUSH_BEFORE": self.PINNED_BOOTSTRAP_BEFORE}),
            ):
                with self.subTest(case=label):
                    gh_out.write_text("", encoding="utf-8")
                    if scan_ran.exists():
                        scan_ran.unlink()
                    proc_fail = subprocess.run(
                        ["bash", "-c", run_scan_script],
                        cwd=root,
                        env={**common_env, **ev_env},
                        capture_output=True,
                        text=True,
                        check=False,
                    )
                    combined_fail = proc_fail.stdout + proc_fail.stderr
                    self.assertNotEqual(0, proc_fail.returncode, combined_fail)
                    self.assertIn("--help probe failed", combined_fail)
                    self.assertFalse(scan_ran.exists(), "probe guard must stop before running the scan")
                    self.assertEqual("", gh_out.read_text(encoding="utf-8").strip())

            # (d) ONLY push to refs/heads/main with before == PINNED_BOOTSTRAP_BEFORE is allowed bootstrap.
            (root / "tests/check_public_release.py").write_text(old_scanner, encoding="utf-8")
            gh_out.write_text("", encoding="utf-8")
            proc_pinned = subprocess.run(
                ["bash", "-c", run_scan_script],
                cwd=root,
                env={
                    **common_env,
                    "EVENT_NAME": "push",
                    "GITHUB_REF": "refs/heads/main",
                    "PUSH_BEFORE": self.PINNED_BOOTSTRAP_BEFORE,
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, proc_pinned.returncode, proc_pinned.stdout + proc_pinned.stderr)
            self.assertIn("identity_verified=bootstrap", gh_out.read_text(encoding="utf-8"))

            # Result aggregator also rejects bootstrap outside the single pinned main push.
            proc_res_pr_boot = subprocess.run(
                ["bash", "-c", result_script],
                env={
                    "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                    "EVENT_NAME": "pull_request_target",
                    "GITHUB_REF": "refs/heads/main",
                    "PUSH_BEFORE": "",
                    "LINUX_RESULT": "success",
                    "LINUX_VERIFIED": "true",
                    "LINUX_IDENTITY_VERIFIED": "bootstrap",
                    "MACOS_RESULT": "success",
                    "MACOS_VERIFIED": "true",
                    "MACOS_IDENTITY_VERIFIED": "bootstrap",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(0, proc_res_pr_boot.returncode, proc_res_pr_boot.stdout + proc_res_pr_boot.stderr)

    def test_n2_bare_cr_yaml_phone_list_is_caught_while_preserving_git_line_numbers(self) -> None:
        bare_digits = join("415", "867", "5309")
        hyphen_phone = join("415", "-", "867", "-", "5309")
        for label, payload, secret_token in (
            ("bare_digits_yaml_list", f"phones:\r  - {bare_digits}\n".encode("utf-8"), bare_digits),
            ("multi_item_bare_digits", f"phones:\r  - 5555550100\r  - {bare_digits}\n".encode("utf-8"), bare_digits),
            ("hyphenated_yaml_list", f"phones:\r  - {hyphen_phone}\n".encode("utf-8"), hyphen_phone),
        ):
            with self.subTest(case=label):
                res = scan.scan_sources([scan.SourceFile("docs/contacts.yaml", payload)])
                self.assertEqual(["docs/contacts.yaml:1: matched rule 'phone'"], res.failures)
                self.assertNotIn(secret_token, "\n".join(res.failures))

    def test_n3_clean_edit_next_to_unchanged_identity_line_passes_while_real_wrap_fails(self) -> None:
        author = join("Kestrel", " ", "Nolan")
        email = join("knolan", "@", "northwind-lab", ".test")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "docs").mkdir()
            (root / "docs/notes.md").write_text(
                f"Reviewed by {author}.\n",
                encoding="utf-8",
            )
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: pre-existing identity line")
            base = git(root, "rev-parse", "HEAD").stdout.strip()

            # (a) Adding a clean line right next to the unchanged identity line MUST pass in CI --range mode.
            git(root, "checkout", "-q", "-b", "feat/clean-adjacent")
            (root / "docs/notes.md").write_text(
                f"Reviewed by {author}.\nAdded a clean follow-up sentence.\n",
                encoding="utf-8",
            )
            git(root, "add", "docs/notes.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: add clean adjacent line",
                author_name=author,
                author_email=email,
            )
            clean_head = git(root, "rev-parse", "HEAD").stdout.strip()
            code_clean, out_clean, err_clean = run_main(
                ["--root", str(root), "--range", base, clean_head, "--identity-source", "ci"]
            )
            self.assertEqual(0, code_clean, out_clean + err_clean)

            # (b) Adding the second word of a wrapped two-word identity on an added line MUST still fail.
            git(root, "checkout", "-q", "-b", "feat/real-wrap", base)
            (root / "docs/wrap.md").write_text("Assigned to Kestrel\n", encoding="utf-8")
            git(root, "add", "docs/wrap.md")
            git(root, "commit", "-q", "-m", "chore: first word on base")
            wrap_base = git(root, "rev-parse", "HEAD").stdout.strip()

            (root / "docs/wrap.md").write_text("Assigned to Kestrel\nNolan today.\n", encoding="utf-8")
            git(root, "add", "docs/wrap.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: complete wrapped name",
                author_name=author,
                author_email=email,
            )
            wrap_head = git(root, "rev-parse", "HEAD").stdout.strip()
            code_wrap, out_wrap, err_wrap = run_main(
                ["--root", str(root), "--range", wrap_base, wrap_head, "--identity-source", "ci"]
            )
            self.assertEqual(1, code_wrap, out_wrap + err_wrap)
            self.assertIn("docs/wrap.md:1: matched rule 'identity'", out_wrap + err_wrap)

    def test_n4_keyed_list_matches_mailto_with_query_string_in_linear_time(self) -> None:
        keyed = self._keyed_mod()
        email_val = join("client", "@", "northwind-corp", ".test")
        digest = keyed.compute_keyed_digest(
            self.SYNTHETIC_KEY_BYTES,
            email_val,
            normalize_fn=scan.normalize_text,
        )
        matcher = keyed.KeyedListMatcher(
            self.SYNTHETIC_KEY_BYTES,
            frozenset({digest}),
            normalize_fn=scan.normalize_text,
        )
        for label, line in (
            ("markdown_mailto_subject", f"[x](mailto:{email_val}?subject=hi)"),
            ("bare_mailto_cc", f"mailto:{email_val}?cc=x"),
        ):
            with self.subTest(case=label):
                hits = matcher.detect(scan.normalize_text(line))
                self.assertIn(digest, hits, f"missed keyed mailto query shape '{label}'")

    def test_n5_collection_failure_redacts_angle_bracket_exemption_and_synthetic_ref_filename(self) -> None:
        secret_host = join("leaked", "-", "node88")
        angle_path = f"<{secret_host}>"
        spoof_filename = f"commit:{secret_host}:message"

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(
                root,
                exemptions=[
                    {
                        "path": angle_path,
                        "rule": "identity",
                        "digest": scan.compute_exemption_digest("identity", secret_host),
                        "reason": "expired angle-bracket fixture",
                        "approved_by": "@owner",
                        "expires": "2020-01-01",
                    }
                ],
            )
            git(root, "init", "-q", "-b", "main")
            # Create a real tracked file whose name mimics commit:<id>:message and contains an uncollected secret token
            # plus a binary payload (NUL byte) so scan_source emits a failure referencing the file path!
            (root / spoof_filename).write_bytes(b"bin\x00payload")
            git(root, "add", "-f", "-A")
            git(root, "commit", "-q", "-m", "chore: n5 fixture")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            err_obj = scan._identity_mod.IdentityCollectionError("simulated collection error")
            for mode_name, argv in (
                ("commits", ["--root", str(root), "--commits", head, "--identity-source", "local"]),
                ("worktree", ["--root", str(root), "--allow-missing-license", "--identity-source", "local"]),
            ):
                with self.subTest(mode=mode_name):
                    with mock.patch.object(scan, "_collect_identity_profile", side_effect=err_obj):
                        code, out, err = run_main(argv)
                    combined = out + err
                    self.assertEqual(1, code, combined)
                    self.assertIn("identity check failed", combined)
                    self.assertIn("<redacted-", combined)
                    self.assertNotIn(secret_host, combined.casefold())


class Round4ReviewRegressionTests(unittest.TestCase):
    """Regression tests for PR #45 round 4 surgical fixes (Q1-Q4)."""

    SYNTHETIC_KEY_BYTES = bytes(range(30, 62))
    PINNED_BOOTSTRAP_BEFORE = "85f4b9eccbf5fb4a62599beeb32a4cd4cd6961d4"

    def _identity_mod(self):
        return _load_module("public_scan_identity_r4", "tests/public_scan_identity.py")

    def _keyed_mod(self):
        return _load_module("public_scan_keyed_r4", "tests/public_scan_keyed.py")

    def test_q1_bare_cr_union_catches_yaml_phone_list_and_three_part_identity_and_cr_phone(self) -> None:
        ident = self._identity_mod()
        three_part = join("Kestrel", " ", "Maren", " ", "Nolan")
        matcher = ident.IdentityMatcher(
            ident.derive_identity_tokens([("git-name", three_part)], normalize_fn=scan.normalize_text)
        )

        # (a) Three-part identity split across two bare CRs on Git line 1 MUST be caught on line 1.
        cr_three_part = join("Kestrel", "\r", "Maren", "\r", "Nolan", "\n").encode("utf-8")
        res_id = scan.scan_sources(
            [scan.SourceFile("docs/three.txt", cr_three_part)],
            identity_matcher=matcher,
        )
        self.assertEqual(["docs/three.txt:1: matched rule 'identity'"], res_id.failures)
        self.assertNotIn(join("kestrel"), "\n".join(res_id.failures).casefold())

        # (b) Phone number split across bare CRs (e.g. "phone: 415\r867\r5309") MUST be caught on line 1.
        p1, p2, p3 = "415", "867", "5309"
        cr_phone = f"phone: {p1}\r{p2}\r{p3}\n".encode("utf-8")
        res_phone = scan.scan_sources([scan.SourceFile("docs/cr-phone.txt", cr_phone)])
        self.assertEqual(["docs/cr-phone.txt:1: matched rule 'phone'"], res_phone.failures)

        # (c) Bare-CR YAML phone list (bare 10 digits) MUST still be caught on line 1.
        bare_digits = join("415", "867", "5309")
        yaml_cr = f"phones:\r  - {bare_digits}\n".encode("utf-8")
        res_yaml = scan.scan_sources([scan.SourceFile("docs/contacts.yaml", yaml_cr)])
        self.assertEqual(["docs/contacts.yaml:1: matched rule 'phone'"], res_yaml.failures)

    def test_q2_keyed_list_matches_both_exact_question_mark_values_and_mailto_query(self) -> None:
        keyed = self._keyed_mod()
        qmark_val = join("cobalt", "?", "signal")
        email_val = join("client", "@", "northwind-corp", ".test")

        for label, value, line in (
            ("exact_qmark_in_markdown", qmark_val, f"[x]({qmark_val})"),
            ("exact_qmark_bare", qmark_val, f"Tag {qmark_val} active."),
            ("mailto_with_subject", email_val, f"[x](mailto:{email_val}?subject=hi)"),
            ("mailto_with_cc", email_val, f"mailto:{email_val}?cc=x"),
        ):
            with self.subTest(case=label):
                digest = keyed.compute_keyed_digest(
                    self.SYNTHETIC_KEY_BYTES,
                    value,
                    normalize_fn=scan.normalize_text,
                )
                matcher = keyed.KeyedListMatcher(
                    self.SYNTHETIC_KEY_BYTES,
                    frozenset({digest}),
                    normalize_fn=scan.normalize_text,
                )
                hits = matcher.detect(scan.normalize_text(line))
                self.assertIn(digest, hits, f"keyed matcher missed shape '{label}'")

    def test_q3_wrapped_identity_touching_added_line_caught_even_when_unchanged_line_has_whole_identity(self) -> None:
        author = join("Kestrel", " ", "Nolan")
        email = join("knolan", "@", "northwind-lab", ".test")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "docs").mkdir()
            (root / "docs/notes.md").write_text(
                f"{author} met Kestrel\n",
                encoding="utf-8",
            )
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: base with whole identity and trailing first word")
            base = git(root, "rev-parse", "HEAD").stdout.strip()

            # (a) Clean edit adjacent to unchanged identity line MUST still pass.
            git(root, "checkout", "-q", "-b", "feat/q3-clean")
            (root / "docs/notes.md").write_text(
                f"{author} met Kestrel\nClean follow-up line.\n",
                encoding="utf-8",
            )
            git(root, "add", "docs/notes.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: clean adjacent line",
                author_name=author,
                author_email=email,
            )
            clean_head = git(root, "rev-parse", "HEAD").stdout.strip()
            code_clean, out_clean, err_clean = run_main(
                ["--root", str(root), "--range", base, clean_head, "--identity-source", "ci"]
            )
            self.assertEqual(0, code_clean, out_clean + err_clean)

            # (b) Adding "Nolan today." completes a genuine wrapped identity spanning into added line 2,
            # even though line 1 ALSO already contains the whole identity "Kestrel Nolan" -> MUST fail!
            git(root, "checkout", "-q", "-b", "feat/q3-wrap", base)
            (root / "docs/notes.md").write_text(
                f"{author} met Kestrel\nNolan today.\n",
                encoding="utf-8",
            )
            git(root, "add", "docs/notes.md")
            git(
                root,
                "commit",
                "-q",
                "-m",
                "docs: wrapped second word",
                author_name=author,
                author_email=email,
            )
            wrap_head = git(root, "rev-parse", "HEAD").stdout.strip()
            code_wrap, out_wrap, err_wrap = run_main(
                ["--root", str(root), "--range", base, wrap_head, "--identity-source", "ci"]
            )
            combined_wrap = out_wrap + err_wrap
            self.assertEqual(1, code_wrap, combined_wrap)
            self.assertIn("docs/notes.md:1: matched rule 'identity'", combined_wrap)
            self.assertNotIn(join("kestrel"), combined_wrap.casefold())

    def test_q4_failed_help_probe_fixture_only_fails_help_and_proves_guard_stops_execution(self) -> None:
        # Verify test_n1's failed-probe fixture fails ONLY on --help and asserts the probe guard stops it.
        import inspect

        n1_src = inspect.getsource(
            Round3ReviewRegressionTests.test_n1_bootstrap_is_strictly_one_time_for_pinned_main_push_only
        )
        self.assertIn("--help probe failed", n1_src)
        self.assertIn("scan_ran", n1_src)

        run_scan_script = Round2ReviewRegressionTests()._extract_workflow_step_run(
            ".github/workflows/public-scan.yml",
            "Run deterministic privacy and identity scan on every commit",
        )
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "tests").mkdir()
            gh_out = root / "gh_output.txt"
            sentinel = root / "scan_ran.txt"
            # Fixture fails ONLY on --help; any non-help scan call would succeed and write scan_ran.txt.
            (root / "tests/check_public_release.py").write_text(
                "import pathlib, sys\n"
                "if '--help' in sys.argv:\n"
                "    print('help probe broken', file=sys.stderr)\n"
                "    raise SystemExit(1)\n"
                "pathlib.Path('scan_ran.txt').write_text('ran', encoding='utf-8')\n"
                "print('PUBLIC_SCAN_VERIFIED=1')\n"
                "print('PUBLIC_SCAN_IDENTITY_VERIFIED=1')\n",
                encoding="utf-8",
            )
            for label, ev_env in (
                ("pr_help_only_fail", {"EVENT_NAME": "pull_request_target", "GITHUB_REF": "refs/heads/main", "PUSH_BEFORE": ""}),
                ("push_help_only_fail", {"EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main", "PUSH_BEFORE": self.PINNED_BOOTSTRAP_BEFORE}),
            ):
                with self.subTest(case=label):
                    gh_out.write_text("", encoding="utf-8")
                    if sentinel.exists():
                        sentinel.unlink()
                    proc = subprocess.run(
                        ["bash", "-c", run_scan_script],
                        cwd=root,
                        env={
                            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                            "SCAN_MODE": "range",
                            "BASE_SHA": self.PINNED_BOOTSTRAP_BEFORE,
                            "HEAD_SHA": "b" * 40,
                            "GITHUB_OUTPUT": str(gh_out),
                            **ev_env,
                        },
                        capture_output=True,
                        text=True,
                        check=False,
                    )
                    combined = proc.stdout + proc.stderr
                    self.assertNotEqual(0, proc.returncode, combined)
                    self.assertIn("--help probe failed", combined)
                    self.assertFalse(sentinel.exists(), "probe guard must stop before invoking the scan")
                    self.assertEqual("", gh_out.read_text(encoding="utf-8").strip())


if __name__ == "__main__":
    unittest.main()
