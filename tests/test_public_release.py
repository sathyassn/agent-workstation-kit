#!/usr/bin/env python3

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
import re
import subprocess
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

# Test-only 64-hex HMAC keys (never real secrets).
TEST_PRIVATE_KEY = "a1" * 32
ALT_PRIVATE_KEY = "b2" * 32
# Split synthetic canaries so this test file never holds them verbatim.
SYNTH_PERSON = "canary" + "person9917"
SYNTH_FULL_NAME = "canary" + "person" + " " + "nine"
SYNTH_NET = "canary" + "net9917" + ".internal"


def write_private_key(path: Path, value: str = TEST_PRIVATE_KEY, mode: int = 0o600) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(f"{value}\n", encoding="utf-8")
    if os.name == "posix":
        os.chmod(path, mode)
    return path


def populate_rules(root: Path, key_hex: str = TEST_PRIVATE_KEY) -> None:
    """Give every production rule one digest of an unused test value."""
    key = bytes.fromhex(key_hex)
    (root / "tests/public_scan_rules.json").write_text(
        json.dumps(
            {
                "schema_version": 1,
                "algorithm": "hmac-sha256",
                "rules": [
                    {"id": "personal-name", "digests": [public_release.compute_digest(key, "personal-name", "unused-test-person")]},
                    {"id": "private-network-name", "digests": [public_release.compute_digest(key, "private-network-name", "unused-test.internal")]},
                ],
            }
        ),
        encoding="utf-8",
    )


class PublicReleaseTests(unittest.TestCase):
    def make_repository(
        self,
        root: Path,
        *,
        version: str = "1.2.3",
        include_license: bool = False,
        include_scan_assets: bool = True,
    ) -> list[str]:
        tracked: list[str] = []
        for name in public_release.REQUIRED:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.touch()
            tracked.append(name)
        (root / "VERSION").write_text(f"{version}\n", encoding="utf-8")
        (root / "CHANGELOG.md").write_text(
            f"# Changelog\n\n## [Unreleased]\n\nPlanned as release {version}.\n",
            encoding="utf-8",
        )
        (root / "README.md").write_text(f"Current version: {version}\n", encoding="utf-8")
        (root / "templates/private-fleet/kit.lock").write_text(
            f"{version}\n", encoding="utf-8"
        )
        (root / ".github/dependabot.yml").write_text(
            "".join(public_release.DEPENDABOT_COMMIT_POLICY),
            encoding="utf-8",
        )
        if include_license:
            (root / "LICENSE").write_text(
                (ROOT / "LICENSE").read_text(encoding="utf-8"),
                encoding="utf-8",
            )
            tracked.append("LICENSE")
        if include_scan_assets:
            for rel in (
                "tests/public_scan_rules.json",
                "tests/fixtures/public_scan/synthetic_key.txt",
                "tests/fixtures/public_scan/synthetic_rules.json",
                "tests/fixtures/public_scan/clean_fixture.txt",
            ):
                dst = root / rel
                dst.parent.mkdir(parents=True, exist_ok=True)
                dst.write_text((ROOT / rel).read_text(encoding="utf-8"), encoding="utf-8")
                tracked.append(rel)
        return tracked

    def run_main(
        self,
        argv: list[str],
        *,
        tracked: list[str] | None,
        git_error: str | None = None,
        env: dict[str, str] | None = None,
    ) -> tuple[int, str]:
        stdout = io.StringIO()
        clean_env = {} if env is None else dict(env)
        with (
            mock.patch.object(
                public_release,
                "tracked_files",
                return_value=(tracked, git_error),
            ),
            mock.patch.dict(os.environ, clean_env, clear=True),
            contextlib.redirect_stdout(stdout),
        ):
            code = public_release.main(argv)
        return code, stdout.getvalue()

    def test_draft_allows_missing_license(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root)
            self.assertEqual(
                [],
                public_release.deterministic_failures(
                    root, require_license=False, tracked=tracked
                ),
            )

    def test_strict_check_requires_license(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root)
            failures = public_release.deterministic_failures(
                root, require_license=True, tracked=tracked
            )
            self.assertTrue(any("LICENSE is missing" in item for item in failures))

    def test_strict_check_rejects_conduct_contact_placeholder(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, include_license=True)
            (root / "CODE_OF_CONDUCT.md").write_text(
                "Before publication, the owner must add a monitored private conduct-report channel.\n",
                encoding="utf-8",
            )
            failures = public_release.deterministic_failures(
                root, require_license=True, tracked=tracked
            )
            self.assertIn(
                "CODE_OF_CONDUCT.md still has the private conduct-contact placeholder",
                failures,
            )

    def test_invalid_semver_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, version="release-one")
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=tracked
            )
            self.assertIn("VERSION is not valid Semantic Versioning", failures)

    def test_changelog_must_include_version(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root)
            (root / "CHANGELOG.md").write_text("# Changelog\n", encoding="utf-8")
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=tracked
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
            tracked = self.make_repository(root)
            version = (root / "VERSION").read_text(encoding="utf-8").strip()
            (root / "CHANGELOG.md").write_text(
                f"# Changelog\n\n## [Unreleased]\n\n## [{version}] - 2026-11-20\n",
                encoding="utf-8",
            )
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=tracked
            )
            self.assertFalse(
                [failure for failure in failures if "CHANGELOG" in failure], failures
            )
            (root / "CHANGELOG.md").write_text(
                f"# Changelog\n\n## [{version}] - 2026-11-20\n", encoding="utf-8"
            )
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=tracked
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
                tracked=[
                    "config/profiles/node.local.toml",
                    "reports/audit.txt",
                    ".secrets/public-scan-hmac",
                ],
            )
            self.assertEqual(3, len(failures))

    def test_dependabot_conventional_subject_policy_is_required(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root)
            (root / ".github/dependabot.yml").write_text(
                "version: 2\n", encoding="utf-8"
            )
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=tracked
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
            self.assertEqual(
                "the directory is not a readable Git worktree",
                warning,
            )

    def test_empty_file_listing_fails_closed(self) -> None:
        empty_proc = subprocess.CompletedProcess(
            args=["git", "ls-files", "-z"], returncode=0, stdout=b"", stderr=b""
        )
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            public_release.subprocess,
            "run",
            return_value=empty_proc,
        ):
            tracked, error = public_release.tracked_files(Path(directory))
            self.assertIsNone(tracked)
            self.assertEqual("tracked file listing is empty", error)

    def test_deterministic_failures_rejects_empty_or_none_tracked(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            for tracked in (None, []):
                with self.subTest(tracked=tracked):
                    failures = public_release.deterministic_failures(
                        root, require_license=False, tracked=tracked
                    )
                    self.assertTrue(
                        any("tracked file listing" in item for item in failures),
                        failures,
                    )

    def test_failed_or_empty_file_listing_fails_main_even_in_draft_or_synthetic_mode(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root, include_license=True)
            cases = [
                (["--root", str(root)], {"PUBLIC_SCAN_HMAC_KEY": TEST_PRIVATE_KEY}, None, "Git is unavailable"),
                (["--root", str(root), "--allow-missing-license"], {"PUBLIC_SCAN_HMAC_KEY": TEST_PRIVATE_KEY}, None, "the directory is not a readable Git worktree"),
                (["--root", str(root), "--synthetic"], {}, None, "tracked file listing is empty"),
                (["--root", str(root), "--allow-missing-license", "--synthetic"], {}, [], None),
            ]
            for argv, env, tracked, git_error in cases:
                with self.subTest(argv=argv, git_error=git_error):
                    code, output = self.run_main(
                        argv, tracked=tracked, git_error=git_error, env=env
                    )
                    self.assertEqual(1, code, output)
                    self.assertIn("tracked file listing", output)

    def test_missing_or_empty_key_fails_closed_even_in_draft_mode(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, include_license=True)
            cases = [
                (["--root", str(root)], {"GITHUB_ACTIONS": "true"}),
                (["--root", str(root), "--allow-missing-license"], {"GITHUB_ACTIONS": "true"}),
                (["--root", str(root)], {"GITHUB_ACTIONS": "true", "PUBLIC_SCAN_HMAC_KEY": ""}),
                (["--root", str(root)], {"PUBLIC_SCAN_HMAC_KEY": ""}),
                (["--root", str(root)], {"PUBLIC_SCAN_HMAC_KEY": "   \n"}),
                (["--root", str(root)], {"PUBLIC_SCAN_HMAC_KEY": "too-short"}),
                (["--root", str(root)], {"PUBLIC_SCAN_HMAC_KEY_FILE": ""}),
                (["--root", str(root)], {"PUBLIC_SCAN_SYNTHETIC": "0"}),
                (["--root", str(root)], {"PUBLIC_SCAN_SYNTHETIC": "true"}),
            ]
            for argv, env in cases:
                with self.subTest(argv=argv, env=env):
                    code, output = self.run_main(argv, tracked=tracked, env=env)
                    self.assertEqual(1, code, output)
                    self.assertNotIn(
                        "keyed private-name rules not checked locally; CI runs them",
                        output,
                    )

    def test_local_run_without_key_warns_runs_synthetic_and_exits_zero(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, include_license=True)
            for argv in (
                ["--root", str(root)],
                ["--root", str(root), "--allow-missing-license"],
            ):
                with self.subTest(argv=argv):
                    code, output = self.run_main(argv, tracked=tracked, env={})
                    self.assertEqual(0, code, output)
                    self.assertIn(
                        "keyed private-name rules not checked locally; CI runs them",
                        output,
                    )

            # Local fallback must still enforce synthetic scan matches on tracked files.
            canary_doc = root / "docs/canary-leak.md"
            canary_doc.parent.mkdir(parents=True, exist_ok=True)
            canary_doc.write_text(f"owner = {SYNTH_PERSON}\n", encoding="utf-8")
            code, output = self.run_main(
                ["--root", str(root)],
                tracked=[*tracked, "docs/canary-leak.md"],
                env={},
            )
            self.assertEqual(1, code, output)
            self.assertIn("docs/canary-leak.md:1: matched keyed rule 'personal-name'", output)
            canary_doc.unlink()

            # Local fallback must still reject a corrupted production rule file.
            (root / "tests/public_scan_rules.json").write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "digests": ["plaintext-name"]},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
                encoding="utf-8",
            )
            code, output = self.run_main(["--root", str(root)], tracked=tracked, env={})
            self.assertEqual(1, code, output)
            self.assertNotIn("plaintext-name", output)

            # Hash generation mode must never silently fall back to the public synthetic key.
            stdout = io.StringIO()
            stdin = io.StringIO(f"{SYNTH_PERSON}\n")
            with (
                mock.patch.object(sys, "stdin", stdin),
                mock.patch.dict(os.environ, {}, clear=True),
                contextlib.redirect_stdout(stdout),
            ):
                hash_code = public_release.main(
                    ["--root", str(root), "--hash-rule", "personal-name"]
                )
            self.assertEqual(1, hash_code, stdout.getvalue())

    def test_github_actions_same_repo_without_key_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, include_license=True)
            cases = [
                (["--root", str(root)], {"GITHUB_ACTIONS": "true"}),
                (["--root", str(root), "--allow-missing-license"], {"GITHUB_ACTIONS": "true"}),
                (
                    ["--root", str(root)],
                    {"GITHUB_ACTIONS": "true", "PUBLIC_SCAN_HMAC_KEY": ""},
                ),
                (
                    ["--root", str(root)],
                    {"GITHUB_ACTIONS": "1"},
                ),
            ]
            for argv, env in cases:
                with self.subTest(argv=argv, env=env):
                    code, output = self.run_main(argv, tracked=tracked, env=env)
                    self.assertEqual(1, code, output)
                    self.assertNotIn(
                        "keyed private-name rules not checked locally; CI runs them",
                        output,
                    )

    def test_github_actions_fork_synthetic_exits_zero(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, include_license=True)
            code, output = self.run_main(
                ["--root", str(root)],
                tracked=tracked,
                env={"GITHUB_ACTIONS": "true", "PUBLIC_SCAN_SYNTHETIC": "1"},
            )
            self.assertEqual(0, code, output)
            self.assertIn("synthetic", output.lower())
            self.assertIn("public-scan workflow", output.lower())
            self.assertNotIn(
                "keyed private-name rules not checked locally; CI runs them",
                output,
            )

    def test_keyed_scan_fails_when_any_production_rule_has_no_digests(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, include_license=True)
            for env in (
                {"PUBLIC_SCAN_HMAC_KEY": TEST_PRIVATE_KEY},
                {"GITHUB_ACTIONS": "true", "PUBLIC_SCAN_HMAC_KEY": TEST_PRIVATE_KEY},
            ):
                with self.subTest(env=env):
                    code, output = self.run_main(["--root", str(root)], tracked=tracked, env=env)
                    self.assertEqual(1, code, output)
                    self.assertIn("no keyed rules populated for: personal-name, private-network-name", output)

            # One populated rule is still a failure naming only the empty one.
            key = bytes.fromhex(TEST_PRIVATE_KEY)
            person_digest = public_release.compute_digest(key, "personal-name", "populated-only-test-person")
            (root / "tests/public_scan_rules.json").write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "digests": [person_digest]},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
                encoding="utf-8",
            )
            code, output = self.run_main(
                ["--root", str(root)], tracked=tracked, env={"PUBLIC_SCAN_HMAC_KEY": TEST_PRIVATE_KEY}
            )
            self.assertEqual(1, code, output)
            self.assertIn("no keyed rules populated for: private-network-name;", output)

            # Every rule populated passes.
            populate_rules(root)
            code, output = self.run_main(
                ["--root", str(root)], tracked=tracked, env={"PUBLIC_SCAN_HMAC_KEY": TEST_PRIVATE_KEY}
            )
            self.assertEqual(0, code, output)

            # Synthetic contributor runs keep the empty-production allowance.
            (root / "tests/public_scan_rules.json").write_text(
                (ROOT / "tests/public_scan_rules.json").read_text(encoding="utf-8"), encoding="utf-8"
            )
            code, output = self.run_main(
                ["--root", str(root)], tracked=tracked, env={"PUBLIC_SCAN_SYNTHETIC": "1"}
            )
            self.assertEqual(0, code, output)

    def test_synthetic_key_refused_in_private_mode_and_ambiguous_sources_refused(self) -> None:
        synthetic_key = (
            ROOT / "tests/fixtures/public_scan/synthetic_key.txt"
        ).read_text(encoding="utf-8").strip()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, include_license=True)
            key_file = write_private_key(Path(directory) / "ext-key")
            cases = [
                (["--root", str(root)], {"PUBLIC_SCAN_HMAC_KEY": synthetic_key}),
                (["--root", str(root), "--synthetic"], {"PUBLIC_SCAN_HMAC_KEY": TEST_PRIVATE_KEY}),
                (["--root", str(root)], {"PUBLIC_SCAN_SYNTHETIC": "1", "PUBLIC_SCAN_HMAC_KEY": TEST_PRIVATE_KEY}),
                (["--root", str(root), "--synthetic", "--key-file", str(key_file)], {}),
                (["--root", str(root), "--key-file", str(key_file)], {"PUBLIC_SCAN_HMAC_KEY": TEST_PRIVATE_KEY}),
            ]
            for argv, env in cases:
                with self.subTest(argv=argv, env=env):
                    code, output = self.run_main(argv, tracked=tracked, env=env)
                    self.assertEqual(1, code, output)
                    self.assertNotIn(TEST_PRIVATE_KEY, output)

    def test_local_untracked_key_file_permissions_symlink_and_tracking_guards(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, include_license=True)
            key_path = write_private_key(root / ".secrets/public-scan-hmac", mode=0o600)
            populate_rules(root)

            code, output = self.run_main(["--root", str(root)], tracked=tracked)
            self.assertEqual(0, code, output)

            # Tracked key file must fail closed.
            code, output = self.run_main(
                ["--root", str(root)],
                tracked=[*tracked, ".secrets/public-scan-hmac"],
            )
            self.assertEqual(1, code, output)
            self.assertIn("tracked", output.lower())

            # Broad permissions on POSIX must fail closed.
            if os.name == "posix":
                os.chmod(key_path, 0o644)
                code, output = self.run_main(["--root", str(root)], tracked=tracked)
                self.assertEqual(1, code, output)
                self.assertIn("permissions", output.lower())

            # Symlinked key file must fail closed.
            real_target = write_private_key(Path(directory) / "outside-key", mode=0o600)
            key_path.unlink()
            key_path.symlink_to(real_target)
            code, output = self.run_main(["--root", str(root)], tracked=tracked)
            self.assertEqual(1, code, output)
            self.assertIn("symlink", output.lower())

            # Oversized key file must fail closed.
            key_path.unlink()
            write_private_key(key_path, value="0" * 5000, mode=0o600)
            code, output = self.run_main(["--root", str(root)], tracked=tracked)
            self.assertEqual(1, code, output)
            self.assertIn("maximum size", output.lower())

    def test_synthetic_mode_runs_fixtures_and_validates_production_rules(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tracked = self.make_repository(root, include_license=True)
            code, output = self.run_main(
                ["--root", str(root)],
                tracked=tracked,
                env={"PUBLIC_SCAN_SYNTHETIC": "1"},
            )
            self.assertEqual(0, code, output)
            self.assertIn("synthetic", output.lower())
            self.assertIn("public-scan workflow", output.lower())

            # Corrupting the production rule file with plaintext must fail even in synthetic mode.
            (root / "tests/public_scan_rules.json").write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "digests": ["plaintext-name"]},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
                encoding="utf-8",
            )
            code, output = self.run_main(
                ["--root", str(root), "--synthetic"],
                tracked=tracked,
            )
            self.assertEqual(1, code, output)
            self.assertNotIn("plaintext-name", output)

    def test_keyed_matching_catches_personal_and_network_names_quietly(self) -> None:
        key = bytes.fromhex(TEST_PRIVATE_KEY)
        person_single = "alpha" + "person73"
        person_full = "alpha" + "person" + " " + "beta"
        net_domain = "privnet73" + ".internal"
        net_hyphen = "priv" + "-tail73"

        rules = public_release.RuleSet(
            schema_version=1,
            algorithm="hmac-sha256",
            rules=(
                public_release.KeyedRule(
                    rule_id="personal-name",
                    digests=frozenset(
                        {
                            public_release.compute_digest(key, "personal-name", person_single),
                            public_release.compute_digest(key, "personal-name", person_full),
                        }
                    ),
                ),
                public_release.KeyedRule(
                    rule_id="private-network-name",
                    digests=frozenset(
                        {
                            public_release.compute_digest(key, "private-network-name", net_domain),
                            public_release.compute_digest(key, "private-network-name", net_hyphen),
                        }
                    ),
                ),
            ),
        )

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            doc = root / "docs/sample.md"
            doc.parent.mkdir(parents=True, exist_ok=True)
            doc.write_text(
                "\n".join(
                    [
                        "line one is clean acme lab text",
                        f"uppercase match: {person_single.upper()}-ws",
                        f"two word name: {person_full.title()}",
                        f"camel name: AlphaPerson73",
                        f"url host: https://node-01.{net_domain}:8443/api",
                        f"hyphenated host: ws-01-{net_hyphen}-prod",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            named_file = root / f"docs/{person_single}-notes.md"
            named_file.write_text("clean content\n", encoding="utf-8")

            findings = public_release.scan_tracked_files(
                root,
                ["docs/sample.md", f"docs/{person_single}-notes.md"],
                rules=rules,
                key=key,
            )

            expected = {
                "docs/sample.md:2: matched keyed rule 'personal-name'",
                "docs/sample.md:3: matched keyed rule 'personal-name'",
                "docs/sample.md:4: matched keyed rule 'personal-name'",
                "docs/sample.md:5: matched keyed rule 'private-network-name'",
                "docs/sample.md:6: matched keyed rule 'private-network-name'",
                "docs/"
                + public_release.path_component_id(key, f"{person_single}-notes.md")
                + ":0: matched keyed rule 'personal-name'",
            }
            self.assertEqual(expected, set(findings))

            # No finding, path findings included, reveals a matched value or the key.
            joined = "\n".join(findings)
            for secret_value in (person_single, person_full, net_domain, net_hyphen, TEST_PRIVATE_KEY):
                self.assertNotIn(secret_value, joined.lower())

            # Different key must not match the same digests.
            alt_findings = public_release.scan_tracked_files(
                root,
                ["docs/sample.md"],
                rules=rules,
                key=bytes.fromhex(ALT_PRIVATE_KEY),
            )
            self.assertEqual([], alt_findings)

    def test_scan_tracked_files_fails_closed_on_missing_traversal_and_handles_symlinks(
        self,
    ) -> None:
        rules = public_release.load_rules(
            ROOT / "tests/fixtures/public_scan/synthetic_rules.json",
            require_nonempty_digests=True,
        )
        key = bytes.fromhex(
            (ROOT / "tests/fixtures/public_scan/synthetic_key.txt").read_text(encoding="utf-8").strip()
        )

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.assertTrue(
                public_release.scan_tracked_files(root, [], rules=rules, key=key)
            )
            missing = public_release.scan_tracked_files(
                root, ["does-not-exist.txt"], rules=rules, key=key
            )
            self.assertTrue(any("missing" in item for item in missing), missing)

            traversal = public_release.scan_tracked_files(
                root, ["../escape.txt"], rules=rules, key=key
            )
            self.assertTrue(any("invalid tracked path" in item for item in traversal), traversal)

            # Symlink target is scanned without dereferencing outside root.
            link = root / "docs/link.txt"
            link.parent.mkdir(parents=True, exist_ok=True)
            link.symlink_to(f"/nonexistent/{SYNTH_NET}/target")
            link_findings = public_release.scan_tracked_files(
                root, ["docs/link.txt"], rules=rules, key=key
            )
            self.assertEqual(
                ["docs/link.txt:1: matched keyed rule 'private-network-name'"],
                link_findings,
            )

    def test_checked_in_rule_files_hold_only_hmac_digests(self) -> None:
        digest_re = re.compile(r"^[0-9a-f]{64}$")
        for rel, require_nonempty in (
            ("tests/public_scan_rules.json", False),
            ("tests/fixtures/public_scan/synthetic_rules.json", True),
        ):
            path = ROOT / rel
            with self.subTest(path=rel):
                ruleset = public_release.load_rules(
                    path, require_nonempty_digests=require_nonempty
                )
                self.assertEqual(1, ruleset.schema_version)
                self.assertEqual("hmac-sha256", ruleset.algorithm)
                raw = json.loads(path.read_text(encoding="utf-8"))
                self.assertEqual({"schema_version", "algorithm", "rules"}, set(raw.keys()))
                for entry in raw["rules"]:
                    self.assertEqual({"id", "digests"}, set(entry.keys()))
                    self.assertIn(entry["id"], public_release.ALLOWED_RULE_IDS)
                    for digest in entry["digests"]:
                        self.assertIsInstance(digest, str)
                        self.assertRegex(digest, digest_re)

    def test_rule_file_rejects_plaintext_extra_keys_and_invalid_schemas(self) -> None:
        valid_digest = "a" * 64
        invalid_payloads: list[tuple[str, str]] = [
            (
                "plaintext_in_digests",
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "digests": ["alice"]},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
            ),
            (
                "uppercase_digest",
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "digests": ["A" * 64]},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
            ),
            (
                "short_digest",
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "digests": ["a" * 63]},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
            ),
            (
                "duplicate_digest",
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "digests": [valid_digest, valid_digest]},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
            ),
            (
                "extra_top_level_key",
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "comment": "no free text allowed",
                        "rules": [
                            {"id": "personal-name", "digests": []},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
            ),
            (
                "extra_rule_key",
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "description": "free text", "digests": []},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
            ),
            (
                "unknown_rule_id",
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "digests": []},
                            {"id": "private-network-name", "digests": []},
                            {"id": "custom-rule", "digests": []},
                        ],
                    }
                ),
            ),
            (
                "missing_required_rule_id",
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "hmac-sha256",
                        "rules": [
                            {"id": "personal-name", "digests": []},
                        ],
                    }
                ),
            ),
            (
                "boolean_schema_version",
                '{"schema_version": true, "algorithm": "hmac-sha256", "rules": [{"id": "personal-name", "digests": []}, {"id": "private-network-name", "digests": []}]}',
            ),
            (
                "wrong_algorithm",
                json.dumps(
                    {
                        "schema_version": 1,
                        "algorithm": "sha256",
                        "rules": [
                            {"id": "personal-name", "digests": []},
                            {"id": "private-network-name", "digests": []},
                        ],
                    }
                ),
            ),
            (
                "duplicate_json_key",
                '{"schema_version": 1, "algorithm": "hmac-sha256", "rules": [{"id": "personal-name", "digests": [], "digests": ["'
                + valid_digest
                + '"]}, {"id": "private-network-name", "digests": []}]}',
            ),
        ]
        with tempfile.TemporaryDirectory() as directory:
            for label, payload in invalid_payloads:
                with self.subTest(case=label):
                    rule_file = Path(directory) / f"{label}.json"
                    rule_file.write_text(payload, encoding="utf-8")
                    with self.assertRaises(public_release.ScanConfigError):
                        public_release.load_rules(rule_file)

    def test_hash_rule_cli_computes_matching_digest_from_stdin(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            key_path = write_private_key(Path(directory) / "key-file", mode=0o600)
            stdout = io.StringIO()
            stdin = io.StringIO(f"  {SYNTH_PERSON.upper()}  \n\n{SYNTH_FULL_NAME}\n")
            with (
                mock.patch.object(sys, "stdin", stdin),
                mock.patch.dict(os.environ, {}, clear=True),
                contextlib.redirect_stdout(stdout),
            ):
                code = public_release.main(
                    [
                        "--key-file",
                        str(key_path),
                        "--hash-rule",
                        "personal-name",
                    ]
                )
            self.assertEqual(0, code)
            lines = stdout.getvalue().strip().splitlines()
            expected = [
                public_release.compute_digest(
                    bytes.fromhex(TEST_PRIVATE_KEY), "personal-name", SYNTH_PERSON
                ),
                public_release.compute_digest(
                    bytes.fromhex(TEST_PRIVATE_KEY), "personal-name", SYNTH_FULL_NAME
                ),
            ]
            self.assertEqual(expected, lines)
            self.assertNotIn(SYNTH_PERSON, stdout.getvalue().lower())
            self.assertNotIn(TEST_PRIVATE_KEY, stdout.getvalue())

    def test_keyed_scan_runs_from_the_base_commit_and_only_one_step_sees_the_key(self) -> None:
        workflows = ROOT / ".github/workflows"
        scan = (workflows / "public-scan.yml").read_text(encoding="utf-8")

        # Triggers: pull_request_target runs this file from the default
        # branch, so a pull request cannot edit the job that judges it.
        self.assertRegex(scan, r"(?m)^  pull_request_target:$")
        self.assertNotRegex(scan, r"(?m)^  pull_request:$")
        self.assertRegex(scan, r"(?m)^permissions:\n  contents: read$")

        step_blocks = re.split(r"(?m)^      - name: ", scan)[1:]
        steps = {block.splitlines()[0].strip(): block for block in step_blocks}
        self.assertEqual(
            ["Check out the trusted commit", "Fetch the commit to scan as git data", "Run the keyed scan on the tree"],
            list(steps),
        )

        checkout = steps["Check out the trusted commit"]
        self.assertIn("github.event.pull_request.base.sha", checkout)
        self.assertNotIn("head.sha", checkout)
        self.assertIn("persist-credentials: false", checkout)

        fetch = steps["Fetch the commit to scan as git data"]
        self.assertIn("refs/pull/${PR_NUMBER}/head:refs/scan/head", fetch)
        self.assertIn('"$fetched" != "$HEAD_SHA"', fetch)
        for forbidden in ("checkout", "make ", "pip ", "npm ", "bash ", "./"):
            self.assertNotIn(forbidden, fetch.split("run: |", 1)[1])

        run = steps["Run the keyed scan on the tree"]
        self.assertIn("PUBLIC_SCAN_HMAC_KEY: ${{ secrets.PUBLIC_SCAN_HMAC_KEY }}", run)
        self.assertIn('run: python3 -I -B tests/check_public_release.py --tree "$TARGET_SHA"', run)
        self.assertNotIn("make", run)

        # Only that step names the secret, here and in every other workflow.
        for name, block in steps.items():
            if name != "Run the keyed scan on the tree":
                with self.subTest(step=name):
                    self.assertNotIn("secrets.", block)
        for other in workflows.glob("*.yml"):
            if other.name != "public-scan.yml":
                with self.subTest(workflow=other.name):
                    self.assertNotIn("PUBLIC_SCAN_HMAC_KEY", other.read_text(encoding="utf-8"))

        # The pull_request CI job runs head code, so it scans synthetically.
        ci = (workflows / "ci.yml").read_text(encoding="utf-8")
        ci_steps = {b.splitlines()[0].strip(): b for b in re.split(r"(?m)^      - name: ", ci)[1:]}
        self.assertIn('PUBLIC_SCAN_SYNTHETIC: "1"', ci_steps["Run public-release gate (synthetic scan)"])


if __name__ == "__main__":
    unittest.main()
