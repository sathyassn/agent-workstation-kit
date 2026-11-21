"""The public scan allows CodeFlow's synthetic values only inside CodeFlow's folders."""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_repository as scan  # noqa: E402

WILDCARD = "/home" + "/*/"


class AllowlistTests(unittest.TestCase):
    def test_listed_value_in_codeflow_folder_is_allowed(self) -> None:
        self.assertFalse(scan.unexpected_matches(scan.PERSONAL_PATH, WILDCARD, ".grok/sandbox.toml"))

    def test_listed_value_outside_codeflow_folders_fails(self) -> None:
        self.assertTrue(scan.unexpected_matches(scan.PERSONAL_PATH, WILDCARD, "scripts/x.sh"))

    def test_unlisted_value_in_codeflow_folder_fails(self) -> None:
        token = "sk" + "-" + "A" * 30
        self.assertTrue(scan.unexpected_matches(scan.SECRET_LIKE, token, ".agents/skills/x/cases.json"))
        self.assertTrue(scan.unexpected_matches(scan.PERSONAL_PATH, "/Users" + "/alice/", ".grok/sandbox.toml"))

    def test_one_unlisted_match_among_listed_ones_fails(self) -> None:
        content = WILDCARD + " " + "/home" + "/bob/"
        self.assertTrue(scan.unexpected_matches(scan.PERSONAL_PATH, content, ".grok/sandbox.toml"))

    def test_lookalike_prefix_is_not_managed(self) -> None:
        self.assertFalse(scan.codeflow_managed(".agentsx/skills/x"))
        self.assertFalse(scan.codeflow_managed(".claude/settings.json"))


if __name__ == "__main__":
    unittest.main()
