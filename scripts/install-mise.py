#!/usr/bin/env python3
"""Install a reviewed official mise binary for the current account, with SHA-256 verification."""
from __future__ import annotations
import argparse
import hashlib
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import tomllib
import urllib.request


def asset_name(version, system, architecture):
    if not re.fullmatch(r"202[6-9]\.\d{1,2}\.\d{1,2}", version):
        raise ValueError("select an explicit reviewed mise version (2026.M.P or newer)")
    os_name = {"Linux": "linux", "Darwin": "macos"}.get(system)
    arch = {"x86_64": "x64", "amd64": "x64", "aarch64": "arm64", "arm64": "arm64"}.get(architecture)
    if not os_name or not arch:
        raise ValueError("unsupported mise OS/architecture")
    return "mise-v" + version + "-" + os_name + "-" + arch


def install(version, expected, *, apply=False):
    if not re.fullmatch(r"[a-f0-9]{64}", expected):
        raise ValueError("supply the independently reviewed official binary SHA-256")
    if os.geteuid() == 0:
        raise ValueError("mise user-space installation runs as the intended non-root account")
    architecture = platform.machine()
    if sys.platform == "darwin" and architecture == "x86_64":
        probe = subprocess.run(["/usr/sbin/sysctl", "-n", "hw.optional.arm64"], text=True, capture_output=True, check=False)
        if probe.returncode == 0 and probe.stdout.strip() == "1":
            architecture = "arm64"
    asset = asset_name(version, platform.system(), architecture)
    url = "https://github.com/jdx/mise/releases/download/v" + version + "/" + asset
    target = Path.home() / ".local/bin/mise"
    if target.is_symlink():
        raise ValueError("existing mise symlink is preserved; review its package owner separately")
    if target.exists():
        if hashlib.sha256(target.read_bytes()).hexdigest() != expected:
            raise ValueError("existing mise differs from the approved binary; no automatic replacement or downgrade")
        print("NO-OP existing mise matches approved SHA-256")
        return 0
    existing = shutil.which("mise")
    if existing:
        raise ValueError("mise already exists elsewhere on PATH; preserve it and review a single installation method")
    print(("APPLY " if apply else "PREVIEW ") + "download " + url + "; verify SHA-256 " + expected + "; install " + str(target))
    if not apply:
        return 0
    target.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    info = target.parent.stat()
    if target.parent.is_symlink() or info.st_uid != os.geteuid() or info.st_mode & 0o022:
        raise ValueError("user binary directory must be owned by this account and not group/world writable")
    descriptor, temporary_name = tempfile.mkstemp(prefix=".mise-", dir=target.parent)
    temporary = Path(temporary_name)
    try:
        checksum = hashlib.sha256()
        size = 0
        with os.fdopen(descriptor, "wb") as stream, urllib.request.urlopen(url, timeout=30) as response:
            while True:
                chunk = response.read(1024 * 1024)
                if not chunk:
                    break
                size += len(chunk)
                if size > 256 * 1024 * 1024:
                    raise ValueError("mise download exceeds the bounded binary size")
                checksum.update(chunk)
                stream.write(chunk)
        if checksum.hexdigest() != expected:
            raise ValueError("mise checksum mismatch; downloaded content was not executed or installed")
        temporary.chmod(0o755)
        result = subprocess.run([str(temporary), "--version"], text=True, capture_output=True, check=False)
        if result.returncode or not re.search(r"(?<![\d.])" + re.escape(version) + r"(?![\d.])", result.stdout):
            raise ValueError("verified binary did not report the selected mise version")
        # A concurrent/existing file is never overwritten, even after download.
        os.link(temporary, target)
        print("PASS installed mise " + version + " with verified source digest")
        return 0
    finally:
        temporary.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version")
    parser.add_argument("--sha256")
    parser.add_argument("--baseline", action="store_true", help="use the signature-reviewed Linux binary catalog")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        if args.baseline:
            if args.version or args.sha256 or platform.system() != "Linux":
                parser.error("baseline is a standalone Linux choice; no version overrides or duplicate Mac Homebrew install")
            data = tomllib.loads((Path(__file__).resolve().parents[1] / "config/mise-bootstrap.toml").read_text())
            asset = asset_name(data["version"], "Linux", platform.machine())
            key = asset.removeprefix("mise-v" + data["version"] + "-")
            return install(data["version"], data["sha256"][key], apply=args.apply)
        if not args.version or not args.sha256:
            parser.error("select --baseline or both --version and --sha256")
        return install(args.version, args.sha256, apply=args.apply)
    except (OSError, ValueError) as error:
        print("ERROR mise: " + str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
