"""Read-only evidence shared by both pilot entry points."""
from pathlib import Path
import os
import subprocess


def source_evidence(root: Path, version_file: str = "VERSION") -> tuple[str, str]:
    root = root.resolve()
    if not (root / ".git").exists():
        return "FAIL", "not a source checkout (archive/staged snapshot); run readiness from the reviewed source worktree"
    def git(*args: str):
        return subprocess.run(["git", "-C", str(root), *args], text=True, capture_output=True, check=False)
    try:
        top = git("rev-parse", "--show-toplevel")
        if top.returncode or Path(top.stdout.strip()).resolve() != root:
            return "FAIL", "Git root does not match toolkit root"
        revision = git("rev-parse", "--verify", "HEAD")
        version = (root / version_file).read_text().strip()
        committed = git("show", f"HEAD:{version_file}")
        if revision.returncode or committed.returncode or committed.stdout.strip() != version:
            return "FAIL", "cannot bind version pin to the source revision"
        return "PASS", f"source version={version} revision={revision.stdout.strip()}; remote freshness unverified (no fetch performed)"
    except (OSError, ValueError) as error:
        return "FAIL", f"source evidence unavailable: {error}"


def execution_evidence(declared: str, environment=None) -> tuple[str, str]:
    env = os.environ if environment is None else environment
    restricted = any(env.get(key) for key in ("CODEX_SANDBOX", "SANDBOX_EXECUTION_ID", "container"))
    if declared != "host" or restricted:
        return "FAIL", "host checks unverified: use an ordinary target-host terminal and --execution-context host; sandbox output cannot certify the host"
    return "PASS", "target-host terminal attested by operator; verify host probes directly and record denied/unknown results separately"
