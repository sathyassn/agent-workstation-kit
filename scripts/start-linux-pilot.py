#!/usr/bin/env python3
"""Assess whether a fresh Ubuntu host is ready for setup-agent handoff.

This command is intentionally non-mutating. It validates the toolkit and
private-fleet relationship, runs local checks, verifies Codex authentication,
and prints the next handoff prompt. It never invokes sudo or reads secrets.
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
import tomllib
from dataclasses import dataclass
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


@dataclass(frozen=True)
class Check:
    status: str
    name: str
    detail: str


def read_os_release(path: Path = Path("/etc/os-release")) -> dict[str, str]:
    values: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key] = value.strip().strip('"')
    return values


def execution_user_check(euid: int | None = None) -> Check:
    detected = os.geteuid() if euid is None else euid
    if detected == 0:
        return Check("FAIL", "execution user", "run as the bootstrap human, never root")
    return Check("PASS", "execution user", f"uid={detected}")


def operating_system_check(expected: str, path: Path = Path("/etc/os-release")) -> Check:
    try:
        release = read_os_release(path)
    except OSError as exc:
        return Check("FAIL", "operating system", str(exc))
    detected = release.get("VERSION_ID", "unknown")
    if release.get("ID") == "ubuntu" and detected == expected:
        return Check("PASS", "operating system", release.get("PRETTY_NAME", detected))
    return Check(
        "FAIL",
        "operating system",
        f"expected Ubuntu {expected}; detected {release.get('PRETTY_NAME', detected)}",
    )


def run(command: list[str], *, cwd: Path = ROOT) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, cwd=cwd, check=False, text=True, capture_output=True)


def resolve_profile(fleet_root: Path, profile: Path) -> tuple[Path | None, str | None]:
    candidate = (fleet_root / profile).resolve() if not profile.is_absolute() else profile.resolve()
    machines = (fleet_root / "machines").resolve()
    try:
        candidate.relative_to(machines)
    except ValueError:
        return None, "profile must resolve below the fleet machines/ directory"
    if candidate.parent != machines:
        return None, "nested machine profile directories are unsupported"
    return candidate, None


def repository_check() -> Check:
    if shutil.which("git") is None:
        return Check("FAIL", "toolkit checkout", "git is unavailable")
    status = run(["git", "status", "--porcelain", "--untracked-files=all"])
    if status.returncode != 0:
        return Check("FAIL", "toolkit checkout", status.stderr.strip() or "git status failed")
    if status.stdout.strip():
        return Check("FAIL", "toolkit checkout", "working tree is not clean")
    revision = run(["git", "rev-parse", "HEAD"])
    if revision.returncode != 0:
        return Check("FAIL", "toolkit checkout", "cannot resolve the Git revision")
    return Check("PASS", "toolkit checkout", f"clean at {revision.stdout.strip()[:12]}")


def fleet_checks(fleet_root: Path, profile: Path | None) -> list[Check]:
    checks: list[Check] = []
    if not fleet_root.is_dir() or fleet_root.is_symlink():
        return [Check("FAIL", "private fleet", f"not a real directory: {fleet_root}")]
    version = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
    lock = fleet_root / "kit.lock"
    if not lock.is_file() or lock.is_symlink():
        checks.append(Check("FAIL", "fleet kit.lock", f"missing regular file: {lock}"))
    elif lock.read_text(encoding="utf-8").strip() != version:
        checks.append(Check("FAIL", "fleet kit.lock", f"must equal toolkit version {version}"))
    else:
        checks.append(Check("PASS", "fleet kit.lock", f"matches {version}"))

    if profile is None:
        checks.append(Check("NEXT", "machine profile", "create it with fleetctl init during the agent interview"))
        return checks
    resolved, error = resolve_profile(fleet_root, profile)
    if error:
        checks.append(Check("FAIL", "machine profile", error))
        return checks
    assert resolved is not None
    if not resolved.is_file() or resolved.is_symlink():
        checks.append(Check("NEXT", "machine profile", f"not created yet: {resolved}"))
        return checks
    relative = resolved.relative_to(fleet_root)
    validation = run(
        [
            str(ROOT / "scripts/fleetctl.py"),
            "--fleet-root",
            str(fleet_root),
            "validate",
            str(relative),
        ]
    )
    if validation.returncode != 0:
        detail = validation.stderr.strip().splitlines()[0] if validation.stderr.strip() else "validation failed"
        checks.append(Check("FAIL", "machine profile", detail))
        return checks
    checks.append(Check("PASS", "machine profile", f"draft-valid: {relative}"))
    try:
        with resolved.open("rb") as handle:
            state = tomllib.load(handle).get("state")
    except (OSError, tomllib.TOMLDecodeError) as exc:
        checks.append(Check("FAIL", "profile state", str(exc)))
        return checks
    if state != "approved":
        checks.append(Check("NEXT", "profile state", "resolve every 'ask', review, then set state = approved"))
        return checks
    ready = run(
        [
            str(ROOT / "scripts/fleetctl.py"),
            "--fleet-root",
            str(fleet_root),
            "validate",
            str(relative),
            "--ready",
        ]
    )
    fleet = run([str(ROOT / "scripts/validate-fleet.py"), str(fleet_root)])
    if ready.returncode or fleet.returncode:
        failed = ready if ready.returncode else fleet
        output = "\n".join(part for part in (failed.stderr, failed.stdout) if part).strip()
        detail = output.splitlines()[0] if output else "ready validation failed"
        checks.append(Check("FAIL", "approved fleet", detail))
    else:
        checks.append(Check("PASS", "approved fleet", "ready profile and whole-fleet validation pass"))
    return checks


def fleet_repository_check(fleet_root: Path, profile: Path | None) -> Check:
    """Require private input to be an exact, committed Git revision."""
    if shutil.which("git") is None:
        return Check("FAIL", "private fleet revision", "git is unavailable")
    status = run(
        ["git", "-C", str(fleet_root), "status", "--porcelain", "--untracked-files=all"]
    )
    if status.returncode != 0:
        return Check(
            "FAIL",
            "private fleet revision",
            status.stderr.strip() or "not a Git checkout",
        )
    if status.stdout.strip():
        return Check("FAIL", "private fleet revision", "working tree is not clean")
    revision = run(["git", "-C", str(fleet_root), "rev-parse", "HEAD"])
    if revision.returncode != 0:
        return Check("FAIL", "private fleet revision", "cannot resolve the Git revision")
    if profile is not None:
        resolved, error = resolve_profile(fleet_root, profile)
        if error is None and resolved is not None and resolved.is_file():
            relative = resolved.relative_to(fleet_root)
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
                return Check(
                    "FAIL",
                    "private fleet revision",
                    f"profile is not committed: {relative}",
                )
    return Check(
        "PASS",
        "private fleet revision",
        f"clean at {revision.stdout.strip()[:12]}",
    )


def codex_check() -> Check:
    if shutil.which("codex") is None:
        return Check("FAIL", "Codex CLI", "not installed; follow the day-zero guide")
    status = run(["codex", "login", "status"])
    combined = "\n".join(part.strip() for part in (status.stdout, status.stderr) if part.strip())
    if status.returncode != 0:
        return Check("FAIL", "Codex authentication", combined.splitlines()[0] if combined else "not authenticated")
    return Check("PASS", "Codex authentication", combined.splitlines()[0] if combined else "authenticated")


def preflight_check(path: Path = ROOT / "scripts/preflight.sh") -> Check:
    if not path.is_file() or not os.access(path, os.X_OK):
        return Check("FAIL", "host preflight", f"missing executable: {path}")
    try:
        preflight = subprocess.run([str(path)], cwd=ROOT, check=False)
    except OSError as exc:
        return Check("FAIL", "host preflight", str(exc))
    return Check(
        "PASS" if preflight.returncode == 0 else "FAIL",
        "host preflight",
        "read-only preflight completed" if preflight.returncode == 0 else "preflight failed",
    )


def repository_suite_check() -> Check:
    if shutil.which("make") is None:
        return Check("FAIL", "repository checks", "make is unavailable")
    result = subprocess.run(["make", "check"], cwd=ROOT, check=False)
    return Check(
        "PASS" if result.returncode == 0 else "FAIL",
        "repository checks",
        "make check passed" if result.returncode == 0 else "make check failed",
    )


def print_handoff(fleet_root: Path, profile: Path | None) -> None:
    profile_text = str(profile) if profile else "machines/mp-ws-001.toml (create during the interview)"
    print("\nSetup-agent handoff prompt\n==========================")
    print(
        f"""This is a supervised work Linux pilot. Use
skills/setup-agent-workstation/SKILL.md and begin read-only.

Toolkit root: {ROOT}
Private fleet root: {fleet_root}
Target profile: {profile_text}

Run no privileged or mutating operation until you show the exact preview,
explain its effect and recovery path, and receive human approval. Never read,
print, copy, or store passwords, tokens, private keys, or recovery material.
Stop at the first unsupported assumption or failed validation."""
    )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Read-only readiness check for the first Ubuntu setup-agent handoff."
    )
    parser.add_argument("--fleet-root", type=Path, required=True)
    parser.add_argument("--profile", type=Path)
    parser.add_argument("--expected-ubuntu", default="24.04")
    parser.add_argument(
        "--run-private-hardware-audit",
        action="store_true",
        help="print serial-bearing hardware evidence to this terminal",
    )
    args = parser.parse_args()

    checks: list[Check] = []
    if sys.platform.startswith("linux"):
        checks.append(Check("PASS", "platform", "Linux"))
    else:
        checks.append(Check("FAIL", "platform", f"Linux required; detected {sys.platform}"))
    checks.append(execution_user_check())
    checks.append(operating_system_check(args.expected_ubuntu))
    checks.append(repository_check())
    checks.append(repository_suite_check())

    checks.append(preflight_check())
    if args.run_private_hardware_audit:
        print("\nPRIVATE hardware evidence follows; do not publish it.\n")
        audit = subprocess.run([str(ROOT / "scripts/hardware-audit-linux.sh")], cwd=ROOT, check=False)
        checks.append(
            Check(
                "PASS" if audit.returncode == 0 else "FAIL",
                "hardware audit",
                "completed" if audit.returncode == 0 else "failed",
            )
        )
    else:
        checks.append(Check("NEXT", "hardware audit", "rerun with --run-private-hardware-audit"))

    # Preserve the final path component so fleet_checks can reject a symlink.
    fleet_root = args.fleet_root.expanduser().absolute()
    checks.extend(fleet_checks(fleet_root, args.profile))
    checks.append(fleet_repository_check(fleet_root, args.profile))
    checks.append(codex_check())

    print("\nReadiness summary\n=================")
    for check in checks:
        print(f"{check.status:<5} {check.name}: {check.detail}")
    failures = [check for check in checks if check.status == "FAIL"]
    if failures:
        print("\nNOT READY: resolve every FAIL, then rerun this command.")
        return 1
    print("\nREADY: start Codex from the toolkit root and use the prompt below.")
    print_handoff(fleet_root, args.profile)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
