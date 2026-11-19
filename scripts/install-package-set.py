#!/usr/bin/env python3
"""Preview or install a profile-selected package set; never upgrades existing packages."""
import argparse
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent / "lib"))
from package_setup import provision, selected_packages, release_ssh_guard


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--platform", choices=("linux", "macos"), required=True)
    parser.add_argument("--apt", action="append", default=[])
    parser.add_argument("--formula", action="append", default=[])
    parser.add_argument("--cask", action="append", default=[])
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--apply", action="store_true")
    mode.add_argument("--check", action="store_true", help="verify installed selection without applying")
    parser.add_argument("--release-ssh-guard", action="store_true", help="hardening-only: release kit-owned masks after firewall validation")
    args = parser.parse_args()
    if (args.platform == "macos" and args.apt) or (args.platform == "linux" and (args.formula or args.cask)):
        parser.error("package options must match the target platform")
    try:
        if args.release_ssh_guard:
            if args.platform != "linux" or args.check or args.apt or args.formula or args.cask:
                parser.error("SSH guard release is a separate Linux hardening operation")
            return release_ssh_guard(apply=args.apply)
        return provision(args.platform, selected_packages(args.platform, {"apt": args.apt, "formulae": args.formula, "casks": args.cask}), apply=args.apply, check=args.check)
    except (OSError, ValueError) as error:
        print(f"ERROR packages: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
