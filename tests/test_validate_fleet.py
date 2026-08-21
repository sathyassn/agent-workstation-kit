#!/usr/bin/env python3

from __future__ import annotations

import copy
import importlib.util
import sys
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("validate_fleet", ROOT / "scripts/validate-fleet.py")
assert SPEC and SPEC.loader
validate_fleet = importlib.util.module_from_spec(SPEC)
sys.modules["validate_fleet"] = validate_fleet
SPEC.loader.exec_module(validate_fleet)


def ready_profile(machine_id: str) -> dict:
    profile = validate_fleet.fleetctl.load_profile(ROOT / "config/profiles/work.example.toml")
    profile["state"] = "approved"
    profile["machine"]["id"] = machine_id
    profile["remote"]["tailscale_tailnet"] = "organization.example"
    profile["remote"]["desktop_lock_mode"] = "dedicated-shared"
    profile["tooling"]["gws"] = "skip"
    profile["tooling"]["secrets_provider"] = "organization-vault"
    profile["tooling"]["antidote_ref"] = "0123456789abcdef0123456789abcdef01234567"
    profile["security"]["endpoint_management"] = "mdm-and-edr"
    profile["backup"]["target"] = "corporate-backup"
    profile["maintenance"]["update_window"] = "Sunday 02:00-04:00"
    profile["maintenance"]["owner"] = "platform-team"
    return profile


class FleetDirectoryTests(unittest.TestCase):
    def test_empty_fleet_is_rejected(self) -> None:
        self.assertEqual(2, validate_fleet.validate_paths([]))

    def test_duplicate_machine_ids_are_rejected(self) -> None:
        profile = ready_profile("ai-node-01")
        with mock.patch.object(
            validate_fleet.fleetctl,
            "load_profile",
            side_effect=[profile, copy.deepcopy(profile)],
        ):
            self.assertEqual(1, validate_fleet.validate_paths([Path("a.toml"), Path("b.toml")]))

    def test_unique_ready_profiles_pass(self) -> None:
        first = ready_profile("ai-node-01")
        second = ready_profile("ai-node-02")
        with mock.patch.object(validate_fleet.fleetctl, "load_profile", side_effect=[first, second]):
            self.assertEqual(0, validate_fleet.validate_paths([Path("a.toml"), Path("b.toml")]))


if __name__ == "__main__":
    unittest.main()
