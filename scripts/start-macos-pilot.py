#!/usr/bin/env python3
"""Assess whether a fresh Mac is ready for setup-agent handoff.

This command is deliberately read-only. It checks the local macOS bootstrap,
the toolkit/private-fleet relationship and Codex authentication. It never
invokes sudo, changes System Settings or reads credential values.
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

try:
    import tomllib
except ModuleNotFoundError:
    print(
        "ERROR: start-macos-pilot.py requires Python 3.11 or newer; "
        "run it with the Homebrew Python selected in the macOS day-zero guide.",
        file=sys.stderr,
    )
    raise SystemExit(2)


ROOT = Path(__file__).resolve().parents[1]


@dataclass(frozen=True)
class Check:
    status: str
    name: str
    detail: str


def run(command: list[str], *, cwd: Path = ROOT) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, cwd=cwd, check=False, text=True, capture_output=True)


def execution_user_check(euid: int | None = None) -> Check:
    detected = os.geteuid() if euid is None else euid
    if detected == 0:
        return Check("FAIL", "execution user", "run as the bootstrap human, never root")
    return Check("PASS", "execution user", f"uid={detected}")


def operating_system_check(
    platform: str | None = None,
    runner: Callable[[list[str]], subprocess.CompletedProcess[str]] = run,
) -> Check:
    detected_platform = sys.platform if platform is None else platform
    if detected_platform != "darwin":
        return Check("FAIL", "platform", f"macOS required; detected {detected_platform}")
    result = runner(["sw_vers", "-productVersion"])
    if result.returncode != 0 or not result.stdout.strip():
        return Check("FAIL", "operating system", result.stderr.strip() or "sw_vers failed")
    return Check("PASS", "operating system", f"macOS {result.stdout.strip()}")


def command_check(
    name: str, command: list[str], *, include_output: bool = True
) -> Check:
    if shutil.which(command[0]) is None:
        return Check("FAIL", name, f"{command[0]} is unavailable")
    result = run(command)
    output = "\n".join(
        part.strip() for part in (result.stdout, result.stderr) if part.strip()
    )
    if output and include_output:
        detail = output.splitlines()[0]
    else:
        detail = "check passed" if result.returncode == 0 else "check failed"
    return Check(
        "PASS" if result.returncode == 0 else "FAIL",
        name,
        detail,
    )


def clean_git_checkout_check(path: Path, name: str) -> Check:
    if shutil.which("git") is None:
        return Check("FAIL", name, "git is unavailable")
    status = run(
        ["git", "-C", str(path), "status", "--porcelain", "--untracked-files=all"]
    )
    if status.returncode != 0:
        return Check("FAIL", name, status.stderr.strip() or "not a Git checkout")
    if status.stdout.strip():
        return Check("FAIL", name, "working tree is not clean")
    revision = run(["git", "-C", str(path), "rev-parse", "HEAD"])
    if revision.returncode != 0:
        return Check("FAIL", name, "cannot resolve the Git revision")
    return Check("PASS", name, f"clean at {revision.stdout.strip()[:12]}")


def fleet_checks(fleet_root: Path, profile: Path | None) -> list[Check]:
    if not fleet_root.is_dir() or fleet_root.is_symlink():
        return [Check("FAIL", "private fleet", f"not a real directory: {fleet_root}")]

    fleet_real = fleet_root.resolve()
    checks = [clean_git_checkout_check(fleet_root, "private fleet revision")]
    version = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
    lock = fleet_root / "kit.lock"
    if not lock.is_file() or lock.is_symlink():
        checks.append(Check("FAIL", "fleet kit.lock", f"missing regular file: {lock}"))
    elif lock.read_text(encoding="utf-8").strip() != version:
        checks.append(
            Check("FAIL", "fleet kit.lock", f"must equal toolkit version {version}")
        )
    else:
        checks.append(Check("PASS", "fleet kit.lock", f"matches {version}"))

    if profile is None:
        checks.append(
            Check(
                "NEXT",
                "machine profile",
                "create it with fleetctl init during the interview",
            )
        )
        return checks

    machines = (fleet_real / "machines").resolve()
    requested = (
        fleet_real / profile
        if not profile.is_absolute()
        else profile.expanduser().absolute()
    )
    if requested.is_symlink():
        checks.append(
            Check("FAIL", "machine profile", "profile path must not be a symlink")
        )
        return checks
    candidate = requested.resolve()
    if candidate.parent != machines or candidate.suffix != ".toml":
        checks.append(
            Check(
                "FAIL",
                "machine profile",
                "profile must be a direct TOML child of machines/",
            )
        )
        return checks
    if not candidate.is_file() or candidate.is_symlink():
        checks.append(Check("NEXT", "machine profile", f"not created yet: {candidate}"))
        return checks

    relative = candidate.relative_to(fleet_real)
    tracked = run(
        [
            "git",
            "-C",
            str(fleet_root),
            "ls-files",
            "--error-unmatch",
            "--",
            str(relative),
        ]
    )
    if tracked.returncode != 0:
        checks.append(Check("FAIL", "machine profile", f"profile is not committed: {relative}"))
        return checks
    validation = run(
        [
            sys.executable,
            str(ROOT / "scripts/fleetctl.py"),
            "--fleet-root",
            str(fleet_root),
            "validate",
            str(relative),
        ]
    )
    if validation.returncode != 0:
        detail = (
            validation.stderr.strip().splitlines()[0]
            if validation.stderr.strip()
            else "validation failed"
        )
        checks.append(Check("FAIL", "machine profile", detail))
    else:
        try:
            profile_data = tomllib.loads(candidate.read_text(encoding="utf-8"))
            profile_platform = profile_data.get("machine", {}).get("platform")
        except (OSError, tomllib.TOMLDecodeError) as error:
            checks.append(Check("FAIL", "machine profile", f"cannot read profile: {error}"))
        else:
            if profile_platform != "macos":
                checks.append(
                    Check(
                        "FAIL",
                        "machine profile",
                        f"machine.platform must be macos; found {profile_platform!r}",
                    )
                )
            else:
                checks.append(
                    Check(
                        "PASS",
                        "machine profile",
                        f"valid macOS profile and committed: {relative}",
                    )
                )
    return checks


def repository_suite_check() -> Check:
    if shutil.which("make") is None:
        return Check("FAIL", "repository checks", "make is unavailable")
    environment = os.environ.copy()
    environment["PYTHON"] = sys.executable
    result = subprocess.run(
        ["make", "check"], cwd=ROOT, check=False, env=environment
    )
    return Check(
        "PASS" if result.returncode == 0 else "FAIL",
        "repository checks",
        "make check passed" if result.returncode == 0 else "make check failed",
    )


def preflight_check() -> Check:
    path = ROOT / "scripts/preflight.sh"
    if not path.is_file() or not os.access(path, os.X_OK):
        return Check("FAIL", "host preflight", f"missing executable: {path}")
    result = subprocess.run([str(path)], cwd=ROOT, check=False)
    return Check(
        "PASS" if result.returncode == 0 else "FAIL",
        "host preflight",
        "read-only preflight completed" if result.returncode == 0 else "preflight failed",
    )


def print_handoff(fleet_root: Path, profile: Path | None) -> None:
    profile_text = (
        str(profile)
        if profile
        else "machines/acme-mac-001.toml (create during the interview)"
    )
    print("\nSetup-agent handoff prompt\n==========================")
    print(
        f"""This is a supervised macOS workstation setup. Use
skills/setup-agent-workstation/SKILL.md and begin read-only.

Toolkit root: {ROOT}
Private fleet root: {fleet_root}
Target profile: {profile_text}

Run no privileged or mutating operation until you show the exact preview,
explain its effect and recovery path, and receive human approval. Stop for
Setup Assistant, FileVault, MDM, privacy, system-extension, Xcode/signing and
credential ceremonies. Never read, print, copy or store secret values."""
    )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Read-only readiness check for a macOS setup-agent handoff."
    )
    parser.add_argument("--fleet-root", type=Path, required=True)
    parser.add_argument("--profile", type=Path)
    args = parser.parse_args()

    fleet_root = args.fleet_root.expanduser().absolute()
    checks = [
        operating_system_check(),
        execution_user_check(),
        clean_git_checkout_check(ROOT, "toolkit checkout"),
        repository_suite_check(),
        preflight_check(),
        command_check("Xcode Command Line Tools", ["xcode-select", "-p"]),
        *fleet_checks(fleet_root, args.profile),
        command_check(
            "Codex authentication",
            ["codex", "login", "status"],
            include_output=False,
        ),
    ]

    print("\nReadiness summary\n=================")
    for check in checks:
        print(f"{check.status:<5} {check.name}: {check.detail}")
    if any(check.status == "FAIL" for check in checks):
        print("\nNOT READY: resolve every FAIL, then rerun this command.")
        return 1
    print("\nREADY: start Codex from the toolkit root and use the prompt below.")
    print_handoff(fleet_root, args.profile)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
