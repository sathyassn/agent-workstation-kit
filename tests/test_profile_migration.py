#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("check_profile_migration", ROOT / "scripts/check-profile-migration.py")
assert SPEC and SPEC.loader
migration = importlib.util.module_from_spec(SPEC)
sys.modules["check_profile_migration"] = migration
SPEC.loader.exec_module(migration)


class ProfileMigrationTests(unittest.TestCase):
    def profiles(self) -> tuple[dict, dict]:
        new = migration.fleetctl.load_profile(ROOT / "config/profiles/work.example.toml")
        old = migration.fleetctl.load_profile(ROOT / "tests/fixtures/schema2-work.toml")
        return old, new

    def test_reviewed_schema_migration_passes(self) -> None:
        old, new = self.profiles()
        self.assertEqual([], migration.check_migration(old, new))

    def test_migration_rejects_identity_or_policy_changes(self) -> None:
        old, new = self.profiles()
        new["machine"]["uuid"] = "22222222-2222-4222-8222-222222222222"
        new["security"]["remote_scope"] = "public"
        errors = migration.check_migration(old, new)
        self.assertTrue(any("machine.uuid changed" in error for error in errors))
        self.assertTrue(any("security changed" in error for error in errors))

    def test_migration_candidate_must_remain_draft(self) -> None:
        old, new = self.profiles()
        new["state"] = "approved"
        self.assertTrue(any("state must be draft" in error for error in migration.check_migration(old, new)))

    def test_cli_accepts_historical_schema_two_fixture_and_candidate_file(self) -> None:
        source = ROOT / "tests/fixtures/schema2-work.toml"
        candidate_text = (ROOT / "config/profiles/work.example.toml").read_text(encoding="utf-8")
        with tempfile.TemporaryDirectory() as directory:
            candidate = Path(directory) / "ac-ws-001.toml"
            candidate.write_text(candidate_text, encoding="utf-8")
            result = subprocess.run(
                [str(ROOT / "scripts/check-profile-migration.py"), str(source), str(candidate)],
                check=False,
                capture_output=True,
                text=True,
            )
        self.assertEqual(0, result.returncode, result.stderr)


if __name__ == "__main__":
    unittest.main()
