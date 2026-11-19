"""Keep private profiles, execution bundles and journals outside every kit worktree."""
from __future__ import annotations
from pathlib import Path
import subprocess


def outside_kit(path, kit_root):
    roots = {kit_root.resolve()}
    if (kit_root / ".git").exists():
        result = subprocess.run(["git", "-C", str(kit_root), "worktree", "list", "--porcelain", "-z"], capture_output=True, text=True, check=False, timeout=5)
        if result.returncode:
            raise ValueError("cannot establish public worktree boundaries; check Git before choosing private storage")
        roots.update(Path(record.removeprefix("worktree ")).resolve() for record in result.stdout.split("\0") if record.startswith("worktree "))
    candidate = path.resolve()
    return not any(candidate.is_relative_to(root) for root in roots)
