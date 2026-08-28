#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "tests/check_repository.py"
SPEC = importlib.util.spec_from_file_location("check_repository", SCRIPT)
assert SPEC and SPEC.loader
check_repository = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = check_repository
SPEC.loader.exec_module(check_repository)


class DocumentationCheckTests(unittest.TestCase):
    def test_fenced_comments_are_not_markdown_headings(self) -> None:
        headings = check_repository.markdown_headings(
            "# Visible\n\n```bash\n# not-a-heading\n```\n\n## Also visible\n"
        )
        self.assertEqual({"visible", "also-visible"}, headings)

    def test_documentation_home_requires_a_real_link(self) -> None:
        self.assertIsNone(check_repository.DOCS_HOME_LINK.search("Documentation home"))
        self.assertIsNotNone(
            check_repository.DOCS_HOME_LINK.search(
                "[Documentation home](../README.md)"
            )
        )

    def test_non_neutral_hostname_examples_are_rejected(self) -> None:
        self.assertEqual(
            {"corp"},
            check_repository.non_neutral_hostname_examples("corp" + "-ws-001"),
        )
        self.assertEqual(
            set(),
            check_repository.non_neutral_hostname_examples(
                "acme-ws-001 lab-mac-002 home-srv-003"
            ),
        )

    def test_kvm_model_codes_require_the_full_gl_model(self) -> None:
        self.assertIsNotNone(
            check_repository.BARE_KVM_MODEL.search("buy RM1PE later")
        )
        self.assertIsNone(
            check_repository.BARE_KVM_MODEL.search(
                "GL.iNet Comet PoE (GL-RM1PE)"
            )
        )

    def test_privileged_linux_examples_use_staged_inputs(self) -> None:
        linux_guide = (ROOT / "docs/02-linux-setup.md").read_text(encoding="utf-8")
        self.assertNotIn("/path/to/ac-ws-001.toml", linux_guide)
        self.assertNotIn("run PROFILE", linux_guide)
        self.assertIn("cd /opt/agent-workstation-kit", linux_guide)
        self.assertIn("--fleet-root /opt/agent-workstation-fleet", linux_guide)

    def test_profile_commit_precedes_staging_archive(self) -> None:
        day_zero = (ROOT / "docs/runbooks/day-zero-linux.md").read_text(
            encoding="utf-8"
        )
        commit = day_zero.index('commit -m "fleet: approve acme-ws-001 baseline"')
        archive = day_zero.index("git archive --format=tar")
        self.assertLess(commit, archive)
        self.assertIn("/opt/.agent-workstation-stage.", day_zero)
        self.assertIn('test ! -e "$kit_target"', day_zero)
        self.assertIn('test ! -L "$kit_target"', day_zero)
        self.assertIn('test ! -e "$fleet_target"', day_zero)
        self.assertIn('test ! -L "$fleet_target"', day_zero)
        self.assertIn('test "$kit_published" -eq 1', day_zero)
        self.assertIn('test "$fleet_published" -eq 1', day_zero)
        self.assertIn("sha256sum --check --strict", day_zero)
        self.assertIn('cmp --silent "$kit_stage/VERSION"', day_zero)

    def test_pilot_sequence_has_one_controlling_path(self) -> None:
        pilot = (ROOT / "docs/runbooks/first-linux-pilot.md").read_text(
            encoding="utf-8"
        )
        self.assertIn("Use this checklist alongside", pilot)
        self.assertIn("## Before power-on", pilot)
        self.assertIn("day-zero setup-agent", pilot)

    def test_macos_has_day_zero_and_cross_platform_operator_paths(self) -> None:
        day_zero = (ROOT / "docs/runbooks/day-zero-macos.md").read_text(
            encoding="utf-8"
        )
        staging = (
            ROOT / "docs/runbooks/stage-approved-macos-snapshots.md"
        ).read_text(encoding="utf-8")
        remote = (ROOT / "docs/08-network-remote-access-and-files.md").read_text(
            encoding="utf-8"
        )
        self.assertIn("scripts/start-macos-pilot.py", day_zero)
        self.assertIn(
            "[macOS staging runbook](stage-approved-macos-snapshots.md)", day_zero
        )
        for invariant in (
            "TOOLKIT_ARCHIVE=",
            "FLEET_ARCHIVE=",
            "PROFILE=",
            "sudo /usr/bin/env -i",
            "shasum -a 256 --check --strict",
            "if test ! -e /opt",
            'find "$kit_stage" ! -type d ! -type f',
            'find "$fleet_stage" ! -type d ! -type f',
            'trap "exit 130" INT',
            "Toolkit VERSION and fleet kit.lock do not match",
            "An existing `/opt/homebrew` is expected",
            "replaces every `REVIEWED_*` value",
            "sudo -K",
        ):
            self.assertIn(invariant, staging)
        self.assertLess(
            day_zero.index("## 3. Complete Setup Assistant"),
            day_zero.index("## 9. Perform the setup-agent handoff"),
        )
        for platform in ("macOS", "Windows", "Linux", "iPadOS", "Android"):
            self.assertIn(platform, remote)


if __name__ == "__main__":
    unittest.main()
