#!/usr/bin/env python3
"""Create, validate, plan, and apply non-secret workstation profiles.

Profiles are data, never shell code. This controller uses only the Python
standard library, passes argument arrays to subprocesses, and never handles
credentials. Mutating phases remain preview-only unless explicitly approved.
"""

from __future__ import annotations

import argparse
import fcntl
import grp
import ipaddress
import math
import os
import pwd
import re
import shlex
import shutil
import socket
import stat
import subprocess
import sys

# Readiness and planning are read-only: never write bytecode caches.
sys.dont_write_bytecode = True
import tomllib
import unicodedata
import uuid
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError


ROOT = Path(__file__).resolve().parents[1]


def load_version(root: Path = ROOT) -> str:
    try:
        value = (root / "VERSION").read_text(encoding="utf-8").strip()
    except OSError:
        return "0.0.0+unknown"
    return value or "0.0.0+unknown"


VERSION = load_version()
SCHEMA_VERSION = 3
UNIX_NAME = re.compile(r"^[a-z_][a-z0-9_-]{0,30}$")
HOSTNAME = re.compile(r"^(?P<namespace>[a-z0-9]{2,8})-(?P<class>ws|mac|hv|vws|nas|mgmt|srv)-[0-9]{3}$")
RELEASE_REF = re.compile(r"^[A-Za-z0-9._/-]+$")

TOP_LEVEL = {
    "schema_version", "profile", "state", "deployment", "machine", "accounts",
    "remote", "tooling", "source_control", "collaboration", "model_auth", "security", "resources",
    "backup", "maintenance",
}
SECTION_KEYS = {
    "deployment": {"namespace", "context", "ownership"},
    "machine": {"hostname", "display_name", "uuid", "asset_tag", "platform", "os_family", "hardware_profile", "role"},
    "accounts": {"agent", "humans", "admins", "admin_assignments", "services", "operators", "viewers", "ssh_users"},
    "remote": {"tailscale_tailnet", "tailscale_tags", "nomachine_port", "kvm", "preferred_kvm", "fallback_kvm", "desktop_lock_mode"},
    "tooling": {"install_agents", "gws", "secrets_provider", "antidote_ref"},
    "source_control": {"gitlab_host", "gitlab_identity", "gitlab_principal", "github_host", "github_identity", "github_principal"},
    "collaboration": {"atlassian_site", "atlassian_identity", "atlassian_principal", "atlassian_mcp_auth"},
    "model_auth": {"codex", "claude", "grok"},
    "security": {"disk_encryption_required", "secure_boot_required", "remote_scope", "endpoint_management"},
    "resources": {"policy", "os_cpu_reserve_threads"},
    "backup": {"target", "retention_days"},
    "maintenance": {"timezone", "update_window", "owner"},
}
OPTIONAL_KEYS = {
    "tooling": {"apt_packages", "brew_formulae", "brew_casks", "mise_version", "mise_sha256"},
    "accounts": {"macos_provisioning"},
    # os_memory_reserve_gib is deprecated: still accepted, no longer used.
    "resources": {
        "os_memory_reserve_percent",
        "os_memory_reserve_gib",
        "reserve_fixed_gib",
        "reserve_cores",
        "cpu_weight",
    },
}
for _section, _keys in OPTIONAL_KEYS.items():
    SECTION_KEYS[_section] |= _keys

READY_FIELDS = {
    "machine.asset_tag", "remote.tailscale_tailnet", "remote.desktop_lock_mode",
    "tooling.gws", "tooling.secrets_provider", "tooling.antidote_ref",
    "source_control.gitlab_principal", "source_control.github_principal",
    "collaboration.atlassian_site", "collaboration.atlassian_identity",
    "collaboration.atlassian_principal", "collaboration.atlassian_mcp_auth",
    "security.endpoint_management", "backup.target", "maintenance.update_window",
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
    return isinstance(value, str) and value in choices


def valid_uuid4(value: Any) -> bool:
    if not isinstance(value, str):
        return False
    try:
        parsed = uuid.UUID(value)
    except (ValueError, AttributeError):
        return False
    return parsed.version == 4 and str(parsed) == value


def valid_display_name(value: Any) -> bool:
    """Accept a readable ASCII label without mixed-script lookalikes."""
    return (
        isinstance(value, str)
        and value == value.strip()
        and "  " not in value
        and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}", value) is not None
    )


def comparison_key(value: str) -> str:
    """Normalize a human-assigned identifier for fleet-wide comparison."""
    normalized = unicodedata.normalize("NFKC", value).strip()
    return re.sub(r"\s+", " ", normalized).casefold()


def valid_plain_value(value: Any, *, max_length: int = 128) -> bool:
    return (
        isinstance(value, str)
        and value == value.strip()
        and not value.startswith("-")
        and 1 <= len(value) <= max_length
        and value == unicodedata.normalize("NFKC", value)
        and all(
            character.isprintable() and not unicodedata.category(character).startswith("C")
            for character in value
        )
    )


def looks_like_credential(value: str) -> bool:
    """Reject recognizable provider-secret prefixes from non-secret profiles."""
    lowered = value.casefold()
    prefixes = (
        "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
        "glpat-", "glptt-", "glcbt-", "gldt-", "glrt-", "atatt",
        "sk-", "sk-ant-", "xai-",
    )
    return lowered.startswith(prefixes)


def valid_principal(value: Any) -> bool:
    return (
        isinstance(value, str)
        and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{1,99}", value) is not None
        and not looks_like_credential(value)
    )


def valid_named_human_principal(value: Any) -> bool:
    if valid_principal(value):
        return True
    if not isinstance(value, str) or len(value) > 254 or looks_like_credential(value):
        return False
    return re.fullmatch(
        r"[A-Za-z0-9][A-Za-z0-9._%+-]{0,63}@[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?",
        value,
    ) is not None


def validate_profile(profile: dict[str, Any], *, ready: bool) -> list[Issue]:
    issues: list[Issue] = []
    for key in sorted(set(profile) - TOP_LEVEL):
        issues.append(Issue(key, "unknown top-level key"))
    if type(profile.get("schema_version")) is not int or profile.get("schema_version") != SCHEMA_VERSION:
        issues.append(Issue("schema_version", f"must be integer {SCHEMA_VERSION}"))
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
        for key in sorted(allowed - set(value) - OPTIONAL_KEYS.get(section, set())):
            issues.append(Issue(f"{section}.{key}", "required key is missing"))
    if issues:
        return issues

    deployment = profile["deployment"]
    machine = profile["machine"]
    accounts = profile["accounts"]
    remote = profile["remote"]
    tooling = profile["tooling"]
    scm = profile["source_control"]
    collaboration = profile["collaboration"]
    auth = profile["model_auth"]
    security = profile["security"]
    resources = profile["resources"]
    backup = profile["backup"]
    maintenance = profile["maintenance"]

    sys.path.insert(0, str(ROOT / "scripts/lib"))
    from package_setup import valid_package
    for key, kind, platform in (("apt_packages", "apt", "linux"), ("brew_formulae", "formulae", "macos"), ("brew_casks", "casks", "macos")):
        values = tooling.get(key, [])
        if not isinstance(values, list) or not all(valid_package(value, kind) for value in values):
            issues.append(Issue(f"tooling.{key}", "must be a list of approved package names (no options, URLs or third-party taps)"))
        elif len(values) != len(set(values)):
            issues.append(Issue(f"tooling.{key}", "duplicate package selections"))
        elif values and machine["platform"] != platform:
            issues.append(Issue(f"tooling.{key}", f"requires platform {platform}"))
    if "mise_version" in tooling or "mise_sha256" in tooling:
        if not isinstance(tooling.get("mise_version"), str) or not re.fullmatch(r"202[6-9]\.\d{1,2}\.\d{1,2}", tooling.get("mise_version", "")):
            issues.append(Issue("tooling.mise_version", "select an explicit reviewed version with its binary digest"))
        if not isinstance(tooling.get("mise_sha256"), str) or not re.fullmatch(r"[a-f0-9]{64}", tooling.get("mise_sha256", "")):
            issues.append(Issue("tooling.mise_sha256", "requires the independently reviewed official platform binary SHA-256"))
        if machine["platform"] == "macos":
            issues.append(Issue("tooling.mise_version", "macOS baseline uses Homebrew mise; do not select a second installation method"))
    if "macos_provisioning" in accounts:
        if machine["platform"] != "macos" or not one_of(accounts["macos_provisioning"], {"local", "managed"}):
            issues.append(Issue("accounts.macos_provisioning", "requires macOS and local or managed selection"))

    namespace = deployment["namespace"]
    if not isinstance(namespace, str) or not re.fullmatch(r"[a-z0-9]{2,8}", namespace):
        issues.append(Issue("deployment.namespace", "must be 2-8 lowercase letters/digits"))
    if deployment["context"] != profile["profile"]:
        issues.append(Issue("deployment.context", "must match profile"))
    if not one_of(deployment["ownership"], {"individual", "organization"}):
        issues.append(Issue("deployment.ownership", "must be individual or organization"))
    if profile["profile"] == "work" and deployment["ownership"] != "organization":
        issues.append(Issue("deployment.ownership", "work profiles must be organization-owned"))

    match = HOSTNAME.fullmatch(machine["hostname"]) if isinstance(machine["hostname"], str) else None
    if not match:
        issues.append(Issue("machine.hostname", "must be <namespace>-<class>-<NNN>, for example acme-ws-001"))
    elif match.group("namespace") != namespace:
        issues.append(Issue("machine.hostname", "namespace prefix must match deployment.namespace"))
    if not valid_display_name(machine["display_name"]):
        issues.append(
            Issue(
                "machine.display_name",
                "must be 1-64 trimmed ASCII letters, digits, single spaces, dots, underscores, or hyphens and start with a letter or digit",
            )
        )
    if not valid_uuid4(machine["uuid"]):
        issues.append(Issue("machine.uuid", "must be a canonical UUIDv4 generated once per machine"))
    for key in ("asset_tag", "hardware_profile", "role"):
        if not valid_plain_value(machine[key]):
            issues.append(Issue(f"machine.{key}", "must be a canonical, trimmed printable value and cannot start with '-'"))
    if not one_of(machine["platform"], {"linux", "macos"}):
        issues.append(Issue("machine.platform", "must be linux or macos"))
    expected_os = {"linux": "ubuntu", "macos": "macos"}.get(machine["platform"]) if isinstance(machine["platform"], str) else None
    if machine["os_family"] != expected_os:
        issues.append(Issue("machine.os_family", f"must be {expected_os!r} for selected platform"))
    if match and machine["platform"] == "macos" and match.group("class") != "mac":
        issues.append(Issue("machine.hostname", "macOS hosts must use the mac class"))
    if match and machine["platform"] == "linux" and match.group("class") == "mac":
        issues.append(Issue("machine.hostname", "Linux hosts cannot use the mac class"))

    agent = accounts["agent"]
    if not isinstance(agent, str) or not UNIX_NAME.fullmatch(agent):
        issues.append(Issue("accounts.agent", "must be a valid Unix account name"))
    elif not re.fullmatch(r"agent-[0-9]{2}", agent):
        issues.append(Issue("accounts.agent", "must use agent-NN, for example agent-01"))

    lists: dict[str, list[str]] = {}
    for key in ("humans", "admins", "services", "operators", "viewers", "ssh_users"):
        values = accounts[key]
        if not isinstance(values, list) or any(type(item) is not str for item in values):
            issues.append(Issue(f"accounts.{key}", "must be an array of strings"))
            continue
        lists[key] = values
        if len(values) != len(set(values)):
            issues.append(Issue(f"accounts.{key}", "contains duplicate names"))
        for value in values:
            if not UNIX_NAME.fullmatch(value):
                issues.append(Issue(f"accounts.{key}", f"invalid Unix name: {value}"))
    humans, admins = set(lists.get("humans", [])), set(lists.get("admins", []))
    if not humans:
        issues.append(Issue("accounts.humans", "must contain at least one named human"))
    if any(not re.fullmatch(r"admin-[0-9]{2}", value) for value in admins):
        issues.append(Issue("accounts.admins", "every administrator must use admin-NN"))
    if any(not value.startswith("svc-") for value in lists.get("services", [])):
        issues.append(Issue("accounts.services", "every service account must use svc-purpose"))
    if set(lists.get("services", [])) & (humans | admins):
        issues.append(Issue("accounts.services", "service identities must be separate from human/admin accounts"))
    for key in ("operators", "viewers"):
        extra = set(lists.get(key, [])) - humans
        if extra:
            issues.append(Issue(f"accounts.{key}", f"must be named humans; unexpected: {sorted(extra)}"))
    extra_ssh = set(lists.get("ssh_users", [])) - (humans | admins)
    if extra_ssh:
        issues.append(Issue("accounts.ssh_users", f"must be declared human/admin accounts: {sorted(extra_ssh)}"))
    if not lists.get("ssh_users") or not (set(lists.get("ssh_users", [])) & admins):
        issues.append(Issue("accounts.ssh_users", "must include at least one declared administrator for recovery"))
    if humans & admins:
        issues.append(Issue("accounts", f"daily and administrator accounts must be separate: {sorted(humans & admins)}"))
    if isinstance(agent, str) and agent in humans | admins | set(lists.get("services", [])):
        issues.append(Issue("accounts.agent", "must be separate from every human/admin/service account"))
    assignments = accounts["admin_assignments"]
    if not isinstance(assignments, dict) or any(type(k) is not str or type(v) is not str for k, v in assignments.items()):
        issues.append(Issue("accounts.admin_assignments", "must map each admin account to one named human"))
    else:
        if set(assignments) != admins:
            issues.append(Issue("accounts.admin_assignments", "keys must exactly match accounts.admins"))
        unknown_owners = set(assignments.values()) - humans
        if unknown_owners:
            issues.append(Issue("accounts.admin_assignments", f"owners must be declared humans: {sorted(unknown_owners)}"))
        if len(assignments.values()) != len(set(assignments.values())):
            issues.append(Issue("accounts.admin_assignments", "each admin must be assigned to a different human"))

    if not isinstance(remote["nomachine_port"], int) or isinstance(remote["nomachine_port"], bool) or not 1 <= remote["nomachine_port"] <= 65535:
        issues.append(Issue("remote.nomachine_port", "must be an integer from 1 through 65535"))
    if not one_of(remote["desktop_lock_mode"], {"ask", "dedicated-shared", "locked"}):
        issues.append(Issue("remote.desktop_lock_mode", "must be ask, dedicated-shared, or locked"))
    if not one_of(remote["kvm"], {"deferred", "installed", "not-required"}):
        issues.append(Issue("remote.kvm", "must be deferred, installed, or not-required"))
    for key in ("preferred_kvm", "fallback_kvm", "tailscale_tailnet"):
        if not isinstance(remote[key], str) or not remote[key].strip():
            issues.append(Issue(f"remote.{key}", "must be a non-empty inventory choice"))
    if not isinstance(remote["tailscale_tags"], list) or not remote["tailscale_tags"] or any(
        not isinstance(tag, str) or not tag.startswith("tag:") for tag in remote["tailscale_tags"]
    ):
        issues.append(Issue("remote.tailscale_tags", "must contain at least one tag:* string"))

    if tooling["install_agents"] is not True:
        issues.append(Issue("tooling.install_agents", "Codex, Claude Code, and Grok Build are required"))
    if not one_of(tooling["gws"], {"ask", "install", "skip"}):
        issues.append(Issue("tooling.gws", "must be ask, install, or skip"))
    if not one_of(tooling["secrets_provider"], {"ask", "1password", "bitwarden", "both", "organization-vault"}):
        issues.append(Issue("tooling.secrets_provider", "unsupported selection"))
    if tooling["antidote_ref"] != "ask" and (not isinstance(tooling["antidote_ref"], str) or tooling["antidote_ref"].startswith("-") or not RELEASE_REF.fullmatch(tooling["antidote_ref"])):
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
    for provider in ("gitlab", "github"):
        identity, principal = scm[f"{provider}_identity"], scm[f"{provider}_principal"]
        if identity == "none" and principal != "none":
            issues.append(Issue(f"source_control.{provider}_principal", "must be none when the provider identity is none"))
        if identity != "none" and (principal == "none" or not valid_principal(principal)):
            issues.append(Issue(f"source_control.{provider}_principal", "must be an approved non-secret principal label, not none or a URL"))

    if not one_of(collaboration["atlassian_identity"], {"ask", "service-account", "named-human", "none"}):
        issues.append(Issue("collaboration.atlassian_identity", "must be ask, service-account, named-human, or none"))
    if not one_of(collaboration["atlassian_mcp_auth"], {"ask", "service-account-api-key", "oauth-2.1", "none"}):
        issues.append(Issue("collaboration.atlassian_mcp_auth", "unsupported Atlassian MCP authentication mode"))
    atlassian_identity = collaboration["atlassian_identity"]
    if profile["profile"] == "work" and atlassian_identity == "named-human":
        issues.append(Issue("collaboration.atlassian_identity", "a shared work agent cannot use a named-human Atlassian identity"))
    if len(humans) > 1 and atlassian_identity == "named-human":
        issues.append(Issue("collaboration.atlassian_identity", "a multi-operator agent cannot use one person's Atlassian identity"))
    if atlassian_identity == "service-account" and collaboration["atlassian_mcp_auth"] != "service-account-api-key":
        issues.append(Issue("collaboration.atlassian_mcp_auth", "a service account must use service-account-api-key"))
    if atlassian_identity == "named-human" and collaboration["atlassian_mcp_auth"] != "oauth-2.1":
        issues.append(Issue("collaboration.atlassian_mcp_auth", "a named human must use oauth-2.1"))
    if atlassian_identity == "none" and (collaboration["atlassian_principal"] != "none" or collaboration["atlassian_mcp_auth"] != "none"):
        issues.append(Issue("collaboration", "an unused Atlassian integration must set principal and MCP auth to none"))
    atlassian_principal = collaboration["atlassian_principal"]
    if atlassian_identity == "named-human":
        atlassian_principal_ok = valid_named_human_principal(atlassian_principal)
    elif atlassian_identity == "service-account":
        atlassian_principal_ok = (
            valid_principal(atlassian_principal)
            and re.fullmatch(r"[A-Za-z0-9]{6,30}", atlassian_principal) is not None
        )
    else:
        atlassian_principal_ok = valid_principal(atlassian_principal)
    if atlassian_identity not in {"ask", "none"} and (
        atlassian_principal == "none" or not atlassian_principal_ok
    ):
        issues.append(Issue("collaboration.atlassian_principal", "must be a 6-30 character alphanumeric service-account name or, for named-human, an approved label/email address"))
    site = collaboration["atlassian_site"]
    if site not in {"ask", "none"} and (not isinstance(site, str) or not site or "://" in site or "/" in site):
        issues.append(Issue("collaboration.atlassian_site", "must be a hostname without a URL scheme/path"))
    if atlassian_identity == "none" and site != "none":
        issues.append(Issue("collaboration.atlassian_site", "must be none when Atlassian is unused"))
    if atlassian_identity not in {"ask", "none"} and site in {"ask", "none"}:
        issues.append(Issue("collaboration.atlassian_site", "an enabled Atlassian identity requires a site hostname"))

    allowed_auth = {"api-workload", "enterprise-federated", "named-human", "none"}
    for key in ("codex", "claude", "grok"):
        if not one_of(auth[key], allowed_auth):
            issues.append(Issue(f"model_auth.{key}", "unsupported authentication mode"))
        if profile["profile"] == "work" and auth[key] == "named-human":
            issues.append(Issue(f"model_auth.{key}", "work shared homes require a workload/enterprise identity"))
        if len(humans) > 1 and auth[key] == "named-human":
            issues.append(Issue(f"model_auth.{key}", "a personal identity cannot be shared by multiple operators"))

    for key in ("disk_encryption_required", "secure_boot_required"):
        if type(security[key]) is not bool or not security[key]:
            issues.append(Issue(f"security.{key}", "must be true for this baseline"))
    if security["remote_scope"] != "tailscale-only":
        issues.append(Issue("security.remote_scope", "must be tailscale-only"))
    if not one_of(security["endpoint_management"], {"ask", "mdm", "edr", "mdm-and-edr", "not-required"}):
        issues.append(Issue("security.endpoint_management", "unsupported selection"))
    if resources["policy"] != "measured-balanced":
        issues.append(Issue("resources.policy", "must be measured-balanced"))
    for key, lower, upper in (("os_memory_reserve_percent", 5, 50), ("os_memory_reserve_gib", 4, 32), ("os_cpu_reserve_threads", 1, 8)):
        if key not in resources:
            continue
        value = resources[key]
        if type(value) is not int or not lower <= value <= upper:
            issues.append(Issue(f"resources.{key}", f"must be an integer from {lower} through {upper}"))
    if "reserve_fixed_gib" in resources:
        value = resources["reserve_fixed_gib"]
        if type(value) not in (int, float) or not math.isfinite(value) or value <= 0:
            issues.append(Issue("resources.reserve_fixed_gib", "must be a positive number"))
        if "os_memory_reserve_percent" in resources:
            issues.append(Issue("resources.reserve_fixed_gib", "cannot be combined with resources.os_memory_reserve_percent"))
    if "reserve_cores" in resources:
        value = resources["reserve_cores"]
        if type(value) is not int or value < 0:
            issues.append(Issue("resources.reserve_cores", "must be an integer >= 0"))
    if "cpu_weight" in resources:
        value = resources["cpu_weight"]
        if type(value) is not int or not 1 <= value <= 10000:
            issues.append(Issue("resources.cpu_weight", "must be an integer from 1 through 10000"))
    if not isinstance(backup["retention_days"], int) or isinstance(backup["retention_days"], bool) or backup["retention_days"] < 1:
        issues.append(Issue("backup.retention_days", "must be a positive integer"))
    for path in ("maintenance.timezone", "maintenance.update_window", "maintenance.owner", "backup.target"):
        value = nested(profile, path)
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


def phase_for(
    profile: dict[str, Any],
    name: str,
    *,
    apply: bool,
    recovery: bool,
    connection_context: str | None = None,
    ssh_source_ip: str | None = None,
) -> Phase:
    machine, accounts, remote, tooling = (profile[k] for k in ("machine", "accounts", "remote", "tooling"))
    suffix = ["--apply"] if apply else []
    if name == "base":
        script = "bootstrap-linux.sh" if machine["platform"] == "linux" else "bootstrap-macos.sh"
        extras: list[str] = []
        for key, flag in (("apt_packages", "--apt"), ("brew_formulae", "--formula"), ("brew_casks", "--cask")):
            for package in tooling.get(key, []):
                extras.extend((flag, package))
        return Phase(name, (str(ROOT / "scripts" / script), *extras, *suffix), machine["platform"] == "linux", "Install missing baseline and selected packages; preserve existing versions; authentication remains separate.")
    if name == "identity":
        safety: list[str] = []
        if apply and recovery:
            safety.append("--confirm-recovery-tested")
        if apply and connection_context:
            safety.extend(("--connection-context", connection_context))
            if ssh_source_ip:
                safety.extend(("--ssh-source-ip", ssh_source_ip))
        command = (
            str(ROOT / "scripts/install-machine-identity.py"),
            "--hostname", machine["hostname"],
            "--display-name", machine["display_name"],
            "--uuid", machine["uuid"],
            "--asset-tag", machine["asset_tag"],
            "--namespace", profile["deployment"]["namespace"],
            "--platform", machine["platform"],
            "--role", machine["role"],
            *safety,
            *suffix,
        )
        return Phase(name, command, True, "Sets stable OS names and installs a root-owned, non-secret local identity record.")
    if name == "accounts":
        if machine["platform"] == "macos":
            argv = [str(ROOT / "scripts/setup-accounts-macos.sh"), "--agent", accounts["agent"], "--management", accounts.get("macos_provisioning", "managed")]
            for key, flag in (("humans", "--human"), ("admins", "--admin"), ("services", "--service")):
                for account in accounts[key]:
                    argv.extend((flag, account))
            return Phase(name, (*argv, *suffix), True, "Local opt-in creates missing accounts with hidden vendor prompts; managed mode verifies MDM-provisioned accounts and preserves roles.")
        argv = [str(ROOT / "scripts/setup-accounts-linux.sh"), "--agent", accounts["agent"]]
        for key, flag in (("humans", "--human"), ("admins", "--admin"), ("services", "--service"), ("operators", "--operator"), ("viewers", "--viewer")):
            for value in accounts[key]:
                argv.extend((flag, value))
        argv.extend(suffix)
        return Phase(name, tuple(argv), True, "Creates declared accounts/groups; passwords and keys remain human-only.")
    if name == "remote-hardening":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "Use the documented macOS/MDM remote-access workflow.")
        argv = [str(ROOT / "scripts/harden-remote-access-linux.sh"), "--agent", accounts["agent"], "--nomachine-port", str(remote["nomachine_port"])]
        for value in accounts["ssh_users"]:
            argv.extend(("--ssh-user", value))
        if apply and recovery:
            argv.append("--confirm-recovery-tested")
        if apply and connection_context:
            argv.extend(("--connection-context", connection_context))
            if ssh_source_ip:
                argv.extend(("--ssh-source-ip", ssh_source_ip))
        argv.extend(suffix)
        return Phase(name, tuple(argv), True, "Requires Tailscale, named-user keys, and tested console recovery; KVM may be deferred during a supervised pilot.")
    if name == "agentctl":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "agentctl is Linux-only; use the owning agent desktop on macOS.")
        command = (str(ROOT / "scripts/install-agentctl-linux.sh"), "--agent", accounts["agent"], "--target", machine["hostname"], *suffix)
        return Phase(name, command, True, "Installs the terminal broker and scoped sudoers entry.")
    if name == "shell":
        return Phase(name, (str(ROOT / "scripts/install-shell-baseline.sh"), "--antidote-ref", tooling["antidote_ref"], *suffix), False, "Run as the agent account; existing dotfiles are preserved.")
    if name == "user-tooling":
        argv = [str(ROOT / "scripts/install-user-tooling.sh"), "--agents"]
        if "mise_version" in tooling:
            argv.extend(("--mise-version", tooling["mise_version"], "--mise-sha256", tooling["mise_sha256"]))
        if tooling["gws"] == "install":
            argv.append("--gws")
        argv.extend(suffix)
        return Phase(name, tuple(argv), False, "Run as the agent account; authentication is a separate human ceremony.")
    if name == "workloads":
        if machine["platform"] == "macos":
            return Phase(name, (str(ROOT / "scripts/check-project-workloads.sh"), "--agent", accounts["agent"], "--start-orbstack", *suffix), False, "Run in the agent-owned session; verify OrbStack. The helper can install project-locked Playwright browsers and run a digest-pinned container smoke test.")
        return Phase(name, (str(ROOT / "scripts/install-workloads-linux.sh"), "--agent", accounts["agent"], *suffix), True, "Installs rootless containers, Chromium, Xvfb, and browser libraries.")
    if name == "resources":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "Measure and apply macOS controls through the documented managed workflow.")
        resources = profile["resources"]
        argv = [str(ROOT / "scripts/apply-resource-policy-linux.sh"), "--agent", accounts["agent"]]
        if "os_memory_reserve_percent" in resources:
            argv.extend(("--memory-reserve-percent", str(resources["os_memory_reserve_percent"])))
        if "reserve_fixed_gib" in resources:
            argv.extend(("--reserve-fixed-gib", str(resources["reserve_fixed_gib"])))
        if "reserve_cores" in resources:
            argv.extend(("--reserve-cores", str(resources["reserve_cores"])))
        if "cpu_weight" in resources:
            argv.extend(("--cpu-weight", str(resources["cpu_weight"])))
        argv.extend(suffix)
        return Phase(name, tuple(argv), True, "Apply only after observation and load testing; audit cgroup placement after reboot.")
    if name == "audit":
        return Phase(name, (str(ROOT / "scripts/validate-host.sh"), accounts["agent"], str(remote["nomachine_port"])), True, "Compare declared accounts and live security state.")
    raise ValueError(f"unknown phase: {name}")


PHASES = ("base", "identity", "accounts", "remote-hardening", "agentctl", "shell", "user-tooling", "workloads", "resources", "audit")


def account_groups(account: str) -> set[str]:
    result = subprocess.run(["id", "-nG", account], check=False, text=True, capture_output=True)
    return set(result.stdout.split()) if result.returncode == 0 else set()


def audit_declared_accounts(profile: dict[str, Any]) -> int:
    accounts, failures = profile["accounts"], 0
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
        if account == accounts["agent"]:
            # Existing human/admin accounts are preserved with their own shell.
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
        for suffix, people in (("operators", accounts["operators"]), ("viewers", accounts["viewers"])):
            group_name = f"{accounts['agent']}-{suffix}"
            try:
                grp.getgrnam(group_name)
                exists = True
            except KeyError:
                exists = False
            check(exists, f"group {group_name} exists")
            for account in people:
                check(group_name in account_groups(account), f"{account} is an authorized {suffix[:-1]}")
        if os.geteuid() == 0 and shutil.which("sshd"):
            result = subprocess.run(["sshd", "-T"], check=False, text=True, capture_output=True)
            effective: set[str] = set()
            for line in result.stdout.splitlines():
                fields = line.split()
                if fields and fields[0] == "allowusers":
                    effective.update(fields[1:])
            check(result.returncode == 0, "effective sshd policy can be read")
            check(effective == set(accounts["ssh_users"]), "sshd AllowUsers exactly matches accounts.ssh_users")
        else:
            check(False, "profile audit requires root and sshd to compare effective AllowUsers")
    print("MANUAL evidence required: Tailscale device identity/tags/grants and desktop lock behavior")
    print("MANUAL evidence required: endpoint management, backup/restore, and maintenance ownership/window")
    print("MANUAL evidence required: secret-provider, source-control, and model-provider identities")
    return failures


def local_identity_path(platform: str) -> Path:
    if platform == "linux":
        return Path("/etc/agent-workstation-kit/identity.toml")
    return Path("/Library/Application Support/Agent Workstation Kit/identity.toml")


def live_machine_names(platform: str) -> tuple[str | None, str | None, str | None]:
    def read(command: list[str]) -> str | None:
        try:
            result = subprocess.run(command, check=False, text=True, capture_output=True)
        except OSError:
            return None
        return result.stdout.strip() if result.returncode == 0 else None

    if platform == "linux":
        try:
            runtime_hostname = socket.gethostname().split(".", 1)[0]
        except OSError:
            runtime_hostname = None
        return (
            read(["hostnamectl", "--static"]) or runtime_hostname,
            read(["hostnamectl", "--pretty"]),
            runtime_hostname,
        )
    return (
        read(["scutil", "--get", "HostName"]) or socket.gethostname(),
        read(["scutil", "--get", "ComputerName"]),
        read(["scutil", "--get", "LocalHostName"]),
    )


def linux_hostname_resolves(hostname: str) -> bool:
    """Prove NSS resolves the hostname to this host or a loopback address."""
    try:
        resolved = subprocess.run(
            ["getent", "hosts", hostname],
            check=False,
            text=True,
            capture_output=True,
        )
        interfaces = subprocess.run(
            ["ip", "-o", "addr", "show"],
            check=False,
            text=True,
            capture_output=True,
        )
    except OSError:
        return False
    if resolved.returncode != 0 or interfaces.returncode != 0:
        return False

    local_addresses: set[ipaddress.IPv4Address | ipaddress.IPv6Address] = set()
    try:
        for line in interfaces.stdout.splitlines():
            fields = line.split()
            for family in ("inet", "inet6"):
                if family in fields:
                    local_addresses.add(ipaddress.ip_address(fields[fields.index(family) + 1].split("/", 1)[0]))
        resolved_addresses = [
            ipaddress.ip_address(line.split()[0])
            for line in resolved.stdout.splitlines()
            if line.split()
        ]
    except (ValueError, IndexError):
        return False
    return bool(resolved_addresses) and any(
        address.is_loopback or address in local_addresses
        for address in resolved_addresses
    )


def audit_machine_identity(profile: dict[str, Any]) -> int:
    machine, deployment, failures = profile["machine"], profile["deployment"], 0

    def check(condition: bool, message: str) -> None:
        nonlocal failures
        print(("PASS " if condition else "FAIL ") + message)
        failures += 0 if condition else 1

    target = local_identity_path(machine["platform"])
    record_ok = target.is_file() and not target.is_symlink()
    check(record_ok, f"local identity record exists as a regular, non-symlink file at {target}")
    parent = target.parent
    try:
        parent_stat = parent.stat()
        parent_ok = parent.is_dir() and not parent.is_symlink()
        parent_secure = (
            parent_stat.st_uid == 0
            and parent_stat.st_gid == 0
            and parent_stat.st_mode & 0o777 == 0o755
        )
    except OSError:
        parent_ok = parent_secure = False
    check(parent_ok and parent_secure, "local identity directory is root-owned mode 0755 and is not a symlink")
    if record_ok:
        try:
            with target.open("rb") as handle:
                document = tomllib.load(handle)
            identity = document.get("identity", {})
        except (OSError, tomllib.TOMLDecodeError):
            identity = {}
        expected = {
            "hostname": machine["hostname"],
            "display_name": machine["display_name"],
            "uuid": machine["uuid"],
            "asset_tag": machine["asset_tag"],
            "namespace": deployment["namespace"],
            "platform": machine["platform"],
            "role": machine["role"],
        }
        check(identity == expected, "local identity record exactly matches the approved profile")
        try:
            record_stat = target.stat()
            record_secure = (
                record_stat.st_uid == 0
                and record_stat.st_gid == 0
                and record_stat.st_mode & 0o777 == 0o644
            )
        except OSError:
            record_secure = False
        check(record_secure, "local identity record is root-owned mode 0644")
    live_hostname, live_display, live_local_hostname = live_machine_names(machine["platform"])
    check(live_hostname == machine["hostname"], "live technical hostname matches machine.hostname")
    check(live_display == machine["display_name"], "live pretty/computer name matches machine.display_name")
    if machine["platform"] == "macos":
        check(live_local_hostname == machine["hostname"], "live LocalHostName matches machine.hostname")
    else:
        check(live_local_hostname == machine["hostname"], "live kernel/runtime hostname matches machine.hostname")
        check(
            linux_hostname_resolves(machine["hostname"]),
            "machine.hostname resolves through host NSS to a local or loopback address",
        )
    return failures


def actual_platform() -> str:
    if sys.platform.startswith("linux"):
        return "linux"
    if sys.platform == "darwin":
        return "macos"
    return "unsupported"


def toml_string(value: str) -> str:
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'


def render_profile(args: argparse.Namespace) -> str:
    platform = args.platform
    namespace = args.namespace
    hostname = args.hostname
    people = args.human or ["operator"]
    admins = args.admin or ["admin-01"]
    assignments = {name: people[min(index, len(people) - 1)] for index, name in enumerate(admins)}
    arr = lambda values: "[" + ", ".join(toml_string(v) for v in values) + "]"
    mapping = "{ " + ", ".join(f"{toml_string(k)} = {toml_string(v)}" for k, v in assignments.items()) + " }"
    os_family = "ubuntu" if platform == "linux" else "macos"
    ownership = "organization" if args.context == "work" else "individual"
    endpoint = "ask" if args.context == "work" else "not-required"
    return f'''# Generated by fleetctl init. Contains desired state only; never add secrets.\n\
schema_version = {SCHEMA_VERSION}\nprofile = {toml_string(args.context)}\nstate = "draft"\n\n\
[deployment]\nnamespace = {toml_string(namespace)}\ncontext = {toml_string(args.context)}\nownership = {toml_string(ownership)}\n\n\
[machine]\nhostname = {toml_string(hostname)}\ndisplay_name = {toml_string(args.display_name or hostname)}\nuuid = {toml_string(str(uuid.uuid4()))}\nasset_tag = {toml_string(args.asset_tag)}\nplatform = {toml_string(platform)}\nos_family = {toml_string(os_family)}\nhardware_profile = {toml_string(args.hardware_profile)}\nrole = "agent-workstation"\n\n\
[accounts]\nagent = {toml_string(args.agent)}\nhumans = {arr(people)}\nadmins = {arr(admins)}\nadmin_assignments = {mapping}\nservices = []\noperators = {arr(people)}\nviewers = []\nssh_users = {arr([*people, *admins])}\n\n\
[remote]\ntailscale_tailnet = "ask"\ntailscale_tags = {arr([f"tag:{namespace}-workstation"])}\nnomachine_port = 4000\nkvm = "deferred"\npreferred_kvm = "glinet-comet-x-gl-rm4pe"\nfallback_kvm = "glinet-comet-poe-gl-rm1pe"\ndesktop_lock_mode = "ask"\n\n\
[tooling]\ninstall_agents = true\ngws = "ask"\nsecrets_provider = "ask"\nantidote_ref = "ask"\n\n\
[source_control]\ngitlab_host = "gitlab.com"\ngitlab_identity = "service-account"\ngitlab_principal = {toml_string(f"{namespace}-agent-dev")}\ngithub_host = "github.com"\ngithub_identity = "app"\ngithub_principal = {toml_string(f"{namespace}-agent-dev")}\n\n\
[collaboration]\natlassian_site = "ask"\natlassian_identity = "ask"\natlassian_principal = "ask"\natlassian_mcp_auth = "ask"\n\n\
[model_auth]\ncodex = "api-workload"\nclaude = "api-workload"\ngrok = "api-workload"\n\n\
[security]\ndisk_encryption_required = true\nsecure_boot_required = true\nremote_scope = "tailscale-only"\nendpoint_management = {toml_string(endpoint)}\n\n\
[resources]\npolicy = "measured-balanced"\nos_cpu_reserve_threads = 2\n\n\
[backup]\ntarget = "ask"\nretention_days = 30\n\n\
[maintenance]\ntimezone = "America/Toronto"\nupdate_window = "ask"\nowner = "ask"\n'''


def resolve_profile(path: Path, fleet_root: Path | None) -> Path:
    if path.is_absolute() or fleet_root is None:
        return path
    return fleet_root / path


def fleet_lock_issue(fleet_root: Path | None) -> str | None:
    """Return a fail-closed version-pin error for an external fleet."""
    if fleet_root is None:
        return None
    lock = fleet_root / "kit.lock"
    if not lock.is_file():
        return f"{lock} is missing"
    try:
        pinned = lock.read_text(encoding="utf-8").strip()
    except (OSError, UnicodeError) as exc:
        return f"cannot read {lock}: {exc}"
    if pinned != VERSION:
        return f"{lock} pins {pinned or '<empty>'}, but this toolkit is {VERSION}"
    return None


@contextmanager
def fleet_identity_lock(lock_path: Path):
    """Serialize identity allocation so concurrent init calls cannot collide."""
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    flags = os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(lock_path, flags, 0o600)
    with os.fdopen(descriptor, "a+", encoding="utf-8") as handle:
        metadata = os.fstat(handle.fileno())
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.geteuid():
            raise OSError(f"allocation lock must be a regular file owned by uid {os.geteuid()}: {lock_path}")
        os.fchmod(handle.fileno(), 0o600)
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def allocate_profile(args: argparse.Namespace, output: Path, scan_directory: Path) -> Issue | None:
    """Reserve a unique machine identity and create its draft atomically."""
    lock_path = (args.fleet_root if args.fleet_root else scan_directory) / ".fleetctl-identity.lock"
    with fleet_identity_lock(lock_path):
        nested_profiles = sorted(
            path for path in scan_directory.rglob("*.toml") if path.parent != scan_directory
        )
        if nested_profiles:
            names = ", ".join(str(path) for path in nested_profiles)
            return Issue("machine identity", f"nested profiles are unsupported and escape uniqueness checks: {names}")
        inferred_fleet_root = (
            scan_directory.parent
            if not args.fleet_root and scan_directory.name == "machines"
            else None
        )
        retirement_root = args.fleet_root or inferred_fleet_root
        if retirement_root:
            retired_path = retirement_root / "retired-hostnames.txt"
            try:
                retired = {
                    line.strip()
                    for line in retired_path.read_text(encoding="utf-8").splitlines()
                    if line.strip() and not line.lstrip().startswith("#")
                } if retired_path.is_file() else set()
            except OSError as exc:
                return Issue("machine.hostname", f"cannot prove retirement status from {retired_path}: {exc}")
            if args.hostname in retired:
                return Issue("machine.hostname", f"{args.hostname!r} is retired and cannot be reused")

        for sibling in sorted(scan_directory.glob("*.toml")):
            try:
                existing = load_profile(sibling)
                existing_hostname = existing["machine"]["hostname"]
                existing_display = existing["machine"]["display_name"]
            except (ValueError, KeyError, TypeError) as exc:
                return Issue("machine identity", f"cannot prove uniqueness because {sibling} is unreadable/incomplete: {exc}")
            if not isinstance(existing_hostname, str) or not valid_display_name(existing_display):
                return Issue("machine identity", f"cannot prove uniqueness because {sibling} has invalid hostname/display_name types or values")
            if existing_hostname == args.hostname:
                return Issue("machine.hostname", f"{args.hostname!r} duplicates the technical name in {sibling}")
            if comparison_key(existing_display) == comparison_key(args.display_name):
                return Issue("machine.display_name", f"{args.display_name!r} duplicates the assigned name in {sibling}")

        try:
            with output.open("x", encoding="utf-8") as handle:
                handle.write(render_profile(args))
        except FileExistsError:
            return Issue("output", f"refusing to overwrite {output}")
    return None


def private_root_issue(path: Path) -> str | None:
    """Why PATH cannot be a private fleet root, or None when it can."""
    sys.path.insert(0, str(ROOT / "scripts/lib"))
    from path_safety import outside_kit
    try:
        inside = not outside_kit(path, ROOT)
    except ValueError as error:
        return str(error)
    return "the private fleet root must be outside every public toolkit worktree" if inside else None


def setup_revision(root: Path, label: str, expected: str | None) -> str:
    def git(*arguments: str) -> str:
        result = subprocess.run(
            ["git", "--no-optional-locks", "-C", str(root), *arguments],
            capture_output=True, text=True, check=False, timeout=15,
        )
        if result.returncode:
            raise ValueError(f"{label}: Git {arguments[0]} failed at {root}; check path, ownership and Git availability")
        return result.stdout.rstrip("\n")

    if not (root / ".git").exists():
        raise ValueError(f"{label}: no Git metadata at {root}; use the reviewed source checkout")
    if Path(git("rev-parse", "--show-toplevel")).resolve() != root.resolve():
        raise ValueError(f"{label}: use the source checkout root at {root}")
    revision = git("rev-parse", "HEAD")
    if expected and revision != expected:
        raise ValueError(f"{label}: expected {expected}, found {revision}")
    if git("status", "--porcelain", "--untracked-files=all"):
        raise ValueError(f"{label}: review local changes or use a clean checkout of the reviewed revision")
    return revision


def guided_setup(args: argparse.Namespace, path: Path, profile: dict[str, Any]) -> int:
    fleet = args.fleet_root.expanduser().absolute()
    machine = profile["machine"]
    hostname = machine["hostname"]
    platform = machine["platform"]
    relative = Path("machines") / f"{hostname}.toml"
    handoff = Path("handoffs") / f"{hostname}-bootstrap.md"
    if path.absolute() != fleet / relative:
        raise ValueError(f"use the target profile at {relative} inside the fleet checkout")
    if platform == "macos" and args.expected_ubuntu:
        raise ValueError("--expected-ubuntu applies only to Linux profiles")
    ubuntu = args.expected_ubuntu or "24.04"
    launcher = "start-linux-pilot.py" if platform == "linux" else "start-macos-pilot.py"
    guide = "day-zero-linux.md" if platform == "linux" else "day-zero-macos.md"

    if args.handoff_draft:
        accounts = profile["accounts"]
        # macOS /usr/bin/python3 can be 3.9 without tomllib; the day-zero guide
        # sets PYTHON_BIN to the Homebrew Python 3.11+.
        interpreter = "python3" if platform == "linux" else '"$PYTHON_BIN"'
        arguments = ["setup", str(relative)]
        if platform == "linux":
            arguments.extend(["--expected-ubuntu", ubuntu])
        arguments.extend(["--execution-context", "host"])
        command_line = interpreter + ' scripts/fleetctl.py --fleet-root "$FLEET_ROOT" ' + shlex.join(arguments)
        boot = (
            "Review the Ubuntu release and disk plan. Prove console access and LUKS recovery."
            if platform == "linux" else
            "Confirm ownership and MDM at Setup Assistant. Verify FileVault escrow and console recovery."
        )
        admins = ", ".join(
            f"{admin} ({owner})" for admin, owner in accounts["admin_assignments"].items()
        )
        print(f"""# {hostname} bootstrap setup-agent handoff

Draft for review. Fill in the actual stopping point, approved client access,
recovery owner, package decisions and remaining work before committing.

- Target profile: `{relative}`
- Platform and hardware: {platform}, {machine['hardware_profile']}
- Named humans: {', '.join(accounts['humans'])}
- Assigned administrators: {admins}
- Shared runtime: {accounts['agent']}
- Kit version: {VERSION}
- Approved OS build and exceptions: record during review.
- Stopping point and next phase: record during review.
- Approved access and recovery owners: record during review.

1. Follow the kit's `docs/runbooks/{guide}` from the local console. {boot}
2. Complete day-zero prerequisites. Use the reviewed kit and fleet source
   checkouts, and set `FLEET_ROOT` to the fleet checkout{' and `PYTHON_BIN` as in day zero' if platform == 'macos' else ''}.
3. Run readiness from an ordinary terminal on the target (not an agent
   sandbox); `--execution-context host` states that. Resolve failures and
   rerun the same command from source.
4. Start the setup agent with this handoff and the stopping point above.
   Review the ordered plan. Preview each remaining phase and apply only
   approved changes from the required execution context.
5. Record pairing, desktop access, reconnect, transfer, and recovery separately.

```sh
{command_line}
```

## Setup-agent prompt

Read this handoff and `{relative}`. Follow the kit setup skill and platform
runbook. Start read-only, confirm the intended target and resume from observed
state. Keep credentials and recovery material out of Git. Stop for the named
operator at approval, authentication and recovery steps.""")
        return 0

    if actual_platform() != platform:
        raise ValueError(f"run readiness on the selected {platform} workstation")
    if os.geteuid() == 0:
        raise ValueError("run readiness as the approved non-root setup operator")
    if (fleet / handoff).is_symlink() or (fleet / handoff).parent.is_symlink() or not (fleet / handoff).is_file():
        raise ValueError(f"review and commit {handoff}; use --handoff-draft to start it")
    kit_revision = setup_revision(ROOT, "toolkit source checkout", args.kit_revision)
    fleet_revision = setup_revision(fleet, "fleet source checkout", args.fleet_revision)
    committed = subprocess.run(
        ["git", "-C", str(fleet), "cat-file", "-e", f"HEAD:{handoff}"],
        capture_output=True, check=False, timeout=15,
    )
    if committed.returncode:
        raise ValueError(f"commit the reviewed handoff at {handoff} before readiness")
    print(f"Toolkit source {kit_revision} at {ROOT}", flush=True)
    print(f"Fleet source   {fleet_revision} at {fleet}", flush=True)
    print(f"Target         {hostname} ({platform})", flush=True)
    print("These are local source revisions. Use reviewed staged snapshots for privileged apply.", flush=True)
    print("Running platform and repository checks…", flush=True)
    command = [sys.executable, str(ROOT / "scripts" / launcher), "--fleet-root", str(fleet), "--profile", str(relative)]
    if platform == "linux":
        command.extend(["--expected-ubuntu", ubuntu])
    # The launchers fail closed unless the operator states a host terminal.
    command.extend(["--execution-context", args.execution_context])
    result = subprocess.run(command, cwd=ROOT, check=False)
    if result.returncode == 0:
        print(f"Use the reviewed host handoff at {fleet / handoff} when starting the setup agent.")
    return result.returncode


def main() -> int:
    parser = argparse.ArgumentParser(description="Manage agent-workstation-kit profiles.")
    parser.add_argument("--version", action="version", version=f"%(prog)s {VERSION}")
    parser.add_argument("--fleet-root", type=Path, help="resolve relative profile paths in a separate private fleet repository")
    subs = parser.add_subparsers(dest="command", required=True)
    init = subs.add_parser("init", help="generate a validated draft profile without secrets")
    init.add_argument("output", type=Path)
    init.add_argument("--context", choices=("work", "personal"), required=True)
    init.add_argument("--namespace", required=True)
    init.add_argument("--hostname", required=True)
    init.add_argument("--display-name", help="human-friendly name; defaults to the technical hostname")
    init.add_argument("--platform", choices=("linux", "macos"), required=True)
    init.add_argument("--hardware-profile", default="generic")
    init.add_argument("--asset-tag", default="ask")
    init.add_argument("--human", action="append")
    init.add_argument("--admin", action="append")
    init.add_argument("--agent", default="agent-01")
    for command, help_text in (("validate", "validate profile structure and policy"), ("plan", "render the ordered phase plan")):
        sub = subs.add_parser(command, help=help_text)
        sub.add_argument("profile", type=Path)
        if command == "validate":
            sub.add_argument("--ready", action="store_true", help="require all apply-time decisions")
    setup = subs.add_parser("setup", help="draft a host handoff or run platform readiness from reviewed source")
    setup.add_argument("profile", type=Path)
    setup.add_argument("--handoff-draft", action="store_true", help="print a draft to review; write no files")
    setup.add_argument("--expected-ubuntu", help="reviewed Ubuntu release (default: 24.04)")
    setup.add_argument("--kit-revision", help="require the exact reviewed toolkit commit")
    setup.add_argument("--fleet-revision", help="require the exact reviewed fleet commit")
    setup.add_argument("--execution-context", choices=("host", "sandbox", "unknown"), default="unknown",
                       help="state that readiness runs in an ordinary target-host terminal (host); anything else reports NOT READY")
    run = subs.add_parser("run", help="run exactly one phase")
    run.add_argument("profile", type=Path)
    run.add_argument("phase", choices=PHASES)
    run.add_argument("--apply", action="store_true")
    run.add_argument("--confirm-recovery-tested", action="store_true")
    run.add_argument(
        "--connection-context",
        choices=("local-console", "tailscale-ssh"),
        help="required for identity/remote-hardening apply; states how this shell reaches the host",
    )
    run.add_argument(
        "--ssh-source-ip",
        help="SSH peer captured before sudo; required with tailscale-ssh",
    )
    args = parser.parse_args()

    if args.command == "setup":
        if args.fleet_root is None:
            parser.error("setup requires --fleet-root")
        args.fleet_root = args.fleet_root.expanduser().absolute()
        if issue := private_root_issue(args.fleet_root):
            print(f"ERROR fleet root: {issue}", file=sys.stderr)
            return 2
        if args.handoff_draft and (args.kit_revision or args.fleet_revision):
            parser.error("revision options apply to readiness only")
        if args.expected_ubuntu and not re.fullmatch(r"[0-9]{2}\.[0-9]{2}", args.expected_ubuntu):
            parser.error("--expected-ubuntu must use YY.MM format")
        for revision in (args.kit_revision, args.fleet_revision):
            if revision and not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", revision):
                parser.error("provide full lowercase Git commit IDs")

    if args.command == "init":
        # Without --fleet-root, init may write an ignored local experiment.
        if args.fleet_root is not None and (issue := private_root_issue(args.fleet_root.expanduser().absolute())):
            print(f"ERROR fleet root: {issue}", file=sys.stderr)
            return 2
        if lock_error := fleet_lock_issue(args.fleet_root):
            print(f"ERROR kit.lock: {lock_error}", file=sys.stderr)
            return 2
        output = resolve_profile(args.output, args.fleet_root)
        if output.exists():
            print(f"ERROR output: refusing to overwrite {output}", file=sys.stderr)
            return 2
        people = args.human or ["operator"]
        admins = args.admin or ["admin-01"]
        if len(admins) > len(people):
            print("ERROR accounts.admin_assignments: init requires at least one distinct human per admin account", file=sys.stderr)
            return 2
        display_name = args.display_name or args.hostname
        if not valid_display_name(display_name):
            print(
                "ERROR machine.display_name: must be 1-64 trimmed ASCII letters, digits, single spaces, dots, underscores, or hyphens and start with a letter or digit",
                file=sys.stderr,
            )
            return 2
        args.display_name = display_name
        scan_directory = (args.fleet_root / "machines") if args.fleet_root else output.parent
        try:
            allocation_issue = allocate_profile(args, output, scan_directory)
        except OSError as exc:
            print(f"ERROR allocation lock: {exc}", file=sys.stderr)
            return 2
        if allocation_issue:
            print(f"ERROR {allocation_issue.path}: {allocation_issue.message}", file=sys.stderr)
            return 2
        try:
            profile = load_profile(output)
        except ValueError as exc:
            output.unlink(missing_ok=True)
            print(f"ERROR profile: {exc}", file=sys.stderr)
            return 2
        issues = validate_profile(profile, ready=False)
        if issues:
            output.unlink(missing_ok=True)
            show_issues(issues)
            return 2
        print(f"CREATED draft {output} ({profile['machine']['hostname']}, uuid={profile['machine']['uuid']})")
        print("NEXT resolve every 'ask', review, change state to approved, then validate --ready")
        return 0

    if lock_error := fleet_lock_issue(args.fleet_root):
        print(f"ERROR kit.lock: {lock_error}", file=sys.stderr)
        return 2
    path = resolve_profile(args.profile, args.fleet_root)
    if args.command == "setup" and (
        path.absolute().parent != args.fleet_root / "machines"
        or args.fleet_root.is_symlink() or path.parent.is_symlink()
        or path.is_symlink() or not path.is_file()
    ):
        print("ERROR setup: select a regular profile directly under the fleet machines/ directory", file=sys.stderr)
        return 2
    try:
        profile = load_profile(path)
    except ValueError as exc:
        print(f"ERROR profile: {exc}", file=sys.stderr)
        return 2
    require_ready = bool(getattr(args, "ready", False) or getattr(args, "apply", False) or getattr(args, "phase", "") == "audit")
    issues = validate_profile(profile, ready=require_ready)
    if issues:
        show_issues(issues)
        return 2
    if args.command == "setup":
        try:
            return guided_setup(args, path, profile)
        except subprocess.TimeoutExpired:
            print("ERROR setup: a readiness check timed out. Check the source checkout and local prerequisites, then rerun.", file=sys.stderr)
            return 2
        except (OSError, ValueError) as exc:
            print(f"ERROR setup: {exc}", file=sys.stderr)
            return 2
        except KeyboardInterrupt:
            print("Setup readiness interrupted. Rerun from the reviewed source checkouts.", file=sys.stderr)
            return 130
    print(f"PASS profile {path} ({profile['profile']}, {profile['machine']['platform']}, state={profile['state']})")
    if args.command == "validate":
        return 0
    if args.command == "plan":
        for name in PHASES:
            phase = phase_for(profile, name, apply=False, recovery=False)
            authority = "human/privileged" if phase.privileged else ("agent account" if name in {"shell", "user-tooling"} else "named package owner")
            command = shlex.join(phase.command) if phase.command else "MANUAL"
            print(f"\n{name}\n  owner:   {authority}\n  preview: {command}\n  note:    {phase.note}")
        return 0
    if profile["machine"]["platform"] != actual_platform():
        print(f"ERROR machine.platform: profile is {profile['machine']['platform']}, host is {actual_platform()}", file=sys.stderr)
        return 2
    if args.phase in {"identity", "remote-hardening"} and args.apply and not args.confirm_recovery_tested:
        print(f"ERROR {args.phase}: --apply also requires --confirm-recovery-tested", file=sys.stderr)
        return 2
    if args.phase in {"identity", "remote-hardening"} and args.apply:
        if not args.connection_context:
            print(f"ERROR {args.phase}: --apply also requires --connection-context", file=sys.stderr)
            return 2
        if args.connection_context == "tailscale-ssh" and not args.ssh_source_ip:
            print(f"ERROR {args.phase}: tailscale-ssh also requires --ssh-source-ip", file=sys.stderr)
            return 2
        if args.connection_context == "local-console" and args.ssh_source_ip:
            print(f"ERROR {args.phase}: local-console must not include --ssh-source-ip", file=sys.stderr)
            return 2
    if (args.phase in {"shell", "user-tooling"} or (args.phase == "workloads" and profile["machine"]["platform"] == "macos")) and pwd.getpwuid(os.getuid()).pw_name != profile["accounts"]["agent"]:
        print(f"ERROR {args.phase}: run this phase as {profile['accounts']['agent']}, not root or a human account", file=sys.stderr)
        return 2
    phase = phase_for(
        profile,
        args.phase,
        apply=args.apply,
        recovery=args.confirm_recovery_tested,
        connection_context=args.connection_context,
        ssh_source_ip=args.ssh_source_ip,
    )
    print(f"PHASE {phase.name}: {phase.note}")
    if phase.command is None:
        print("MANUAL phase: follow the applicable guide; no generic command will be executed.")
        return 0
    if args.phase == "audit":
        failures = audit_machine_identity(profile) + audit_declared_accounts(profile)
        child_env = os.environ.copy()
        child_env.pop("APPLY_CHANGES", None)
        result = subprocess.run(phase.command, check=False, env=child_env)
        return 1 if failures or result.returncode else 0
    print(f"EXEC {shlex.join(phase.command)}")
    child_env = os.environ.copy()
    child_env.pop("APPLY_CHANGES", None)
    child_env["AWK_PYTHON"] = sys.executable
    return subprocess.run(phase.command, check=False, env=child_env).returncode


if __name__ == "__main__":
    raise SystemExit(main())
