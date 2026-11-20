"""Idempotent package installation through the host's approved package managers."""
from __future__ import annotations

import os
import json
import stat
from pathlib import Path
import re
import subprocess
import sys
import tomllib

ROOT = Path(__file__).resolve().parents[2]
# Unresolved, so a symlinked staging path is refused rather than followed.
KIT_ROOT = Path(os.path.abspath(__file__)).parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
from path_safety import require_staged_root  # noqa: E402
APT_SPEC = re.compile(r"^[a-z0-9][a-z0-9+.-]*(?::[a-z0-9]+)?(?:=[A-Za-z0-9.+:~_-]+)?$")
BREW_SPEC = re.compile(r"^[a-z0-9][a-z0-9+._-]*(?:@[a-z0-9][a-z0-9._-]*)?$")


def valid_package(value: object, kind: str) -> bool:
    return isinstance(value, str) and bool((APT_SPEC if kind == "apt" else BREW_SPEC).fullmatch(value))


def selected_packages(platform: str, additions: dict | None = None) -> dict[str, list[str]]:
    baseline = tomllib.loads((ROOT / "config/packages.toml").read_text())[platform]
    additions = additions or {}
    result = {}
    for kind in baseline:
        values = [*baseline[kind], *additions.get(kind, [])]
        if not all(valid_package(value, kind) for value in values):
            raise ValueError(f"invalid {kind} package specification")
        result[kind] = sorted(set(values))
    return result


def run(command: list[str], *, mutate: bool = False) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env.update(HOMEBREW_NO_AUTO_UPDATE="1", HOMEBREW_NO_INSTALL_UPGRADE="1", DEBIAN_FRONTEND="noninteractive")
    if mutate:
        # Keep vendor prompts on the terminal; never capture authentication.
        return subprocess.run(command, env=env, check=False, text=True)
    return subprocess.run(command, env=env, check=False, text=True, capture_output=True)


def inventory(platform: str, packages: dict[str, list[str]]) -> dict[str, dict[str, str]]:
    result: dict[str, dict[str, str]] = {kind: {} for kind in packages}
    if platform == "linux":
        for spec in packages["apt"]:
            name, _, _version = spec.partition("=")
            probe = run(["dpkg-query", "-W", "-f=${db:Status-Status} ${Version}", name])
            if probe.returncode == 0 and probe.stdout.startswith("installed "):
                result["apt"][name] = probe.stdout.split(maxsplit=1)[1].strip()
    else:
        for kind, flag in (("formulae", "--formula"), ("casks", "--cask")):
            names = run(["brew", "list", flag])
            if names.returncode:
                raise ValueError(f"cannot inspect installed Homebrew {kind} names")
            # Query versions only for selected installed packages. A removed,
            # unrelated legacy cask must not break inventory for this setup.
            selected = sorted(set(names.stdout.splitlines()) & set(packages[kind]))
            if not selected:
                continue
            probe = run(["brew", "list", flag, "--versions", *selected])
            if probe.returncode:
                raise ValueError(f"cannot inspect selected installed Homebrew {kind}; existing packages are preserved")
            for line in probe.stdout.splitlines():
                name, _, versions = line.partition(" ")
                if name in selected and versions.strip():
                    result[kind][name] = versions.strip()
            if set(result[kind]) != set(selected):
                raise ValueError(f"selected Homebrew {kind} inventory is incomplete")
    return result


def missing_packages(packages: dict[str, list[str]], installed: dict[str, dict[str, str]]) -> dict[str, list[str]]:
    missing = {}
    for kind, specs in packages.items():
        missing[kind] = []
        for spec in specs:
            name, _, required = spec.partition("=")
            current = installed[kind].get(name)
            if current and required and current != required:
                raise ValueError(f"{name} is {current}, requested {required}; review an explicit upgrade/downgrade separately")
            if not current:
                missing[kind].append(spec)
    return missing


SSH_GUARD = Path("/etc/agent-workstation-kit/ssh-install-guard.json")
SSH_UNITS = ("ssh.service", "ssh.socket")


def _guard_record():
    if not SSH_GUARD.exists() or SSH_GUARD.is_symlink():
        raise ValueError("SSH install guard is missing or unsafe")
    info = SSH_GUARD.stat()
    if info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError("SSH install guard must be root-owned mode 0600")
    data = json.loads(SSH_GUARD.read_text())
    if data != {"masked_units": list(SSH_UNITS)}:
        raise ValueError("SSH install guard does not describe the exact kit-owned units")
    for unit in SSH_UNITS:
        path = Path("/etc/systemd/system") / unit
        if not path.is_symlink() or os.readlink(path) != "/dev/null" or path.lstat().st_uid != 0:
            raise ValueError("SSH guard mask changed; review before continuing")
    return data


def guard_new_ssh_install(*, apply):
    print("SSH INSTALL GUARD: keep fresh OpenSSH service/socket persistently masked until remote hardening validates the firewall and SSH policy")
    if not apply:
        return
    if SSH_GUARD.exists():
        _guard_record()
        return
    if SSH_GUARD.is_symlink():
        raise ValueError("SSH install guard path is a symlink")
    for unit in SSH_UNITS:
        if run(["systemctl", "is-active", "--quiet", unit]).returncode == 0:
            raise ValueError("pre-existing active SSH unit requires separate review; no masking or replacement")
        path = Path("/etc/systemd/system") / unit
        if path.exists() or path.is_symlink():
            raise ValueError("existing SSH unit override/mask is preserved; review its owner separately")
    SSH_GUARD.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    info = SSH_GUARD.parent.lstat()
    if SSH_GUARD.parent.is_symlink() or info.st_uid != 0 or info.st_mode & 0o022:
        raise ValueError("unsafe SSH guard directory")
    if run(["systemctl", "mask", *SSH_UNITS], mutate=True).returncode:
        raise ValueError("could not guard fresh SSH installation; no package installation attempted")
    try:
        descriptor = os.open(SSH_GUARD, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(descriptor, "w") as stream:
            json.dump({"masked_units": list(SSH_UNITS)}, stream)
    except OSError:
        # Without its record the mask cannot be released by the kit later, so
        # undo it now; the units were verified inactive and unmasked above.
        try:
            SSH_GUARD.unlink()
        except FileNotFoundError:
            pass
        if run(["systemctl", "unmask", *SSH_UNITS], mutate=True).returncode:
            raise ValueError("could not record the SSH install guard, and removing its masks also failed; no package was installed. Remove the masks by hand: systemctl unmask " + " ".join(SSH_UNITS))
        raise ValueError("could not record the SSH install guard; the masks were removed and no package was installed")


def release_ssh_guard(*, apply):
    if not SSH_GUARD.exists() and not SSH_GUARD.is_symlink():
        print("NO-OP no kit-owned SSH install guard; existing masks are preserved")
        return 0
    _guard_record()
    print("PREVIEW release only the two kit-owned SSH install masks after validated hardening")
    if not apply:
        return 0
    if os.geteuid() != 0:
        raise ValueError("releasing the SSH install guard requires root, from the root-owned staged copy")
    require_staged_root(KIT_ROOT)
    ufw = run(["ufw", "status"])
    defaults = Path("/etc/default/ufw").read_text()
    if ufw.returncode or "Status: active" not in ufw.stdout or not all(re.search(r'^\s*' + key + r'="?DROP"?\s*$', defaults, re.M) for key in ("DEFAULT_INPUT_POLICY", "DEFAULT_FORWARD_POLICY")):
        raise ValueError("active deny-by-default firewall is required before releasing the SSH guard")
    if run(["systemctl", "unmask", *SSH_UNITS], mutate=True).returncode:
        raise ValueError("SSH install guard release failed")
    # Keep the ownership record until hardening confirms SSH is active. Its
    # failure handler can then stop/re-mask these originally inactive units.
    return 0


def provision(platform: str, packages: dict[str, list[str]], *, apply: bool = False, check: bool = False) -> int:
    actual = "macos" if sys.platform == "darwin" else "linux" if sys.platform.startswith("linux") else "unsupported"
    if platform != actual:
        raise ValueError(f"package platform {platform} differs from host {actual}")
    if platform == "macos" and os.geteuid() == 0:
        raise ValueError("Homebrew runs as its named package owner, never root")
    if apply and platform == "linux":
        if os.geteuid() != 0:
            raise ValueError("APT apply requires root, from the root-owned staged copy")
        require_staged_root(KIT_ROOT)
    installed = inventory(platform, packages)
    missing = missing_packages(packages, installed)
    for kind in packages:
        for name, version in installed[kind].items():
            print(f"INSTALLED {kind} {name} {version}")
    commands = []
    if platform == "linux" and any(spec.partition("=")[0] == "openssh-server" for spec in missing["apt"]):
        guard_new_ssh_install(apply=apply)
    if platform == "linux" and missing["apt"]:
        commands = [["apt-get", "update"], ["apt-get", "install", "-y", *missing["apt"]]]
    if platform == "macos":
        for kind, flag in (("formulae", "--formula"), ("casks", "--cask")):
            if missing[kind]:
                commands.append(["brew", "install", flag, *missing[kind]])
    import shlex
    for command in commands:
        print(("APPLY " if apply else "PREVIEW ") + shlex.join(command), flush=True)
        if apply and run(command, mutate=True).returncode:
            return 1
    if check or apply:
        # Query again after installation. An installer exit code alone is insufficient.
        remaining = missing_packages(packages, inventory(platform, packages) if apply and commands else installed)
        if any(remaining.values()):
            print(f"FAIL missing packages: {remaining}")
            return 1
        print("PASS selected package set is installed")
    elif not commands:
        print("NO-OP selected packages are already installed; no automatic upgrade")
    return 0
