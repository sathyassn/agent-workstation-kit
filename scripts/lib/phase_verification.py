"""Read-only postconditions for execution receipts; no provider authentication."""
from __future__ import annotations
import ipaddress
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import sys
import subprocess


def probe(command, *, env=None):
    try:
        return subprocess.run(command, check=False, text=True, capture_output=True, timeout=60, env=env)
    except (OSError, subprocess.TimeoutExpired):
        return subprocess.CompletedProcess(command, 1, "", "unavailable")


def same_file(source, target):
    return target.is_file() and source.read_bytes() == target.read_bytes()


def identity_matches(profile):
    machine = profile["machine"]
    platform = machine["platform"]
    path = Path("/etc/agent-workstation-kit/identity.toml") if platform == "linux" else Path("/Library/Application Support/Agent Workstation Kit/identity.toml")
    if not path.is_file() or path.is_symlink() or path.stat().st_uid != 0 or stat.S_IMODE(path.stat().st_mode) != 0o644:
        return False
    if path.parent.is_symlink() or path.parent.stat().st_uid != 0 or stat.S_IMODE(path.parent.stat().st_mode) != 0o755:
        return False
    values = {}
    try:
        for line in path.read_text().splitlines():
            if " = " in line and line.split(" = ", 1)[0] != "schema_version":
                key, value = line.split(" = ", 1)
                values[key] = json.loads(value)
    except (ValueError, OSError):
        return False
    expected = {key: machine[key] for key in ("hostname", "display_name", "uuid", "asset_tag", "platform", "role")}
    expected["namespace"] = profile["deployment"]["namespace"]
    if values != expected:
        return False
    if platform == "macos":
        return all(probe(["scutil", "--get", key]).stdout.strip() == value for key, value in (("HostName", machine["hostname"]), ("LocalHostName", machine["hostname"]), ("ComputerName", machine["display_name"])))
    if any(probe(["hostnamectl", flag]).stdout.strip() != value for flag, value in (("--static", machine["hostname"]), ("--transient", machine["hostname"]), ("--pretty", machine["display_name"]))):
        return False
    addresses = probe(["ip", "-o", "addr", "show"])
    resolved = probe(["getent", "hosts", machine["hostname"]])
    if addresses.returncode or resolved.returncode:
        return False
    local = set()
    try:
        for line in addresses.stdout.splitlines():
            fields = line.split()
            for family in ("inet", "inet6"):
                if family in fields:
                    local.add(ipaddress.ip_address(fields[fields.index(family) + 1].split("/")[0]))
        return any((address := ipaddress.ip_address(line.split()[0])).is_loopback or address in local for line in resolved.stdout.splitlines())
    except (ValueError, IndexError):
        return False


def account_problems(profile):
    """Return a reason for every declared-account postcondition that fails.

    Only the agent account must use zsh: the accounts phase creates it with
    zsh, while existing human and admin accounts are preserved unchanged.
    """
    accounts = profile["accounts"]
    group = "sudo" if profile["machine"]["platform"] == "linux" else "admin"
    problems = []
    for name in [accounts["agent"], *accounts["humans"], *accounts["admins"]]:
        try:
            entry = pwd.getpwnam(name)
        except KeyError:
            problems.append(name + " does not exist")
            continue
        if entry.pw_uid == 0:
            problems.append(name + " has UID 0")
            continue
        home = Path(entry.pw_dir)
        if not home.is_dir() or home.stat().st_uid != entry.pw_uid:
            problems.append(name + " does not own an existing home directory")
        if name == accounts["agent"] and not entry.pw_shell.endswith("zsh"):
            problems.append(name + " (agent) does not use zsh")
        memberships = probe(["id", "-nG", name])
        if memberships.returncode:
            problems.append("cannot read group memberships of " + name)
            continue
        groups = memberships.stdout.split()
        if (name in accounts["admins"]) != (group in groups):
            problems.append(name + (" is not" if name in accounts["admins"] else " must not be") + " in " + group)
        if name == accounts["agent"] and "docker" in groups:
            problems.append(name + " is in the root-equivalent docker group")
    if profile["machine"]["platform"] == "linux":
        for suffix in ("operators", "viewers"):
            group_name = accounts["agent"] + "-" + suffix
            if probe(["getent", "group", group_name]).returncode:
                problems.append("group " + group_name + " does not exist")
                continue
            for person in accounts[suffix]:
                if group_name not in probe(["id", "-nG", person]).stdout.split():
                    problems.append(person + " is not in " + group_name)
    for service in accounts.get("services", []):
        try:
            entry = pwd.getpwnam(service)
        except KeyError:
            problems.append("service account " + service + " does not exist")
            continue
        if entry.pw_uid == 0 or not entry.pw_shell.endswith(("nologin", "false")):
            problems.append("service account " + service + " has UID 0 or a login shell")
        memberships = probe(["id", "-nG", service])
        if memberships.returncode or set(memberships.stdout.split()) & {"sudo", "admin", "docker"}:
            problems.append("service account " + service + " has privileged or unreadable groups")
    return problems


def accounts_match(profile):
    problems = account_problems(profile)
    for problem in problems:
        print("REASON accounts: " + problem)
    return not problems


def sshd_effective(output):
    """Parse `sshd -T`, which prints one line per value for list keywords."""
    values = {}
    for line in output.splitlines():
        fields = line.split(maxsplit=1)
        if len(fields) == 2:
            values.setdefault(fields[0].lower(), []).append(fields[1])
    return values


def sshd_problems(output, ssh_users, agent):
    config = sshd_effective(output)
    problems = []
    for key, expected in (("permitrootlogin", "no"), ("passwordauthentication", "no"), ("kbdinteractiveauthentication", "no")):
        if config.get(key) != [expected]:
            problems.append("sshd " + key + " is not " + expected)
    allowed = {user for value in config.get("allowusers", []) for user in value.split()}
    denied = {user for value in config.get("denyusers", []) for user in value.split()}
    if allowed != set(ssh_users):
        problems.append("sshd AllowUsers does not exactly match accounts.ssh_users")
    if agent not in denied:
        problems.append("sshd DenyUsers does not include the agent account")
    return problems


def ufw_allows_in(status, rule, interface):
    """True when `ufw status` output allows RULE inbound on INTERFACE.

    Plain `ufw status` prints incoming rules as `ALLOW` with no direction;
    `ufw status verbose` prints `ALLOW IN`. Outgoing (`ALLOW OUT`) and routed
    (`ALLOW FWD`) rules never count.
    """
    for line in status.splitlines():
        fields = line.split()
        if fields[:4] == [rule, "on", interface, "ALLOW"] and (len(fields) < 5 or fields[4] not in ("OUT", "FWD")):
            return True
    return False


def resource_values(profile, total):
    gib = 1024 * 1024
    reserve = profile["resources"]["os_memory_reserve_gib"]
    hard = max(reserve * gib // 2, 4 * gib) if reserve > 4 else reserve * gib // 2
    high, maximum = total - reserve * gib, total - hard
    if high <= 0 or maximum <= high:
        raise ValueError("insufficient memory for the reviewed resource reserve")
    return {"CPUWeight": "90", "IOWeight": "90", "MemoryHigh": str(high) + "K", "MemoryMax": str(maximum) + "K", "TasksMax": str(max(4096, min(16384, total // gib * 128)))}


def verify_phase(plan, name, root):
    """Return false for unknown/unprovable state; never promote an exit code alone."""
    profile = plan["profile"]
    agent = profile["accounts"]["agent"]
    platform = profile["machine"]["platform"]
    try:
        phase = next(item for item in plan["phases"] if item["name"] == name)
        command = phase["command"]
        if name == "base":
            argv = [str(root / command[0][4:]), *(arg for arg in command[1:] if arg != "--apply"), "--check"]
            env = os.environ.copy()
            env["AWK_PYTHON"] = sys.executable
            ok = probe(argv, env=env).returncode == 0
        elif name == "identity":
            ok = identity_matches(profile)
        elif name == "accounts":
            ok = accounts_match(profile)
        elif name == "shell":
            home = Path(pwd.getpwuid(os.geteuid()).pw_dir)
            ref = profile["tooling"]["antidote_ref"]
            current = probe(["git", "-C", str(home / ".antidote"), "rev-parse", "HEAD"])
            expected = probe(["git", "-C", str(home / ".antidote"), "rev-parse", ref + "^{commit}"])
            ok = current.returncode == expected.returncode == 0 and current.stdout.strip() == expected.stdout.strip() and all(same_file(root / source, home / target) for source, target in (("dotfiles/zshrc.managed", ".zshrc"), ("dotfiles/zsh_plugins.txt", ".zsh_plugins.txt")))
        elif name == "user-tooling":
            tools = ["node", "python", "bun", "pnpm", "gh", "glab", "herdr", "codex", "claude"]
            if profile["tooling"]["gws"] == "install":
                tools.append("gws")
            local_mise = Path(pwd.getpwuid(os.geteuid()).pw_dir) / ".local/bin/mise"
            mise = str(local_mise) if local_mise.is_file() else "mise"
            home = Path(pwd.getpwuid(os.geteuid()).pw_dir)
            desired = (root / "config/mise.toml").read_bytes()
            if profile["tooling"]["gws"] == "install":
                desired += b'\n# Optional profile-selected tool.\n"npm:@googleworkspace/cli" = "latest"\n'
            config = home / ".config/mise/config.toml"
            ok = config.is_file() and config.read_bytes() == desired
            ok = ok and all(probe([mise, "exec", "--", tool, "--version"]).returncode == 0 for tool in tools)
            if "mise_sha256" in profile["tooling"]:
                import hashlib
                ok = ok and local_mise.is_file() and hashlib.sha256(local_mise.read_bytes()).hexdigest() == profile["tooling"]["mise_sha256"]
            # Grok remains a separate reviewed vendor installer, never an invented package.
        elif name == "workloads":
            if platform == "macos":
                ok = probe([str(root / "scripts/check-project-workloads.sh"), "--agent", agent, "--check"]).returncode == 0
            else:
                ok = all(shutil.which(tool) for tool in ("podman", "docker", "chromium-browser", "Xvfb")) and all(re.search(r"^" + re.escape(agent) + r":", Path(path).read_text(), re.M) for path in ("/etc/subuid", "/etc/subgid"))
        elif name == "agentctl":
            targets = {"agentctl": "/usr/local/bin/agentctl", "agentctl-session": "/usr/local/libexec/agentctl-session", "agentctl-observe": "/usr/local/libexec/agentctl-observe", "agentctl-policy": "/usr/local/libexec/agentctl-policy"}
            ok = all(same_file(root / "agentctl" / source, Path(target)) and not Path(target).is_symlink() and Path(target).stat().st_uid == 0 and stat.S_IMODE(Path(target).stat().st_mode) == (0o644 if source == "agentctl-policy" else 0o755) for source, target in targets.items())
            config = Path("/etc/agentctl/target.conf").read_text()
            ok = ok and ("AGENTCTL_ACCOUNT='" + agent + "'") in config and ("AGENTCTL_TARGET='" + profile["machine"]["hostname"] + "'") in config
            sudoers = Path("/etc/sudoers.d/60-agentctl-" + agent)
            ok = ok and not sudoers.is_symlink() and sudoers.stat().st_uid == 0 and stat.S_IMODE(sudoers.stat().st_mode) == 0o440
            ok = ok and probe(["visudo", "-cf", str(sudoers)]).returncode == 0
        elif name == "remote-hardening":
            ssh = probe(["sshd", "-T"])
            problems = ["cannot read effective sshd policy"] if ssh.returncode else sshd_problems(ssh.stdout, profile["accounts"]["ssh_users"], agent)
            for problem in problems:
                print("REASON remote-hardening: " + problem)
            ok = not problems
            ufw = probe(["ufw", "status"])
            ok = ok and ufw.returncode == 0 and "Status: active" in ufw.stdout
            port = str(profile["remote"]["nomachine_port"])
            ok = ok and all(ufw_allows_in(ufw.stdout, rule, "tailscale0") for rule in ("22/tcp", port + "/tcp", port + "/udp"))
            # Use the same restrictive persisted-rule/default parser as the installer.
            check = 'source "$1"; ufw_default_policies_are_safe "$(cat /etc/default/ufw)" || exit 1; rules=$(LC_ALL=C ufw show added) || exit 1; if sed -n "/^ufw /p" <<<"$rules" | first_unsafe_ufw_rule "$2"; then exit 1; fi'
            ok = ok and probe(["bash", "-c", check, "verify", str(root / "scripts/lib/common.sh"), port]).returncode == 0
            tailscale = probe(["tailscale", "status", "--json"])
            ok = ok and tailscale.returncode == 0 and json.loads(tailscale.stdout).get("BackendState") == "Running"
            ok = ok and (probe(["systemctl", "is-active", "--quiet", "ssh.service"]).returncode == 0 or probe(["systemctl", "is-active", "--quiet", "ssh.socket"]).returncode == 0)
        elif name == "resources":
            uid = pwd.getpwnam(agent).pw_uid
            path = Path("/etc/systemd/system/user-" + str(uid) + ".slice.d/50-agent-workstation.conf")
            content = path.read_text()
            total = int(next(line.split()[1] for line in Path("/proc/meminfo").read_text().splitlines() if line.startswith("MemTotal:")))
            expected = resource_values(profile, total)
            fields = dict(line.split("=", 1) for line in content.splitlines() if "=" in line)
            ok = not path.is_symlink() and path.stat().st_uid == 0 and stat.S_IMODE(path.stat().st_mode) == 0o644 and fields == expected
        elif name == "audit":
            # The executor always runs the read-only host audit once per audit
            # invocation, then verifies these extra declared-profile conditions.
            ok = identity_matches(profile) and accounts_match(profile)
        else:
            ok = False
    except (OSError, KeyError, TypeError, ValueError, StopIteration):
        ok = False
    print(("PASS " if ok else "FAIL ") + name + " live postconditions")
    return bool(ok)
