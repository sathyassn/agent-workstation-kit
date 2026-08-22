#!/usr/bin/env python3

from __future__ import annotations

import copy
import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("validate_fleet", ROOT / "scripts/validate-fleet.py")
assert SPEC and SPEC.loader
validate_fleet = importlib.util.module_from_spec(SPEC)
sys.modules["validate_fleet"] = validate_fleet
SPEC.loader.exec_module(validate_fleet)


def ready_profile(hostname: str, machine_uuid: str, asset_tag: str) -> dict:
    profile = validate_fleet.fleetctl.load_profile(ROOT / "config/profiles/work.example.toml")
    profile["state"] = "approved"
    profile["machine"].update(hostname=hostname, uuid=machine_uuid, asset_tag=asset_tag)
    profile["remote"].update(tailscale_tailnet="organization.example", desktop_lock_mode="dedicated-shared")
    profile["tooling"].update(gws="skip", secrets_provider="organization-vault", antidote_ref="v1.9.10")
    profile["security"]["endpoint_management"] = "mdm-and-edr"
    profile["backup"]["target"] = "corporate-backup"
    profile["maintenance"].update(update_window="Sunday 02:00-04:00", owner="platform-team")
    return profile


class FleetDirectoryTests(unittest.TestCase):
    def test_empty_fleet_is_rejected(self) -> None:
        self.assertEqual(2, validate_fleet.validate_paths([]))

    def test_duplicate_host_uuid_and_asset_are_rejected(self) -> None:
        profile = ready_profile("ac-ws-001", "11111111-1111-4111-8111-111111111111", "AC-1")
        with mock.patch.object(validate_fleet.fleetctl, "load_profile", side_effect=[profile, copy.deepcopy(profile)]):
            self.assertEqual(1, validate_fleet.validate_paths([Path("ac-ws-001.toml"), Path("ac-ws-001.toml")]))

    def test_filename_must_match_hostname(self) -> None:
        profile = ready_profile("ac-ws-001", "11111111-1111-4111-8111-111111111111", "AC-1")
        with mock.patch.object(validate_fleet.fleetctl, "load_profile", return_value=profile):
            self.assertEqual(1, validate_fleet.validate_paths([Path("wrong.toml")]))

    def test_unique_ready_profiles_and_matching_lock_pass(self) -> None:
        first = ready_profile("ac-ws-001", "11111111-1111-4111-8111-111111111111", "AC-1")
        second = ready_profile("ac-ws-002", "22222222-2222-4222-8222-222222222222", "AC-2")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "kit.lock").write_text(validate_fleet.fleetctl.VERSION + "\n")
            with mock.patch.object(validate_fleet.fleetctl, "load_profile", side_effect=[first, second]):
                self.assertEqual(0, validate_fleet.validate_paths([Path("ac-ws-001.toml"), Path("ac-ws-002.toml")], fleet_root=root))

    def test_retired_hostname_cannot_be_reused(self) -> None:
        profile = ready_profile("ac-ws-001", "11111111-1111-4111-8111-111111111111", "AC-1")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "kit.lock").write_text(validate_fleet.fleetctl.VERSION + "\n")
            (root / "retired-hostnames.txt").write_text("ac-ws-001\n")
            with mock.patch.object(validate_fleet.fleetctl, "load_profile", return_value=profile):
                self.assertEqual(1, validate_fleet.validate_paths([Path("ac-ws-001.toml")], fleet_root=root))

    def test_cli_rejects_misplaced_root_profile_when_machines_exists(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "machines").mkdir()
            (root / "misplaced.toml").write_text("schema_version = 2\n")
            with mock.patch.object(sys, "argv", ["validate-fleet.py", str(root)]):
                self.assertEqual(1, validate_fleet.main())


if __name__ == "__main__":
    unittest.main()
