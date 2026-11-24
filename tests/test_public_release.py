#!/usr/bin/env python3
"""Unit tests for public-release prerequisites, detectors, modes, and CODEOWNERS."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "check_public_release", ROOT / "tests/check_public_release.py"
)
assert SPEC and SPEC.loader
public_release = importlib.util.module_from_spec(SPEC)
sys.modules["check_public_release"] = public_release
SPEC.loader.exec_module(public_release)

REPO_SPEC = importlib.util.spec_from_file_location(
    "check_repository_for_public_tests", ROOT / "tests/check_repository.py"
)
assert REPO_SPEC and REPO_SPEC.loader
check_repository = importlib.util.module_from_spec(REPO_SPEC)
sys.modules["check_repository_for_public_tests"] = check_repository
REPO_SPEC.loader.exec_module(check_repository)


def ip(*octets: str) -> str:
    return ".".join(octets)


def join(*parts: str) -> str:
    return "".join(parts)


class PublicReleaseTests(unittest.TestCase):
    def make_repository(self, root: Path, *, version: str = "1.2.3") -> None:
        for name in public_release.REQUIRED:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.touch()
        (root / "VERSION").write_text(f"{version}\n", encoding="utf-8")
        (root / "CHANGELOG.md").write_text(
            f"# Changelog\n\n## [Unreleased]\n\nPlanned as release {version}.\n",
            encoding="utf-8",
        )
        (root / "README.md").write_text(f"Current version: {version}\n", encoding="utf-8")
        (root / "templates/private-fleet/kit.lock").write_text(f"{version}\n", encoding="utf-8")
        (root / ".github/dependabot.yml").write_text(
            "".join(public_release.DEPENDABOT_COMMIT_POLICY),
            encoding="utf-8",
        )
        (root / "tests/public_scan_modes.json").write_text(
            json.dumps({"schema_version": 1, "modes": dict(public_release.DEFAULT_MODES)}) + "\n",
            encoding="utf-8",
        )
        (root / "tests/public_scan_exemptions.json").write_text(
            json.dumps({"schema_version": 1, "exemptions": []}) + "\n",
            encoding="utf-8",
        )
        (root / "tests/public_scan_binary_exceptions.json").write_text(
            json.dumps({"schema_version": 1, "binary": []}) + "\n",
            encoding="utf-8",
        )

    def test_draft_allows_missing_license(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            self.assertEqual(
                [],
                public_release.deterministic_failures(
                    root, require_license=False, tracked=["README.md"]
                ),
            )

    def test_strict_check_requires_license(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            failures = public_release.deterministic_failures(
                root, require_license=True, tracked=["README.md"]
            )
            self.assertTrue(any("LICENSE is missing" in item for item in failures))

    def test_strict_check_rejects_conduct_contact_placeholder(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            (root / "LICENSE").write_text(
                (ROOT / "LICENSE").read_text(encoding="utf-8"),
                encoding="utf-8",
            )
            (root / "CODE_OF_CONDUCT.md").write_text(
                "Before publication, the owner must add a monitored private conduct-report channel.\n",
                encoding="utf-8",
            )
            failures = public_release.deterministic_failures(
                root, require_license=True, tracked=["README.md"]
            )
            self.assertIn(
                "CODE_OF_CONDUCT.md still has the private conduct-contact placeholder",
                failures,
            )

    def test_invalid_semver_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root, version="release-one")
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=["README.md"]
            )
            self.assertIn("VERSION is not valid Semantic Versioning", failures)

    def test_changelog_must_include_version(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            (root / "CHANGELOG.md").write_text("# Changelog\n", encoding="utf-8")
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=["README.md"]
            )
            self.assertIn(
                "CHANGELOG.md must start with a ## [Unreleased] section (Keep a Changelog)",
                failures,
            )
            self.assertIn(
                "CHANGELOG.md has neither a dated ## [VERSION] heading nor an [Unreleased] section planned as VERSION",
                failures,
            )

    def test_changelog_accepts_a_dated_release_heading_for_version(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            version = (root / "VERSION").read_text(encoding="utf-8").strip()
            (root / "CHANGELOG.md").write_text(
                f"# Changelog\n\n## [Unreleased]\n\n## [{version}] - 2026-11-20\n",
                encoding="utf-8",
            )
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=["README.md"]
            )
            self.assertFalse([failure for failure in failures if "CHANGELOG" in failure], failures)
            (root / "CHANGELOG.md").write_text(
                f"# Changelog\n\n## [{version}] - 2026-11-20\n",
                encoding="utf-8",
            )
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=["README.md"]
            )
            self.assertIn(
                "CHANGELOG.md must start with a ## [Unreleased] section (Keep a Changelog)",
                failures,
            )

    def test_private_or_generated_tracked_paths_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            failures = public_release.deterministic_failures(
                root,
                require_license=False,
                tracked=["config/profiles/node.local.toml", "reports/audit.txt"],
            )
            self.assertEqual(2, len(failures))

    def test_empty_tracked_listing_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=[]
            )
            self.assertIn("tracked file listing is missing or empty", failures)

    def test_dependabot_conventional_subject_policy_is_required(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            (root / ".github/dependabot.yml").write_text("version: 2\n", encoding="utf-8")
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=["README.md"]
            )
            self.assertIn(
                "Dependabot must emit conventional build(scope) commit subjects",
                failures,
            )

    def test_missing_git_is_reported_without_traceback(self) -> None:
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            public_release.subprocess,
            "run",
            side_effect=FileNotFoundError,
        ):
            tracked, warning = public_release.tracked_files(Path(directory))
            self.assertIsNone(tracked)
            self.assertEqual("Git is unavailable", warning)

    def test_non_worktree_is_reported_without_traceback(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            tracked, warning = public_release.tracked_files(Path(directory))
            self.assertIsNone(tracked)
            self.assertEqual("the directory is not a readable Git worktree", warning)


class DeterministicDetectorTests(unittest.TestCase):
    """AC-1: one test per detector with a hit, an allowed value, and a near-miss."""

    def assertRuleOutcome(
        self,
        rule_id: str,
        *,
        hit: str,
        allowed: str,
        near_miss: str,
    ) -> None:
        hit_res = public_release.scan_sources(
            [public_release.SourceFile("docs/sample.txt", f"{hit}\n".encode("utf-8"))]
        )
        self.assertTrue(
            any(f"matched rule '{rule_id}'" in f for f in hit_res.failures),
            (rule_id, "hit", hit_res.failures),
        )

        allowed_res = public_release.scan_sources(
            [public_release.SourceFile("docs/sample.txt", f"{allowed}\n".encode("utf-8"))]
        )
        self.assertEqual([], allowed_res.failures, (rule_id, "allowed", allowed_res.failures))

        near_res = public_release.scan_sources(
            [public_release.SourceFile("docs/sample.txt", f"{near_miss}\n".encode("utf-8"))]
        )
        self.assertTrue(
            any(f"matched rule '{rule_id}'" in f for f in near_res.failures),
            (rule_id, "near_miss", near_res.failures),
        )

    def test_email_detector(self) -> None:
        self.assertRuleOutcome(
            "email",
            hit="contact " + join("jdoe", "@", "acme-corp", ".com"),
            allowed="contact alice@example.com and ops@example.org and ci@example.net",
            near_miss="contact " + join("alice", "@", "myexample", ".com"),
        )
        # Subdomain and suffix near-misses must also fail (never substring matching).
        for variant in (
            join("alice", "@", "sub.example", ".com"),
            join("alice", "@", "example.com", ".evil.org"),
        ):
            res = public_release.scan_sources(
                [public_release.SourceFile("docs/sample.txt", f"{variant}\n".encode("utf-8"))]
            )
            self.assertTrue(any("matched rule 'email'" in f for f in res.failures))

    def test_phone_detector(self) -> None:
        self.assertRuleOutcome(
            "phone",
            hit="call " + join("+1", "-415-", "867-", "5309"),
            allowed="call +1-555-555-0100 or (555) 555-0199",
            near_miss="call " + join("+1", "-555-", "555-", "0200"),
        )

    def test_ipv4_detector_private_and_public(self) -> None:
        self.assertRuleOutcome(
            "ipv4",
            hit="priv " + ip("10", "24", "8", "9"),
            allowed="doc 192.0.2.10 and 198.51.100.42 and 203.0.113.7",
            near_miss="near " + ip("192", "0", "3", "10"),
        )
        # Public IPv4 outside documentation ranges is also blocked.
        pub = public_release.scan_sources(
            [public_release.SourceFile("docs/sample.txt", (ip("8", "8", "8", "8") + "\n").encode())]
        )
        self.assertEqual(["docs/sample.txt:1: matched rule 'ipv4'"], pub.failures)

    def test_ipv6_detector_private_and_public(self) -> None:
        self.assertRuleOutcome(
            "ipv6",
            hit="host " + join("fd12", ":", "3456", "::", "9"),
            allowed="doc 2001:db8::1 and loopback ::1",
            near_miss="near " + join("2001", ":", "db9", "::", "1"),
        )
        # Public IPv6 outside 2001:db8::/32 is also blocked.
        pub = public_release.scan_sources(
            [public_release.SourceFile("docs/sample.txt", (join("2606", ":4700:4700", "::", "1111") + "\n").encode())]
        )
        self.assertEqual(["docs/sample.txt:1: matched rule 'ipv6'"], pub.failures)

    def test_mac_address_detector(self) -> None:
        self.assertRuleOutcome(
            "mac-address",
            hit="nic " + join("aa:bb", ":cc:dd", ":ee:ff"),
            allowed="doc 00:00:5e:00:53:01",
            near_miss="near " + join("00:00:5e", ":00:54", ":01"),
        )

    def test_home_path_detector(self) -> None:
        self.assertRuleOutcome(
            "home-path",
            hit="dir " + join("/Users", "/jdoe", "/work"),
            allowed="dir " + join("/Users", "/alice", "/work") + " and " + join("/home", "/agent-01", "/src"),
            near_miss="dir " + join("/Users", "/alice2", "/work"),
        )

    def test_private_dns_detector(self) -> None:
        self.assertRuleOutcome(
            "private-dns",
            hit="host " + join("workbox", ".tail9a8b", ".ts.net"),
            allowed="host host-a.ts.net and host-a.internal and host-a.local",
            near_miss="host " + join("host-a", "-prod", ".ts.net"),
        )
        for suffix in (".internal", ".local"):
            res = public_release.scan_sources(
                [public_release.SourceFile("docs/sample.txt", (join("db01", ".corp", suffix) + "\n").encode())]
            )
            self.assertEqual(["docs/sample.txt:1: matched rule 'private-dns'"], res.failures)

    def test_cloud_id_detector(self) -> None:
        self.assertRuleOutcome(
            "cloud-id",
            hit="role " + join("arn:aws:iam::", "987654321098", ":role/ops"),
            allowed="role arn:aws:iam::123456789012:role/example and gcp_project_id = 'example-project'",
            near_miss="role " + join("arn:aws:iam::", "123456789013", ":role/example"),
        )
        gcp_hit = public_release.scan_sources(
            [public_release.SourceFile("docs/sample.txt", (join("gcp_project_id", " = '", "prod-analytics-42", "'\n")).encode())]
        )
        self.assertEqual(["docs/sample.txt:1: matched rule 'cloud-id'"], gcp_hit.failures)

    def test_chat_tracker_id_detector(self) -> None:
        self.assertRuleOutcome(
            "chat-tracker-id",
            hit="mention " + join("<@U012", "AB3CD>"),
            allowed="mention <@U00000000> and 000000:00000000-0000-0000-0000-000000000000",
            near_miss="mention " + join("<@U000", "00001>"),
        )
        jira_hit = public_release.scan_sources(
            [
                public_release.SourceFile(
                    "docs/sample.txt",
                    (join("557058", ":", "11111111-2222-3333-4444-555555555555") + "\n").encode(),
                )
            ]
        )
        self.assertEqual(["docs/sample.txt:1: matched rule 'chat-tracker-id'"], jira_hit.failures)

    def test_secret_detector(self) -> None:
        self.assertRuleOutcome(
            "secret",
            hit="key=" + join("AKIA", "9999888877776666"),
            allowed="key=" + join("AKIA", "IOSFODNN7EXAMPLE"),
            near_miss="key=" + join("AKIA", "IOSFODNN7EXAMPL1"),
        )
        token_hit = public_release.scan_sources(
            [public_release.SourceFile("docs/sample.txt", (join("glpat-", "abcdef1234567890abcd") + "\n").encode())]
        )
        self.assertEqual(["docs/sample.txt:1: matched rule 'secret'"], token_hit.failures)


class DetectorModeTests(unittest.TestCase):
    """AC-8: block, warn, and off modes plus mandatory non-default mode reporting."""

    def test_block_warn_and_off_modes_behave_and_report_non_default(self) -> None:
        sample = join("jdoe", "@", "acme-corp", ".com") + "\n"
        source = [public_release.SourceFile("docs/sample.txt", sample.encode("utf-8"))]

        # block (default)
        block_res = public_release.scan_sources(source)
        self.assertEqual(["docs/sample.txt:1: matched rule 'email'"], block_res.failures)
        self.assertEqual([], block_res.warnings)

        # warn
        warn_modes = dict(public_release.DEFAULT_MODES)
        warn_modes["email"] = "warn"
        warn_res = public_release.scan_sources(source, modes=warn_modes)
        self.assertEqual([], warn_res.failures)
        self.assertEqual(["docs/sample.txt:1: matched rule 'email'"], warn_res.warnings)

        # off
        off_modes = dict(public_release.DEFAULT_MODES)
        off_modes["email"] = "off"
        off_res = public_release.scan_sources(source, modes=off_modes)
        self.assertEqual([], off_res.failures)
        self.assertEqual([], off_res.warnings)

        # _report prints every non-default mode on every run so off is never silent.
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = public_release._report(
                failures=off_res.failures,
                warnings=off_res.warnings,
                modes=off_modes,
                exemptions=(),
                applied_exemptions=frozenset(),
                passed_message="passed",
            )
        self.assertEqual(0, code)
        self.assertIn("Non-default detector modes:", out.getvalue())
        self.assertIn("- email: off", out.getvalue())

        warn_out = io.StringIO()
        with contextlib.redirect_stdout(warn_out):
            code = public_release._report(
                failures=warn_res.failures,
                warnings=warn_res.warnings,
                modes=warn_modes,
                exemptions=(),
                applied_exemptions=frozenset(),
                passed_message="passed",
            )
        self.assertEqual(0, code)
        self.assertIn("- email: warn", warn_out.getvalue())
        self.assertIn("docs/sample.txt:1: matched rule 'email'", warn_out.getvalue())
        self.assertNotIn("acme-corp", warn_out.getvalue())

    def test_invalid_mode_config_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "modes.json"
            bad_mode = dict(public_release.DEFAULT_MODES)
            bad_mode["email"] = "ignore"
            path.write_text(json.dumps({"schema_version": 1, "modes": bad_mode}), encoding="utf-8")
            with self.assertRaises(public_release.ScanConfigError):
                public_release.load_modes(path)

            missing = dict(public_release.DEFAULT_MODES)
            del missing["secret"]
            path.write_text(json.dumps({"schema_version": 1, "modes": missing}), encoding="utf-8")
            with self.assertRaises(public_release.ScanConfigError):
                public_release.load_modes(path)


class CodeownersCoverageTests(unittest.TestCase):
    """AC-11: repository check verifying .github/CODEOWNERS covers every required path."""

    def test_repository_codeowners_covers_every_required_path(self) -> None:
        self.assertEqual([], check_repository.codeowners_issues(ROOT))

    def test_missing_or_unowned_path_in_codeowners_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            # Missing CODEOWNERS fails.
            self.assertTrue(check_repository.codeowners_issues(root))

            codeowners = root / ".github/CODEOWNERS"
            codeowners.parent.mkdir(parents=True)
            # Partial coverage fails for missing paths.
            codeowners.write_text("/.github/ @owner\n", encoding="utf-8")
            issues = check_repository.codeowners_issues(root)
            self.assertTrue(any("tests/check_public_release.py" in i for i in issues), issues)
            self.assertTrue(any("tests/public_scan_identity.py" in i for i in issues), issues)
            self.assertTrue(any("tests/public_scan_keyed.py" in i for i in issues), issues)
            self.assertTrue(any("tests/public_scan_modes.json" in i for i in issues), issues)
            self.assertTrue(any("tests/public_scan_exemptions.json" in i for i in issues), issues)
            self.assertTrue(any("tests/public_scan_binary_exceptions.json" in i for i in issues), issues)

            # Last-match-wins unowned override fails.
            codeowners.write_text(
                "/.github/ @owner\n"
                "/tests/check_public_release.py @owner\n"
                "/tests/public_scan_identity.py @owner\n"
                "/tests/public_scan_keyed.py @owner\n"
                "/tests/public_scan_modes.json @owner\n"
                "/tests/public_scan_exemptions.json @owner\n"
                "/tests/public_scan_binary_exceptions.json @owner\n"
                "/.github/CODEOWNERS\n",
                encoding="utf-8",
            )
            issues = check_repository.codeowners_issues(root)
            self.assertTrue(any(".github/CODEOWNERS" in i for i in issues), issues)

            # Unowned override on identity helper fails.
            codeowners.write_text(
                "/.github/ @owner\n"
                "/tests/check_public_release.py @owner\n"
                "/tests/public_scan_identity.py @owner\n"
                "/tests/public_scan_keyed.py @owner\n"
                "/tests/public_scan_modes.json @owner\n"
                "/tests/public_scan_exemptions.json @owner\n"
                "/tests/public_scan_binary_exceptions.json @owner\n"
                "/tests/public_scan_identity.py\n",
                encoding="utf-8",
            )
            issues = check_repository.codeowners_issues(root)
            self.assertTrue(any("tests/public_scan_identity.py" in i for i in issues), issues)


if __name__ == "__main__":
    unittest.main()
