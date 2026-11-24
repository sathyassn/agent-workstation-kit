#!/usr/bin/env python3
"""Contributor and CI commit identity collection and linear-time matching.

Local mode collects the executing workstation's identity at check time and
keeps it strictly in memory (never written to disk, never printed to output or
errors):
- git user.name and user.email
- account username and full name (passwd/GECOS, os.getlogin when available)
- host name (socket.gethostname)
- macOS scutil ComputerName, LocalHostName, and HostName (when set)
- Linux hostnamectl static/pretty/transient hostnames
- Tailscale node and tailnet names when Tailscale is installed (reported as
  absent when not installed; fails closed if installed and returning an error)

CI mode extracts git author and committer names and emails for every commit
under review and fails closed on incomplete/shallow history or missing fields.

Every collected identity token goes through the same decoding and folding as
tests/check_public_release.py before matching via an Aho-Corasick automaton
(linear time, no regex backtracking).
"""

from __future__ import annotations

import json
import os
import pwd
import re
import shutil
import socket
import subprocess
import sys
from collections import deque
from collections.abc import Callable, Iterable, Sequence
from dataclasses import dataclass
from pathlib import Path


IDENTITY_RULE_ID = "identity"
IDENTITY_VERIFIED_MARKER = "PUBLIC_SCAN_IDENTITY_VERIFIED=1"
COMMAND_TIMEOUT_SECONDS = 5.0
MIN_IDENTITY_TOKEN_LEN = 3

WHITESPACE_RE = re.compile(r"\s+")
SEP_COLLAPSE_RE = re.compile(r"[-_.\s]+")
ZERO_PAD_RE = re.compile(r"(?<!\d)0+(\d+)")
CAMEL_BOUNDARY_RE = re.compile(r"(?<=[a-z])(?=[A-Z])")
GITHUB_NOREPLY_LOCAL_RE = re.compile(r"^\d+\+(.+)$")

# Documented exact allow list shared with tests/check_public_release.py.
ALLOWED_EMAIL_DOMAINS = frozenset(
    {
        "example.com",
        "example.org",
        "example.net",
        "example.invalid",
    }
)
ALLOWED_HOME_USERS = frozenset(
    {
        "alice",
        "bob",
        "agent",
        "agent-01",
        "admin",
        "admin-01",
        "operator",
        "user",
        "username",
        "example",
        "runner",
        "ubuntu",
        "dev",
        "acme",
        "lab",
    }
)
ALLOWED_PRIVATE_DNS = frozenset(
    {
        "host-a.ts.net",
        "host-a.internal",
        "host-a.local",
        "host-a.example.ts.net",
        "example.ts.net",
        "example.internal",
        "example.local",
        "localhost.local",
    }
)
ALLOWED_IDENTITY_TOKENS = frozenset(
    ALLOWED_HOME_USERS
    | ALLOWED_PRIVATE_DNS
    | ALLOWED_EMAIL_DOMAINS
    | {
        "host-a",
        "localhost",
        "localhost.localdomain",
    }
)
GENERIC_EMAIL_LOCAL_PARTS = frozenset(
    ALLOWED_IDENTITY_TOKENS
    | {
        "noreply",
        "no-reply",
        "root",
        "info",
        "mail",
        "support",
        "contact",
        "security",
        "hello",
        "team",
        "bot",
        "ci",
        "ops",
        "me",
    }
)

# Exact platform service committer (name, email) pairs (casefolded).
# GitHub web merge/squash commits set committer to GitHub with GitHub's noreply address.
PLATFORM_SERVICE_COMMITTERS = frozenset(
    {
        ("github", "noreply" + "@" + "github.com"),
        ("dependabot[bot]", "49699333+dependabot[bot]" + "@" + "users.noreply.github.com"),
        ("github-actions[bot]", "41898282+github-actions[bot]" + "@" + "users.noreply.github.com"),
    }
)

# Exact documented public project addresses per docs/standards/engineering.md
# ("Public identifiers"). Only these exact repository/release URLs are masked;
# other repositories under the same owner (e.g. a private fleet repo) remain checked.
ALLOWED_PUBLIC_PROJECT_ADDRESSES = (
    "github.com/sathyassn/agent-workstation-kit",
    "github.com/sathyassn/codeflow",
)
PUBLIC_PROJECT_ADDR_RE = re.compile(
    r"(?<![A-Za-z0-9.-])(?:"
    + "|".join(re.escape(addr) for addr in ALLOWED_PUBLIC_PROJECT_ADDRESSES)
    + r")(?![A-Za-z0-9._-])",
    re.IGNORECASE,
)
CODEOWNERS_OWNER_RE = re.compile(r"(?<=\s)@[A-Za-z0-9](?:[A-Za-z0-9-]|/[A-Za-z0-9_-])*(?=$|\s)")
EXEMPTION_APPROVED_BY_RE = re.compile(
    r'^(\s*"approved_by"\s*:\s*)"@[A-Za-z0-9](?:[A-Za-z0-9-]|/[A-Za-z0-9_-])*"(?=\s*,?\s*$)',
    re.MULTILINE,
)

HOSTNAMECTL_KEYS = frozenset({"static hostname", "pretty hostname", "transient hostname"})
HOSTNAMECTL_EMPTY_VALUES = frozenset({"", "(none)", "n/a", "none"})


class IdentityCollectionError(ValueError):
    """Raised when an identity source fails, times out, or returns missing data.

    Messages must never contain any collected identity values.
    """


@dataclass(frozen=True)
class IdentityProfile:
    """In-memory identity tokens and safe status notes (never persisted)."""

    source_kind: str  # "local" | "ci"
    tokens: frozenset[str]
    notes: tuple[str, ...] = ()


def _default_run_cmd(
    cmd: Sequence[str],
    *,
    cwd: Path | None = None,
    timeout: float = COMMAND_TIMEOUT_SECONDS,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        list(cmd),
        cwd=cwd,
        capture_output=True,
        text=True,
        timeout=timeout,
        check=False,
    )


def _exec_source(
    cmd: Sequence[str],
    source_label: str,
    *,
    cwd: Path | None = None,
    run_cmd: Callable[..., subprocess.CompletedProcess[str]],
    timeout: float,
) -> subprocess.CompletedProcess[str]:
    try:
        return run_cmd(list(cmd), cwd=cwd, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        raise IdentityCollectionError(
            f"identity source '{source_label}' timed out; failing closed"
        ) from exc
    except FileNotFoundError as exc:
        raise IdentityCollectionError(
            f"identity source '{source_label}' command is unavailable; failing closed"
        ) from exc
    except OSError as exc:
        raise IdentityCollectionError(
            f"identity source '{source_label}' could not be executed; failing closed"
        ) from exc


def _fold_identity_text(value: str, normalize_fn: Callable[[str], str]) -> str:
    return WHITESPACE_RE.sub(" ", normalize_fn(value).strip()).casefold()


def derive_identity_tokens(
    raw_items: Iterable[tuple[str, str]],
    *,
    normalize_fn: Callable[[str], str],
) -> frozenset[str]:
    """Fold raw (kind, value) identity entries into matchable canonical tokens."""
    tokens: set[str] = set()

    def add_token(candidate: str) -> None:
        folded = _fold_identity_text(candidate, normalize_fn)
        if not folded or len(folded) < MIN_IDENTITY_TOKEN_LEN:
            return
        if folded in ALLOWED_IDENTITY_TOKENS:
            return
        tokens.add(folded)

    for kind, raw_value in raw_items:
        folded = _fold_identity_text(raw_value, normalize_fn)
        if not folded:
            continue

        if kind in ("git-email", "commit-email"):
            if "@" in folded:
                local_part, _, domain = folded.partition("@")
                if domain in ALLOWED_EMAIL_DOMAINS:
                    continue
                add_token(folded)
                if local_part and local_part not in GENERIC_EMAIL_LOCAL_PARTS:
                    add_token(local_part)
                noreply = GITHUB_NOREPLY_LOCAL_RE.fullmatch(local_part)
                if noreply:
                    handle = noreply.group(1)
                    if handle not in GENERIC_EMAIL_LOCAL_PARTS:
                        add_token(handle)
            else:
                add_token(folded)
            continue

        if kind in ("hostname", "scutil", "hostnamectl", "tailscale-dns", "tailscale-node", "tailscale-tailnet"):
            cleaned = folded.rstrip(".")
            if not cleaned:
                continue
            add_token(cleaned)
            if "." in cleaned:
                labels = [part for part in cleaned.split(".") if part]
                if labels:
                    add_token(labels[0])
                if cleaned.endswith(".ts.net") and len(labels) >= 3:
                    tailnet_suffix = ".".join(labels[-3:])
                    add_token(tailnet_suffix)
                    add_token(labels[-3])
            continue

        if kind in ("git-name", "commit-name", "account-gecos"):
            add_token(folded)
            words = folded.split(" ")
            # Collapse single-character middle initial ("zora k. vance" -> "zora vance").
            if len(words) == 3 and len(words[1].rstrip(".")) == 1:
                add_token(f"{words[0]} {words[2]}")
            continue

        add_token(folded)

    return frozenset(tokens)


def _parse_hostnamectl(stdout: str) -> list[str]:
    values: list[str] = []
    for line in stdout.splitlines():
        key, sep, val = line.partition(":")
        if not sep:
            continue
        if key.strip().casefold() not in HOSTNAMECTL_KEYS:
            continue
        cleaned = val.strip()
        if cleaned.casefold() in HOSTNAMECTL_EMPTY_VALUES:
            continue
        values.append(cleaned)
    return values


def _parse_tailscale_status_json(stdout: str) -> list[tuple[str, str]]:
    try:
        data = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise IdentityCollectionError(
            "identity source 'tailscale' returned malformed JSON; failing closed"
        ) from exc
    if not isinstance(data, dict):
        raise IdentityCollectionError(
            "identity source 'tailscale' returned unexpected JSON structure; failing closed"
        )
    collected: list[tuple[str, str]] = []
    self_node = data.get("Self")
    if isinstance(self_node, dict):
        host_name = self_node.get("HostName")
        if isinstance(host_name, str) and host_name.strip():
            collected.append(("tailscale-node", host_name.strip()))
        dns_name = self_node.get("DNSName")
        if isinstance(dns_name, str) and dns_name.strip():
            collected.append(("tailscale-dns", dns_name.strip()))
    current_tailnet = data.get("CurrentTailnet")
    if isinstance(current_tailnet, dict):
        tailnet_name = current_tailnet.get("Name")
        if isinstance(tailnet_name, str) and tailnet_name.strip():
            collected.append(("tailscale-tailnet", tailnet_name.strip()))
        tn_magic = current_tailnet.get("MagicDNSSuffix")
        if isinstance(tn_magic, str) and tn_magic.strip():
            collected.append(("tailscale-tailnet", tn_magic.strip()))
    top_magic = data.get("MagicDNSSuffix")
    if isinstance(top_magic, str) and top_magic.strip():
        collected.append(("tailscale-tailnet", top_magic.strip()))
    if not collected:
        raise IdentityCollectionError(
            "identity source 'tailscale' returned no node or tailnet name; failing closed"
        )
    return collected


def collect_local_identity(
    root: Path,
    *,
    normalize_fn: Callable[[str], str],
    platform_name: str | None = None,
    run_cmd: Callable[..., subprocess.CompletedProcess[str]] | None = None,
    get_pwuid: Callable[[int], object] | None = None,
    get_login: Callable[[], str] | None = None,
    get_hostname: Callable[[], str] | None = None,
    which: Callable[[str], str | None] | None = None,
    timeout: float = COMMAND_TIMEOUT_SECONDS,
) -> IdentityProfile:
    """Collect local machine identity at check time; fail closed on any source error."""
    plat = sys.platform if platform_name is None else platform_name
    runner = _default_run_cmd if run_cmd is None else run_cmd
    pwuid_fn = pwd.getpwuid if get_pwuid is None else get_pwuid
    login_fn = os.getlogin if get_login is None else get_login
    hostname_fn = socket.gethostname if get_hostname is None else get_hostname
    which_fn = shutil.which if which is None else which

    raw_items: list[tuple[str, str]] = []
    notes: list[str] = []

    # 1. Required: git user.name and user.email
    for git_key, kind in (("user.name", "git-name"), ("user.email", "git-email")):
        proc = _exec_source(
            ["git", "--no-optional-locks", "config", "--get", git_key],
            f"git {git_key}",
            cwd=root,
            run_cmd=runner,
            timeout=timeout,
        )
        if proc.returncode != 0 or not proc.stdout.strip():
            raise IdentityCollectionError(
                f"required identity source 'git {git_key}' is missing or empty; failing closed"
            )
        raw_items.append((kind, proc.stdout.strip()))

    # 2. Required: account username and full name (GECOS)
    try:
        pw = pwuid_fn(os.getuid())
        pw_name = getattr(pw, "pw_name", "")
        pw_gecos = getattr(pw, "pw_gecos", "")
    except Exception as exc:  # noqa: BLE001 - fail closed without leaking system details
        raise IdentityCollectionError(
            "required identity source 'account' could not be read; failing closed"
        ) from exc
    if not isinstance(pw_name, str) or not pw_name.strip():
        raise IdentityCollectionError(
            "required identity source 'account' returned an empty username; failing closed"
        )
    raw_items.append(("account-user", pw_name.strip()))
    if isinstance(pw_gecos, str):
        full_name = pw_gecos.split(",", 1)[0].strip()
        if full_name:
            raw_items.append(("account-gecos", full_name))
    try:
        login_name = login_fn()
    except OSError:
        login_name = ""
    if isinstance(login_name, str) and login_name.strip():
        raw_items.append(("account-user", login_name.strip()))

    # 3. Required: host name
    try:
        host_name = hostname_fn()
    except OSError as exc:
        raise IdentityCollectionError(
            "required identity source 'hostname' failed; failing closed"
        ) from exc
    if not isinstance(host_name, str) or not host_name.strip():
        raise IdentityCollectionError(
            "required identity source 'hostname' is empty; failing closed"
        )
    raw_items.append(("hostname", host_name.strip()))

    # 4. OS-specific host names
    if plat.startswith("darwin"):
        for scutil_key in ("ComputerName", "LocalHostName"):
            proc = _exec_source(
                ["scutil", "--get", scutil_key],
                f"scutil {scutil_key}",
                run_cmd=runner,
                timeout=timeout,
            )
            if proc.returncode != 0 or not proc.stdout.strip():
                raise IdentityCollectionError(
                    f"required macOS identity source 'scutil {scutil_key}' failed or returned empty; failing closed"
                )
            raw_items.append(("scutil", proc.stdout.strip()))

        # HostName is optional when explicitly unset on macOS ("HostName: not set").
        host_proc = _exec_source(
            ["scutil", "--get", "HostName"],
            "scutil HostName",
            run_cmd=runner,
            timeout=timeout,
        )
        if host_proc.returncode == 0:
            if host_proc.stdout.strip():
                raw_items.append(("scutil", host_proc.stdout.strip()))
        else:
            combined_msg = (host_proc.stderr + " " + host_proc.stdout).casefold()
            if "not set" not in combined_msg:
                raise IdentityCollectionError(
                    "macOS identity source 'scutil HostName' failed; failing closed"
                )
    elif plat.startswith("linux"):
        proc = _exec_source(
            ["hostnamectl"],
            "hostnamectl",
            run_cmd=runner,
            timeout=timeout,
        )
        if proc.returncode != 0:
            raise IdentityCollectionError(
                "required Linux identity source 'hostnamectl' failed; failing closed"
            )
        parsed_hosts = _parse_hostnamectl(proc.stdout)
        if not parsed_hosts:
            raise IdentityCollectionError(
                "required Linux identity source 'hostnamectl' returned no hostnames; failing closed"
            )
        for value in parsed_hosts:
            raw_items.append(("hostnamectl", value))

    # 5. Tailscale (optional when not installed; fail closed when installed and failing)
    tailscale_bin = which_fn("tailscale")
    if not tailscale_bin:
        notes.append("Identity source 'tailscale': absent (not installed)")
    else:
        ts_proc = _exec_source(
            [tailscale_bin, "status", "--json"],
            "tailscale",
            run_cmd=runner,
            timeout=timeout,
        )
        if ts_proc.returncode != 0 or not ts_proc.stdout.strip():
            raise IdentityCollectionError(
                "installed identity source 'tailscale' returned an error; failing closed"
            )
        raw_items.extend(_parse_tailscale_status_json(ts_proc.stdout))

    tokens = derive_identity_tokens(raw_items, normalize_fn=normalize_fn)
    return IdentityProfile(source_kind="local", tokens=tokens, notes=tuple(notes))


def collect_ci_commit_identity(
    root: Path,
    commits: Sequence[str],
    *,
    normalize_fn: Callable[[str], str],
    base: str | None = None,
    head: str | None = None,
    require_complete_history: bool = False,
    run_cmd: Callable[..., subprocess.CompletedProcess[str]] | None = None,
    timeout: float = COMMAND_TIMEOUT_SECONDS,
) -> IdentityProfile:
    """Extract author/committer identities of every commit under review in CI.

    Fails closed when history is incomplete (shallow clone missing base or
    grafted inside base..head) or when any commit author/committer field is
    missing or empty.
    """
    runner = _default_run_cmd if run_cmd is None else run_cmd
    if not commits:
        raise IdentityCollectionError("CI identity check received an empty commit list; failing closed")

    if base is not None and head is not None:
        for label, sha in (("base", base), ("head", head)):
            cat = _exec_source(
                ["git", "--no-optional-locks", "cat-file", "-e", f"{sha}^{{commit}}"],
                f"git cat-file {label}",
                cwd=root,
                run_cmd=runner,
                timeout=timeout,
            )
            if cat.returncode != 0:
                raise IdentityCollectionError(
                    f"CI identity check cannot verify {label} commit in repository history; failing closed"
                )
        mb = _exec_source(
            ["git", "--no-optional-locks", "merge-base", base, head],
            "git merge-base",
            cwd=root,
            run_cmd=runner,
            timeout=timeout,
        )
        if mb.returncode != 0 or not mb.stdout.strip():
            raise IdentityCollectionError(
                "CI identity check refused incomplete/disconnected history between base and head"
            )
        shallow_proc = _exec_source(
            ["git", "--no-optional-locks", "rev-parse", "--is-shallow-repository"],
            "git rev-parse --is-shallow-repository",
            cwd=root,
            run_cmd=runner,
            timeout=timeout,
        )
        if shallow_proc.returncode != 0:
            raise IdentityCollectionError(
                "CI identity check could not determine repository shallow state; failing closed"
            )
        if shallow_proc.stdout.strip() == "true":
            shallow_path_proc = _exec_source(
                ["git", "--no-optional-locks", "rev-parse", "--git-path", "shallow"],
                "git rev-parse --git-path shallow",
                cwd=root,
                run_cmd=runner,
                timeout=timeout,
            )
            if shallow_path_proc.returncode != 0 or not shallow_path_proc.stdout.strip():
                raise IdentityCollectionError(
                    "CI identity check refused shallow repository without readable shallow boundary"
                )
            shallow_file = Path(shallow_path_proc.stdout.strip())
            if not shallow_file.is_absolute():
                shallow_file = root / shallow_file
            try:
                grafts = {
                    line.strip()
                    for line in shallow_file.read_text(encoding="utf-8").splitlines()
                    if line.strip()
                }
            except OSError as exc:
                raise IdentityCollectionError(
                    "CI identity check could not read shallow boundary file; failing closed"
                ) from exc
            if any(commit in grafts for commit in commits):
                raise IdentityCollectionError(
                    "CI identity check refused shallow/truncated commit history in range"
                )
    elif require_complete_history:
        shallow_proc = _exec_source(
            ["git", "--no-optional-locks", "rev-parse", "--is-shallow-repository"],
            "git rev-parse --is-shallow-repository",
            cwd=root,
            run_cmd=runner,
            timeout=timeout,
        )
        if shallow_proc.returncode != 0 or shallow_proc.stdout.strip() == "true":
            raise IdentityCollectionError(
                "CI identity check refused shallow repository history; failing closed"
            )

    raw_items: list[tuple[str, str]] = []
    for commit in commits:
        proc = _exec_source(
            ["git", "--no-optional-locks", "show", "-s", "--format=%an%x00%ae%x00%cn%x00%ce", commit],
            "git commit identity",
            cwd=root,
            run_cmd=runner,
            timeout=timeout,
        )
        if proc.returncode != 0:
            raise IdentityCollectionError(
                f"CI identity check could not read commit {commit[:12]} metadata; failing closed"
            )
        parts = proc.stdout.rstrip("\r\n").split("\x00")
        if len(parts) != 4:
            raise IdentityCollectionError(
                f"CI identity check received malformed identity header for commit {commit[:12]}; failing closed"
            )
        author_name, author_email, committer_name, committer_email = (part.strip() for part in parts)
        if not (author_name and author_email and committer_name and committer_email):
            raise IdentityCollectionError(
                f"commit {commit[:12]} has missing or empty git author/committer identity; failing closed"
            )
        raw_items.append(("commit-name", author_name))
        raw_items.append(("commit-email", author_email))
        if (committer_name.casefold(), committer_email.casefold()) not in PLATFORM_SERVICE_COMMITTERS:
            raw_items.append(("commit-name", committer_name))
            raw_items.append(("commit-email", committer_email))

    tokens = derive_identity_tokens(raw_items, normalize_fn=normalize_fn)
    return IdentityProfile(source_kind="ci", tokens=tokens, notes=())


class _AhoCorasick:
    """Linear-time multi-pattern matcher with alphanumeric boundary checks."""

    def __init__(self, pattern_to_canonical: dict[str, set[str]]) -> None:
        self._goto: list[dict[str, int]] = [{}]
        self._fail: list[int] = [0]
        # Each output entry is (pattern_len, tuple_of_canonical_tokens)
        self._out: list[list[tuple[int, tuple[str, ...]]]] = [[]]

        for pattern, canonicals in pattern_to_canonical.items():
            if not pattern:
                continue
            state = 0
            for ch in pattern:
                nxt = self._goto[state].get(ch)
                if nxt is None:
                    nxt = len(self._goto)
                    self._goto[state][ch] = nxt
                    self._goto.append({})
                    self._fail.append(0)
                    self._out.append([])
                state = nxt
            self._out[state].append((len(pattern), tuple(sorted(canonicals))))

        queue: deque[int] = deque()
        for nxt in self._goto[0].values():
            self._fail[nxt] = 0
            queue.append(nxt)
        while queue:
            state = queue.popleft()
            for ch, nxt in self._goto[state].items():
                queue.append(nxt)
                fail_state = self._fail[state]
                while fail_state and ch not in self._goto[fail_state]:
                    fail_state = self._fail[fail_state]
                self._fail[nxt] = self._goto[fail_state].get(ch, 0)
                if self._out[self._fail[nxt]]:
                    self._out[nxt].extend(self._out[self._fail[nxt]])

    def find_bounded(self, text: str) -> list[str]:
        """Return canonical tokens whose patterns match on non-alnum boundaries."""
        if not text or not self._goto[0]:
            return []
        hits: list[str] = []
        seen: set[str] = set()
        state = 0
        n = len(text)
        goto = self._goto
        fail = self._fail
        out = self._out
        for idx, ch in enumerate(text):
            while state and ch not in goto[state]:
                state = fail[state]
            state = goto[state].get(ch, 0)
            outputs = out[state]
            if not outputs:
                continue
            end = idx + 1
            if end < n and text[end].isalnum():
                continue
            for pat_len, canonicals in outputs:
                start = end - pat_len
                if start > 0 and text[start - 1].isalnum():
                    continue
                for canonical in canonicals:
                    if canonical not in seen:
                        seen.add(canonical)
                        hits.append(canonical)
        return hits


def _mask_public_identifier_contexts(text: str, *, path: str = "") -> str:
    """Mask only exact documented public project addresses and CODEOWNERS/approver handles."""
    masked = text
    if "github.com/" in masked.casefold():
        masked = PUBLIC_PROJECT_ADDR_RE.sub(" ", masked)
    if path == ".github/CODEOWNERS" and "@" in masked:
        lines = masked.split("\n")
        cleaned_lines: list[str] = []
        for line in lines:
            stripped = line.lstrip()
            if not stripped or stripped.startswith("#"):
                cleaned_lines.append(line)
            else:
                code_part, sep, comment_part = line.partition("#")
                cleaned_lines.append(CODEOWNERS_OWNER_RE.sub(" ", code_part) + sep + comment_part)
        masked = "\n".join(cleaned_lines)
    elif path == "tests/public_scan_exemptions.json" and "approved_by" in masked:
        masked = EXEMPTION_APPROVED_BY_RE.sub(r'\1""', masked)
    return masked


class IdentityMatcher:
    """Linear-time matcher for folded contributor or CI commit identity tokens."""

    def __init__(self, tokens: Iterable[str]) -> None:
        self.tokens: frozenset[str] = frozenset(t for t in tokens if t and len(t) >= MIN_IDENTITY_TOKEN_LEN)
        direct_map: dict[str, set[str]] = {}
        sep_map: dict[str, set[str]] = {}
        zero_map: dict[str, set[str]] = {}

        for token in self.tokens:
            direct_map.setdefault(token, set()).add(token)
            sep_norm = SEP_COLLAPSE_RE.sub(" ", token).strip()
            if sep_norm and sep_norm != token and len(sep_norm) >= MIN_IDENTITY_TOKEN_LEN:
                sep_map.setdefault(sep_norm, set()).add(token)
            if any(ch.isdigit() for ch in token):
                unpadded = ZERO_PAD_RE.sub(r"\1", token)
                if unpadded and len(unpadded) >= MIN_IDENTITY_TOKEN_LEN:
                    zero_map.setdefault(unpadded, set()).add(token)
                unpadded_sep = ZERO_PAD_RE.sub(r"\1", sep_norm)
                if unpadded_sep and len(unpadded_sep) >= MIN_IDENTITY_TOKEN_LEN:
                    zero_map.setdefault(unpadded_sep, set()).add(token)

        self._direct = _AhoCorasick(direct_map)
        self._sep = _AhoCorasick(sep_map) if sep_map else None
        self._zero = _AhoCorasick(zero_map) if zero_map else None

    def detect(self, text: str, *, path: str = "") -> list[str]:
        if not self.tokens or not text:
            return []
        masked = _mask_public_identifier_contexts(text, path=path)
        lower = masked.casefold()
        seen: set[str] = set()
        hits: list[str] = []

        def record(found: list[str]) -> None:
            for item in found:
                if item not in seen:
                    seen.add(item)
                    hits.append(item)

        record(self._direct.find_bounded(lower))

        # CamelCase split catches glued identifiers like myWorkbox77 without breaking boundary safety.
        if masked != lower:
            camel_split = CAMEL_BOUNDARY_RE.sub(" ", masked).casefold()
            if camel_split != lower:
                record(self._direct.find_bounded(camel_split))

        # Whitespace-collapsed view catches extra space/tab padding ("Zora   Vance").
        ws_collapsed = WHITESPACE_RE.sub(" ", lower)
        if ws_collapsed != lower:
            record(self._direct.find_bounded(ws_collapsed))

        # Separator-collapsed view catches underscore/hyphen/dot swaps ("nordic_workbox_07").
        if self._sep is not None:
            sep_view = SEP_COLLAPSE_RE.sub(" ", lower)
            record(self._sep.find_bounded(sep_view))

        # Digit-unpadded view catches zero-padded numbers ("nordic-workbox-007").
        if self._zero is not None and any(ch.isdigit() for ch in lower):
            zero_view = ZERO_PAD_RE.sub(r"\1", lower)
            record(self._zero.find_bounded(zero_view))
            zero_sep = ZERO_PAD_RE.sub(r"\1", SEP_COLLAPSE_RE.sub(" ", lower))
            record(self._zero.find_bounded(zero_sep))

        return hits
