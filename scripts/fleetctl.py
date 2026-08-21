#!/usr/bin/env python3
"""Validate a fleet profile and orchestrate reviewed, phase-scoped scripts.

The profile is data, never shell code. This controller uses only the Python
standard library, passes argv arrays to subprocesses, and never reads secrets.
Mutating phases remain preview-only unless the caller explicitly supplies
--apply and the profile has passed ready-to-apply validation.
"""

from __future__ import annotations

import argparse
import grp
import os
import pwd
import re
import shlex
import shutil
import subprocess
import sys
import tomllib
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError
from dataclasses import dataclass
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]


def load_version(root: Path = ROOT) -> str:
    """Return the repository version without making the CLI unstartable."""
    try:
        version = (root / "VERSION").read_text(encoding="utf-8").strip()
    except OSError:
        return "0.0.0+unknown"
    return version or "0.0.0+unknown"


VERSION = load_version()
UNIX_NAME = re.compile(r"^[a-z_][a-z0-9_-]{0,30}$")
IDENTIFIER = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$")
RELEASE_REF = re.compile(r"^[A-Za-z0-9._/-]+$")

TOP_LEVEL = {
    "schema_version",
    "profile",
    "state",
    "machine",
    "accounts",
    "remote",
    "tooling",
    "source_control",
    "model_auth",
    "security",
    "resources",
    "backup",
    "maintenance",
}
SECTION_KEYS = {
    "machine": {"id", "platform", "os_family", "role"},
    "accounts": {"agent", "humans", "admins", "operators", "viewers", "ssh_users"},
    "remote": {"tailscale_tailnet", "tailscale_tags", "nomachine_port", "kvm", "desktop_lock_mode"},
    "tooling": {"install_agents", "gws", "secrets_provider", "antidote_ref"},
    "source_control": {"gitlab_host", "gitlab_identity", "github_host", "github_identity"},
    "model_auth": {"codex", "claude", "grok"},
    "security": {"disk_encryption_required", "remote_scope", "endpoint_management"},
    "resources": {"policy"},
    "backup": {"target", "retention_days"},
    "maintenance": {"timezone", "update_window", "owner"},
}
READY_FIELDS = {
    "remote.kvm",
    "remote.tailscale_tailnet",
    "remote.desktop_lock_mode",
    "tooling.gws",
    "tooling.secrets_provider",
    "tooling.antidote_ref",
    "security.endpoint_management",
    "backup.target",
    "maintenance.update_window",
    "maintenance.owner",
}


@dataclass(frozen=True)
class Issue:
    path: str
    message: str


@dataclass(frozen=True)
class Phase:
    name: str
    command: tuple[str, ...] | None
    privileged: bool
    note: str


def load_profile(path: Path) -> dict[str, Any]:
    try:
        with path.open("rb") as handle:
            value = tomllib.load(handle)
    except (OSError, tomllib.TOMLDecodeError) as exc:
        raise ValueError(f"cannot load {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ValueError("profile root must be a TOML table")
    return value


def nested(profile: dict[str, Any], dotted: str) -> Any:
    value: Any = profile
    for part in dotted.split("."):
        if not isinstance(value, dict) or part not in value:
            return None
        value = value[part]
    return value


def one_of(value: Any, choices: set[str]) -> bool:
    """Return false, rather than raising, for malformed non-string values."""
    return isinstance(value, str) and value in choices


def validate_profile(profile: dict[str, Any], *, ready: bool) -> list[Issue]:
    issues: list[Issue] = []

    unknown_top = sorted(set(profile) - TOP_LEVEL)
    for key in unknown_top:
        issues.append(Issue(key, "unknown top-level key"))

    if type(profile.get("schema_version")) is not int or profile.get("schema_version") != 1:
        issues.append(Issue("schema_version", "must be integer 1"))
    if not one_of(profile.get("profile"), {"work", "personal"}):
        issues.append(Issue("profile", "must be work or personal"))
    if not one_of(profile.get("state"), {"draft", "approved"}):
        issues.append(Issue("state", "must be draft or approved"))

    for section, allowed in SECTION_KEYS.items():
        value = profile.get(section)
        if not isinstance(value, dict):
            issues.append(Issue(section, "must be a TOML table"))
            continue
        for key in sorted(set(value) - allowed):
            issues.append(Issue(f"{section}.{key}", "unknown key"))
        for key in sorted(allowed - set(value)):
            issues.append(Issue(f"{section}.{key}", "required key is missing"))

    if issues:
        return issues

    machine = profile["machine"]
    accounts = profile["accounts"]
    remote = profile["remote"]
    tooling = profile["tooling"]
    scm = profile["source_control"]
    auth = profile["model_auth"]
    security = profile["security"]
    resources = profile["resources"]
    backup = profile["backup"]
    maintenance = profile["maintenance"]

    for path in ("machine.id", "machine.role"):
        value = nested(profile, path)
        if not isinstance(value, str) or not IDENTIFIER.fullmatch(value):
            issues.append(Issue(path, "must be a non-empty portable identifier"))
    if not one_of(machine["platform"], {"linux", "macos"}):
        issues.append(Issue("machine.platform", "must be linux or macos"))
    expected_os = {"linux": "ubuntu", "macos": "macos"}.get(machine["platform"]) if isinstance(machine["platform"], str) else None
    if machine["os_family"] != expected_os:
        issues.append(Issue("machine.os_family", f"must be {expected_os!r} for selected platform"))

    for key in ("agent",):
        if not isinstance(accounts[key], str) or not UNIX_NAME.fullmatch(accounts[key]):
            issues.append(Issue(f"accounts.{key}", "must be a valid Unix account name"))
    if isinstance(accounts["agent"], str) and not accounts["agent"].startswith("agt-"):
        issues.append(Issue("accounts.agent", "must use the agt-* convention"))

    account_lists: dict[str, list[str]] = {}
    for key in ("humans", "admins", "operators", "viewers", "ssh_users"):
        values = accounts[key]
        if not isinstance(values, list) or any(type(item) is not str for item in values):
            issues.append(Issue(f"accounts.{key}", "must be an array of strings"))
            continue
        account_lists[key] = values
        if len(values) != len(set(values)):
            issues.append(Issue(f"accounts.{key}", "contains duplicate names"))
        for value in values:
            if not UNIX_NAME.fullmatch(value):
                issues.append(Issue(f"accounts.{key}", f"invalid Unix name: {value}"))
    if not account_lists.get("humans"):
        issues.append(Issue("accounts.humans", "must contain at least one named human"))
    if any(not item.startswith("adm-") for item in account_lists.get("admins", [])):
        issues.append(Issue("accounts.admins", "every administrator must use the adm-* convention"))
    humans = set(account_lists.get("humans", []))
    admins = set(account_lists.get("admins", []))
    for key in ("operators", "viewers"):
        extra = set(account_lists.get(key, [])) - humans
        if extra:
            issues.append(Issue(f"accounts.{key}", f"must be named humans; unexpected: {sorted(extra)}"))
    extra_ssh = set(account_lists.get("ssh_users", [])) - (humans | admins)
    if extra_ssh:
        issues.append(Issue("accounts.ssh_users", f"must be declared human/admin accounts: {sorted(extra_ssh)}"))
    if not account_lists.get("ssh_users"):
        issues.append(Issue("accounts.ssh_users", "must contain at least one named remote-recovery user"))
    elif not (set(account_lists["ssh_users"]) & admins):
        issues.append(Issue("accounts.ssh_users", "must include at least one declared administrator for recovery"))
    all_people = humans | admins
    overlap = humans & admins
    if overlap:
        issues.append(Issue("accounts", f"daily human and administrator accounts must be separate: {sorted(overlap)}"))
    if isinstance(accounts["agent"], str) and accounts["agent"] in all_people:
        issues.append(Issue("accounts.agent", "must be separate from every human/admin account"))

    if not isinstance(remote["nomachine_port"], int) or isinstance(remote["nomachine_port"], bool) or not 1 <= remote["nomachine_port"] <= 65535:
        issues.append(Issue("remote.nomachine_port", "must be an integer from 1 through 65535"))
    if not one_of(remote["desktop_lock_mode"], {"ask", "dedicated-shared", "locked"}):
        issues.append(Issue("remote.desktop_lock_mode", "must be ask, dedicated-shared, or locked"))
    if not isinstance(remote["tailscale_tags"], list) or not remote["tailscale_tags"] or any(
        not isinstance(tag, str) or not tag.startswith("tag:") for tag in remote["tailscale_tags"]
    ):
        issues.append(Issue("remote.tailscale_tags", "must contain at least one tag:* string"))

    if tooling["install_agents"] is not True:
        issues.append(
            Issue(
                "tooling.install_agents",
                "must be true; Codex, Claude Code, and Grok Build are required by this baseline",
            )
        )
    if not one_of(tooling["gws"], {"ask", "install", "skip"}):
        issues.append(Issue("tooling.gws", "must be ask, install, or skip"))
    if not one_of(tooling["secrets_provider"], {"ask", "1password", "bitwarden", "both", "organization-vault"}):
        issues.append(Issue("tooling.secrets_provider", "unsupported selection"))
    if tooling["antidote_ref"] != "ask" and (
        not isinstance(tooling["antidote_ref"], str) or not RELEASE_REF.fullmatch(tooling["antidote_ref"])
    ):
        issues.append(Issue("tooling.antidote_ref", "must be ask or a reviewed release ref"))

    if not one_of(scm["gitlab_identity"], {"service-account", "none"}):
        issues.append(Issue("source_control.gitlab_identity", "must be service-account or none"))
    if not one_of(scm["github_identity"], {"app", "machine-user", "none"}):
        issues.append(Issue("source_control.github_identity", "must be app, machine-user, or none"))
    if scm["gitlab_identity"] == "none" and scm["github_identity"] == "none":
        issues.append(Issue("source_control", "at least one source-control identity is required"))
    for key in ("gitlab_host", "github_host"):
        if not isinstance(scm[key], str) or not scm[key] or "://" in scm[key] or "/" in scm[key]:
            issues.append(Issue(f"source_control.{key}", "must be a hostname without a URL scheme/path"))

    allowed_auth = {"api-workload", "enterprise-federated", "named-human", "none"}
    for key in ("codex", "claude", "grok"):
        if not one_of(auth[key], allowed_auth):
            issues.append(Issue(f"model_auth.{key}", "unsupported authentication mode"))
        if profile["profile"] == "work" and auth[key] == "named-human":
            issues.append(Issue(f"model_auth.{key}", "work shared homes require a workload/enterprise identity"))
        if len(humans) > 1 and auth[key] == "named-human":
            issues.append(Issue(f"model_auth.{key}", "a personal identity cannot be shared by multiple operators"))

    if type(security["disk_encryption_required"]) is not bool or not security["disk_encryption_required"]:
        issues.append(Issue("security.disk_encryption_required", "must be true for this baseline"))
    if security["remote_scope"] != "tailscale-only":
        issues.append(Issue("security.remote_scope", "must be tailscale-only"))
    if not one_of(security["endpoint_management"], {"ask", "mdm", "edr", "mdm-and-edr", "not-required"}):
        issues.append(Issue("security.endpoint_management", "unsupported selection"))
    if resources["policy"] != "balanced":
        issues.append(Issue("resources.policy", "only the measured balanced policy is supported"))
    if not isinstance(backup["retention_days"], int) or isinstance(backup["retention_days"], bool) or backup["retention_days"] < 1:
        issues.append(Issue("backup.retention_days", "must be a positive integer"))
    for path, value in (("maintenance.timezone", maintenance["timezone"]), ("maintenance.update_window", maintenance["update_window"]), ("maintenance.owner", maintenance["owner"]), ("backup.target", backup["target"]), ("remote.kvm", remote["kvm"]), ("remote.tailscale_tailnet", remote["tailscale_tailnet"])):
        if not isinstance(value, str) or not value.strip():
            issues.append(Issue(path, "must be a non-empty string"))
    if isinstance(maintenance["timezone"], str) and maintenance["timezone"].strip():
        try:
            ZoneInfo(maintenance["timezone"])
        except (ZoneInfoNotFoundError, ValueError):
            issues.append(Issue("maintenance.timezone", "must be a recognized IANA timezone"))

    if ready:
        if profile["state"] != "approved":
            issues.append(Issue("state", "must be approved before apply/audit"))
        for path in sorted(READY_FIELDS):
            if nested(profile, path) == "ask":
                issues.append(Issue(path, "must be resolved before apply/audit"))

    return issues


def show_issues(issues: list[Issue]) -> None:
    for issue in issues:
        print(f"ERROR {issue.path}: {issue.message}", file=sys.stderr)


def phase_for(profile: dict[str, Any], name: str, *, apply: bool, recovery: bool) -> Phase:
    machine = profile["machine"]
    accounts = profile["accounts"]
    remote = profile["remote"]
    tooling = profile["tooling"]
    suffix = ["--apply"] if apply else []

    if name == "base":
        script = "bootstrap-linux.sh" if machine["platform"] == "linux" else "bootstrap-macos.sh"
        return Phase(name, (str(ROOT / "scripts" / script), *suffix), machine["platform"] == "linux", "Base packages; external authentication remains manual.")
    if name == "accounts":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "Create macOS accounts through the documented human/MDM workflow.")
        argv = [str(ROOT / "scripts/setup-accounts-linux.sh"), "--agent", accounts["agent"]]
        for key, flag in (("humans", "--human"), ("admins", "--admin"), ("operators", "--operator"), ("viewers", "--viewer")):
            for value in accounts[key]:
                argv.extend((flag, value))
        argv.extend(suffix)
        return Phase(name, tuple(argv), True, "Creates declared OS accounts/groups; passwords and keys remain human-only.")
    if name == "remote-hardening":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "Use the documented macOS/MDM remote-access workflow.")
        argv = [str(ROOT / "scripts/harden-remote-access-linux.sh"), "--agent", accounts["agent"], "--nomachine-port", str(remote["nomachine_port"])]
        for value in accounts["ssh_users"]:
            argv.extend(("--ssh-user", value))
        if apply and recovery:
            argv.append("--confirm-recovery-tested")
        argv.extend(suffix)
        return Phase(name, tuple(argv), True, "Requires active Tailscale, named-user keys, and open console/KVM recovery.")
    if name == "agentctl":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "agentctl is Linux-only; use the owning agt-* macOS desktop.")
        command = (str(ROOT / "scripts/install-agentctl-linux.sh"), "--agent", accounts["agent"], "--target", machine["id"], *suffix)
        return Phase(name, command, True, "Installs the validated terminal broker and scoped sudoers entry.")
    if name == "shell":
        command = (str(ROOT / "scripts/install-shell-baseline.sh"), "--antidote-ref", tooling["antidote_ref"], *suffix)
        return Phase(name, command, False, "Run as the declared agt-* account; preserves existing dotfiles.")
    if name == "user-tooling":
        argv = [str(ROOT / "scripts/install-user-tooling.sh")]
        if tooling["install_agents"]:
            argv.append("--agents")
        if tooling["gws"] == "install":
            argv.append("--gws")
        argv.extend(suffix)
        return Phase(name, tuple(argv), False, "Run as the declared agt-* account; authentication is separate.")
    if name == "workloads":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "Validate Chrome, OrbStack/Docker, and project-owned Playwright browsers through the macOS guide.")
        command = (str(ROOT / "scripts/install-workloads-linux.sh"), "--agent", accounts["agent"], *suffix)
        return Phase(name, command, True, "Installs rootless container compatibility, Chromium, Xvfb, and browser libraries; project Playwright versions remain project-owned.")
    if name == "resources":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "Measure and apply macOS controls through the documented managed workflow.")
        command = (str(ROOT / "scripts/apply-resource-policy-linux.sh"), "--agent", accounts["agent"], *suffix)
        return Phase(name, command, True, "Apply only after observation; validates cgroup placement during audit.")
    if name == "audit":
        command = (str(ROOT / "scripts/validate-host.sh"), accounts["agent"], str(remote["nomachine_port"]))
        return Phase(name, command, True, "Run from a named administrator; compares accounts, then runs live host/security validation.")
    raise ValueError(f"unknown phase: {name}")


PHASES = ("base", "accounts", "remote-hardening", "agentctl", "shell", "user-tooling", "workloads", "resources", "audit")


def account_groups(account: str) -> set[str]:
    result = subprocess.run(["id", "-nG", account], check=False, text=True, capture_output=True)
    return set(result.stdout.split()) if result.returncode == 0 else set()


def audit_declared_accounts(profile: dict[str, Any]) -> int:
    accounts = profile["accounts"]
    failures = 0

    def check(condition: bool, message: str) -> None:
        nonlocal failures
        print(("PASS " if condition else "FAIL ") + message)
        failures += 0 if condition else 1

    declared = [accounts["agent"], *accounts["humans"], *accounts["admins"]]
    for account in declared:
        try:
            entry = pwd.getpwnam(account)
        except KeyError:
            check(False, f"declared account {account} exists")
            continue
        home = Path(entry.pw_dir)
        check(home.is_dir(), f"{account} has a home directory")
        if home.is_dir():
            check(home.stat().st_uid == entry.pw_uid, f"{account} owns its home directory")
        check(entry.pw_shell.endswith("zsh"), f"{account} uses zsh")

    admin_group = "sudo" if profile["machine"]["platform"] == "linux" else "admin"
    for account in accounts["admins"]:
        check(admin_group in account_groups(account), f"{account} belongs to {admin_group}")
    for account in accounts["humans"]:
        check(admin_group not in account_groups(account), f"daily human {account} is not an administrator")

    agent_groups = account_groups(accounts["agent"])
    for group_name in (admin_group, "docker"):
        check(group_name not in agent_groups, f"{accounts['agent']} is not in {group_name}")

    if profile["machine"]["platform"] == "linux":
        operator_group = f"{accounts['agent']}-operators"
        viewer_group = f"{accounts['agent']}-viewers"
        try:
            grp.getgrnam(operator_group)
            operator_exists = True
        except KeyError:
            operator_exists = False
        try:
            grp.getgrnam(viewer_group)
            viewer_exists = True
        except KeyError:
            viewer_exists = False
        check(operator_exists, f"group {operator_group} exists")
        check(viewer_exists, f"group {viewer_group} exists")
        for account in accounts["operators"]:
            check(operator_group in account_groups(account), f"{account} is an authorized operator")
        for account in accounts["viewers"]:
            check(viewer_group in account_groups(account), f"{account} is an authorized viewer")
        if os.geteuid() == 0 and shutil.which("sshd"):
            sshd_result = subprocess.run(["sshd", "-T"], check=False, text=True, capture_output=True)
            effective_allowusers: set[str] = set()
            for line in sshd_result.stdout.splitlines():
                fields = line.split()
                if fields and fields[0] == "allowusers":
                    effective_allowusers.update(fields[1:])
            check(sshd_result.returncode == 0, "effective sshd policy can be read")
            check(effective_allowusers == set(accounts["ssh_users"]), "sshd AllowUsers exactly matches accounts.ssh_users")
        else:
            check(False, "profile audit requires root and sshd to compare effective AllowUsers")
    return failures


def actual_platform() -> str:
    if sys.platform.startswith("linux"):
        return "linux"
    if sys.platform == "darwin":
        return "macos"
    return "unsupported"


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate and apply agent-fleet onboarding profiles.")
    parser.add_argument("--version", action="version", version=f"%(prog)s {VERSION}")
    subparsers = parser.add_subparsers(dest="command", required=True)

    validate_parser = subparsers.add_parser("validate", help="validate profile structure and policy")
    validate_parser.add_argument("profile", type=Path)
    validate_parser.add_argument("--ready", action="store_true", help="also require all apply-time decisions")

    plan_parser = subparsers.add_parser("plan", help="render the ordered phase plan")
    plan_parser.add_argument("profile", type=Path)

    run_parser = subparsers.add_parser("run", help="run exactly one phase")
    run_parser.add_argument("profile", type=Path)
    run_parser.add_argument("phase", choices=PHASES)
    run_parser.add_argument("--apply", action="store_true")
    run_parser.add_argument("--confirm-recovery-tested", action="store_true")

    args = parser.parse_args()
    try:
        profile = load_profile(args.profile)
    except ValueError as exc:
        print(f"ERROR profile: {exc}", file=sys.stderr)
        return 2

    require_ready = bool(getattr(args, "ready", False) or getattr(args, "apply", False) or getattr(args, "phase", "") == "audit")
    issues = validate_profile(profile, ready=require_ready)
    if issues:
        show_issues(issues)
        return 2
    print(f"PASS profile {args.profile} ({profile['profile']}, {profile['machine']['platform']}, state={profile['state']})")

    if args.command == "validate":
        return 0

    if args.command == "plan":
        for name in PHASES:
            phase = phase_for(profile, name, apply=False, recovery=False)
            authority = "human/privileged" if phase.privileged else ("agt-* user" if name in {"shell", "user-tooling"} else "named package owner")
            command = shlex.join(phase.command) if phase.command else "MANUAL"
            print(f"\n{name}\n  owner:   {authority}\n  preview: {command}\n  note:    {phase.note}")
        return 0

    if profile["machine"]["platform"] != actual_platform():
        print(f"ERROR machine.platform: profile is {profile['machine']['platform']}, host is {actual_platform()}", file=sys.stderr)
        return 2

    if args.phase == "remote-hardening" and args.apply and not args.confirm_recovery_tested:
        print("ERROR remote-hardening: --apply also requires --confirm-recovery-tested", file=sys.stderr)
        return 2
    if args.phase in {"shell", "user-tooling"} and pwd.getpwuid(os.getuid()).pw_name != profile["accounts"]["agent"]:
        print(f"ERROR {args.phase}: run this phase as {profile['accounts']['agent']}, not root or a human account", file=sys.stderr)
        return 2

    phase = phase_for(profile, args.phase, apply=args.apply, recovery=args.confirm_recovery_tested)
    print(f"PHASE {phase.name}: {phase.note}")
    if phase.command is None:
        print("MANUAL phase: follow the applicable guide; no generic command will be executed.")
        return 0
    if args.phase == "audit":
        account_failures = audit_declared_accounts(profile)
        result = subprocess.run(phase.command, check=False)
        return 1 if account_failures or result.returncode else 0
    print(f"EXEC {shlex.join(phase.command)}")
    return subprocess.run(phase.command, check=False).returncode


if __name__ == "__main__":
    raise SystemExit(main())
