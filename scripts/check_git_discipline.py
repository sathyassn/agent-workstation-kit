#!/usr/bin/env python3
"""Enforce the repository's self-contained Git contribution policy."""

from __future__ import annotations

import argparse
import fnmatch
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import TextIO


ROOT = Path(__file__).resolve().parents[1]
POLICY_PATH = ROOT / ".codeflow/policy.json"
CONVENTIONAL_SUBJECT = re.compile(
    r"^(?P<type>[a-z][a-z0-9-]*)(?:\((?P<scope>[a-z0-9][a-z0-9._/-]*)\))?"
    r"(?P<breaking>!)?: (?P<description>.+)$"
)
HEADING = re.compile(r"^##\s+(.+?)\s*$", re.MULTILINE)
AI_ATTRIBUTION = re.compile(
    r"(?:co-authored-by:.*(?:claude|anthropic|chatgpt|codex|openai|grok)|"
    r"generated (?:by|with) (?:ai|claude|chatgpt|codex|grok))",
    re.IGNORECASE,
)
EMOJI = re.compile(
    "["
    "\U0001F1E6-\U0001F1FF"
    "\U0001F300-\U0001FAFF"
    "\U00002600-\U000027BF"
    "]"
)
SECRET_LIKE = re.compile(
    r"(?:AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{30,}|"
    r"glpat-[A-Za-z0-9_-]{20,}|sk-[A-Za-z0-9_-]{24,})"
)
FORBIDDEN_STAGED_NAMES = {
    ".env",
    "auth.json",
    "credentials.json",
    "id_ed25519",
    "id_rsa",
}


class DisciplineError(RuntimeError):
    """A policy violation suitable for concise command-line output."""


@dataclass(frozen=True)
class Policy:
    """The validated Git subset consumed by this repository's checker."""

    protected_branches: tuple[str, ...]
    commit_types: tuple[str, ...]
    commit_desc_max_len: int
    commit_subject_max_len: int
    commit_body_max_bullets: int
    commit_body_bullet_max_len: int
    commit_footer_tokens: tuple[str, ...]
    branch_prefixes: tuple[str, ...]
    pr_required_sections: tuple[str, ...]
    pr_code_sections: tuple[str, ...]
    breaking_watch_paths: tuple[str, ...]


def git(*args: str, check: bool = True) -> str:
    """Run Git in the repository and return stdout."""
    result = subprocess.run(
        ["git", *args],
        cwd=ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    if check and result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or "Git command failed"
        raise DisciplineError(detail)
    return result.stdout


def _string_list(container: dict[str, object], key: str) -> tuple[str, ...]:
    value = container.get(key)
    if not isinstance(value, list) or not value or not all(
        isinstance(item, str) and item for item in value
    ):
        raise DisciplineError(f"policy key git.{key} must be a non-empty string list")
    return tuple(value)


def _positive_int(container: dict[str, object], key: str) -> int:
    value = container.get(key)
    if not isinstance(value, int) or isinstance(value, bool) or value <= 0:
        raise DisciplineError(f"policy key git.{key} must be a positive integer")
    return value


def load_policy(path: Path = POLICY_PATH) -> Policy:
    """Load and validate the policy fields enforced by the native checker."""
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise DisciplineError(f"policy file is missing: {path}") from exc
    except json.JSONDecodeError as exc:
        raise DisciplineError(f"invalid policy JSON: {exc}") from exc
    if not isinstance(raw, dict) or raw.get("schema_version") != 1:
        raise DisciplineError("policy schema_version must be 1")
    rules = raw.get("git")
    if not isinstance(rules, dict):
        raise DisciplineError("policy must contain a git object")

    for key in (
        "commit_to_protected",
        "push_to_protected",
        "force_push_protected",
        "force_push_unprotected",
        "delete_protected",
        "hard_reset_protected",
        "merge_to_protected",
        "pr_merge_to_protected",
        "local_ref_protection",
        "hook_integrity",
        "commit_format",
        "commit_body",
        "ai_attribution",
        "commit_emoji",
        "pr_sections",
        "branch_naming",
        "secret_scan",
        "test_gate_on_push",
    ):
        if rules.get(key) != "block":
            raise DisciplineError(f"policy key git.{key} must remain 'block'")

    prefixes = _string_list(rules, "branch_prefixes")
    if any(not prefix.endswith("/") for prefix in prefixes):
        raise DisciplineError("every branch prefix must end with '/'")

    return Policy(
        protected_branches=_string_list(rules, "protected_branches"),
        commit_types=_string_list(rules, "commit_types"),
        commit_desc_max_len=_positive_int(rules, "commit_desc_max_len"),
        commit_subject_max_len=_positive_int(rules, "commit_subject_max_len"),
        commit_body_max_bullets=_positive_int(rules, "commit_body_max_bullets"),
        commit_body_bullet_max_len=_positive_int(
            rules, "commit_body_bullet_max_len"
        ),
        commit_footer_tokens=_string_list(rules, "commit_footer_tokens"),
        branch_prefixes=prefixes,
        pr_required_sections=_string_list(rules, "pr_required_sections"),
        pr_code_sections=_string_list(rules, "pr_code_sections"),
        breaking_watch_paths=_string_list(rules, "breaking_watch_paths"),
    )


def current_branch() -> str:
    """Return the current branch, rejecting detached HEAD."""
    branch = git("branch", "--show-current").strip()
    if not branch:
        raise DisciplineError("detached HEAD is not a valid contribution branch")
    return branch


def validate_branch(
    branch: str, policy: Policy, *, allow_protected: bool = False
) -> None:
    """Validate a contribution branch against the configured prefixes."""
    if branch in policy.protected_branches:
        if allow_protected:
            return
        raise DisciplineError(f"protected branch is not writable: {branch}")
    if not any(branch.startswith(prefix) for prefix in policy.branch_prefixes):
        allowed = ", ".join(policy.branch_prefixes)
        raise DisciplineError(f"branch '{branch}' must start with one of: {allowed}")
    remainder = branch.split("/", 1)[1]
    if branch.startswith("dependabot/"):
        if (
            re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+@~/-]*", remainder)
            and ".." not in remainder
            and "//" not in remainder
            and "@{" not in remainder
        ):
            return
        raise DisciplineError("Dependabot branch description is invalid")
    if not re.fullmatch(r"[a-z0-9]+(?:-[a-z0-9]+)*", remainder):
        raise DisciplineError("branch description must be lower-case kebab-case")


def _visible_message_lines(message: str) -> list[str]:
    return [line.rstrip() for line in message.splitlines() if not line.startswith("#")]


def validate_message(
    message: str, policy: Policy, *, allow_generated_body: bool = False
) -> None:
    """Validate one non-merge commit message."""
    lines = _visible_message_lines(message)
    while lines and not lines[-1]:
        lines.pop()
    if not lines or not lines[0]:
        raise DisciplineError("commit message is empty")
    subject = lines[0]
    if subject.startswith("Merge "):
        return
    if len(subject) > policy.commit_subject_max_len:
        raise DisciplineError(
            f"commit subject exceeds {policy.commit_subject_max_len} characters"
        )
    match = CONVENTIONAL_SUBJECT.fullmatch(subject)
    if not match:
        raise DisciplineError("commit subject must use type(scope): description")
    if match.group("type") not in policy.commit_types:
        allowed = ", ".join(policy.commit_types)
        raise DisciplineError(f"commit type must be one of: {allowed}")
    description = match.group("description")
    if len(description) > policy.commit_desc_max_len:
        raise DisciplineError(
            f"commit description exceeds {policy.commit_desc_max_len} characters"
        )
    if description.endswith("."):
        raise DisciplineError("commit description must not end with a period")
    visible_message = "\n".join(lines)
    if AI_ATTRIBUTION.search(visible_message):
        raise DisciplineError("AI attribution is not allowed in commit messages")
    if EMOJI.search(visible_message):
        raise DisciplineError("emoji is not allowed in commit messages")

    if allow_generated_body:
        if match.group("breaking") and "BREAKING CHANGE:" not in visible_message:
            raise DisciplineError(
                "a breaking subject requires a BREAKING CHANGE: footer"
            )
        return

    body = lines[1:]
    while body and not body[0]:
        body.pop(0)
    bullets = 0
    breaking_footer = False
    allowed_footers = (*policy.commit_footer_tokens, "BREAKING CHANGE")
    for line in body:
        if not line:
            continue
        footer = next(
            (token for token in allowed_footers if line.startswith(f"{token}:")),
            None,
        )
        if footer:
            breaking_footer = breaking_footer or footer == "BREAKING CHANGE"
            continue
        if not line.startswith("- "):
            raise DisciplineError("commit body may contain only short '-' bullets")
        bullets += 1
        if len(line) > policy.commit_body_bullet_max_len:
            raise DisciplineError(
                "commit body bullet exceeds "
                f"{policy.commit_body_bullet_max_len} characters"
            )
    if bullets > policy.commit_body_max_bullets:
        raise DisciplineError(
            f"commit body has more than {policy.commit_body_max_bullets} bullets"
        )
    if match.group("breaking") and not breaking_footer:
        raise DisciplineError("a breaking subject requires a BREAKING CHANGE: footer")


def warn_unmarked_contract_change(
    message: str, paths: list[str], policy: Policy
) -> None:
    """Warn when declared contract surfaces change without a breaking marker."""
    lines = _visible_message_lines(message)
    if not lines or "!:" in lines[0] or "BREAKING CHANGE:" in message:
        return
    watched = sorted(
        path
        for path in paths
        if any(
            fnmatch.fnmatchcase(path, pattern)
            for pattern in policy.breaking_watch_paths
        )
    )
    if watched:
        print(
            "Git discipline warning: review whether this changes a consumed contract: "
            + ", ".join(watched),
            file=sys.stderr,
        )


def _staged_paths() -> list[str]:
    return [
        item
        for item in git(
            "diff", "--cached", "--name-only", "--diff-filter=ACMR"
        ).splitlines()
        if item
    ]


def validate_staged_secrets() -> None:
    """Reject obvious credential files and secret-shaped staged additions."""
    for relative in _staged_paths():
        path = Path(relative)
        if (
            path.name in FORBIDDEN_STAGED_NAMES
            or path.suffix.lower() in {".key", ".pem", ".p12", ".pfx"}
        ):
            raise DisciplineError(
                f"credential-like file must not be staged: {relative}"
            )
    added = git("diff", "--cached", "--no-ext-diff", "--unified=0", "--")
    additions = "\n".join(
        line[1:]
        for line in added.splitlines()
        if line.startswith("+") and not line.startswith("+++")
    )
    if SECRET_LIKE.search(additions):
        raise DisciplineError("secret-shaped value found in staged additions")


def validate_pr_body(body: str, policy: Policy, *, code_change: bool) -> None:
    """Validate required PR sections plus attribution and emoji rules."""
    if AI_ATTRIBUTION.search(body):
        raise DisciplineError("AI attribution is not allowed in pull-request bodies")
    if EMOJI.search(body):
        raise DisciplineError("emoji is not allowed in pull-request bodies")
    headings = {heading.strip().lower() for heading in HEADING.findall(body)}
    required = list(policy.pr_required_sections)
    if code_change:
        required.extend(policy.pr_code_sections)
    missing = [section for section in required if section.lower() not in headings]
    if missing:
        raise DisciplineError(
            "pull-request body is missing sections: " + ", ".join(missing)
        )


def _changed_paths(base: str, head: str) -> list[str]:
    return [
        item
        for item in git("diff", "--name-only", base, head).splitlines()
        if item
    ]


def _is_docs_only(paths: list[str]) -> bool:
    top_level_docs = {
        "CHANGELOG.md",
        "CODE_OF_CONDUCT.md",
        "CONTRIBUTING.md",
        "README.md",
        "SECURITY.md",
    }
    return bool(paths) and all(
        path.startswith("docs/")
        or path in top_level_docs
        or Path(path).name.startswith("LICENSE")
        or path == ".github/pull_request_template.md"
        for path in paths
    )


def validate_commit_range(
    base: str,
    head: str,
    policy: Policy,
    *,
    allow_generated_body: bool = False,
) -> None:
    """Validate every non-merge commit in an explicit, bounded range."""
    commits = [
        item
        for item in git(
            "rev-list", "--reverse", "--no-merges", f"{base}..{head}"
        ).splitlines()
        if item
    ]
    if not commits:
        raise DisciplineError(f"commit range is empty: {base}..{head}")
    for commit in commits:
        message = git("show", "-s", "--format=%B", commit)
        try:
            validate_message(
                message, policy, allow_generated_body=allow_generated_body
            )
        except DisciplineError as exc:
            short = git("rev-parse", "--short", commit).strip()
            raise DisciplineError(f"commit {short}: {exc}") from exc
        paths = [
            item
            for item in git(
                "diff-tree", "--no-commit-id", "--name-only", "-r", commit
            ).splitlines()
            if item
        ]
        warn_unmarked_contract_change(message, paths, policy)


def _is_ancestor(old: str, new: str) -> bool:
    result = subprocess.run(
        ["git", "merge-base", "--is-ancestor", old, new],
        cwd=ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    return result.returncode == 0


def _is_zero_oid(value: str) -> bool:
    """Recognize Git's null object ID for either SHA-1 or SHA-256 repositories."""
    return bool(value) and set(value) == {"0"}


def _protected_sync_head(branch: str) -> str:
    """Return the exact configured upstream tip used to prove a safe sync."""
    upstream = git(
        "rev-parse",
        "--symbolic-full-name",
        f"{branch}@{{upstream}}",
        check=False,
    ).strip()
    if not upstream:
        return ""
    return git("rev-parse", "--verify", upstream, check=False).strip()


def run_pre_push(stdin: TextIO, policy: Policy) -> None:
    """Validate ref updates from Git's pre-push protocol, then run the full gate."""
    updates = [line.split() for line in stdin if line.strip()]
    for fields in updates:
        if len(fields) != 4:
            raise DisciplineError("unexpected pre-push input")
        local_ref, local_oid, remote_ref, remote_oid = fields
        if not remote_ref.startswith("refs/heads/"):
            continue
        branch = remote_ref.removeprefix("refs/heads/")
        if branch in policy.protected_branches:
            raise DisciplineError(
                f"direct push to protected branch is blocked: {branch}"
            )
        validate_branch(branch, policy)
        if _is_zero_oid(local_oid):
            continue
        if not _is_zero_oid(remote_oid) and not _is_ancestor(remote_oid, local_oid):
            raise DisciplineError(f"force push is blocked: {branch}")
    if updates:
        gate_env = os.environ.copy()
        for inherited_git_key in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"):
            gate_env.pop(inherited_git_key, None)
        try:
            result = subprocess.run(
                ["make", "ci-check"],
                cwd=ROOT,
                check=False,
                timeout=600,
                env=gate_env,
            )
        except subprocess.TimeoutExpired as exc:
            raise DisciplineError(
                "pre-push repository gate exceeded 10 minutes"
            ) from exc
        if result.returncode != 0:
            raise DisciplineError("pre-push repository gate failed")


def run_reference_transaction(stage: str, stdin: TextIO, policy: Policy) -> None:
    """Allow exact upstream syncs but block other protected local ref changes."""
    if stage != "prepared":
        return
    for line in stdin:
        fields = line.split()
        if len(fields) != 3:
            continue
        _old_oid, new_oid, ref = fields
        if ref.startswith("refs/heads/"):
            branch = ref.removeprefix("refs/heads/")
            if branch not in policy.protected_branches:
                continue
            sync_head = _protected_sync_head(branch)
            if not sync_head:
                raise DisciplineError(
                    f"protected branch has no configured upstream: {branch}"
                )
            if _is_zero_oid(new_oid) or new_oid != sync_head:
                raise DisciplineError(
                    "local update that is not an exact upstream sync is blocked: "
                    f"{branch}"
                )


def command_policy(_args: argparse.Namespace, policy: Policy) -> None:
    print(f"Git discipline policy is valid: {POLICY_PATH.relative_to(ROOT)}")


def command_pre_commit(_args: argparse.Namespace, policy: Policy) -> None:
    validate_branch(current_branch(), policy)
    validate_staged_secrets()


def command_commit_msg(args: argparse.Namespace, policy: Policy) -> None:
    validate_branch(current_branch(), policy)
    message = Path(args.message_file).read_text(encoding="utf-8")
    validate_message(message, policy)
    warn_unmarked_contract_change(message, _staged_paths(), policy)


def command_pre_merge(_args: argparse.Namespace, policy: Policy) -> None:
    validate_branch(current_branch(), policy)


def command_pre_push(args: argparse.Namespace, policy: Policy) -> None:
    del args
    run_pre_push(sys.stdin, policy)


def command_reference(args: argparse.Namespace, policy: Policy) -> None:
    run_reference_transaction(args.stage, sys.stdin, policy)


def command_ci(args: argparse.Namespace, policy: Policy) -> None:
    head_repository = getattr(args, "head_repository", "")
    base_repository = getattr(args, "base_repository", "")
    is_fork = bool(
        head_repository
        and base_repository
        and head_repository != base_repository
    )
    actor = getattr(args, "actor", "")
    is_dependabot = actor == "dependabot[bot]" and args.branch.startswith(
        "dependabot/"
    )
    validate_branch(args.branch, policy, allow_protected=is_fork)
    validate_commit_range(
        args.base,
        args.head,
        policy,
        allow_generated_body=is_dependabot,
    )
    body = os.environ.get(args.pr_body_env)
    if body is None:
        raise DisciplineError(f"environment variable is missing: {args.pr_body_env}")
    if not is_dependabot:
        validate_pr_body(
            body,
            policy,
            code_change=not _is_docs_only(_changed_paths(args.base, args.head)),
        )
    print(f"Git discipline passed for {args.base}..{args.head} on {args.branch}")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    subparsers.add_parser("policy", help="validate the checked-in policy")
    subparsers.add_parser("pre-commit", help="run the pre-commit policy")
    commit_msg = subparsers.add_parser("commit-msg", help="validate a message file")
    commit_msg.add_argument("message_file")
    subparsers.add_parser("pre-merge-commit", help="block merges on protected branches")
    pre_push = subparsers.add_parser(
        "pre-push", help="validate ref updates and run the full gate"
    )
    pre_push.add_argument("remote_name", nargs="?")
    pre_push.add_argument("remote_location", nargs="?")
    reference = subparsers.add_parser(
        "reference-transaction", help="protect local branch refs"
    )
    reference.add_argument("stage", choices=("prepared", "committed", "aborted"))
    ci = subparsers.add_parser("ci", help="validate a pull-request commit range")
    ci.add_argument("--base", required=True)
    ci.add_argument("--head", required=True)
    ci.add_argument("--branch", required=True)
    ci.add_argument("--head-repository", default="")
    ci.add_argument("--base-repository", default="")
    ci.add_argument("--actor", default="")
    ci.add_argument("--pr-body-env", required=True)
    return parser


COMMANDS = {
    "policy": command_policy,
    "pre-commit": command_pre_commit,
    "commit-msg": command_commit_msg,
    "pre-merge-commit": command_pre_merge,
    "pre-push": command_pre_push,
    "reference-transaction": command_reference,
    "ci": command_ci,
}


def main() -> int:
    args = build_parser().parse_args()
    try:
        policy = load_policy()
        COMMANDS[args.command](args, policy)
    except (DisciplineError, OSError) as exc:
        print(f"Git discipline blocked the operation: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
