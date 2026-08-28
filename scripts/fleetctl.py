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
import os
import pwd
import re
import shlex
import shutil
import socket
import stat
import subprocess
import sys
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
    "resources": {"policy", "os_memory_reserve_gib", "os_cpu_reserve_threads"},
    "backup": {"target", "retention_days"},
    "maintenance": {"timezone", "update_window", "owner"},
}
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
        for key in sorted(allowed - set(value)):
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
    if tooling["antidote_ref"] != "ask" and (not isinstance(tooling["antidote_ref"], str) or not RELEASE_REF.fullmatch(tooling["antidote_ref"])):
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
    for key, lower, upper in (("os_memory_reserve_gib", 4, 32), ("os_cpu_reserve_threads", 1, 8)):
        value = resources[key]
        if type(value) is not int or not lower <= value <= upper:
            issues.append(Issue(f"resources.{key}", f"must be an integer from {lower} through {upper}"))
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
        return Phase(name, (str(ROOT / "scripts" / script), *suffix), machine["platform"] == "linux", "Base packages; external authentication remains manual.")
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
            return Phase(name, None, True, "Create macOS accounts through the documented human/MDM workflow.")
        argv = [str(ROOT / "scripts/setup-accounts-linux.sh"), "--agent", accounts["agent"]]
        for key, flag in (("humans", "--human"), ("admins", "--admin"), ("operators", "--operator"), ("viewers", "--viewer")):
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
        if tooling["gws"] == "install":
            argv.append("--gws")
        argv.extend(suffix)
        return Phase(name, tuple(argv), False, "Run as the agent account; authentication is a separate human ceremony.")
    if name == "workloads":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "Validate Chrome, OrbStack/Docker, and project-owned Playwright browsers through the macOS guide.")
        return Phase(name, (str(ROOT / "scripts/install-workloads-linux.sh"), "--agent", accounts["agent"], *suffix), True, "Installs rootless containers, Chromium, Xvfb, and browser libraries.")
    if name == "resources":
        if machine["platform"] == "macos":
            return Phase(name, None, True, "Measure and apply macOS controls through the documented managed workflow.")
        return Phase(name, (str(ROOT / "scripts/apply-resource-policy-linux.sh"), "--agent", accounts["agent"], "--memory-reserve-gib", str(profile["resources"]["os_memory_reserve_gib"]), *suffix), True, "Apply only after observation and load testing; audit cgroup placement after reboot.")
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
[resources]\npolicy = "measured-balanced"\nos_memory_reserve_gib = 8\nos_cpu_reserve_threads = 2\n\n\
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
    except OSError as exc:
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

    if args.command == "init":
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
    if args.phase in {"shell", "user-tooling"} and pwd.getpwuid(os.getuid()).pw_name != profile["accounts"]["agent"]:
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
    return subprocess.run(phase.command, check=False, env=child_env).returncode


if __name__ == "__main__":
    raise SystemExit(main())
