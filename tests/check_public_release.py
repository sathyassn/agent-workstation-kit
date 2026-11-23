#!/usr/bin/env python3
"""Check deterministic repository prerequisites and keyed public-safety rules."""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import stat
import subprocess
import sys
import unicodedata
from collections.abc import Iterable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SEMVER = re.compile(
    r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$"
)
REQUIRED = (
    "README.md",
    "VERSION",
    "CHANGELOG.md",
    "CONTRIBUTING.md",
    "CODE_OF_CONDUCT.md",
    "SECURITY.md",
    "SUPPORT.md",
    ".github/workflows/ci.yml",
    ".github/dependabot.yml",
    ".github/pull_request_template.md",
    ".github/ISSUE_TEMPLATE/bug_report.yml",
    ".github/ISSUE_TEMPLATE/feature_request.yml",
    ".github/ISSUE_TEMPLATE/config.yml",
    "docs/13-public-release-checklist.md",
    "docs/README.md",
    "docs/runbooks/day-zero-linux.md",
    "docs/runbooks/day-zero-macos.md",
    "docs/runbooks/stage-approved-macos-snapshots.md",
    "docs/runbooks/first-linux-pilot.md",
    "docs/hardware/minisforum-ms-s1-max.md",
    "templates/private-fleet/README.md",
    "skills/setup-agent-workstation/SKILL.md",
    "scripts/start-linux-pilot.py",
    "scripts/start-macos-pilot.py",
)
CONDUCT_CONTACT_PLACEHOLDER = "owner must add a monitored private conduct-report"
DEPENDABOT_COMMIT_POLICY = (
    "    commit-message:\n",
    "      prefix: build\n",
    "      prefix-development: build\n",
    "      include: scope\n",
)

DEFAULT_RULES_PATH = Path("tests/public_scan_rules.json")
DEFAULT_LOCAL_KEY_PATH = Path(".secrets/public-scan-hmac")
SYNTHETIC_KEY_PATH = Path("tests/fixtures/public_scan/synthetic_key.txt")
SYNTHETIC_RULES_PATH = Path("tests/fixtures/public_scan/synthetic_rules.json")
SYNTHETIC_CLEAN_FIXTURE_PATH = Path("tests/fixtures/public_scan/clean_fixture.txt")

KEY_ENV_VAR = "PUBLIC_SCAN_HMAC_KEY"
KEY_FILE_ENV_VAR = "PUBLIC_SCAN_HMAC_KEY_FILE"
SYNTHETIC_ENV_VAR = "PUBLIC_SCAN_SYNTHETIC"
GITHUB_ACTIONS_ENV_VAR = "GITHUB_ACTIONS"

MIN_KEY_BYTES = 16
MAX_KEY_FILE_BYTES = 4096
RULES_SCHEMA_VERSION = 1
RULES_ALGORITHM = "hmac-sha256"
HMAC_DOMAIN_PREFIX = "wskit-public-scan:v1"
ALLOWED_RULE_IDS = frozenset({"personal-name", "private-network-name"})
DIGEST_RE = re.compile(r"^[0-9a-f]{64}$")

COMPOUND_TOKEN_RE = re.compile(r"[^\W_](?:[\w.-]*[^\W_])?", re.UNICODE)
WORD_RUN_RE = re.compile(r"[^\W_]+", re.UNICODE)
CAMEL_SPLIT_RE = re.compile(
    r"[A-Z]+(?=[A-Z][a-z]|\b)|[A-Z]?[a-z]+|[0-9]+|[^\W_A-Za-z0-9]+",
    re.UNICODE,
)
WHITESPACE_RE = re.compile(r"\s+")
MAX_DOT_SLICE_LABELS = 6
LONG_DOT_CHAIN_THRESHOLD = 16
LONG_DOT_CHAIN_WINDOW = 4


class ScanConfigError(ValueError):
    """Raised when keyed scan configuration, key material, or rules are invalid."""


@dataclass(frozen=True)
class KeyedRule:
    rule_id: str
    digests: frozenset[str]


@dataclass(frozen=True)
class RuleSet:
    schema_version: int
    algorithm: str
    rules: tuple[KeyedRule, ...]

    @property
    def digest_count(self) -> int:
        return sum(len(rule.digests) for rule in self.rules)


@dataclass(frozen=True)
class KeyMaterial:
    key: bytes
    synthetic: bool
    source: str
    local_fallback: bool = False


@dataclass(frozen=True)
class ScanFinding:
    path: str
    line: int
    rule_id: str

    def format(self) -> str:
        return f"{self.path}:{self.line}: matched keyed rule '{self.rule_id}'"


def normalize_value(value: str) -> str:
    """Normalize a candidate token for deterministic keyed HMAC matching."""
    normalized = unicodedata.normalize("NFKC", value).strip().casefold()
    return WHITESPACE_RE.sub(" ", normalized)


def compute_digest(key: bytes, rule_id: str, value: str) -> str:
    """Compute a domain-separated HMAC-SHA256 digest for one normalized value."""
    if len(key) < MIN_KEY_BYTES:
        raise ScanConfigError(
            f"HMAC scan key must be at least {MIN_KEY_BYTES} bytes"
        )
    if rule_id not in ALLOWED_RULE_IDS:
        raise ScanConfigError(f"unknown keyed scan rule id: {rule_id}")
    normalized = normalize_value(value)
    if not normalized:
        raise ScanConfigError("value to hash must not be empty")
    payload = f"{HMAC_DOMAIN_PREFIX}:{rule_id}:{normalized}".encode("utf-8")
    return hmac.new(key, payload, hashlib.sha256).hexdigest()


def _strict_json_object_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    obj: dict[str, object] = {}
    for key, value in pairs:
        if key in obj:
            raise ScanConfigError(f"duplicate JSON key in rule file: {key}")
        obj[key] = value
    return obj


def load_rules(path: Path, *, require_nonempty_digests: bool = False) -> RuleSet:
    """Load and strictly validate a digest-only keyed-scan rule file."""
    if path.is_symlink():
        raise ScanConfigError(f"rule file must not be a symlink: {path.name}")
    try:
        st = os.lstat(path)
    except OSError as exc:
        raise ScanConfigError(f"rule file is missing or unreadable: {path.name}") from exc
    if not stat.S_ISREG(st.st_mode):
        raise ScanConfigError(f"rule file is not a regular file: {path.name}")
    try:
        raw_text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise ScanConfigError(f"rule file could not be read as UTF-8: {path.name}") from exc
    try:
        data = json.loads(raw_text, object_pairs_hook=_strict_json_object_pairs)
    except json.JSONDecodeError as exc:
        raise ScanConfigError(f"rule file is not valid JSON: {path.name}") from exc

    if not isinstance(data, dict):
        raise ScanConfigError("rule file root must be a JSON object")
    expected_top_keys = {"schema_version", "algorithm", "rules"}
    if set(data.keys()) != expected_top_keys:
        raise ScanConfigError(
            "rule file must contain only schema_version, algorithm, and rules"
        )

    schema_version = data["schema_version"]
    if type(schema_version) is not int or schema_version != RULES_SCHEMA_VERSION:
        raise ScanConfigError(
            f"rule file schema_version must be integer {RULES_SCHEMA_VERSION}"
        )

    algorithm = data["algorithm"]
    if algorithm != RULES_ALGORITHM:
        raise ScanConfigError(f"rule file algorithm must be '{RULES_ALGORITHM}'")

    raw_rules = data["rules"]
    if not isinstance(raw_rules, list) or not raw_rules:
        raise ScanConfigError("rule file rules must be a non-empty list")

    parsed_rules: list[KeyedRule] = []
    seen_ids: set[str] = set()
    for entry in raw_rules:
        if not isinstance(entry, dict) or set(entry.keys()) != {"id", "digests"}:
            raise ScanConfigError("each rule entry must contain only id and digests")
        rule_id = entry["id"]
        if not isinstance(rule_id, str) or rule_id not in ALLOWED_RULE_IDS:
            raise ScanConfigError("rule file contains an unsupported rule id")
        if rule_id in seen_ids:
            raise ScanConfigError(f"duplicate rule id in rule file: {rule_id}")
        seen_ids.add(rule_id)

        raw_digests = entry["digests"]
        if not isinstance(raw_digests, list):
            raise ScanConfigError(f"digests for rule '{rule_id}' must be a list")
        if require_nonempty_digests and not raw_digests:
            raise ScanConfigError(
                f"synthetic rule '{rule_id}' must contain at least one HMAC digest"
            )
        digests: set[str] = set()
        for item in raw_digests:
            if not isinstance(item, str) or not DIGEST_RE.fullmatch(item):
                raise ScanConfigError(
                    f"rule '{rule_id}' contains an entry that is not a lowercase 64-hex HMAC-SHA256 digest"
                )
            if item in digests:
                raise ScanConfigError(
                    f"rule '{rule_id}' contains a duplicate HMAC digest"
                )
            digests.add(item)
        parsed_rules.append(KeyedRule(rule_id=rule_id, digests=frozenset(digests)))

    if seen_ids != ALLOWED_RULE_IDS:
        missing = sorted(ALLOWED_RULE_IDS - seen_ids)
        raise ScanConfigError(
            "rule file is missing required rule(s): " + ", ".join(missing)
        )

    return RuleSet(
        schema_version=schema_version,
        algorithm=algorithm,
        rules=tuple(parsed_rules),
    )


def tracked_files(root: Path) -> tuple[list[str] | None, str | None]:
    """Return tracked paths, or a concise error when listing fails or is empty."""
    try:
        result = subprocess.run(
            ["git", "ls-files", "-z"],
            cwd=root,
            check=True,
            capture_output=True,
            text=True,
        )
    except FileNotFoundError:
        return None, "Git is unavailable"
    except subprocess.CalledProcessError:
        return None, "the directory is not a readable Git worktree"
    except OSError as exc:
        return None, f"Git could not be executed: {exc.strerror or type(exc).__name__}"
    paths = [item for item in result.stdout.split("\0") if item]
    if not paths:
        return None, "tracked file listing is empty"
    return paths, None


def _parse_key_text(raw_text: str) -> bytes:
    cleaned = raw_text.strip()
    if not cleaned:
        raise ScanConfigError("HMAC scan key is missing or empty")
    key_bytes = cleaned.encode("utf-8")
    if len(key_bytes) < MIN_KEY_BYTES:
        raise ScanConfigError(
            f"HMAC scan key must be at least {MIN_KEY_BYTES} bytes"
        )
    return key_bytes


def load_synthetic_key(root: Path) -> bytes:
    """Load the public non-secret synthetic key fixture for fork checks."""
    key_path = root / SYNTHETIC_KEY_PATH
    if key_path.is_symlink():
        raise ScanConfigError("public synthetic key fixture must not be a symlink")
    try:
        st = os.lstat(key_path)
    except OSError as exc:
        raise ScanConfigError("public synthetic key fixture is missing") from exc
    if not stat.S_ISREG(st.st_mode):
        raise ScanConfigError("public synthetic key fixture is not a regular file")
    try:
        raw_text = key_path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise ScanConfigError("public synthetic key fixture could not be read") from exc
    return _parse_key_text(raw_text)


def load_key_from_file(
    key_path: Path,
    *,
    root: Path,
    tracked: Sequence[str] | None,
) -> bytes:
    """Load a private HMAC key from an untracked, owner-only regular file."""
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(key_path, flags)
    except OSError as exc:
        if exc.errno in (getattr(os, "ELOOP", -1), 62, 40) or key_path.is_symlink():
            raise ScanConfigError("HMAC scan key file must not be a symlink") from exc
        raise ScanConfigError("HMAC scan key file is missing or unreadable") from exc

    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode):
            raise ScanConfigError("HMAC scan key file must be a regular file")
        if st.st_size > MAX_KEY_FILE_BYTES:
            raise ScanConfigError(
                f"HMAC scan key file exceeds maximum size ({MAX_KEY_FILE_BYTES} bytes)"
            )
        if os.name == "posix" and (st.st_mode & 0o077) != 0:
            raise ScanConfigError(
                "HMAC scan key file permissions are too broad; restrict to owner only (chmod 0600)"
            )
        with os.fdopen(fd, "rb") as handle:
            fd = -1
            raw_bytes = handle.read(MAX_KEY_FILE_BYTES + 1)
    finally:
        if fd != -1:
            os.close(fd)

    if len(raw_bytes) > MAX_KEY_FILE_BYTES:
        raise ScanConfigError(
            f"HMAC scan key file exceeds maximum size ({MAX_KEY_FILE_BYTES} bytes)"
        )

    resolved_root = root.resolve()
    resolved_key = key_path.resolve()
    if tracked:
        tracked_set = set(tracked)
        if resolved_key.is_relative_to(resolved_root):
            rel = resolved_key.relative_to(resolved_root).as_posix()
            if rel in tracked_set:
                raise ScanConfigError("HMAC scan key file must not be tracked by Git")
        if key_path.is_relative_to(root):
            rel_raw = key_path.relative_to(root).as_posix()
            if rel_raw in tracked_set:
                raise ScanConfigError("HMAC scan key file must not be tracked by Git")

    try:
        raw_text = raw_bytes.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ScanConfigError("HMAC scan key file could not be read as UTF-8") from exc
    return _parse_key_text(raw_text)


def is_github_actions(environ: Mapping[str, str]) -> bool:
    """Return True when GITHUB_ACTIONS indicates a GitHub Actions runner."""
    value = environ.get(GITHUB_ACTIONS_ENV_VAR, "").strip().lower()
    return value not in ("", "0", "false")


def resolve_key_material(
    root: Path,
    *,
    tracked: Sequence[str] | None,
    key_file: Path | None = None,
    synthetic_flag: bool = False,
    allow_local_fallback: bool = False,
    environ: Mapping[str, str] | None = None,
) -> KeyMaterial:
    """Resolve private or synthetic HMAC key material, failing closed in CI or on misconfiguration."""
    env = os.environ if environ is None else environ

    synthetic_env = env.get(SYNTHETIC_ENV_VAR, "")
    if synthetic_env not in ("", "1"):
        raise ScanConfigError(
            f"invalid {SYNTHETIC_ENV_VAR} value; expected '1' or unset"
        )
    synthetic = synthetic_flag or (synthetic_env == "1")

    has_cli_key_file = key_file is not None
    has_env_key_file = KEY_FILE_ENV_VAR in env
    has_env_key = KEY_ENV_VAR in env
    explicit_private_sources = int(has_cli_key_file) + int(has_env_key_file) + int(has_env_key)

    if synthetic and explicit_private_sources > 0:
        raise ScanConfigError(
            "cannot combine synthetic scan mode with a private HMAC key source"
        )

    if synthetic:
        return KeyMaterial(
            key=load_synthetic_key(root),
            synthetic=True,
            source=str(SYNTHETIC_KEY_PATH),
        )

    if explicit_private_sources > 1:
        raise ScanConfigError("specify only one HMAC key source")

    if has_cli_key_file:
        assert key_file is not None
        key_bytes = load_key_from_file(key_file, root=root, tracked=tracked)
        source = str(key_file)
    elif has_env_key_file:
        env_path_raw = env[KEY_FILE_ENV_VAR].strip()
        if not env_path_raw:
            raise ScanConfigError(f"{KEY_FILE_ENV_VAR} is set but empty")
        env_path = Path(env_path_raw)
        key_bytes = load_key_from_file(env_path, root=root, tracked=tracked)
        source = KEY_FILE_ENV_VAR
    elif has_env_key:
        key_bytes = _parse_key_text(env[KEY_ENV_VAR])
        source = KEY_ENV_VAR
    else:
        default_path = root / DEFAULT_LOCAL_KEY_PATH
        if default_path.exists() or default_path.is_symlink():
            key_bytes = load_key_from_file(default_path, root=root, tracked=tracked)
            source = str(DEFAULT_LOCAL_KEY_PATH)
        elif allow_local_fallback and not is_github_actions(env):
            return KeyMaterial(
                key=load_synthetic_key(root),
                synthetic=True,
                source=str(SYNTHETIC_KEY_PATH),
                local_fallback=True,
            )
        else:
            raise ScanConfigError(
                "HMAC scan key is missing; provide PUBLIC_SCAN_HMAC_KEY, create untracked "
                f"{DEFAULT_LOCAL_KEY_PATH} (mode 0600), or pass --synthetic for fork checks"
            )

    synthetic_path = root / SYNTHETIC_KEY_PATH
    if synthetic_path.is_file() and not synthetic_path.is_symlink():
        try:
            public_synthetic = load_synthetic_key(root)
        except ScanConfigError:
            public_synthetic = b""
        if public_synthetic and hmac.compare_digest(key_bytes, public_synthetic):
            raise ScanConfigError(
                "refusing to use the public synthetic key as a private HMAC key"
            )

    return KeyMaterial(key=key_bytes, synthetic=False, source=source)


def extract_candidates(line: str) -> set[str]:
    """Extract normalized tokens, domain slices, camelCase parts, and word n-grams."""
    candidates: set[str] = set()
    words: list[str] = []

    for match in COMPOUND_TOKEN_RE.finditer(line):
        raw_token = match.group(0)
        norm_token = normalize_value(raw_token)
        if norm_token:
            candidates.add(norm_token)

        if "." in raw_token:
            dot_parts = [p for p in raw_token.split(".") if p]
            n_parts = len(dot_parts)
            max_window = (
                LONG_DOT_CHAIN_WINDOW
                if n_parts > LONG_DOT_CHAIN_THRESHOLD
                else min(n_parts, MAX_DOT_SLICE_LABELS)
            )
            for start in range(n_parts):
                for width in range(1, min(max_window, n_parts - start) + 1):
                    sliced = normalize_value(".".join(dot_parts[start : start + width]))
                    if sliced:
                        candidates.add(sliced)

        if "-" in raw_token:
            for dot_seg in raw_token.split("."):
                if "-" not in dot_seg:
                    continue
                dash_parts = [p for p in dot_seg.split("-") if p]
                n_dash = len(dash_parts)
                for start in range(n_dash):
                    for width in range(1, min(LONG_DOT_CHAIN_WINDOW, n_dash - start) + 1):
                        sliced = normalize_value("-".join(dash_parts[start : start + width]))
                        if sliced:
                            candidates.add(sliced)

        for run_match in WORD_RUN_RE.finditer(raw_token):
            run = run_match.group(0)
            norm_run = normalize_value(run)
            if norm_run:
                candidates.add(norm_run)
            camel_parts = CAMEL_SPLIT_RE.findall(run)
            if len(camel_parts) > 1:
                for part in camel_parts:
                    norm_part = normalize_value(part)
                    if norm_part:
                        candidates.add(norm_part)
                        words.append(norm_part)
            elif norm_run:
                words.append(norm_run)

    for idx in range(len(words)):
        if idx + 1 < len(words):
            candidates.add(f"{words[idx]} {words[idx + 1]}")
        if idx + 2 < len(words):
            candidates.add(f"{words[idx]} {words[idx + 1]} {words[idx + 2]}")

    return candidates


def _match_candidates(
    candidates: Iterable[str],
    *,
    active_rules: Sequence[KeyedRule],
    key: bytes,
    cache: dict[str, tuple[str, ...]],
) -> set[str]:
    matched_rules: set[str] = set()
    for candidate in candidates:
        cached = cache.get(candidate)
        if cached is None:
            hits = tuple(
                rule.rule_id
                for rule in active_rules
                if hmac.new(
                    key,
                    f"{HMAC_DOMAIN_PREFIX}:{rule.rule_id}:{candidate}".encode("utf-8"),
                    hashlib.sha256,
                ).hexdigest()
                in rule.digests
            )
            cache[candidate] = hits
            cached = hits
        if cached:
            matched_rules.update(cached)
    return matched_rules


def scan_text(
    relative_path: str,
    text: str,
    *,
    rules: RuleSet,
    key: bytes,
    cache: dict[str, tuple[str, ...]] | None = None,
) -> list[ScanFinding]:
    """Scan a relative path and text lines against keyed HMAC rules."""
    active_rules = tuple(rule for rule in rules.rules if rule.digests)
    if not active_rules:
        return []
    shared_cache: dict[str, tuple[str, ...]] = {} if cache is None else cache
    findings: list[ScanFinding] = []

    path_hits = _match_candidates(
        extract_candidates(relative_path),
        active_rules=active_rules,
        key=key,
        cache=shared_cache,
    )
    for rule_id in sorted(path_hits):
        findings.append(ScanFinding(path=relative_path, line=0, rule_id=rule_id))

    normalized_text = text.replace("\r\n", "\n").replace("\r", "\n")
    for line_no, line in enumerate(normalized_text.split("\n"), start=1):
        if not line:
            continue
        line_hits = _match_candidates(
            extract_candidates(line),
            active_rules=active_rules,
            key=key,
            cache=shared_cache,
        )
        for rule_id in sorted(line_hits):
            findings.append(
                ScanFinding(path=relative_path, line=line_no, rule_id=rule_id)
            )

    return findings


def scan_tracked_files(
    root: Path,
    tracked: Sequence[str] | None,
    *,
    rules: RuleSet,
    key: bytes,
) -> list[str]:
    """Scan every tracked file in root using keyed HMAC rules; fail closed on gaps."""
    if not tracked:
        return ["tracked file listing is missing or empty"]

    failures: list[str] = []
    root_resolved = root.resolve()
    cache: dict[str, tuple[str, ...]] = {}

    for relative in tracked:
        rel_path = Path(relative)
        if rel_path.is_absolute() or ".." in rel_path.parts:
            failures.append(f"invalid tracked path: {relative}")
            continue
        full_path = root / rel_path
        if full_path.is_symlink():
            try:
                link_target = os.readlink(full_path)
            except OSError:
                failures.append(f"tracked symlink is unreadable: {relative}")
                continue
            findings = scan_text(
                relative, link_target, rules=rules, key=key, cache=cache
            )
            failures.extend(item.format() for item in findings)
            continue

        if not full_path.exists():
            failures.append(f"tracked file is missing from worktree: {relative}")
            continue
        if not full_path.is_file():
            failures.append(f"tracked path is not a readable file: {relative}")
            continue
        if not full_path.resolve().is_relative_to(root_resolved):
            failures.append(f"tracked file resolves outside repository root: {relative}")
            continue
        try:
            raw_bytes = full_path.read_bytes()
        except OSError:
            failures.append(f"tracked file is unreadable: {relative}")
            continue
        text = raw_bytes.decode("utf-8", errors="replace")
        findings = scan_text(relative, text, rules=rules, key=key, cache=cache)
        failures.extend(item.format() for item in findings)

    return failures


def verify_synthetic_fixtures(root: Path, *, rules: RuleSet, key: bytes) -> list[str]:
    """Verify synthetic clean and positive fixtures against the public synthetic key."""
    failures: list[str] = []
    clean_path = root / SYNTHETIC_CLEAN_FIXTURE_PATH
    if clean_path.is_symlink() or not clean_path.is_file():
        return [f"synthetic clean fixture is missing: {SYNTHETIC_CLEAN_FIXTURE_PATH}"]
    try:
        clean_text = clean_path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return [f"synthetic clean fixture is unreadable: {SYNTHETIC_CLEAN_FIXTURE_PATH}"]

    clean_findings = scan_text(
        str(SYNTHETIC_CLEAN_FIXTURE_PATH), clean_text, rules=rules, key=key
    )
    if clean_findings:
        failures.append("synthetic clean fixture unexpectedly matched a keyed rule")

    # Assemble synthetic canaries from parts so they never appear verbatim in tracked files.
    canary_person = "canary" + "person9917"
    canary_full_name = "canary" + "person" + " " + "nine"
    canary_net = "canary" + "net9917" + ".internal"
    positive_text = "\n".join(
        [
            f"owner = {canary_person}",
            f"author = {canary_full_name}",
            f"host = ws-01.{canary_net}",
        ]
    )
    positive_findings = scan_text("synthetic-positive-fixture", positive_text, rules=rules, key=key)
    expected = {
        ScanFinding(path="synthetic-positive-fixture", line=1, rule_id="personal-name"),
        ScanFinding(path="synthetic-positive-fixture", line=2, rule_id="personal-name"),
        ScanFinding(path="synthetic-positive-fixture", line=3, rule_id="private-network-name"),
    }
    if set(positive_findings) != expected:
        failures.append("synthetic positive fixture did not produce expected keyed rule findings")
    else:
        formatted = "\n".join(item.format() for item in positive_findings).lower()
        for canary in (canary_person, canary_full_name, canary_net):
            if canary in formatted:
                failures.append("synthetic positive fixture finding leaked matched text")

    return failures


def deterministic_failures(
    root: Path,
    *,
    require_license: bool,
    tracked: Iterable[str] | None,
) -> list[str]:
    """Evaluate repository metadata without performing network operations."""
    failures: list[str] = []

    for name in REQUIRED:
        if not (root / name).is_file():
            failures.append(f"required public metadata is missing: {name}")

    version_path = root / "VERSION"
    version = version_path.read_text(encoding="utf-8").strip() if version_path.is_file() else ""
    if not SEMVER.fullmatch(version):
        failures.append("VERSION is not valid Semantic Versioning")
    changelog = root / "CHANGELOG.md"
    if version and changelog.is_file():
        text = changelog.read_text(encoding="utf-8")
        headings = [line for line in text.splitlines() if line.startswith("## ")]
        if not headings or headings[0] != "## [Unreleased]":
            failures.append(
                "CHANGELOG.md must start with a ## [Unreleased] section (Keep a Changelog)"
            )
        released = re.search(
            r"^## \[" + re.escape(version) + r"\] - \d{4}-\d{2}-\d{2}$", text, re.M
        )
        unreleased = re.search(r"^## \[Unreleased\]\n(.*?)(?=^## |\Z)", text, re.M | re.S)
        planned = bool(unreleased) and f"Planned as release {version}." in unreleased.group(1)
        if not (released or planned):
            failures.append(
                "CHANGELOG.md has neither a dated ## [VERSION] heading nor an [Unreleased] section planned as VERSION"
            )
    readme = root / "README.md"
    if version and readme.is_file() and version not in readme.read_text(encoding="utf-8"):
        failures.append("README.md does not mention VERSION")
    template_lock = root / "templates/private-fleet/kit.lock"
    if version and (
        not template_lock.is_file()
        or template_lock.read_text(encoding="utf-8").strip() != version
    ):
        failures.append("templates/private-fleet/kit.lock does not match VERSION")

    if not (root / "LICENSE").is_file() and require_license:
        failures.append("LICENSE is missing; owner/legal license selection is required")
    elif (root / "LICENSE").is_file() and "Apache License\nVersion 2.0" not in (
        root / "LICENSE"
    ).read_text(encoding="utf-8"):
        failures.append("LICENSE is not the selected Apache License 2.0 text")

    conduct = root / "CODE_OF_CONDUCT.md"
    if (
        require_license
        and conduct.is_file()
        and CONDUCT_CONTACT_PLACEHOLDER in conduct.read_text(encoding="utf-8").lower()
    ):
        failures.append(
            "CODE_OF_CONDUCT.md still has the private conduct-contact placeholder"
        )

    support = root / "SUPPORT.md"
    contributing = root / "CONTRIBUTING.md"
    if support.is_file() and contributing.is_file():
        support_text = support.read_text(encoding="utf-8").lower()
        if "external contributions are not accepted" in support_text:
            failures.append("SUPPORT.md contradicts the contribution policy")

    dependabot = root / ".github/dependabot.yml"
    if dependabot.is_file():
        dependabot_text = dependabot.read_text(encoding="utf-8")
        if any(
            fragment not in dependabot_text for fragment in DEPENDABOT_COMMIT_POLICY
        ):
            failures.append(
                "Dependabot must emit conventional build(scope) commit subjects"
            )

    tracked_list = list(tracked) if tracked is not None else None
    if not tracked_list:
        failures.append("tracked file listing is missing or empty")
    else:
        for name in tracked_list:
            if (
                name.endswith(".local.toml")
                or name.startswith("reports/")
                or name.startswith("artifacts/")
                or name.startswith(".secrets/")
            ):
                failures.append(f"private/generated path is tracked: {name}")
            if name.startswith("skills/setup-agent-dev-machine/") and (root / name).exists():
                failures.append(f"retired skill path is tracked: {name}")

    return failures


def _run_hash_mode(
    *,
    root: Path,
    rule_id: str,
    key_file: Path | None,
    synthetic: bool,
) -> int:
    if rule_id not in ALLOWED_RULE_IDS:
        print(f"Unsupported rule id: {rule_id}")
        return 1
    tracked, _ = tracked_files(root)
    try:
        key_material = resolve_key_material(
            root,
            tracked=tracked,
            key_file=key_file,
            synthetic_flag=synthetic,
        )
    except ScanConfigError as exc:
        print(f"Cannot compute HMAC digest: {exc}")
        return 1

    digests: list[str] = []
    for raw_line in sys.stdin:
        stripped = raw_line.strip()
        if not stripped:
            continue
        digests.append(compute_digest(key_material.key, rule_id, stripped))
    if not digests:
        print("Cannot compute HMAC digest: no non-empty values provided on stdin")
        return 1
    for digest in digests:
        print(digest)
    return 0


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--allow-missing-license",
        action="store_true",
        help="validate a private draft while retaining fail-closed file-listing and keyed scan gates",
    )
    parser.add_argument(
        "--synthetic",
        action="store_true",
        help="run with the public synthetic key and fixtures (for fork pull requests)",
    )
    parser.add_argument(
        "--key-file",
        type=Path,
        default=None,
        help="path to an untracked owner-only HMAC key file",
    )
    parser.add_argument(
        "--rules-file",
        type=Path,
        default=None,
        help="override the digest-only rule file path",
    )
    parser.add_argument(
        "--root",
        type=Path,
        default=ROOT,
        help="repository root to inspect (defaults to this checkout)",
    )
    parser.add_argument(
        "--hash-rule",
        default=None,
        help="compute keyed HMAC digests for one value per line from stdin for RULE_ID",
    )
    args = parser.parse_args(argv)
    root = args.root.resolve()

    if args.hash_rule is not None:
        return _run_hash_mode(
            root=root,
            rule_id=args.hash_rule,
            key_file=args.key_file,
            synthetic=args.synthetic,
        )

    tracked, git_error = tracked_files(root)
    failures = deterministic_failures(
        root,
        require_license=not args.allow_missing_license,
        tracked=tracked,
    )
    if git_error:
        failures = [
            item
            for item in failures
            if item != "tracked file listing is missing or empty"
        ]
        failures.append(f"tracked file listing failed: {git_error}")

    key_material: KeyMaterial | None = None
    try:
        key_material = resolve_key_material(
            root,
            tracked=tracked,
            key_file=args.key_file,
            synthetic_flag=args.synthetic,
            allow_local_fallback=True,
        )
    except ScanConfigError as exc:
        failures.append(str(exc))

    prod_rules_path = args.rules_file if args.rules_file is not None else (root / DEFAULT_RULES_PATH)
    prod_rules: RuleSet | None = None
    try:
        prod_rules = load_rules(prod_rules_path, require_nonempty_digests=False)
    except ScanConfigError as exc:
        failures.append(str(exc))

    active_rules: RuleSet | None = prod_rules
    if key_material is not None and key_material.synthetic:
        synthetic_rules_path = (
            (root / SYNTHETIC_RULES_PATH)
            if key_material.local_fallback or args.rules_file is None
            else args.rules_file
        )
        try:
            active_rules = load_rules(
                synthetic_rules_path, require_nonempty_digests=True
            )
        except ScanConfigError as exc:
            active_rules = None
            failures.append(str(exc))
        else:
            if key_material.local_fallback or args.rules_file is None:
                failures.extend(
                    verify_synthetic_fixtures(
                        root, rules=active_rules, key=key_material.key
                    )
                )

    if tracked and key_material is not None and active_rules is not None:
        failures.extend(
            scan_tracked_files(
                root,
                tracked,
                rules=active_rules,
                key=key_material.key,
            )
        )

    if failures:
        if key_material is not None and key_material.local_fallback:
            print("WARNING: keyed private-name rules not checked locally; CI runs them.")
        print("Public release checks failed:")
        for failure in failures:
            print(f"- {failure}")
        return 1

    qualifier = "draft " if args.allow_missing_license else ""
    if key_material is not None and key_material.local_fallback:
        print("WARNING: keyed private-name rules not checked locally; CI runs them.")
        print(
            f"Deterministic public-release {qualifier}checks passed (local synthetic scan); "
            "complete docs/13-public-release-checklist.md before publication."
        )
    elif key_material is not None and key_material.synthetic:
        print(
            f"Deterministic public-release {qualifier}checks passed (public synthetic scan); "
            "a maintainer must run the keyed scan on this revision before merge."
        )
    elif active_rules is not None and active_rules.digest_count == 0:
        print(
            "WARNING: no keyed rules populated; keyed scan checked 0 digests "
            "(see docs/13-public-release-checklist.md)."
        )
        print(
            f"Deterministic public-release {qualifier}checks passed (no keyed rules populated); "
            "complete docs/13-public-release-checklist.md before publication."
        )
    else:
        if active_rules is not None:
            empty_rules = [rule.rule_id for rule in active_rules.rules if not rule.digests]
            if empty_rules:
                print(
                    "WARNING: keyed rule(s) have no digests populated: "
                    + ", ".join(empty_rules)
                    + "."
                )
        print(
            f"Deterministic public-release {qualifier}checks passed; "
            "complete docs/13-public-release-checklist.md before publication."
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
