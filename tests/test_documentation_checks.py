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
        commit = day_zero.index('commit -m "fleet: approve mp-ws-001 baseline"')
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


if __name__ == "__main__":
    unittest.main()
