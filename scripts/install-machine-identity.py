#!/usr/bin/env python3
"""Install a durable, non-secret machine identity record.

The private fleet profile remains authoritative. This local copy makes a live
host self-identifying without placing credentials, hardware serials, or tokens
on disk. Apply is explicit and requires root.
"""

from __future__ import annotations

import argparse
import ipaddress
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import unicodedata
import uuid
from pathlib import Path


HOSTNAME = re.compile(r"^[a-z0-9]{2,8}-(?:ws|mac|hv|vws|nas|mgmt|srv)-[0-9]{3}$")
TARGETS = {
    "linux": Path("/etc/agent-workstation-kit/identity.toml"),
    "macos": Path("/Library/Application Support/Agent Workstation Kit/identity.toml"),
}


def toml_string(value: str) -> str:
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'


def actual_platform() -> str:
    if sys.platform.startswith("linux"):
        return "linux"
    if sys.platform == "darwin":
        return "macos"
    return "unsupported"


def valid_display_name(value: str) -> bool:
    return (
        value == value.strip()
        and "  " not in value
        and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}", value) is not None
    )


def valid_uuid4(value: str) -> bool:
    try:
        parsed = uuid.UUID(value)
    except ValueError:
        return False
    return parsed.version == 4 and str(parsed) == value


def valid_plain_value(value: str) -> bool:
    return (
        value == value.strip()
        and not value.startswith("-")
        and 1 <= len(value) <= 128
        and value == unicodedata.normalize("NFKC", value)
        and all(
            character.isprintable() and not unicodedata.category(character).startswith("C")
            for character in value
        )
    )


def render(args: argparse.Namespace) -> str:
    values = {
        "hostname": args.hostname,
        "display_name": args.display_name,
        "uuid": args.uuid,
        "asset_tag": args.asset_tag,
        "namespace": args.namespace,
        "platform": args.platform,
        "role": args.role,
    }
    lines = [
        "# Managed by agent-workstation-kit. Non-secret identity only.",
        "schema_version = 1",
        "",
        "[identity]",
        *(f"{key} = {toml_string(value)}" for key, value in values.items()),
        "",
    ]
    return "\n".join(lines)


def set_os_names(args: argparse.Namespace) -> None:
    if args.platform == "linux":
        subprocess.run(["hostnamectl", "set-hostname", args.hostname, "--static"], check=True)
        subprocess.run(["hostnamectl", "set-hostname", args.hostname, "--transient"], check=True)
        subprocess.run(["hostnamectl", "set-hostname", args.display_name, "--pretty"], check=True)
        return
    subprocess.run(["scutil", "--set", "HostName", args.hostname], check=True)
    subprocess.run(["scutil", "--set", "LocalHostName", args.hostname], check=True)
    subprocess.run(["scutil", "--set", "ComputerName", args.display_name], check=True)


def linux_hosts_conflicts(new_hostname: str, hosts_path: Path = Path("/etc/hosts")) -> str | None:
    """Return the old short hostname if /etc/hosts still binds it."""
    old_hostname = socket.gethostname().split(".", 1)[0]
    if old_hostname == new_hostname:
        return None
    lines = hosts_path.read_text(encoding="utf-8").splitlines()
    for line in lines:
        tokens = line.split("#", 1)[0].split()
        if old_hostname in tokens[1:]:
            return old_hostname
    return None


def linux_hostname_resolves_locally(hostname: str) -> bool:
    """Return true only when NSS resolves the name to this host or loopback."""
    try:
        resolved = subprocess.run(
            ["getent", "hosts", hostname], check=False, text=True, capture_output=True
        )
        interfaces = subprocess.run(
            ["ip", "-o", "addr", "show"], check=False, text=True, capture_output=True
        )
    except OSError:
        return False
    if resolved.returncode != 0 or interfaces.returncode != 0:
        return False
    try:
        local_addresses = {
            ipaddress.ip_address(fields[fields.index(family) + 1].split("/", 1)[0])
            for line in interfaces.stdout.splitlines()
            for fields in (line.split(),)
            for family in ("inet", "inet6")
            if family in fields
        }
        resolved_addresses = [
            ipaddress.ip_address(line.split()[0])
            for line in resolved.stdout.splitlines()
            if line.split()
        ]
    except (ValueError, IndexError):
        return False
    return bool(resolved_addresses) and any(
        address.is_loopback or address in local_addresses for address in resolved_addresses
    )


REMOTE_ANCESTOR_NAMES = {"sshd", "tailscaled", "mosh-server"}


def remote_login_ancestor_detected() -> bool | None:
    """Detect an SSH/Tailscale/Mosh ancestor even when sudo strips SSH_*.

    Return None when the process chain cannot be inspected. Local-console mode
    treats that as a failure: an unprovable console claim must not authorize a
    hostname change.
    """
    pid = os.getpid()
    seen: set[int] = set()
    for _ in range(128):
        if pid <= 1:
            return False
        if pid in seen:
            return None
        seen.add(pid)
        try:
            if sys.platform.startswith("linux"):
                process_name = Path(f"/proc/{pid}/comm").read_text(encoding="utf-8").strip()
                stat_line = Path(f"/proc/{pid}/stat").read_text(encoding="utf-8")
                closing = stat_line.rfind(")")
                fields = stat_line[closing + 2 :].split() if closing >= 0 else []
                if len(fields) < 2:
                    return None
                parent = int(fields[1])
            elif sys.platform == "darwin":
                result = subprocess.run(
                    ["/bin/ps", "-o", "ppid=", "-o", "comm=", "-p", str(pid)],
                    check=False,
                    text=True,
                    capture_output=True,
                )
                if result.returncode != 0 or not result.stdout.strip():
                    return None
                parent_text, process_name = result.stdout.strip().split(maxsplit=1)
                parent = int(parent_text)
            else:
                return None
        except (OSError, ValueError):
            return None
        if Path(process_name).name.casefold() in REMOTE_ANCESTOR_NAMES:
            return True
        pid = parent
    return None


def connection_context_is_valid(context: str, ssh_source_ip: str | None) -> bool:
    """Verify observable session evidence; the recovery confirmation remains human."""
    reported_fields = os.environ.get("SSH_CONNECTION", "").split(maxsplit=1)
    reported_source = reported_fields[0] if reported_fields else ""
    if context == "local-console":
        if ssh_source_ip is not None or reported_source:
            return False
        return remote_login_ancestor_detected() is False
    if not ssh_source_ip or ssh_source_ip.startswith("-") or any(character.isspace() for character in ssh_source_ip):
        return False
    if reported_source and reported_source != ssh_source_ip:
        return False
    try:
        result = subprocess.run(
            ["tailscale", "whois", ssh_source_ip],
            check=False,
            text=True,
            capture_output=True,
        )
    except OSError:
        return False
    return result.returncode == 0


def atomic_install(target: Path, content: str) -> None:
    target.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    if target.parent.is_symlink() or not target.parent.is_dir():
        raise OSError(f"identity directory must be a real directory: {target.parent}")
    os.chown(target.parent, 0, 0)
    os.chmod(target.parent, 0o755)
    descriptor, temporary = tempfile.mkstemp(prefix=".identity.", dir=target.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o644)
        os.chown(temporary, 0, 0)
        os.replace(temporary, target)
        directory = os.open(target.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def main() -> int:
    parser = argparse.ArgumentParser(description="Install the approved local machine identity record.")
    parser.add_argument("--hostname", required=True)
    parser.add_argument("--display-name", required=True)
    parser.add_argument("--uuid", required=True)
    parser.add_argument("--asset-tag", required=True)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--platform", choices=tuple(TARGETS), required=True)
    parser.add_argument("--role", required=True)
    parser.add_argument("--confirm-recovery-tested", action="store_true")
    parser.add_argument("--connection-context", choices=("local-console", "tailscale-ssh"))
    parser.add_argument("--ssh-source-ip")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()

    if not HOSTNAME.fullmatch(args.hostname):
        parser.error("--hostname must use the managed <namespace>-<class>-<NNN> form")
    if not valid_display_name(args.display_name):
        parser.error("--display-name must be 1-64 trimmed ASCII letters, digits, single spaces, dots, underscores, or hyphens and start with a letter or digit")
    if not valid_uuid4(args.uuid):
        parser.error("--uuid must be a canonical UUIDv4")
    if not re.fullmatch(r"[a-z0-9]{2,8}", args.namespace):
        parser.error("--namespace must be 2-8 lowercase letters/digits")
    if not valid_plain_value(args.asset_tag) or not valid_plain_value(args.role):
        parser.error("--asset-tag and --role must be non-empty trimmed printable values")
    if args.hostname.split("-", 1)[0] != args.namespace:
        parser.error("--namespace must match the hostname prefix")
    content = render(args)
    target = TARGETS[args.platform]
    print(f"Identity target: {target}")
    print(content, end="")
    if not args.apply:
        print("PREVIEW only; apply through the documented staged workflow after review.")
        return 0
    if os.geteuid() != 0:
        print("ERROR: apply requires root", file=sys.stderr)
        return 2
    if actual_platform() != args.platform:
        print(f"ERROR: profile platform is {args.platform}, host is {actual_platform()}", file=sys.stderr)
        return 2
    if not args.confirm_recovery_tested or not args.connection_context:
        print("ERROR: apply requires tested recovery and an explicit connection context", file=sys.stderr)
        return 2
    if not connection_context_is_valid(args.connection_context, args.ssh_source_ip):
        print(
            "ERROR: connection context does not match observable console/SSH/Tailscale session evidence",
            file=sys.stderr,
        )
        return 2
    required_commands = ("hostnamectl", "getent", "ip") if args.platform == "linux" else ("scutil",)
    for required_command in required_commands:
        if shutil.which(required_command) is None:
            print(f"ERROR: required command is unavailable: {required_command}", file=sys.stderr)
            return 2
    try:
        old_hostname = linux_hosts_conflicts(args.hostname) if args.platform == "linux" else None
    except OSError as exc:
        print(f"ERROR: cannot inspect /etc/hosts before hostname change: {exc}", file=sys.stderr)
        return 2
    if old_hostname:
        print(
            f"ERROR: /etc/hosts still maps current hostname {old_hostname!r}; replace only that existing alias "
            f"with {args.hostname!r} in a reviewed privileged change, then rerun identity",
            file=sys.stderr,
        )
        return 2
    try:
        atomic_install(target, content)
    except OSError as exc:
        print(f"ERROR: local identity record was not installed: {exc}", file=sys.stderr)
        print(
            "RECOVERY: keep console access, repair the target directory or filesystem, "
            "then rerun this phase; no OS naming command was attempted",
            file=sys.stderr,
        )
        return 1
    try:
        # Install desired identity first. If an OS naming command fails, the
        # audit has a durable record against which to report the remaining drift.
        set_os_names(args)
    except (OSError, subprocess.CalledProcessError) as exc:
        print(f"ERROR: local identity was installed but OS naming did not complete: {exc}", file=sys.stderr)
        print(
            "RECOVERY: keep console access, repair the failed naming command, "
            f"rerun this phase, then run audit; record={target}",
            file=sys.stderr,
        )
        return 1
    if args.platform == "linux" and not linux_hostname_resolves_locally(args.hostname):
        print(
            "ERROR: local identity and OS names were installed, but the new hostname does not resolve "
            "through NSS to a local or loopback address",
            file=sys.stderr,
        )
        print(
            f"RECOVERY: keep console access, repair /etc/hosts or approved local DNS for {args.hostname!r}, "
            "then rerun this phase and audit",
            file=sys.stderr,
        )
        return 1
    print(f"PASS installed {target} as root-owned mode 0644")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
