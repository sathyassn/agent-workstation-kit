#!/usr/bin/env python3
"""Hardening, decoding, exemption, fail-closed, base-revision, timing, self-scan, and journey tests.

Every sensitive test value is synthetic and assembled from parts so this file
passes the strict public-safety self-scan without any exemptions.
"""

from __future__ import annotations

import contextlib
import datetime
import hashlib
import importlib.util
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "check_public_release_hardening", ROOT / "tests/check_public_release.py"
)
assert SPEC and SPEC.loader
scan = importlib.util.module_from_spec(SPEC)
sys.modules["check_public_release_hardening"] = scan
SPEC.loader.exec_module(scan)


def ip(*octets: str) -> str:
    return ".".join(octets)


def join(*parts: str) -> str:
    return "".join(parts)


def git(root: Path, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    env = {
        **os.environ,
        "GIT_AUTHOR_NAME": "Test Contributor",
        "GIT_AUTHOR_EMAIL": "contributor@example.invalid",
        "GIT_COMMITTER_NAME": "Test Contributor",
        "GIT_COMMITTER_EMAIL": "contributor@example.invalid",
    }
    return subprocess.run(
        ["git", *args],
        cwd=root,
        check=check,
        capture_output=True,
        text=True,
        env=env,
    )


def run_main(argv: list[str], *, stdin_text: str | None = None) -> tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        if stdin_text is not None:
            with mock.patch.object(scan.sys, "stdin", io.StringIO(stdin_text)):
                code = scan.main(argv)
        else:
            code = scan.main(argv)
    return code, out.getvalue(), err.getvalue()


def scan_bytes(
    content: bytes,
    path: str = "docs/sample.txt",
    *,
    modes=None,
    exemptions=(),
    binary_exceptions=None,
    detectors=None,
) -> scan.ScanResult:
    return scan.scan_sources(
        [scan.SourceFile(path, content)],
        modes=modes,
        exemptions=exemptions,
        binary_exceptions=binary_exceptions,
        detectors=detectors,
    )


def scan_line(text: str, path: str = "docs/sample.txt", **kwargs) -> scan.ScanResult:
    return scan_bytes((text + "\n").encode("utf-8"), path, **kwargs)


def write_scan_configs(
    root: Path,
    *,
    modes: dict[str, str] | None = None,
    exemptions: list[dict[str, object]] | None = None,
    binary: list[dict[str, object]] | None = None,
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


class DecodingAndFoldingTests(unittest.TestCase):
    """AC-2: JSON/URL escapes, UTF-16, case, confusables, zero-padded octets, glued/partial prefixes, binary NUL."""

    def test_json_escapes_are_decoded(self) -> None:
        escaped_email = join("jdoe", "\\u0040", "acme-corp", "\\u002e", "com")
        res = scan_line(f'"{escaped_email}"')
        self.assertEqual(["docs/sample.txt:1: matched rule 'email'"], res.failures)

        escaped_ip = join("10", "\\u002e", "1", "\\u002e", "2", "\\u002e", "3")
        res_ip = scan_line(f'"{escaped_ip}"')
        self.assertEqual(["docs/sample.txt:1: matched rule 'ipv4'"], res_ip.failures)

        escaped_home = join("\\/Users", "\\/jdoe", "\\/work")
        res_home = scan_line(f'"{escaped_home}"')
        self.assertEqual(["docs/sample.txt:1: matched rule 'home-path'"], res_home.failures)

    def test_url_escapes_are_decoded(self) -> None:
        url_email = join("mailto:jdoe", "%40", "acme-corp", "%2E", "com")
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'email'"],
            scan_line(url_email).failures,
        )
        url_home = join("%2FUsers", "%2Fjdoe", "%2Fnotes")
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'home-path'"],
            scan_line(url_home).failures,
        )
        url_ip = join("192", "%2e", "168", "%2e", "1", "%2e", "10")
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'ipv4'"],
            scan_line(url_ip).failures,
        )
        # URL-encoded UTF-8 Cyrillic lookalike 'а' (%D0%B0) in domain is decoded then folded.
        url_confusable = join("jdoe", "%40", "c%D0%B0rp", ".com")
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'email'"],
            scan_line(url_confusable).failures,
        )

    def test_utf16_with_and_without_bom_is_decoded_and_scanned(self) -> None:
        secret_email = join("jdoe", "@", "acme-corp", ".com") + "\n"
        for encoding in ("utf-16", "utf-16-le", "utf-16-be"):
            with self.subTest(encoding=encoding):
                res = scan_bytes(secret_email.encode(encoding))
                self.assertEqual(["docs/sample.txt:1: matched rule 'email'"], res.failures)
        # UTF-8 BOM prepended before UTF-16-LE cannot hide NUL-interleaved text.
        hidden = b"\xef\xbb\xbf" + secret_email.encode("utf-16-le")
        self.assertEqual(["docs/sample.txt:1: matched rule 'email'"], scan_bytes(hidden).failures)

    def test_case_folding(self) -> None:
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'email'"],
            scan_line(join("JDOE", "@", "ACME-CORP", ".COM")).failures,
        )
        self.assertEqual([], scan_line("ALICE@EXAMPLE.COM").failures)
        self.assertEqual([], scan_line("2001:DB8::1").failures)
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'private-dns'"],
            scan_line(join("MYBOX", ".TAIL9A8B", ".TS.NET")).failures,
        )

    def test_confusable_lookalikes_and_zero_width_marks(self) -> None:
        cases = {
            "cyrillic lookalike in email": ("email", join("jd", "о", "e@", "а", "cme-corp.com")),
            "greek lookalike in dns": ("private-dns", join("myb", "ο", "x", ".ts", ".net")),
            "fullwidth digits and dots": ("ipv4", join("１０", "．", "１", "．", "２", "．", "３")),
            "fullwidth @ and dot": ("email", join("jdoe", "＠", "acme-corp", "．", "com")),
            "zero-width space inside ip": ("ipv4", "1\u200b0." + ip("1", "2", "3")),
            "combining mark inside ip": ("ipv4", "10\u0301." + ip("1", "2", "3")),
            "homoglyph dot in dns": ("private-dns", join("mybox", "\u2024", "ts", "\u3002", "net")),
            "format char between words": ("private-dns", join("prefix\u200b", "mybox", ".ts", ".net")),
        }
        for name, (rule, text) in cases.items():
            with self.subTest(case=name):
                res = scan_line(text)
                self.assertTrue(
                    any(f"matched rule '{rule}'" in f for f in res.failures),
                    (name, res.failures),
                )

    def test_zero_padded_octets(self) -> None:
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'ipv4'"],
            scan_line("host " + ip("010", "001", "002", "003")).failures,
        )
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'ipv4'"],
            scan_line("pub " + ip("008", "008", "008", "008")).failures,
        )
        # Zero-padded documentation IP folds to 192.0.2.1 (allowed).
        self.assertEqual([], scan_line("doc " + ip("192", "000", "002", "001")).failures)

    def test_glued_prefixes(self) -> None:
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'ipv4'"],
            scan_line("host" + ip("10", "1", "2", "3")).failures,
        )
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'ipv4'"],
            scan_line(ip("10", "1", "2", "3") + "abc").failures,
        )
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'ipv6'"],
            scan_line("host" + join("fe80", "::", "1")).failures,
        )
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'ipv6'"],
            scan_line("node" + join("fe80", "::", "1")).failures,
        )
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'private-dns'"],
            scan_line("ssh://" + join("mybox", ".ts.net") + "/path").failures,
        )

    def test_partial_private_prefixes(self) -> None:
        for text in (
            "range " + ip("192", "168"),
            "range " + ip("172", "20"),
            "range " + ip("169", "254"),
            "net " + ip("10", "20") + ".",
            "net " + ip("10", "20", "30") + ".x",
            "cidr " + ip("10", "20", "0") + "/16",
            "net " + ip("100", "64", "1"),
        ):
            with self.subTest(text=text):
                self.assertEqual(["docs/sample.txt:1: matched rule 'ipv4'"], scan_line(text).failures)
        # Common numbers and versions remain clean.
        for text in (
            "width " + ip("10", "5") + "rem",
            "ratio " + ip("100", "75"),
            "macOS " + ip("10", "15", "7"),
            "version " + ip("1", "10", "0", "0", "1"),
            "big " + ip("10", "300", "1", "1"),
        ):
            with self.subTest(clean=text):
                self.assertEqual([], scan_line(text).failures)

    def test_binary_nul_refused_unless_hash_bound_exception_matches(self) -> None:
        raw_nul = b"ab\x00\x00\x00cd"
        self.assertEqual(
            ["tracked file is binary, not text: docs/blob.bin"],
            scan_bytes(raw_nul, "docs/blob.bin").failures,
        )
        png_bytes = b"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + bytes(range(256))
        self.assertEqual(
            ["tracked file is not valid UTF-8 or UTF-16 text: docs/logo.png"],
            scan_bytes(png_bytes, "docs/logo.png").failures,
        )
        exc = {
            "docs/logo.png": scan.BinaryException(
                path="docs/logo.png",
                sha256=hashlib.sha256(png_bytes).hexdigest(),
                reason="reviewed logo fixture",
            )
        }
        self.assertEqual([], scan_bytes(png_bytes, "docs/logo.png", binary_exceptions=exc).failures)
        # Modifying one byte fails closed.
        changed = scan_bytes(png_bytes + b"\x00", "docs/logo.png", binary_exceptions=exc)
        self.assertEqual(
            ["binary exception content changed; review and update its sha256: docs/logo.png"],
            changed.failures,
        )


class RedactedOutputTests(unittest.TestCase):
    """AC-3: findings print only file, line, and rule, never the matched text."""

    def test_stdout_and_stderr_never_contain_matched_values(self) -> None:
        email = join("jdoe", "@", "acme-corp", ".com")
        priv_ip = ip("10", "44", "55", "66")
        phone = join("+1", "-415-", "867-", "5309")
        dns = join("secretbox", ".tail9a8b", ".ts.net")
        secret = join("glpat-", "secret99887766554433")

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "docs").mkdir()
            (root / "docs/leak.txt").write_text(
                f"{email}\n{priv_ip}\n{phone}\n{dns}\n{secret}\n",
                encoding="utf-8",
            )
            # Also put a sensitive value inside a filename.
            bad_path = root / "docs" / (email + ".md")
            bad_path.write_text("clean\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: fixture")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            code, out, err = run_main(["--root", str(root), "--tree", head])
            combined = (out + err).casefold()
            self.assertEqual(1, code, combined)
            self.assertIn("docs/leak.txt:1: matched rule 'email'", out)
            self.assertIn("docs/leak.txt:2: matched rule 'ipv4'", out)
            self.assertIn("docs/leak.txt:3: matched rule 'phone'", out)
            self.assertIn("docs/leak.txt:4: matched rule 'private-dns'", out)
            self.assertIn("docs/leak.txt:5: matched rule 'secret'", out)
            self.assertIn("<redacted-", out)
            for sensitive in (email, "acme-corp", priv_ip, phone, "867-5309", dns, "secretbox", secret):
                self.assertNotIn(sensitive.casefold(), combined)

    def test_unflagged_paths_escape_control_characters(self) -> None:
        res = scan_bytes(b"\xff", "docs/evil\nFAKE: passed\r\x1b[2K.md")
        self.assertEqual(1, len(res.failures))
        self.assertNotIn("\n", res.failures[0])
        self.assertNotIn("\r", res.failures[0])
        self.assertNotIn("\x1b", res.failures[0])
        self.assertIn("docs/evil\\x0aFAKE", res.failures[0])

    def test_internal_error_never_leaks_exception_text(self) -> None:
        secret = join("jdoe", "@", "acme-corp", ".com")
        with mock.patch.object(scan, "_run", side_effect=RuntimeError("boom " + secret)):
            code, out, err = run_main([])
        self.assertEqual(1, code)
        self.assertIn("internal error (RuntimeError)", out)
        self.assertNotIn(secret, out + err)


class ExemptionRecordTests(unittest.TestCase):
    """AC-4: digest-bound exemptions, validation, expiry, base-only loading, and applied/unused listing."""

    def test_valid_exemption_matches_exact_path_rule_and_folded_digest(self) -> None:
        email = join("jdoe", "@", "acme-corp", ".com")
        upper_email = email.upper()
        other_email = join("other", "@", "acme-corp", ".com")
        digest = scan.compute_exemption_digest("email", email)
        rec = scan.ExemptionRecord(
            path="docs/a.txt",
            rule="email",
            digest=digest,
            reason="synthetic fixture",
            approved_by="@owner",
            expires="2027-06-01",
        )
        # Exact path + folded value passes and marks exemption applied.
        res_ok = scan_line(upper_email, "docs/a.txt", exemptions=[rec])
        self.assertEqual([], res_ok.failures)
        self.assertIn(rec.key, res_ok.applied_exemptions)

        # Same value in another path is still blocked and exemption stays unused.
        res_other_path = scan_line(email, "docs/b.txt", exemptions=[rec])
        self.assertEqual(["docs/b.txt:1: matched rule 'email'"], res_other_path.failures)
        self.assertEqual(frozenset(), res_other_path.applied_exemptions)

        # Different value in the same path is still blocked.
        res_other_val = scan_line(other_email, "docs/a.txt", exemptions=[rec])
        self.assertEqual(["docs/a.txt:1: matched rule 'email'"], res_other_val.failures)

    def test_expired_undated_malformed_and_incomplete_records_fail(self) -> None:
        digest = scan.compute_exemption_digest("ipv4", ip("10", "1", "2", "3"))
        today = datetime.date(2026, 11, 23)
        base_entry = {
            "path": "tests/fixture.py",
            "rule": "ipv4",
            "digest": digest,
            "reason": "fixture",
            "approved_by": "@owner",
            "expires": "2027-06-01",
        }

        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "exemptions.json"

            # Valid record passes.
            path.write_text(json.dumps({"schema_version": 1, "exemptions": [base_entry]}), encoding="utf-8")
            loaded = scan.load_exemptions(path, today=today)
            self.assertEqual(1, len(loaded))

            bad_cases = {
                "expired": {**base_entry, "expires": "2026-11-22"},
                "undated_missing_key": {k: v for k, v in base_entry.items() if k != "expires"},
                "undated_empty": {**base_entry, "expires": ""},
                "malformed_date_format": {**base_entry, "expires": "2027/06/01"},
                "malformed_calendar_date": {**base_entry, "expires": "2027-02-30"},
                "incomplete_missing_reason": {k: v for k, v in base_entry.items() if k != "reason"},
                "incomplete_empty_reason": {**base_entry, "reason": "   "},
                "incomplete_missing_approver": {k: v for k, v in base_entry.items() if k != "approved_by"},
                "incomplete_missing_digest": {k: v for k, v in base_entry.items() if k != "digest"},
                "malformed_digest_plaintext": {**base_entry, "digest": ip("10", "1", "2", "3")},
                "unknown_rule": {**base_entry, "rule": "not-a-rule"},
                "absolute_path": {**base_entry, "path": "/etc/passwd"},
                "traversal_path": {**base_entry, "path": "../secret.txt"},
                "unexpected_extra_field": {**base_entry, "value": "never-allowed"},
            }
            for name, entry in bad_cases.items():
                with self.subTest(case=name):
                    path.write_text(json.dumps({"schema_version": 1, "exemptions": [entry]}), encoding="utf-8")
                    with self.assertRaises(scan.ScanConfigError):
                        scan.load_exemptions(path, today=today)

    def test_applied_and_unused_exemption_output_never_prints_value(self) -> None:
        used_ip = ip("10", "1", "2", "3")
        unused_email = join("unused", "@", "acme-corp", ".com")
        rec_used = scan.ExemptionRecord(
            path="docs/used.txt",
            rule="ipv4",
            digest=scan.compute_exemption_digest("ipv4", used_ip),
            reason="synthetic",
            approved_by="@owner",
            expires="2027-06-01",
        )
        rec_unused = scan.ExemptionRecord(
            path="docs/other.txt",
            rule="email",
            digest=scan.compute_exemption_digest("email", unused_email),
            reason="synthetic",
            approved_by="@owner",
            expires="2027-06-01",
        )
        res = scan_line(used_ip, "docs/used.txt", exemptions=[rec_used, rec_unused])
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = scan._report(
                failures=res.failures,
                warnings=res.warnings,
                modes=scan.DEFAULT_MODES,
                exemptions=[rec_used, rec_unused],
                applied_exemptions=res.applied_exemptions,
                passed_message="ok",
            )
        text = out.getvalue()
        self.assertEqual(0, code)
        self.assertIn(f"- [applied] docs/used.txt rule=ipv4 digest={rec_used.digest[:12]}", text)
        self.assertIn(f"- [unused] docs/other.txt rule=email digest={rec_unused.digest[:12]}", text)
        self.assertNotIn(used_ip, text)
        self.assertNotIn(unused_email, text)

    def test_exemption_digest_cli_helper(self) -> None:
        val = ip("010", "001", "002", "003")
        code, out, err = run_main(["--exemption-digest", "ipv4"], stdin_text=val + "\n")
        self.assertEqual(0, code, err)
        expected = hashlib.sha256(ip("10", "1", "2", "3").encode("utf-8")).hexdigest()
        self.assertEqual(expected, out.strip())


class FailClosedNegativeTests(unittest.TestCase):
    """AC-5: failed/empty listing, raising/skipped detector, decode error, and exceeded caps fail closed."""

    def test_failed_and_empty_listings_fail_closed_with_no_success_report(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            # Non-git directory fails closed.
            code, out, _ = run_main(["--root", str(root), "--allow-missing-license"])
            self.assertEqual(1, code)
            self.assertNotIn("passed", out.lower())

            # Empty git repo fails closed.
            git(root, "init", "-q", "-b", "main")
            code2, out2, _ = run_main(["--root", str(root), "--allow-missing-license"])
            self.assertEqual(1, code2)
            self.assertIn("tracked file listing is empty", out2)
            self.assertNotIn("passed", out2.lower())

        # Empty source stream fails closed.
        empty_res = scan.scan_sources([])
        self.assertEqual(["tracked file listing is missing or empty"], empty_res.failures)

        # Non-UTF-8 tracked path fails closed.
        bad_listing = subprocess.CompletedProcess(args=[], returncode=0, stdout=b"ok.txt\0bad-\xff.txt\0", stderr=b"")
        with mock.patch.object(scan.subprocess, "run", return_value=bad_listing):
            tracked, error = scan.tracked_files(Path("."))
        self.assertIsNone(tracked)
        self.assertIn("not valid UTF-8", error or "")

    def test_raising_detector_fails_closed_without_leaking_input(self) -> None:
        secret = join("jdoe", "@", "acme-corp", ".com")

        def boom(_text: str) -> list[str]:
            raise RuntimeError("detector crashed on " + secret)

        broken = dict(scan.DETECTORS)
        broken["email"] = boom
        res = scan_line("clean line", detectors=broken)
        self.assertTrue(res.failures)
        self.assertTrue(
            all("detector 'email' failed (RuntimeError); failing closed" in f for f in res.failures),
            res.failures,
        )
        self.assertNotIn(secret, "\n".join(res.failures))

    def test_skipped_or_missing_detector_fails_closed(self) -> None:
        incomplete = dict(scan.DETECTORS)
        del incomplete["phone"]
        with self.assertRaisesRegex(scan.ScanConfigError, "detector 'phone' is missing or skipped"):
            scan.Scanner(detectors=incomplete)

        # A detector returning a non-list fails closed.
        bad_ret = dict(scan.DETECTORS)
        bad_ret["phone"] = lambda _text: None  # type: ignore[assignment,return-value]
        res = scan_line("clean line", detectors=bad_ret)
        self.assertTrue(any("detector 'phone' failed" in f for f in res.failures))

    def test_decode_error_and_unreadable_file_fail_closed(self) -> None:
        res = scan_bytes(b"bad \xff\xfe bytes")
        self.assertEqual(["tracked file is not valid UTF-8 or UTF-16 text: docs/sample.txt"], res.failures)

        if not (hasattr(os, "geteuid") and os.geteuid() == 0):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                locked = root / "locked.txt"
                locked.write_text("clean\n", encoding="utf-8")
                locked.chmod(0)
                try:
                    locked_res = scan.scan_tracked_files(root, ["locked.txt"])
                finally:
                    locked.chmod(0o600)
                self.assertEqual(["tracked file is unreadable: locked.txt"], locked_res.failures)

    def test_size_entry_listing_and_finding_caps_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            git(root, "init", "-q", "-b", "main")
            for idx in range(5):
                (root / f"file-with-name-{idx}.txt").write_text(f"clean line {idx}\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: caps fixture")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            with mock.patch.object(scan, "MAX_TREE_ENTRIES", 3):
                with self.assertRaisesRegex(scan.ScanConfigError, "more than 3 entries"):
                    scan.tree_sources(root, head)
            with mock.patch.object(scan, "MAX_TREE_BYTES", 10):
                with self.assertRaisesRegex(scan.ScanConfigError, "more than 10 bytes"):
                    scan.tree_sources(root, head)
            with mock.patch.object(scan, "MAX_LISTING_BYTES", 50):
                with self.assertRaisesRegex(scan.ScanConfigError, "larger than 50 bytes"):
                    scan.tree_sources(root, head)
            with mock.patch.object(scan, "MAX_SCAN_FILE_BYTES", 5):
                _, sources = scan.tree_sources(root, head)
                res = scan.scan_sources(sources)
                self.assertTrue(any("too large to scan" in f for f in res.failures))

        # Findings cap stops and fails closed.
        email = join("jdoe", "@", "acme-corp", ".com")
        sources = [scan.SourceFile(f"docs/f{i}.txt", f"{email}\n".encode()) for i in range(8)]
        with mock.patch.object(scan, "MAX_FINDINGS", 4):
            res = scan.scan_sources(iter(sources))
        self.assertEqual(5, len(res.failures))
        self.assertEqual("more than 4 findings; the scan stopped after 5 entries", res.failures[-1])

        # Raw per-file match overflow fails closed even if every collected value is exempt.
        exempt_ip = ip("100", "64", "0", "10")
        rec = scan.ExemptionRecord(
            path="docs/many.txt",
            rule="ipv4",
            digest=scan.compute_exemption_digest("ipv4", exempt_ip),
            reason="synthetic",
            approved_by="@owner",
            expires="2027-06-01",
        )
        with mock.patch.object(scan, "MAX_FINDINGS", 5):
            many_res = scan_bytes(((exempt_ip + "\n") * 7).encode(), "docs/many.txt", exemptions=[rec])
        self.assertEqual(
            ["more than 5 matches; the scan cannot check the rest: docs/many.txt"],
            many_res.failures,
        )


class TrustedBaseRevisionSimulationTests(unittest.TestCase):
    """AC-6: base revision supplies scanner, modes, exemptions, binary exceptions; head is data only."""

    def _setup_base_repo(self, root: Path) -> str:
        git(root, "init", "-q", "-b", "main")
        (root / "README.md").write_text("clean base\n", encoding="utf-8")
        shutil.copyfile(ROOT / "tests/check_public_release.py", root / "tests/check_public_release.py")
        shutil.copyfile(ROOT / "tests/public_scan_identity.py", root / "tests/public_scan_identity.py")
        shutil.copyfile(ROOT / "tests/public_scan_keyed.py", root / "tests/public_scan_keyed.py")
        write_scan_configs(root)
        git(root, "add", "-A")
        git(root, "commit", "-q", "-m", "chore: base")
        return git(root, "rev-parse", "HEAD").stdout.strip()

    def test_head_cannot_bypass_by_editing_scanner_modes_exemptions_or_binary(self) -> None:
        leak_email = join("jdoe", "@", "acme-corp", ".com")
        leak_digest = scan.compute_exemption_digest("email", leak_email)
        binary_payload = b"ab\x00\x00cd"
        binary_sha = hashlib.sha256(binary_payload).hexdigest()

        scenarios = (
            "delete_detector_in_head",
            "set_mode_off_in_head",
            "add_exemption_in_head",
            "add_binary_exception_in_head",
        )
        for scenario in scenarios:
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "tests").mkdir()
                base = self._setup_base_repo(root)

                git(root, "checkout", "-q", "-b", "feat/pr-head")
                if scenario == "delete_detector_in_head":
                    (root / "tests/check_public_release.py").write_text("raise SystemExit(0)\n", encoding="utf-8")
                    (root / "leak.txt").write_text(leak_email + "\n", encoding="utf-8")
                elif scenario == "set_mode_off_in_head":
                    modes = dict(scan.DEFAULT_MODES)
                    modes["email"] = "off"
                    write_scan_configs(root, modes=modes)
                    (root / "leak.txt").write_text(leak_email + "\n", encoding="utf-8")
                elif scenario == "add_exemption_in_head":
                    write_scan_configs(
                        root,
                        exemptions=[
                            {
                                "path": "leak.txt",
                                "rule": "email",
                                "digest": leak_digest,
                                "reason": "self-approved in head",
                                "approved_by": "@attacker",
                                "expires": "2027-06-01",
                            }
                        ],
                    )
                    (root / "leak.txt").write_text(leak_email + "\n", encoding="utf-8")
                elif scenario == "add_binary_exception_in_head":
                    write_scan_configs(
                        root,
                        binary=[{"path": "blob.bin", "sha256": binary_sha, "reason": "head binary"}],
                    )
                    (root / "blob.bin").write_bytes(binary_payload)

                git(root, "add", "-A")
                git(root, "commit", "-q", "-m", f"feat: {scenario}")
                head = git(root, "rev-parse", "HEAD").stdout.strip()

                # Checkout trusted base and judge head purely as git objects with --tree.
                git(root, "checkout", "-q", base)
                self.assertFalse((root / "leak.txt").exists())
                self.assertFalse((root / "blob.bin").exists())

                proc = subprocess.run(
                    [sys.executable, "-I", "-B", "tests/check_public_release.py", "--tree", head],
                    cwd=root,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(1, proc.returncode, proc.stdout + proc.stderr)
                self.assertNotIn("passed", proc.stdout.lower())
                if scenario == "add_binary_exception_in_head":
                    self.assertIn("tracked file is binary, not text: blob.bin", proc.stdout)
                else:
                    self.assertIn("leak.txt:1: matched rule 'email'", proc.stdout)
                    self.assertNotIn(leak_email, proc.stdout + proc.stderr)

    def test_public_scan_workflow_uses_trusted_base_both_oses_and_no_secret(self) -> None:
        workflow = (ROOT / ".github/workflows/public-scan.yml").read_text(encoding="utf-8")
        self.assertIn("pull_request_target:", workflow)
        self.assertIn("ref: ${{ github.sha }}", workflow)
        self.assertIn("--tree", workflow)
        self.assertIn("ubuntu-24.04", workflow)
        self.assertIn("macos-latest", workflow)
        self.assertNotIn("secrets.", workflow)
        self.assertNotIn("PUBLIC_SCAN_HMAC_KEY", workflow)


class TimingSelfScanAndJourneyTests(unittest.TestCase):
    """AC-7 (speed < 1s), AC-9 (self-scan with real exemption record), AC-10 (contributor journey)."""

    def test_typical_diff_finishes_well_under_one_second_without_secret(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            for idx in range(25):
                (root / f"module_{idx}.py").write_text(
                    f"# Module {idx}\nVALUE = 'alice@example.com'\nADDR = '192.0.2.{idx + 1}'\n" * 40,
                    encoding="utf-8",
                )
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "feat: typical change")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            clean_env = {k: v for k, v in os.environ.items() if "SECRET" not in k and "HMAC" not in k}
            t0 = time.perf_counter()
            with mock.patch.dict(os.environ, clean_env, clear=True):
                code, out, err = run_main(["--root", str(root), "--tree", head])
            elapsed = time.perf_counter() - t0
            self.assertEqual(0, code, out + err)
            self.assertLess(elapsed, 0.8, f"typical diff scan took {elapsed:.3f}s (expected well under 1s)")

    def test_self_scan_scans_tests_and_real_exemption_records_cleanly(self) -> None:
        # Scanner and both test suites must pass self-scan with zero exemptions.
        self_files = [
            "tests/check_public_release.py",
            "tests/public_scan_identity.py",
            "tests/public_scan_keyed.py",
            "tests/test_public_release.py",
            "tests/test_public_scan_hardening.py",
            "tests/test_public_identity_and_keyed.py",
            "tests/public_scan_modes.json",
            "tests/public_scan_exemptions.json",
            "tests/public_scan_binary_exceptions.json",
        ]
        sources = [scan.SourceFile(rel, (ROOT / rel).read_bytes()) for rel in self_files]
        res = scan.scan_sources(sources)
        self.assertEqual([], res.failures, res.failures)

        # A real exemption record holding only SHA-256 digests of sensitive values
        # never fails the self-scan.
        real_email = join("realperson", "@", "private-org", ".com")
        real_ip = ip("10", "99", "88", "77")
        exemption_doc = {
            "schema_version": 1,
            "exemptions": [
                {
                    "path": "tests/fixture.py",
                    "rule": "email",
                    "digest": scan.compute_exemption_digest("email", real_email),
                    "reason": "reviewed synthetic fixture",
                    "approved_by": "@owner",
                    "expires": "2027-06-01",
                },
                {
                    "path": "tests/fixture.py",
                    "rule": "ipv4",
                    "digest": scan.compute_exemption_digest("ipv4", real_ip),
                    "reason": "reviewed synthetic fixture",
                    "approved_by": "@owner",
                    "expires": "2027-06-01",
                },
            ],
        }
        payload = (json.dumps(exemption_doc, indent=2) + "\n").encode("utf-8")
        self.assertEqual([], scan_bytes(payload, "tests/public_scan_exemptions.json").failures)

    def test_contributor_journey_refuses_push_then_passes_after_redaction(self) -> None:
        """AC-10: real email + private IP refuses push with only file/line/rule; replacing with doc values passes."""
        bad_email = join("jdoe", "@", "acme-corp", ".com")
        bad_ip = ip("10", "42", "7", "9")

        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            remote = base / "remote.git"
            repo = base / "work"
            subprocess.run(["git", "init", "--bare", "-q", str(remote)], check=True)
            git(base, "init", "-q", "-b", "main", str(repo))

            # Install real pre-push hook, policy, discipline checker, scanner, and minimal Makefile.
            (repo / ".codeflow").mkdir()
            shutil.copyfile(ROOT / ".codeflow/policy.json", repo / ".codeflow/policy.json")
            shutil.copytree(ROOT / ".codeflow/git-hooks", repo / ".codeflow/git-hooks")
            (repo / "scripts").mkdir()
            shutil.copyfile(ROOT / "scripts/check_git_discipline.py", repo / "scripts/check_git_discipline.py")
            (repo / "tests").mkdir()
            shutil.copyfile(ROOT / "tests/check_public_release.py", repo / "tests/check_public_release.py")
            shutil.copyfile(ROOT / "tests/public_scan_identity.py", repo / "tests/public_scan_identity.py")
            shutil.copyfile(ROOT / "tests/public_scan_keyed.py", repo / "tests/public_scan_keyed.py")
            write_scan_configs(repo)
            git(repo, "config", "user.name", "Test Contributor")
            git(repo, "config", "user.email", "contributor@example.invalid")
            # Minimal repo metadata for check_public_release.py --allow-missing-license.
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
            (repo / "Makefile").write_text(
                "ci-check:\n\t@python3 -I -B tests/check_public_release.py --allow-missing-license\n",
                encoding="utf-8",
            )

            git(repo, "add", "-A")
            git(repo, "commit", "-q", "-m", "chore: initial repo")
            git(repo, "remote", "add", "origin", str(remote))
            git(repo, "push", "-q", "origin", "main")
            git(repo, "config", "core.hooksPath", ".codeflow/git-hooks")

            # Contributor branches and adds a real-looking email and private IP.
            git(repo, "checkout", "-q", "-b", "feat/contributor-notes")
            notes = repo / "docs/contributor-notes.md"
            notes.write_text(f"Contact: {bad_email}\nHost: {bad_ip}\n", encoding="utf-8")
            git(repo, "add", "docs/contributor-notes.md")
            git(repo, "commit", "-q", "-m", "docs: add contributor notes")

            push_fail = git(repo, "push", "origin", "feat/contributor-notes", check=False)
            transcript_fail = push_fail.stdout + push_fail.stderr
            self.assertNotEqual(0, push_fail.returncode, transcript_fail)
            self.assertIn("docs/contributor-notes.md:1: matched rule 'email'", transcript_fail)
            self.assertIn("docs/contributor-notes.md:2: matched rule 'ipv4'", transcript_fail)
            self.assertNotIn(bad_email, transcript_fail)
            self.assertNotIn(bad_ip, transcript_fail)
            # Remote must not have the rejected branch.
            remote_refs = git(remote, "for-each-ref", "refs/heads/feat/contributor-notes").stdout.strip()
            self.assertEqual("", remote_refs)

            # A follow-up commit fixing only the tip must STILL fail push because
            # the earlier unpushed commit in the pushed range still holds the leak.
            notes.write_text("Contact: alice@example.com\nHost: 192.0.2.10\n", encoding="utf-8")
            git(repo, "add", "docs/contributor-notes.md")
            git(repo, "commit", "-q", "-m", "docs: use documentation placeholders")

            push_dirty_history = git(repo, "push", "origin", "feat/contributor-notes", check=False)
            transcript_dirty = push_dirty_history.stdout + push_dirty_history.stderr
            self.assertNotEqual(0, push_dirty_history.returncode, transcript_dirty)
            self.assertIn("docs/contributor-notes.md:1: matched rule 'email'", transcript_dirty)
            self.assertNotIn(bad_email, transcript_dirty)
            self.assertNotIn(bad_ip, transcript_dirty)

            # Replacing history with only a clean commit passes and updates remote.
            git(repo, "reset", "--soft", "main")
            git(repo, "commit", "-q", "-m", "docs: add clean contributor notes")

            push_ok = git(repo, "push", "origin", "feat/contributor-notes", check=False)
            transcript_ok = push_ok.stdout + push_ok.stderr
            self.assertEqual(0, push_ok.returncode, transcript_ok)
            self.assertIn("Deterministic public-release draft checks passed", transcript_ok)
            remote_refs_after = git(remote, "for-each-ref", "refs/heads/feat/contributor-notes").stdout.strip()
            self.assertIn("refs/heads/feat/contributor-notes", remote_refs_after)


class Round1ReviewRegressionTests(unittest.TestCase):
    """Regression tests for PR #42 round 1 review findings (H1-H4, M1-M7)."""

    def test_h1_range_mode_scans_every_commit_and_public_scan_fetches_range(self) -> None:
        leak_email = join("jdoe", "@", "acme-corp", ".com")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "README.md").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: base")
            base = git(root, "rev-parse", "HEAD").stdout.strip()

            git(root, "checkout", "-q", "-b", "feat/history-leak")
            (root / "notes.txt").write_text(leak_email + "\n", encoding="utf-8")
            git(root, "add", "notes.txt")
            git(root, "commit", "-q", "-m", "docs: leaky commit")
            (root / "notes.txt").write_text("alice@example.com\n", encoding="utf-8")
            git(root, "add", "notes.txt")
            git(root, "commit", "-q", "-m", "docs: redact tip")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            git(root, "checkout", "-q", base)
            code, out, err = run_main(["--root", str(root), "--range", base, head])
            self.assertEqual(1, code, out + err)
            self.assertIn("notes.txt:1: matched rule 'email'", out)
            self.assertNotIn(leak_email, out + err)

        workflow = (ROOT / ".github/workflows/public-scan.yml").read_text(encoding="utf-8")
        self.assertIn("--range", workflow)
        self.assertNotIn("git fetch --no-tags --depth=1", workflow)

    def test_h2_ci_workflow_and_docs_mark_head_jobs_non_authoritative(self) -> None:
        ci_yml = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        checklist = (ROOT / "docs/13-public-release-checklist.md").read_text(encoding="utf-8")
        self.assertIn("non-authoritative", ci_yml.lower())
        self.assertIn("non-authoritative", checklist.lower())
        self.assertIn("no required privacy", checklist.lower())

    def test_h3_required_check_name_aligned_and_tsk040_documented(self) -> None:
        workflow = (ROOT / ".github/workflows/public-scan.yml").read_text(encoding="utf-8")
        checklist = (ROOT / "docs/13-public-release-checklist.md").read_text(encoding="utf-8")
        self.assertIn("name: public-scan / result", workflow)
        self.assertIn("public-scan / result", checklist)
        self.assertIn("TSK-040", checklist)
        self.assertIn("absent", checklist.lower())

    def test_h4_email_trailing_period_digits_only_phone_and_quoted_json_keys(self) -> None:
        # 1) Email followed by a sentence-final period.
        sent_email = join("Reach jdoe", "@", "acme-corp", ".com.")
        self.assertEqual(["docs/sample.txt:1: matched rule 'email'"], scan_line(sent_email).failures)
        self.assertEqual([], scan_line("Reach alice@example.com.").failures)

        # 2) Domestic digits-only phone numbers in phone context.
        for hit_line in (
            join("phone: ", "415", "867", "5309"),
            join('{"mobile": "', "415", "867", "5309", '"}'),
            join("tel:", "415", "867", "5309"),
        ):
            with self.subTest(phone_hit=hit_line[:12]):
                self.assertEqual(["docs/sample.txt:1: matched rule 'phone'"], scan_line(hit_line).failures)
        for clean_line in (
            "phone: 5555550100",
            "timestamp = 1700000000",
            "build_id = 2026112301",
        ):
            with self.subTest(phone_clean=clean_line):
                self.assertEqual([], scan_line(clean_line).failures)

        # 3) Quoted JSON keys for cloud and chat/tracker IDs.
        aws_json = join('{"account_id": "', "987654", "321098", '"}')
        gcp_json = join('{"gcp_project_id": "', "prod-analytics", "-42", '"}')
        slack_json = join('{"slack_user": "', "u04xyz", "9876", '"}')
        jira_json = join('{"jira_account_id": "', "557058:", "11111111-2222-3333-4444-555555555555", '"}')
        self.assertEqual(["docs/sample.txt:1: matched rule 'cloud-id'"], scan_line(aws_json).failures)
        self.assertEqual(["docs/sample.txt:1: matched rule 'cloud-id'"], scan_line(gcp_json).failures)
        self.assertEqual(["docs/sample.txt:1: matched rule 'chat-tracker-id'"], scan_line(slack_json).failures)
        self.assertEqual(["docs/sample.txt:1: matched rule 'chat-tracker-id'"], scan_line(jira_json).failures)
        self.assertEqual([], scan_line('{"account_id": "123456789012", "slack_user": "u00000000"}').failures)

    def test_m1_case_insensitive_home_paths(self) -> None:
        for hit in (
            join("/users", "/jdoe", "/work"),
            join("/HOME", "/jdoe", "/work"),
            join("C:\\users", "\\jdoe", "\\work"),
        ):
            with self.subTest(hit=hit[:8]):
                self.assertEqual(["docs/sample.txt:1: matched rule 'home-path'"], scan_line(hit).failures)
        for allowed in (
            join("/users", "/alice", "/work"),
            join("/HOME", "/ALICE", "/work"),
            join("C:\\users", "\\alice", "\\work"),
        ):
            with self.subTest(allowed=allowed[:8]):
                self.assertEqual([], scan_line(allowed).failures)

    def test_m2_tree_mode_rejects_private_and_generated_paths(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "README.md").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: base")
            base = git(root, "rev-parse", "HEAD").stdout.strip()

            git(root, "checkout", "-q", "-b", "feat/private-path")
            (root / ".secrets").mkdir()
            (root / ".secrets/token.txt").write_text("clean\n", encoding="utf-8")
            (root / "config").mkdir()
            (root / "config/node.local.toml").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-f", ".secrets/token.txt", "config/node.local.toml")
            git(root, "commit", "-q", "-m", "chore: add private paths")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            git(root, "checkout", "-q", base)
            code, out, err = run_main(["--root", str(root), "--tree", head])
            self.assertEqual(1, code, out + err)
            self.assertIn("private/generated path is tracked: .secrets/token.txt", out)
            self.assertIn("private/generated path is tracked: config/node.local.toml", out)

    def test_m3_public_scan_workflow_verifies_positive_marker_and_never_skips_branches(self) -> None:
        workflow = (ROOT / ".github/workflows/public-scan.yml").read_text(encoding="utf-8")
        self.assertIn("PUBLIC_SCAN_VERIFIED=1", workflow)
        self.assertIn("scan_verified", workflow)
        self.assertNotIn("Not a pull request into", workflow)

    def test_m4_iterative_decoding_and_strict_utf16_heuristic(self) -> None:
        # URL-wrapped JSON escape (%5Cu0040 -> \u0040 -> @)
        url_json = join("jdoe", "%5Cu0040", "acme-corp", ".com")
        self.assertEqual(["docs/sample.txt:1: matched rule 'email'"], scan_line(url_json).failures)

        # JSON-wrapped URL escape (\u002540 -> %40 -> @)
        json_url = join("jdoe", "\\u002540", "acme-corp", ".com")
        self.assertEqual(["docs/sample.txt:1: matched rule 'email'"], scan_line(json_url).failures)

        # Deeply nested escape fails closed instead of silently stopping undecoded.
        deep = "jdoe" + "%" + ("25" * 5) + "40" + "acme-corp" + ".com"
        res_deep = scan_line(deep)
        self.assertTrue(res_deep.failures, "deeply nested escape must fail closed")
        self.assertNotIn("acme-corp", "\n".join(res_deep.failures))

        # Opaque NUL-bearing binary data (e.g. 16-bit LE integers/controls) must be refused as binary.
        opaque_nul = b"\x01\x00\x02\x00\x03\x00\x04\x00\x05\x00\x7f\x00\x1b\x00\x00\x00"
        opaque_res = scan_bytes(opaque_nul, "docs/table.bin")
        self.assertTrue(
            any("binary" in f or "not valid UTF-8 or UTF-16" in f for f in opaque_res.failures),
            opaque_res.failures,
        )
        # Even without double-NUL, control-byte NUL-interleaved binary is refused.
        opaque_ctrl = b"\x01\x00\x02\x00\x03\x00\x04\x00\x05\x00\x06\x00\x07\x00\x08\x00"
        ctrl_res = scan_bytes(opaque_ctrl, "docs/ctrl.bin")
        self.assertTrue(
            any("binary" in f or "not valid UTF-8 or UTF-16" in f for f in ctrl_res.failures),
            ctrl_res.failures,
        )

    def test_m5_redacted_paths_everywhere_and_expired_exemption_preserves_inventory(self) -> None:
        secret_email = join("jdoe", "@", "acme-corp", ".com")
        valid_ip = ip("100", "64", "0", "10")
        expired_ip = ip("100", "64", "0", "11")
        sensitive_path = f".secrets/{secret_email}.local.toml"

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(
                root,
                exemptions=[
                    {
                        "path": "docs/valid.txt",
                        "rule": "ipv4",
                        "digest": scan.compute_exemption_digest("ipv4", valid_ip),
                        "reason": "valid fixture",
                        "approved_by": "@owner",
                        "expires": "2027-06-01",
                    },
                    {
                        "path": f"people/{secret_email}/note.txt",
                        "rule": "ipv4",
                        "digest": scan.compute_exemption_digest("ipv4", expired_ip),
                        "reason": "expired fixture",
                        "approved_by": "@owner",
                        "expires": "2020-01-01",
                    },
                ],
            )
            git(root, "init", "-q", "-b", "main")
            (root / "docs").mkdir()
            (root / "docs/valid.txt").write_text(valid_ip + "\n", encoding="utf-8")
            (root / ".secrets").mkdir()
            (root / sensitive_path).write_text("clean\n", encoding="utf-8")
            git(root, "add", "-f", "-A")
            git(root, "commit", "-q", "-m", "chore: m5 fixture")
            head = git(root, "rev-parse", "HEAD").stdout.strip()

            code, out, err = run_main(["--root", str(root), "--tree", head])
            combined = out + err
            self.assertEqual(1, code, combined)
            # Sensitive email in generated path and exemption path must NEVER appear in output.
            self.assertNotIn(secret_email, combined)
            self.assertNotIn("acme-corp", combined)
            self.assertIn("<redacted-", combined)
            # Expired record must NOT wipe the exemption inventory.
            self.assertNotIn("Configured exemptions: none", combined)
            self.assertIn("- [applied] docs/valid.txt rule=ipv4", combined)
            self.assertIn("- [expired] <redacted-", combined)

    def test_m6_cat_file_nonzero_exit_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            git(root, "init", "-q", "-b", "main")
            (root / "README.md").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: base")
            head = git(root, "rev-parse", "HEAD").stdout.strip()
            _, sources = scan.tree_sources(root, head)

            real_popen = scan.subprocess.Popen

            class FailingCatFile:
                def __init__(self, proc):
                    self._proc = proc
                    self.stdin = proc.stdin
                    self.stdout = proc.stdout

                def wait(self) -> int:
                    self._proc.wait()
                    return 128

                def kill(self) -> None:
                    self._proc.kill()

            def wrapped_popen(cmd, *args, **kwargs):
                proc = real_popen(cmd, *args, **kwargs)
                if "cat-file" in cmd:
                    return FailingCatFile(proc)
                return proc

            with mock.patch.object(scan.subprocess, "Popen", side_effect=wrapped_popen):
                with self.assertRaisesRegex(scan.ScanConfigError, "cat-file"):
                    list(sources)

    def test_m7_ci_macos_public_scan_avoids_isolated_unittest_and_checks_count(self) -> None:
        ci_yml = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertNotIn("python3 -I -B -m unittest", ci_yml)
        self.assertIn("unittest discover -s tests -p 'test_public_*.py'", ci_yml)
        self.assertIn("Ran [1-9]", ci_yml)


class Round2ReviewRegressionTests(unittest.TestCase):
    """Regression tests for PR #42 round 2 review findings (R1, R2, R3, R5, R6, R7)."""

    def _make_pre_push_repo(self, base: Path) -> tuple[Path, Path]:
        public_remote = base / "origin.git"
        repo = base / "work"
        subprocess.run(["git", "init", "--bare", "-q", str(public_remote)], check=True)
        git(base, "init", "-q", "-b", "main", str(repo))

        (repo / ".codeflow").mkdir()
        shutil.copyfile(ROOT / ".codeflow/policy.json", repo / ".codeflow/policy.json")
        shutil.copytree(ROOT / ".codeflow/git-hooks", repo / ".codeflow/git-hooks")
        (repo / "scripts").mkdir()
        shutil.copyfile(ROOT / "scripts/check_git_discipline.py", repo / "scripts/check_git_discipline.py")
        (repo / "tests").mkdir()
        shutil.copyfile(ROOT / "tests/check_public_release.py", repo / "tests/check_public_release.py")
        shutil.copyfile(ROOT / "tests/public_scan_identity.py", repo / "tests/public_scan_identity.py")
        shutil.copyfile(ROOT / "tests/public_scan_keyed.py", repo / "tests/public_scan_keyed.py")
        write_scan_configs(repo)
        git(repo, "config", "user.name", "Test Contributor")
        git(repo, "config", "user.email", "contributor@example.invalid")
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
        (repo / "Makefile").write_text(
            "ci-check:\n\t@python3 -I -B tests/check_public_release.py --allow-missing-license\n",
            encoding="utf-8",
        )
        git(repo, "add", "-A")
        git(repo, "commit", "-q", "-m", "chore: initial clean base")
        git(repo, "remote", "add", "origin", str(public_remote))
        git(repo, "push", "-q", "origin", "main")
        return repo, public_remote

    def test_r1_commit_on_private_remote_is_refused_when_pushed_to_public_remote(self) -> None:
        leak_email = join("jdoe", "@", "acme-corp", ".com")
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            repo, public_remote = self._make_pre_push_repo(base)
            private_remote = base / "private.git"
            subprocess.run(["git", "init", "--bare", "-q", str(private_remote)], check=True)
            git(repo, "remote", "add", "private", str(private_remote))

            # Commit a leak, fix the tip, and push ONLY to the private remote first
            # (without hooks so the private remote holds the leaking history).
            git(repo, "checkout", "-q", "-b", "feat/from-private")
            (repo / "notes.txt").write_text(leak_email + "\n", encoding="utf-8")
            git(repo, "add", "notes.txt")
            git(repo, "commit", "-q", "-m", "docs: leak on private")
            (repo / "notes.txt").write_text("alice@example.com\n", encoding="utf-8")
            git(repo, "add", "notes.txt")
            git(repo, "commit", "-q", "-m", "docs: clean tip")
            git(repo, "push", "-q", "private", "feat/from-private")

            # Enable hooks and push the new branch to the public remote (`origin`).
            git(repo, "config", "core.hooksPath", ".codeflow/git-hooks")
            res = git(repo, "push", "origin", "feat/from-private", check=False)
            combined = res.stdout + res.stderr
            self.assertNotEqual(0, res.returncode, combined)
            self.assertIn("notes.txt:1: matched rule 'email'", combined)
            self.assertNotIn(leak_email, combined)
            remote_refs = git(public_remote, "for-each-ref", "refs/heads/feat/from-private").stdout.strip()
            self.assertEqual("", remote_refs)

    def test_r2_tag_push_to_leaking_commit_on_no_branch_is_refused(self) -> None:
        leak_email = join("jdoe", "@", "acme-corp", ".com")
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            repo, public_remote = self._make_pre_push_repo(base)

            # Create a leaking commit on a temporary branch, tag it (annotated + lightweight),
            # then delete the branch and return to clean main.
            git(repo, "checkout", "-q", "-b", "feat/temp-tag")
            (repo / "secret_note.txt").write_text(leak_email + "\n", encoding="utf-8")
            git(repo, "add", "secret_note.txt")
            git(repo, "commit", "-q", "-m", "docs: commit for tag")
            git(repo, "tag", "-a", "v0.0.1-leak", "-m", "annotated leak tag")
            git(repo, "tag", "v0.0.1-light")
            git(repo, "checkout", "-q", "main")
            git(repo, "branch", "-D", "feat/temp-tag")

            git(repo, "config", "core.hooksPath", ".codeflow/git-hooks")
            for tag_name in ("v0.0.1-leak", "v0.0.1-light"):
                with self.subTest(tag=tag_name):
                    res = git(repo, "push", "origin", tag_name, check=False)
                    combined = res.stdout + res.stderr
                    self.assertNotEqual(0, res.returncode, combined)
                    self.assertIn("secret_note.txt:1: matched rule 'email'", combined)
                    self.assertNotIn(leak_email, combined)
                    remote_tag = git(public_remote, "for-each-ref", f"refs/tags/{tag_name}").stdout.strip()
                    self.assertEqual("", remote_tag)

    def test_r3_no_required_ci_job_runs_head_privacy_or_public_release_scan(self) -> None:
        # Structurally parse jobs and their run commands from .github/workflows/ci.yml.
        ci_lines = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8").splitlines()
        jobs: dict[str, list[str]] = {}
        in_jobs = False
        current_job: str | None = None
        in_run_block = False
        run_indent = 0
        for line in ci_lines:
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            indent = len(line) - len(line.lstrip(" "))
            if indent == 0:
                in_jobs = line.strip() == "jobs:"
                current_job = None
                in_run_block = False
                continue
            if not in_jobs:
                continue
            if indent == 2 and line.rstrip().endswith(":"):
                current_job = line.strip()[:-1]
                jobs[current_job] = []
                in_run_block = False
                continue
            if current_job is None:
                continue
            if in_run_block:
                if indent > run_indent:
                    jobs[current_job].append(line.strip())
                    continue
                in_run_block = False
            stripped = line.lstrip()
            if stripped.startswith("run:") or stripped.startswith("- run:"):
                after = stripped.split("run:", 1)[1].strip()
                if after in ("|", ">", "|-", ">-"):
                    in_run_block = True
                    run_indent = indent
                elif after:
                    jobs[current_job].append(after)

        # Structurally parse Makefile targets, prerequisites, and recipes, and
        # traverse transitively from every `make <target>` invoked by required jobs.
        makefile_lines = (ROOT / "Makefile").read_text(encoding="utf-8").splitlines()
        target_deps: dict[str, list[str]] = {}
        target_recipes: dict[str, list[str]] = {}
        cur_target: str | None = None
        for line in makefile_lines:
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            if line.startswith("\t"):
                if cur_target is not None:
                    target_recipes.setdefault(cur_target, []).append(line.strip())
                continue
            if ":" in line and not line.startswith("."):
                lhs, rhs = line.split(":", 1)
                lhs = lhs.strip()
                if " " in lhs or "=" in lhs:
                    cur_target = None
                    continue
                cur_target = lhs
                rhs_clean = rhs.strip()
                if "=" in rhs_clean:
                    continue
                target_deps.setdefault(cur_target, []).extend(rhs_clean.split())

        def transitive_recipes(root_target: str) -> tuple[set[str], str]:
            visited: set[str] = set()
            stack = [root_target]
            recipes: list[str] = []
            while stack:
                tgt = stack.pop()
                if tgt in visited:
                    continue
                visited.add(tgt)
                recipes.extend(target_recipes.get(tgt, []))
                for dep in target_deps.get(tgt, []):
                    if dep not in visited:
                        stack.append(dep)
            return visited, "\n".join(recipes)

        for required_job in ("repository-validation", "secret-scan", "macos-host-lock"):
            self.assertIn(required_job, jobs, f"missing expected job {required_job}")
            job_runs = "\n".join(jobs[required_job])
            for forbidden in (
                "check_public_release.py",
                "make public-check",
                "make public-draft-check",
                "make ci-check",
                "make check",
            ):
                self.assertNotIn(
                    forbidden,
                    job_runs,
                    f"required job {required_job!r} must not invoke head-controlled {forbidden!r}",
                )
            for run_line in jobs[required_job]:
                parts = run_line.split()
                if len(parts) >= 2 and parts[0] == "make":
                    target_name = parts[-1]
                    reached, all_recipes = transitive_recipes(target_name)
                    for forbidden_target in ("public-check", "public-draft-check", "unit-public-scan-check", "repo-check", "check", "ci-check"):
                        self.assertNotIn(
                            forbidden_target,
                            reached,
                            f"required job {required_job!r} transitively reaches {forbidden_target!r} via make {target_name}",
                        )
                    self.assertNotIn("check_public_release.py", all_recipes)
                    self.assertNotIn("test_public_release", all_recipes)
                    self.assertNotIn("test_public_scan", all_recipes)
                    for recipe in all_recipes.splitlines():
                        if "check_repository.py" in recipe:
                            self.assertIn(
                                "--skip-privacy-checks",
                                recipe,
                                f"required job {required_job!r} runs check_repository.py privacy checks",
                            )
                        if "unittest discover" in recipe:
                            self.assertNotIn(
                                "'test_*.py'",
                                recipe,
                                f"required job {required_job!r} runs test_public_*.py via wildcard unittest",
                            )

    def test_r5_phone_keys_with_prefix_or_suffix_and_e164(self) -> None:
        digits = join("415", "867", "5309")
        for hit_line in (
            f"home_phone = {digits}",
            f"work_phone: {digits}",
            f"MOBILE_PHONE={digits}",
            f'{{"contactPhone": "{digits}"}}',
            f"user_cell_number = {digits}",
            f"officeTel: {digits}",
            join("+", "1", "415", "867", "5309"),
        ):
            with self.subTest(hit=hit_line[:16]):
                self.assertEqual(["docs/sample.txt:1: matched rule 'phone'"], scan_line(hit_line).failures)

        for clean_line in (
            "timestamp = 1700000000",
            "order_id = 4158675309",
            "home_phone = 5555550100",
            "contactPhone: 15555550199",
        ):
            with self.subTest(clean=clean_line):
                self.assertEqual([], scan_line(clean_line).failures)

        checklist = (ROOT / "docs/13-public-release-checklist.md").read_text(encoding="utf-8").lower()
        self.assertIn("bare digit", checklist)

    def test_r6_mixed_depth_encoding_caught_at_intermediate_stage(self) -> None:
        # Stage 1 decodes the @ escape while the trailing dot escape is still %2e,
        # exposing the synthetic email before stage 2 turns %2e123 into an invalid .123 TLD.
        mixed = join("jdoe", "%40", "acme-corp", ".com", "%252e", "123")
        res = scan_line(mixed)
        self.assertEqual(["docs/sample.txt:1: matched rule 'email'"], res.failures)

        # 5-level nested encoding still fails closed.
        deep = "jdoe" + "%" + ("25" * 5) + "40" + "acme-corp" + ".com"
        res_deep = scan_line(deep)
        self.assertTrue(any("decode failed" in f for f in res_deep.failures), res_deep.failures)

        checklist = (ROOT / "docs/13-public-release-checklist.md").read_text(encoding="utf-8").lower()
        self.assertIn("4 levels", checklist)

    def test_r7_aggregator_behaviour_rejects_missing_marker_or_failed_platform(self) -> None:
        # Extract the exact aggregator `run:` script from .github/workflows/public-scan.yml
        # and execute it with controlled environment inputs.
        lines = (ROOT / ".github/workflows/public-scan.yml").read_text(encoding="utf-8").splitlines()
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
        self.assertIn("LINUX_VERIFIED", script)
        self.assertIn("MACOS_VERIFIED", script)

        cases = (
            ("both_verified_passes", {"LINUX_RESULT": "success", "LINUX_VERIFIED": "true", "LINUX_IDENTITY_VERIFIED": "true", "MACOS_RESULT": "success", "MACOS_VERIFIED": "true", "MACOS_IDENTITY_VERIFIED": "true"}, 0),
            ("missing_linux_marker_fails", {"LINUX_RESULT": "success", "LINUX_VERIFIED": "", "LINUX_IDENTITY_VERIFIED": "true", "MACOS_RESULT": "success", "MACOS_VERIFIED": "true", "MACOS_IDENTITY_VERIFIED": "true"}, 1),
            ("missing_macos_marker_fails", {"LINUX_RESULT": "success", "LINUX_VERIFIED": "true", "LINUX_IDENTITY_VERIFIED": "true", "MACOS_RESULT": "success", "MACOS_VERIFIED": "", "MACOS_IDENTITY_VERIFIED": "true"}, 1),
            ("linux_failed_fails", {"LINUX_RESULT": "failure", "LINUX_VERIFIED": "true", "LINUX_IDENTITY_VERIFIED": "true", "MACOS_RESULT": "success", "MACOS_VERIFIED": "true", "MACOS_IDENTITY_VERIFIED": "true"}, 1),
            ("macos_failed_with_marker_fails", {"LINUX_RESULT": "success", "LINUX_VERIFIED": "true", "LINUX_IDENTITY_VERIFIED": "true", "MACOS_RESULT": "failure", "MACOS_VERIFIED": "true", "MACOS_IDENTITY_VERIFIED": "true"}, 1),
            ("macos_skipped_fails", {"LINUX_RESULT": "success", "LINUX_VERIFIED": "true", "LINUX_IDENTITY_VERIFIED": "true", "MACOS_RESULT": "skipped", "MACOS_VERIFIED": "", "MACOS_IDENTITY_VERIFIED": ""}, 1),
        )
        for name, env_vars, expected_rc in cases:
            with self.subTest(case=name):
                proc = subprocess.run(
                    ["bash", "-c", script],
                    env={"PATH": os.environ.get("PATH", "/usr/bin:/bin"), **env_vars},
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(expected_rc, proc.returncode, proc.stdout + proc.stderr)

        # Also prove check_public_release.py emits PUBLIC_SCAN_VERIFIED=1 ONLY on clean pass.
        leak = join("jdoe", "@", "acme-corp", ".com")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_scan_configs(root)
            git(root, "init", "-q", "-b", "main")
            (root / "a.txt").write_text("clean\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: clean")
            clean_sha = git(root, "rev-parse", "HEAD").stdout.strip()
            (root / "a.txt").write_text(leak + "\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "chore: leak")
            bad_sha = git(root, "rev-parse", "HEAD").stdout.strip()

            rc_ok, out_ok, _ = run_main(["--root", str(root), "--tree", clean_sha])
            self.assertEqual(0, rc_ok)
            self.assertIn("PUBLIC_SCAN_VERIFIED=1", out_ok.splitlines())

            rc_bad, out_bad, _ = run_main(["--root", str(root), "--tree", bad_sha])
            self.assertEqual(1, rc_bad)
            self.assertNotIn("PUBLIC_SCAN_VERIFIED=1", out_bad)


class Round3ReviewRegressionTests(unittest.TestCase):
    """Regression tests for PR #42 round 3 review findings (S1-S5)."""

    def _make_pre_push_repo(self, base: Path) -> tuple[Path, Path]:
        return Round2ReviewRegressionTests()._make_pre_push_repo(base)

    def test_s1_pushurl_differing_from_fetch_url_scans_full_history(self) -> None:
        leak_email = join("jdoe", "@", "acme-corp", ".com")
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            repo, fetch_remote = self._make_pre_push_repo(base)
            push_remote = base / "public-push.git"
            subprocess.run(["git", "init", "--bare", "-q", str(push_remote)], check=True)
            # Push main to push_remote too so only the feature branch is new there.
            git(repo, "push", "-q", str(push_remote), "main")

            # Commit a leak, fix the tip, and push to the fetch remote (without hooks)
            # so refs/remotes/origin/feat/split-url already knows about the leak.
            git(repo, "checkout", "-q", "-b", "feat/split-url")
            (repo / "notes.txt").write_text(leak_email + "\n", encoding="utf-8")
            git(repo, "add", "notes.txt")
            git(repo, "commit", "-q", "-m", "docs: leak on fetch remote")
            (repo / "notes.txt").write_text("alice@example.com\n", encoding="utf-8")
            git(repo, "add", "notes.txt")
            git(repo, "commit", "-q", "-m", "docs: clean tip")
            git(repo, "push", "-q", "origin", "feat/split-url")

            # Now set remote.origin.pushurl to a DIFFERENT repository (push_remote)
            # and enable hooks; pushing to origin must NOT trust origin's fetch refs!
            git(repo, "remote", "set-url", "--push", "origin", str(push_remote))
            git(repo, "config", "core.hooksPath", ".codeflow/git-hooks")
            res = git(repo, "push", "origin", "feat/split-url", check=False)
            combined = res.stdout + res.stderr
            self.assertNotEqual(0, res.returncode, combined)
            self.assertIn("notes.txt:1: matched rule 'email'", combined)
            self.assertNotIn(leak_email, combined)
            pushed_refs = git(push_remote, "for-each-ref", "refs/heads/feat/split-url").stdout.strip()
            self.assertEqual("", pushed_refs)

    def test_s3_phone_labels_syntax_redos_and_non_hits(self) -> None:
        digits = join("415", "867", "5309")
        for hit_line in (
            f"phone number: {digits}",
            f"contact number: {digits}",
            f"phone.number = {digits}",
            f'phones: ["5555550100", "{digits}"]',
            f'phone => "{digits}"',
            f"phone\t{digits}",
            join("1", "-", "415", "-", "867", "-", "5309"),
            join("(", "415", ") ", "867", "-", "5309"),
            join("415", "\t", "867", "\t", "5309"),
            f"home_phone = {digits}",
            f'{{"contactPhone": "{digits}"}}',
        ):
            with self.subTest(hit=hit_line[:22]):
                self.assertEqual(["docs/sample.txt:1: matched rule 'phone'"], scan_line(hit_line).failures)

        for clean_line in (
            f"hotel_id = {digits}",
            f"intel_serial = {digits}",
            f"cancellation_id = {digits}",
            "phone number: 5555550100",
            "timestamp = 1700000000",
        ):
            with self.subTest(clean=clean_line[:22]):
                self.assertEqual([], scan_line(clean_line).failures)

        # ReDoS guard: 50,000-character identifier with separator reaches PHONE_KEY_RE and finishes under 50ms.
        long_ident = ("hotel_x" * 7200) + " = 1234567890"
        t0 = time.perf_counter()
        self.assertEqual([], scan.detect_phone(long_ident))
        elapsed_ms = (time.perf_counter() - t0) * 1000.0
        self.assertLess(elapsed_ms, 50.0, f"detect_phone took {elapsed_ms:.1f}ms on long identifier")

    def test_s5_commit_messages_and_annotated_tag_messages_are_scanned_and_redacted(self) -> None:
        leak_email = join("jdoe", "@", "acme-corp", ".com")
        leak_ip = ip("10", "44", "55", "66")

        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            repo, public_remote = self._make_pre_push_repo(base)
            base_sha = git(repo, "rev-parse", "HEAD").stdout.strip()

            # (a) Commit with clean files but leaking commit message -> refused in --range and pre-push.
            git(repo, "checkout", "-q", "-b", "feat/msg-leak")
            (repo / "clean.txt").write_text("alice@example.com\n", encoding="utf-8")
            git(repo, "add", "clean.txt")
            git(repo, "commit", "-q", "-m", "docs: add note", "-m", f"- contact {leak_email} at {leak_ip}")
            head_sha = git(repo, "rev-parse", "HEAD").stdout.strip()

            code, out, err = run_main(["--root", str(repo), "--range", base_sha, head_sha])
            combined_cli = out + err
            self.assertEqual(1, code, combined_cli)
            self.assertIn("matched rule 'email'", combined_cli)
            self.assertIn("matched rule 'ipv4'", combined_cli)
            self.assertNotIn(leak_email, combined_cli)
            self.assertNotIn(leak_ip, combined_cli)

            git(repo, "config", "core.hooksPath", ".codeflow/git-hooks")
            push_msg = git(repo, "push", "origin", "feat/msg-leak", check=False)
            combined_push = push_msg.stdout + push_msg.stderr
            self.assertNotEqual(0, push_msg.returncode, combined_push)
            self.assertIn("matched rule 'email'", combined_push)
            self.assertNotIn(leak_email, combined_push)
            self.assertNotIn(leak_ip, combined_push)
            self.assertEqual("", git(public_remote, "for-each-ref", "refs/heads/feat/msg-leak").stdout.strip())

            # (b) Annotated tag pointing to clean main commit, but tag message leaks -> refused in pre-push.
            git(repo, "checkout", "-q", "main")
            git(repo, "tag", "-a", "v0.0.2-tagmsg", "-m", f"Release notes for {leak_email}")
            push_tag = git(repo, "push", "origin", "v0.0.2-tagmsg", check=False)
            combined_tag = push_tag.stdout + push_tag.stderr
            self.assertNotEqual(0, push_tag.returncode, combined_tag)
            self.assertIn("matched rule 'email'", combined_tag)
            self.assertNotIn(leak_email, combined_tag)
            self.assertEqual("", git(public_remote, "for-each-ref", "refs/tags/v0.0.2-tagmsg").stdout.strip())


class Round4ReviewRegressionTests(unittest.TestCase):
    """Regression tests for PR #42 round 4 review findings (U1-U5)."""

    def _make_pre_push_repo(self, base: Path) -> tuple[Path, Path]:
        return Round2ReviewRegressionTests()._make_pre_push_repo(base)

    def test_u1_nested_annotated_tag_messages_are_scanned_and_redacted(self) -> None:
        leak_email = join("jdoe", "@", "acme-corp", ".com")
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            repo, public_remote = self._make_pre_push_repo(base)

            # Create an inner annotated tag whose message leaks, then an outer
            # annotated tag pointing to the inner tag with a clean message.
            git(repo, "tag", "-a", "v0.0.3-inner", "main", "-m", f"Inner release note for {leak_email}")
            git(repo, "tag", "-a", "v0.0.3-outer", "v0.0.3-inner", "-m", "Clean outer release tag")
            git(repo, "config", "core.hooksPath", ".codeflow/git-hooks")

            res = git(repo, "push", "origin", "v0.0.3-outer", check=False)
            combined = res.stdout + res.stderr
            self.assertNotEqual(0, res.returncode, combined)
            self.assertIn("matched rule 'email'", combined)
            self.assertNotIn(leak_email, combined)
            self.assertEqual(
                "",
                git(public_remote, "for-each-ref", "refs/tags/v0.0.3-outer").stdout.strip(),
            )

    def test_u2_phone_camelcase_trailing_space_and_yaml_block_list(self) -> None:
        digits = join("415", "867", "5309")
        dashed = join("415", "-", "867", "-", "5309")
        spaced = join("415", " ", "867", " ", "5309")
        for hit_line in (
            f"phoneNumber: {digits}",
            f'{{"phoneNumber": "{digits}"}}',
            f"homePhoneNumber = {digits}",
            f"phone {digits}   ",
            f"phone number {digits}   ",
            f"{spaced}   ",
        ):
            with self.subTest(hit=hit_line[:24]):
                self.assertEqual(
                    ["docs/sample.txt:1: matched rule 'phone'"],
                    scan_line(hit_line).failures,
                )

        # YAML block lists (both formatted and digits-only) must flag the item line.
        yaml_formatted = f"phones:\n  - {dashed}\n"
        self.assertEqual(
            ["docs/sample.txt:2: matched rule 'phone'"],
            scan_line(yaml_formatted).failures,
        )
        yaml_digits = f"phones:\n  - 5555550100\n  - {digits}\n"
        self.assertEqual(
            ["docs/sample.txt:3: matched rule 'phone'"],
            scan_line(yaml_digits).failures,
        )
        self.assertEqual([], scan_line("phones:\n  - 5555550100\n").failures)

        # Non-hits remain clean.
        for clean_line in (
            f"hotel_id = {digits}",
            f"intel_serial = {digits}",
            f"cancellation_id = {digits}",
        ):
            with self.subTest(clean=clean_line[:24]):
                self.assertEqual([], scan_line(clean_line).failures)

    def test_u3_phone_timing_guard_reaches_regex_and_stays_fast(self) -> None:
        # Verify the timing test input in test_s3 actually enters PHONE_KEY_RE.finditer
        # (does not short-circuit at the separator/context gate) and runs in < 50 ms.
        import inspect
        s3_source = inspect.getsource(
            Round3ReviewRegressionTests.test_s3_phone_labels_syntax_redos_and_non_hits
        )
        self.assertIn('("hotel_x" * 7200) + " = 1234567890"', s3_source)

        long_ident = ("hotel_x" * 7200) + " = 1234567890"
        calls = 0
        real_re = scan.PHONE_KEY_RE

        class CountingPattern:
            def finditer(self, text: str, *args):
                nonlocal calls
                calls += 1
                return real_re.finditer(text, *args)

        with mock.patch.object(scan, "PHONE_KEY_RE", CountingPattern()):
            t0 = time.perf_counter()
            self.assertEqual([], scan.detect_phone(long_ident))
            elapsed_ms = (time.perf_counter() - t0) * 1000.0
        self.assertEqual(1, calls, "timing input must actually reach PHONE_KEY_RE.finditer")
        self.assertLess(elapsed_ms, 50.0, f"detect_phone took {elapsed_ms:.1f}ms on long identifier")

    def test_u4_multiple_fetch_urls_scan_full_history(self) -> None:
        leak_email = join("jdoe", "@", "acme-corp", ".com")
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            repo, fetch_remote = self._make_pre_push_repo(base)
            second_remote = base / "public-second.git"
            subprocess.run(["git", "init", "--bare", "-q", str(second_remote)], check=True)
            git(repo, "push", "-q", str(second_remote), "main")

            # Push a leaking branch with a clean tip to the first fetch URL so
            # refs/remotes/origin/feat/multi-url knows the leak commit.
            git(repo, "checkout", "-q", "-b", "feat/multi-url")
            (repo / "notes.txt").write_text(leak_email + "\n", encoding="utf-8")
            git(repo, "add", "notes.txt")
            git(repo, "commit", "-q", "-m", "docs: leak on first url")
            (repo / "notes.txt").write_text("alice@example.com\n", encoding="utf-8")
            git(repo, "add", "notes.txt")
            git(repo, "commit", "-q", "-m", "docs: clean tip")
            git(repo, "push", "-q", "origin", "feat/multi-url")

            # Add a second remote.origin.url entry (git fetches from the first,
            # pushes to both); pre-push must NOT trust refs/remotes/origin/ when
            # multiple remote.origin.url entries exist.
            git(repo, "config", "--add", "remote.origin.url", str(second_remote))
            git(repo, "config", "core.hooksPath", ".codeflow/git-hooks")
            res = git(repo, "push", "origin", "feat/multi-url", check=False)
            combined = res.stdout + res.stderr
            self.assertNotEqual(0, res.returncode, combined)
            self.assertIn("notes.txt:1: matched rule 'email'", combined)
            self.assertNotIn(leak_email, combined)
            self.assertEqual(
                "",
                git(second_remote, "for-each-ref", "refs/heads/feat/multi-url").stdout.strip(),
            )

    def test_u5_makefile_unit_discovery_resists_shadowed_tests_package(self) -> None:
        makefile_lines = (ROOT / "Makefile").read_text(encoding="utf-8").splitlines()
        recipes: dict[str, list[str]] = {}
        cur: str | None = None
        for line in makefile_lines:
            if line.startswith("\t"):
                if cur is not None:
                    recipes.setdefault(cur, []).append(line.strip())
            elif ":" in line and not line.startswith("."):
                lhs = line.split(":", 1)[0].strip()
                cur = lhs if (" " not in lhs and "=" not in lhs) else None

        non_priv_cmd = next(
            (r for r in recipes.get("unit-non-privacy-check", []) if "$(PYTHON)" in r),
            "",
        )
        pub_cmd = next(
            (r for r in recipes.get("unit-public-scan-check", []) if "$(PYTHON)" in r),
            "",
        )
        self.assertTrue(non_priv_cmd, "unit-non-privacy-check Python recipe missing")
        self.assertTrue(pub_cmd, "unit-public-scan-check Python recipe missing")
        self.assertIn("-t tests", pub_cmd)

        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            poison = base / "site_packages_poison"
            (poison / "tests").mkdir(parents=True)
            (poison / "tests" / "__init__.py").write_text(
                'raise RuntimeError("SHADOWED_TESTS_PACKAGE")\n',
                encoding="utf-8",
            )

            checkout = base / "checkout"
            (checkout / "tests").mkdir(parents=True)
            (checkout / "tests" / "test_smoke.py").write_text(
                "import pathlib, unittest\n"
                "class Smoke(unittest.TestCase):\n"
                "    def test_from_checkout(self):\n"
                "        self.assertEqual(pathlib.Path(__file__).resolve().parent.name, 'tests')\n",
                encoding="utf-8",
            )
            (checkout / "tests" / "test_public_smoke.py").write_text(
                "import unittest\n"
                "class PubSmoke(unittest.TestCase):\n"
                "    def test_pub(self):\n"
                "        self.assertTrue(True)\n",
                encoding="utf-8",
            )

            env = os.environ.copy()
            env["PYTHONPATH"] = str(poison)

            shell_non_priv = non_priv_cmd.lstrip("@").replace("$(PYTHON)", sys.executable)
            r1 = subprocess.run(
                shell_non_priv,
                shell=True,
                cwd=checkout,
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertEqual(0, r1.returncode, r1.stdout + r1.stderr)
            self.assertIn("Ran 1 test", r1.stdout + r1.stderr)
            self.assertNotIn("SHADOWED_TESTS_PACKAGE", r1.stdout + r1.stderr)

            shell_pub = pub_cmd.lstrip("@").replace("$(PYTHON)", sys.executable)
            r2 = subprocess.run(
                shell_pub,
                shell=True,
                cwd=checkout,
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertEqual(0, r2.returncode, r2.stdout + r2.stderr)
            self.assertIn("Ran 1 test", r2.stdout + r2.stderr)
            self.assertNotIn("SHADOWED_TESTS_PACKAGE", r2.stdout + r2.stderr)


class Round5ReviewRegressionTests(unittest.TestCase):
    """Regression tests for PR #42 final review blocker (linear detect_phone + phone list label)."""

    def test_phone_list_label_is_caught(self) -> None:
        digits = join("415", "867", "5309")
        self.assertEqual(
            ["docs/sample.txt:1: matched rule 'phone'"],
            scan_line(f"phone list 1: {digits}").failures,
        )
        # Also verify YAML block list under multi-word phone label.
        self.assertEqual(
            ["docs/sample.txt:2: matched rule 'phone'"],
            scan_line(f"phone list 1:\n  - {digits}\n").failures,
        )

    def test_detect_phone_whitespace_padding_is_linear_time(self) -> None:
        # Test N=2000 before N=20000 so any regression fails fast on N=2000.
        for n in (2000, 20000):
            for label, payload in (
                (f"padded_colon_N{n}", "phone" + (" " * n) + ":" + (" " * n) + "1"),
                (f"padded_list_N{n}", "phone list 1:" + (" " * n)),
            ):
                t0 = time.perf_counter()
                hits = scan.detect_phone(payload)
                elapsed_ms = (time.perf_counter() - t0) * 1000.0
                self.assertEqual([], hits, label)
                self.assertLess(
                    elapsed_ms,
                    50.0,
                    f"detect_phone({label}) took {elapsed_ms:.1f}ms (expected < 50ms)",
                )


if __name__ == "__main__":
    unittest.main()
