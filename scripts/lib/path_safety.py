"""Keep private profiles, execution bundles and journals outside every kit worktree."""
from __future__ import annotations
import os
from pathlib import Path
import subprocess

STAGED_ROOT = Path("/opt/agent-workstation-kit")


def outside_kit(path, kit_root):
    roots = {kit_root.resolve()}
    if (kit_root / ".git").exists():
        result = subprocess.run(["git", "-C", str(kit_root), "worktree", "list", "--porcelain", "-z"], capture_output=True, text=True, check=False, timeout=5)
        if result.returncode:
            raise ValueError("cannot establish public worktree boundaries; check Git before choosing private storage")
        roots.update(Path(record.removeprefix("worktree ")).resolve() for record in result.stdout.split("\0") if record.startswith("worktree "))
    candidate = path.resolve()
    return not any(candidate.is_relative_to(root) for root in roots)


def _root_controlled(path):
    info = path.lstat()
    return not path.is_symlink() and info.st_uid == 0 and not info.st_mode & 0o022


def require_staged_root(kit_root):
    """Refuse root changes unless they come from the root-owned staged copy.

    The kit path must be /opt/agent-workstation-kit, and it, its parents and
    every file and directory inside it must be root-owned, not group or world
    writable, and not symlinks. A user-writable checkout fails here.
    """
    root = Path(os.path.abspath(kit_root))
    if root != STAGED_ROOT:
        raise ValueError("root changes run only from the staged copy at " + str(STAGED_ROOT) + "; this toolkit is at " + str(root))
    for entry in (root, *root.parents):
        if not _root_controlled(entry):
            raise ValueError("staged toolkit path is not root-controlled: " + str(entry))
    for directory, names, files in os.walk(root, followlinks=False):
        for name in (*names, *files):
            entry = Path(directory) / name
            if not _root_controlled(entry):
                raise ValueError("staged toolkit contains a non-root-controlled path: " + str(entry))
