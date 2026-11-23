#!/usr/bin/env python3
"""Regression tests for the public-safety scan's security review fixes.

Every protected value here is synthetic and assembled from parts, and every
address is joined from its octets, so this file never holds one verbatim.
"""

from __future__ import annotations

import contextlib
import hashlib
import importlib.util
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("check_public_release_hardening", ROOT / "tests/check_public_release.py")
assert SPEC and SPEC.loader
scan = importlib.util.module_from_spec(SPEC)
sys.modules["check_public_release_hardening"] = scan
SPEC.loader.exec_module(scan)

KEY_HEX = "c3" * 32
KEY = bytes.fromhex(KEY_HEX)
PERSON = "alpha" + "person73"
FULL_NAME = "alpha" + "person" + " " + "beta"
NET = "privnet73" + ".internal"
APOSTROPHE_NAME = "dan" + "'" + "son73"


def ip(*octets: str) -> str:
    return ".".join(octets)


def rules(**extra: list[str]) -> object:
    person = [PERSON, FULL_NAME, APOSTROPHE_NAME, *extra.get("person", [])]
    net = [NET, *extra.get("net", [])]
    return scan.RuleSet(
        schema_version=1,
        algorithm="hmac-sha256",
        rules=(
            scan.KeyedRule("personal-name", frozenset(scan.compute_digest(KEY, "personal-name", v) for v in person)),
            scan.KeyedRule("private-network-name", frozenset(scan.compute_digest(KEY, "private-network-name", v) for v in net)),
        ),
    )


def scan_bytes(content: bytes, path: str = "docs/sample.txt", exceptions=scan.EMPTY_EXCEPTIONS) -> list[str]:
    return scan.scan_sources([scan.SourceFile(path, content)], rules=rules(), key=KEY, exceptions=exceptions)


def scan_line(text: str, path: str = "docs/sample.txt") -> list[str]:
    return scan_bytes(text.encode("utf-8"), path)


def git(root: Path, *args: str) -> str:
    env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid",
           "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.invalid"}
    return subprocess.run(["git", *args], cwd=root, check=True, capture_output=True, text=True, env=env).stdout.strip()


class PrivateAddressTests(unittest.TestCase):
    def assertFlagged(self, text: str, label: str) -> None:
        hits = scan.private_ipv4_hits(text)
        self.assertTrue(any(h[0] == label for h in hits), (text, hits))

    def assertClean(self, text: str) -> None:
        self.assertEqual([], scan.private_ipv4_hits(text), text)

    def test_range_boundaries(self) -> None:
        for inside, outside, label in (
            ((ip("10", "0", "0", "0"), ip("10", "255", "255", "255")), (ip("9", "255", "255", "255"), ip("11", "0", "0", "0")), "10/8"),
            ((ip("172", "16", "0", "0"), ip("172", "31", "255", "255")), (ip("172", "15", "255", "255"), ip("172", "32", "0", "0")), "172.16/12"),
            ((ip("192", "168", "0", "0"), ip("192", "168", "255", "255")), (ip("192", "167", "255", "255"), ip("192", "169", "0", "0")), "192.168/16"),
            ((ip("100", "64", "0", "0"), ip("100", "127", "255", "255")), (ip("100", "63", "255", "255"), ip("100", "128", "0", "0")), "100.64/10"),
            ((ip("169", "254", "0", "0"), ip("169", "254", "255", "255")), (ip("169", "253", "255", "255"), ip("169", "255", "0", "0")), "169.254/16"),
        ):
            for value in inside:
                with self.subTest(value=value):
                    self.assertFlagged(f"addr {value} end", label)
            for value in outside:
                with self.subTest(value=value):
                    self.assertClean(f"addr {value} end")

    def test_obfuscated_forms_are_canonicalised(self) -> None:
        cases = {
            "zero padded": "host " + ip("010", "001", "002", "003"),
            "letter glued before": "host" + ip("10", "1", "2", "3"),
            "letter glued after": ip("10", "1", "2", "3") + "abc",
            "fullwidth digits and dots": "１０．１．２．３",
            "zero-width inside": "1​0" + "." + ip("1", "2", "3"),
            "json escape for the dot": "10\\u002e1\\u002e2\\u002e3",
        }
        for name, text in cases.items():
            with self.subTest(case=name):
                self.assertFlagged(text, "10/8")
        self.assertEqual("10.1.2.3", scan.private_ipv4_hits(cases["zero padded"])[0][1])

    def test_partial_prefixes(self) -> None:
        self.assertFlagged("range " + ip("192", "168"), "192.168/16")
        self.assertFlagged("range " + ip("172", "20"), "172.16/12")
        self.assertFlagged("range " + ip("169", "254"), "169.254/16")
        self.assertFlagged("net " + ip("10", "20") + ".", "10/8")
        self.assertFlagged("net " + ip("10", "20", "30") + ".x", "10/8")
        self.assertFlagged("cidr " + ip("10", "20", "0") + "/16", "10/8")
        self.assertFlagged("net " + ip("100", "64", "1"), "100.64/10")
        # Common numbers are not addresses.
        for text in ("width " + ip("10", "5") + "rem", "ratio " + ip("100", "75"), "macOS " + ip("10", "15", "7"),
                     "version " + ip("1", "10", "0", "0", "1"), "big " + ip("10", "300", "1", "1")):
            with self.subTest(text=text):
                self.assertClean(text)

    def test_exceptions_are_exact_path_and_value(self) -> None:
        allowed = ip("100", "64", "0", "10")
        other = ip("100", "64", "0", "11")
        exceptions = scan.Exceptions(binary={}, private_ipv4={"tests/a.py": frozenset({allowed})})
        self.assertEqual([], scan_bytes(f"x = '{allowed}'".encode(), "tests/a.py", exceptions))
        self.assertTrue(scan_bytes(f"x = '{other}'".encode(), "tests/a.py", exceptions))
        self.assertTrue(scan_bytes(f"x = '{allowed}'".encode(), "tests/b.py", exceptions))

    def test_exception_file_is_strict(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "exceptions.json"
            good = {"schema_version": 1, "binary": [], "private_ipv4": [
                {"path": "a.py", "values": [ip("100", "64", "0", "10")], "reason": "synthetic"}]}
            path.write_text(json.dumps(good), encoding="utf-8")
            self.assertEqual(frozenset({ip("100", "64", "0", "10")}), scan.load_exceptions(path).private_ipv4["a.py"])
            for bad_values in ([ip("100", "064", "0", "10")], [ip("100", "64", "0", "999")], ["host"], []):
                with self.subTest(values=bad_values):
                    good["private_ipv4"][0]["values"] = bad_values
                    path.write_text(json.dumps(good), encoding="utf-8")
                    with self.assertRaises(scan.ScanConfigError):
                        scan.load_exceptions(path)


class NormalisationTests(unittest.TestCase):
    def assertMatches(self, text: str) -> None:
        findings = scan_line(text)
        self.assertTrue(any("matched keyed rule" in f for f in findings), (text, findings))

    def test_each_bypass_is_caught(self) -> None:
        cases = {
            "fullwidth separator": "host privnet73．internal",
            "zero-width inside a name": "owner alpha​person73",
            "soft hyphen inside a name": "owner alpha­person73",
            "json unicode escape": "owner alpha\\u0070erson73",
            "cyrillic lookalike": "owner аlphaperson73",
            "greek lookalike": "owner " + "alphaperson73".replace("o", "ο"),
            "curly apostrophe": "author Dan’son73",
            "apostrophe dropped": "author Danson73",
            "glued lowercase prefix": "xyz-alphaperson73-prod",
        }
        for name, text in cases.items():
            with self.subTest(case=name):
                self.assertMatches(text)

    def test_line_wraps_are_joined(self) -> None:
        hyphen = scan_line("owner alphaper-\nson73 continues")
        self.assertEqual(["docs/sample.txt:1: matched keyed rule 'personal-name' across a line wrap"], hyphen)
        words = scan_line("written by alphaperson\nbeta in 2026")
        self.assertEqual(["docs/sample.txt:1: matched keyed rule 'personal-name' across a line wrap"], words)
        self.assertEqual([], scan_line("unrelated\ntext"))

    def test_clean_text_does_not_match(self) -> None:
        self.assertEqual([], scan_line("acme lab workstation lab-ws-001.example.invalid"))


class DecodingTests(unittest.TestCase):
    def test_invalid_utf8_fails_the_file(self) -> None:
        findings = scan_bytes(b"clean \xff\xfe\xfd text")
        self.assertEqual(["tracked file is not valid UTF-8 or UTF-16 text: docs/sample.txt"], findings)

    def test_utf16_with_bom_is_scanned(self) -> None:
        content = ("owner " + PERSON + "\n").encode("utf-16")
        self.assertEqual(["docs/sample.txt:1: matched keyed rule 'personal-name'"], scan_bytes(content))

    def test_utf16_without_bom_is_scanned(self) -> None:
        content = ("owner " + PERSON + "\n").encode("utf-16-le")
        self.assertEqual(["docs/sample.txt:1: matched keyed rule 'personal-name'"], scan_bytes(content))

    def test_binary_fails_unless_its_exact_content_is_excepted(self) -> None:
        content = b"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + bytes(range(256))
        self.assertEqual(["tracked file is not valid UTF-8 or UTF-16 text: docs/logo.png"], scan_bytes(content, "docs/logo.png"))
        self.assertEqual(["tracked file is binary, not text: docs/blob.bin"], scan_bytes(b"ab\x00\x00\x00cd", "docs/blob.bin"))
        exceptions = scan.Exceptions(binary={"docs/logo.png": hashlib.sha256(content).hexdigest()}, private_ipv4={})
        self.assertEqual([], scan_bytes(content, "docs/logo.png", exceptions))
        changed = scan_bytes(content + b"\x00", "docs/logo.png", exceptions)
        self.assertEqual(["binary exception content changed; review and update its sha256: docs/logo.png"], changed)

    @unittest.skipIf(hasattr(os, "geteuid") and os.geteuid() == 0, "root can read any file")
    def test_unreadable_file_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "locked.txt"
            target.write_text("text\n", encoding="utf-8")
            target.chmod(0)
            try:
                findings = scan.scan_tracked_files(root, ["locked.txt"], rules=rules(), key=KEY)
            finally:
                target.chmod(0o600)
            self.assertEqual(["tracked file is unreadable: locked.txt"], findings)


class TreeModeTests(unittest.TestCase):
    def make_repo(self, root: Path) -> tuple[str, str]:
        git(root, "init", "-q", "-b", "main")
        (root / "README.md").write_text("clean\n", encoding="utf-8")
        git(root, "add", "-A")
        git(root, "commit", "-q", "-m", "base")
        base = git(root, "rev-parse", "HEAD")
        (root / "docs").mkdir()
        (root / "docs/notes.md").write_text("owner " + PERSON + "\n", encoding="utf-8")
        os.symlink("/nonexistent/" + NET, root / "docs/link")
        # A head that rewrites its own scanner must not change the result.
        (root / "tests").mkdir()
        (root / "tests/check_public_release.py").write_text("raise SystemExit(0)\n", encoding="utf-8")
        git(root, "add", "-A")
        git(root, "commit", "-q", "-m", "head")
        head = git(root, "rev-parse", "HEAD")
        git(root, "checkout", "-q", base)
        return base, head

    def test_tree_is_read_from_git_objects_not_the_worktree(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, head = self.make_repo(root)
            self.assertFalse((root / "docs/notes.md").exists())
            paths, sources = scan.tree_sources(root, head)
            self.assertIn("docs/notes.md", paths)
            findings = scan.scan_sources(sources, rules=rules(), key=KEY)
            self.assertIn("docs/notes.md:1: matched keyed rule 'personal-name'", findings)
            self.assertIn("docs/link:1: matched keyed rule 'private-network-name'", findings)

    def test_tree_needs_a_full_commit_sha(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, head = self.make_repo(root)
            for revision in ("main", "HEAD", head[:12], head.upper()):
                with self.subTest(revision=revision):
                    with self.assertRaises(scan.ScanConfigError):
                        scan.tree_sources(root, revision)

    def test_submodule_entry_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            base, _ = self.make_repo(root)
            git(root, "update-index", "--add", "--cacheinfo", f"160000,{base},vendor/mod")
            git(root, "commit", "-q", "-m", "gitlink")
            paths, sources = scan.tree_sources(root, git(root, "rev-parse", "HEAD"))
            problems = [s.problem for s in sources if s.path == "vendor/mod"]
            self.assertEqual(["tracked entry is not a file (submodule or other object)"], problems)


class DiagnosticLeakTests(unittest.TestCase):
    def run_main(self, argv: list[str], env: dict[str, str]) -> tuple[int, str]:
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, env, clear=True), contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = scan.main(argv)
        return code, out.getvalue() + err.getvalue()

    def assertNoValue(self, output: str) -> None:
        for line in output.splitlines():
            for value in (PERSON, FULL_NAME, NET, KEY_HEX, "alphaperson73"):
                self.assertNotIn(value, line.casefold())

    def test_path_findings_show_an_opaque_id(self) -> None:
        findings = scan_line("clean\n", path=f"people/{PERSON}/notes\x07.md")
        self.assertEqual(1, len(findings))
        self.assertTrue(findings[0].startswith("<redacted-"), findings)
        self.assertNotIn("people", findings[0])
        self.assertNoValue("\n".join(findings))
        address_path = scan_line("clean\n", path="hosts/" + ip("10", "1", "2", "3") + ".txt")
        self.assertTrue(address_path and all(ip("10", "1", "2", "3") not in f for f in address_path))

    def test_config_errors_carry_no_input_text(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            rules_path = Path(directory) / "rules.json"
            for text in (
                '{"schema_version": 1, "algorithm": "hmac-sha256", "%s": 1, "%s": 2}' % (PERSON, PERSON),
                '{"schema_version": 1, "algorithm": "hmac-sha256", "rules": [{"id": "%s", "digests": []}]}' % PERSON,
                '{"schema_version": 1, "algorithm": "hmac-sha256", "rules": [{"id": "personal-name", "digests": ["%s"]}]}' % NET,
                "not json " + PERSON,
            ):
                with self.subTest(text=text[:40]):
                    rules_path.write_text(text, encoding="utf-8")
                    with self.assertRaises(scan.ScanConfigError) as caught:
                        scan.load_rules(rules_path)
                    self.assertNoValue(str(caught.exception))

    def test_internal_errors_print_no_exception_text(self) -> None:
        with mock.patch.object(scan, "_run", side_effect=RuntimeError("boom " + PERSON + " " + KEY_HEX)):
            code, output = self.run_main(["--synthetic"], {})
        self.assertEqual(1, code)
        self.assertIn("internal error (RuntimeError)", output)
        self.assertNoValue(output)

    def test_main_output_on_a_tree_with_values_leaks_nothing(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for rel in ("tests/public_scan_rules.json", "tests/fixtures/public_scan/synthetic_key.txt",
                        "tests/fixtures/public_scan/synthetic_rules.json", "tests/fixtures/public_scan/clean_fixture.txt"):
                (root / rel).parent.mkdir(parents=True, exist_ok=True)
                (root / rel).write_text((ROOT / rel).read_text(encoding="utf-8"), encoding="utf-8")
            (root / "tests/public_scan_rules.json").write_text(json.dumps({
                "schema_version": 1, "algorithm": "hmac-sha256", "rules": [
                    {"id": "personal-name", "digests": [scan.compute_digest(KEY, "personal-name", PERSON)]},
                    {"id": "private-network-name", "digests": [scan.compute_digest(KEY, "private-network-name", NET)]}]}),
                encoding="utf-8")
            git(root, "init", "-q", "-b", "main")
            (root / PERSON).mkdir()
            (root / PERSON / "x.md").write_text(f"{PERSON}\n{NET}\n" + ip("192", "168", "1", "9") + "\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "c")
            head = git(root, "rev-parse", "HEAD")
            code, output = self.run_main(["--root", str(root), "--tree", head], {"PUBLIC_SCAN_HMAC_KEY": KEY_HEX})
            self.assertEqual(1, code, output)
            self.assertIn("matched keyed rule 'personal-name'", output)
            self.assertIn("private IPv4 address or prefix in " + ip("192", "168") + "/16", output)
            self.assertNoValue(output)
            self.assertNotIn(ip("192", "168", "1", "9"), output)


class KeyAndListingTests(unittest.TestCase):
    def test_key_must_be_64_lowercase_hex(self) -> None:
        for bad in ("short", "A1" * 32, "a1" * 31, "a1" * 33, "g1" * 32):
            with self.subTest(key=bad[:6]):
                with self.assertRaises(scan.ScanConfigError) as caught:
                    scan._parse_key_text(bad)
                self.assertNotIn(bad, str(caught.exception))
        self.assertEqual(KEY, scan._parse_key_text(KEY_HEX + "\n"))

    def test_unloadable_synthetic_fixture_fails_a_keyed_run(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / scan.SYNTHETIC_KEY_PATH
            fixture.parent.mkdir(parents=True)
            fixture.symlink_to(ROOT / scan.SYNTHETIC_KEY_PATH)
            with self.assertRaisesRegex(scan.ScanConfigError, "cannot verify the private key differs"):
                scan.resolve_key_material(root, tracked=None, environ={"PUBLIC_SCAN_HMAC_KEY": KEY_HEX})

    def test_non_utf8_tracked_path_gives_a_generic_error(self) -> None:
        listing = subprocess.CompletedProcess(args=[], returncode=0, stdout=b"ok.txt\0bad-\xff-" + PERSON.encode() + b"\0", stderr=b"")
        with mock.patch.object(scan.subprocess, "run", return_value=listing):
            tracked, error = scan.tracked_files(Path("."))
        self.assertIsNone(tracked)
        self.assertEqual("tracked file listing contains a path that is not valid UTF-8", error)

    def test_listing_keeps_carriage_returns_in_paths(self) -> None:
        listing = subprocess.CompletedProcess(args=[], returncode=0, stdout=b"a\rb.txt\0c.txt\0", stderr=b"")
        with mock.patch.object(scan.subprocess, "run", return_value=listing):
            tracked, error = scan.tracked_files(Path("."))
        self.assertEqual(["a\rb.txt", "c.txt"], tracked)
        self.assertIsNone(error)



class SecondReviewTests(unittest.TestCase):
    """Regression tests for the second security review of PR #24."""

    def test_utf8_bom_cannot_hide_nul_interleaved_text(self) -> None:
        hidden = b"\xef\xbb\xbf" + ("owner " + PERSON + "\n").encode("utf-16-le")
        self.assertEqual(["docs/sample.txt:1: matched keyed rule 'personal-name'"], scan_bytes(hidden))
        self.assertEqual(["tracked file is binary, not text: docs/sample.txt"], scan_bytes(b"\xef\xbb\xbfab\x00\x00\x00cd"))

    def test_a_value_spanning_path_components_hides_the_whole_path(self) -> None:
        path = "people/" + FULL_NAME.replace(" ", "/") + ".md"
        findings = scan_line("clean\n", path=path)
        self.assertEqual(1, len(findings), findings)
        self.assertTrue(findings[0].startswith("<redacted-"), findings)
        for part in path.split("/"):
            self.assertNotIn(part, findings[0])

    def test_unmatched_paths_cannot_forge_log_lines(self) -> None:
        findings = scan_bytes(b"\xff", "docs/evil\nFAKE: all checks passed\r\x1b[2K.md")
        self.assertEqual(1, len(findings))
        self.assertNotIn("\n", findings[0])
        self.assertNotIn("\r", findings[0])
        self.assertNotIn("\x1b", findings[0])
        self.assertIn("docs/evil\\x0aFAKE", findings[0])

    def test_punctuation_inside_a_name_is_deleted(self) -> None:
        for text in ("owner alpha!person73", "owner al.pha_person73", "by alpha!person be?ta today"):
            with self.subTest(text=text):
                self.assertTrue(any("matched keyed rule" in f for f in scan_line(text)), text)

    def test_combining_marks_are_removed(self) -> None:
        self.assertTrue(scan_line("owner a\u0301lphaperson73"))
        self.assertTrue(scan_line("owner alpha\u0308person73"))
        self.assertEqual("10/8", scan.private_ipv4_hits("host 10\u0301." + ip("1", "2", "3"))[0][0])

    def test_documented_address_policy_edges(self) -> None:
        # A bare three-group 10.x run is not flagged: it is how versions look.
        self.assertEqual([], scan.private_ipv4_hits("macOS " + ip("10", "15", "7")))
        # A four-part value inside a private range is flagged, version or not;
        # a real version that collides needs a reviewed exception.
        self.assertTrue(scan.private_ipv4_hits("tool " + ip("10", "2", "3", "4")))
        # A prefix with its continuation at the end of a line is flagged there.
        findings = scan_line("net " + ip("10", "20") + ".\n" + ip("30", "40") + " end")
        self.assertIn("docs/sample.txt:1: private IPv4 address or prefix in 10/8", findings)

    def test_shared_blobs_are_read_and_scanned_once(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            git(root, "init", "-q", "-b", "main")
            for index in range(40):
                (root / f"copy{index}.txt").write_text("owner " + PERSON + "\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "copies")
            head = git(root, "rev-parse", "HEAD")
            paths, sources = scan.tree_sources(root, head)
            scanner = scan.Scanner(rules=rules(), key=KEY)
            findings = [f for source in sources for f in scanner.scan_source(source)]
            self.assertEqual(40, len(paths))
            self.assertEqual(40, len(findings))
            self.assertEqual(1, len(scanner.object_findings))

    def test_tree_caps_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            git(root, "init", "-q", "-b", "main")
            for index in range(4):
                (root / f"f{index}.txt").write_text(f"distinct {index}\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "four")
            head = git(root, "rev-parse", "HEAD")
            with mock.patch.object(scan, "MAX_TREE_ENTRIES", 3):
                with self.assertRaisesRegex(scan.ScanConfigError, "more than 3 entries"):
                    scan.tree_sources(root, head)
            with mock.patch.object(scan, "MAX_TREE_BYTES", 10):
                with self.assertRaisesRegex(scan.ScanConfigError, "more than 10 bytes"):
                    scan.tree_sources(root, head)

    def test_candidate_cache_is_bounded(self) -> None:
        scanner = scan.Scanner(rules=rules(), key=KEY)
        with mock.patch.object(scan, "CANDIDATE_CACHE_BYTES", 300):
            for index in range(20):
                scanner.keyed_hits(scan.extract_candidates(f"line {index} with words {index * 7} more {index * 13}"))
                self.assertLessEqual(scanner.cache_bytes, 300)
                self.assertLessEqual(sum(len(k.encode()) for k in scanner.cache), 300)



class ThirdReviewTests(unittest.TestCase):
    """Regression tests for the third security review of PR #24."""

    def test_a_mark_between_words_does_not_join_them(self) -> None:
        self.assertTrue(scan_line("owner " + PERSON + "\u034fxyz"))
        self.assertTrue(scan_line("owner " + PERSON + "\u0301xyz"))

    def test_every_candidate_respects_the_length_bound(self) -> None:
        line = "a" * 300 + " " + "Bb" * 150 + " " + ".".join(["c" * 200] * 3) + " " + "-".join(["d" * 90] * 3)
        candidates = scan.extract_candidates(line)
        self.assertTrue(candidates)
        self.assertLessEqual(max(len(c) for c in candidates), scan.MAX_CANDIDATE_CHARS)

    def test_long_lines_are_not_cached(self) -> None:
        before = scan._cached_candidates.cache_info().currsize
        scan.extract_candidates("long " * (scan.CACHE_LINE_CHARS // 4))
        self.assertEqual(before, scan._cached_candidates.cache_info().currsize)

    def test_findings_are_capped_and_fail_closed(self) -> None:
        sources = [scan.SourceFile(f"docs/f{i}.txt", ("owner " + PERSON + "\n").encode()) for i in range(10)]
        with mock.patch.object(scan, "MAX_FINDINGS", 5):
            findings = scan.scan_sources(iter(sources), rules=rules(), key=KEY)
        self.assertEqual(6, len(findings))
        self.assertEqual("more than 5 findings; the scan stopped after 6 entries", findings[-1])

    def test_listing_bytes_are_capped_while_reading(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            git(root, "init", "-q", "-b", "main")
            for index in range(30):
                (root / f"file-with-a-longer-name-{index}.txt").write_text(f"{index}\n", encoding="utf-8")
            git(root, "add", "-A")
            git(root, "commit", "-q", "-m", "many")
            head = git(root, "rev-parse", "HEAD")
            with mock.patch.object(scan, "MAX_LISTING_BYTES", 500):
                with self.assertRaisesRegex(scan.ScanConfigError, "larger than 500 bytes"):
                    scan.tree_sources(root, head)
            with mock.patch.object(scan, "MAX_TREE_ENTRIES", 5):
                with self.assertRaisesRegex(scan.ScanConfigError, "more than 5 entries"):
                    scan.tree_sources(root, head)



class FourthReviewTests(unittest.TestCase):
    """Regression tests for the fourth security review of PR #24."""

    def test_address_overflow_fails_even_when_every_collected_value_is_allowed(self) -> None:
        allowed = ip("100", "64", "0", "10")
        unapproved = ip("192", "168", "7", "7")
        text = ("x = " + allowed + "\n") * (scan.MAX_FINDINGS + 1) + "y = " + unapproved + "\n"
        exceptions = scan.Exceptions(binary={}, private_ipv4={"tests/a.py": frozenset({allowed})})
        findings = scan_bytes(text.encode(), "tests/a.py", exceptions)
        self.assertEqual(
            [f"more than {scan.MAX_FINDINGS} private address matches; the scan cannot check the rest: tests/a.py"],
            findings,
        )

    def test_wrap_findings_count_toward_the_limit(self) -> None:
        text = "owner alphaper-\nson73 and more\n" * 50
        scanner = scan.Scanner(rules=rules(), key=KEY)
        with mock.patch.object(scan, "MAX_FINDINGS", 7):
            found = scanner.scan_lines(text)
        self.assertEqual(8, len(found))
        self.assertTrue(all("across a line wrap" in label for _, label in found))

    def test_an_escaped_mark_between_words_does_not_join_them(self) -> None:
        self.assertTrue(scan_line("owner " + PERSON + "\\u034fxyz"))
        self.assertTrue(scan_line("owner " + PERSON + "\\u0301xyz"))



class FifthReviewTests(unittest.TestCase):
    """Regression tests for the fifth security review of PR #24."""

    def test_a_format_character_between_words_does_not_join_them(self) -> None:
        for code in ("200b", "200d", "2060"):
            raw = chr(int(code, 16))
            for form in (raw, "\\u" + code):
                with self.subTest(char=code, escaped=form != raw):
                    self.assertTrue(scan_line("owner " + PERSON + form + "xyz"))

    def test_a_format_character_inside_a_name_still_joins_it(self) -> None:
        for code in ("200b", "200d", "2060"):
            raw = chr(int(code, 16))
            for form in (raw, "\\u" + code):
                with self.subTest(char=code, escaped=form != raw):
                    self.assertTrue(scan_line("owner alpha" + form + "person73"))


if __name__ == "__main__":
    unittest.main()
