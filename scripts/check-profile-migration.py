#!/usr/bin/env python3
"""Verify that a manually reviewed schema-2 to schema-3 migration is safe.

The checker never rewrites either profile. It proves that established machine,
account, network, security, and operations decisions did not change while the
new schema-3 identity decisions were added.
"""

from __future__ import annotations

import argparse
import importlib.util
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("fleetctl", ROOT / "scripts/fleetctl.py")
assert SPEC and SPEC.loader
fleetctl = importlib.util.module_from_spec(SPEC)
sys.modules["fleetctl"] = fleetctl
SPEC.loader.exec_module(fleetctl)

PRESERVED_SECTIONS = (
    "deployment",
    "accounts",
    "remote",
    "tooling",
    "model_auth",
    "security",
    "resources",
    "backup",
    "maintenance",
)
PRESERVED_MACHINE_FIELDS = (
    "hostname",
    "uuid",
    "asset_tag",
    "platform",
    "os_family",
    "hardware_profile",
    "role",
)
PRESERVED_SOURCE_FIELDS = (
    "gitlab_host",
    "gitlab_identity",
    "github_host",
    "github_identity",
)


def check_migration(old: dict[str, Any], new: dict[str, Any]) -> list[str]:
    errors: list[str] = []

    def same(path: str, left: Any, right: Any) -> None:
        if left != right:
            errors.append(f"{path} changed during schema migration")

    if old.get("schema_version") != 2:
        errors.append("source profile must use schema_version = 2")
    if new.get("schema_version") != fleetctl.SCHEMA_VERSION:
        errors.append(f"candidate profile must use schema_version = {fleetctl.SCHEMA_VERSION}")
    if new.get("state") != "draft":
        errors.append("candidate state must be draft until migration review completes")
    same("profile", old.get("profile"), new.get("profile"))
    for section in PRESERVED_SECTIONS:
        same(section, old.get(section), new.get(section))
    for field in PRESERVED_MACHINE_FIELDS:
        same(
            f"machine.{field}",
            old.get("machine", {}).get(field),
            new.get("machine", {}).get(field),
        )
    for field in PRESERVED_SOURCE_FIELDS:
        same(
            f"source_control.{field}",
            old.get("source_control", {}).get(field),
            new.get("source_control", {}).get(field),
        )
    for issue in fleetctl.validate_profile(new, ready=False):
        errors.append(f"candidate {issue.path}: {issue.message}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description="Check a schema-2 to schema-3 profile migration without modifying files.")
    parser.add_argument("source_v2", type=Path)
    parser.add_argument("candidate_v3", type=Path)
    args = parser.parse_args()
    try:
        old = fleetctl.load_profile(args.source_v2)
        new = fleetctl.load_profile(args.candidate_v3)
    except ValueError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    errors = check_migration(old, new)
    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print("PASS: schema-3 candidate preserves established schema-2 decisions and is a valid draft")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
