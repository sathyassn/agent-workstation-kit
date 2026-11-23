#!/usr/bin/env python3
"""Check repository prerequisites and the public-safety scan.

The public-safety scan has two parts:

- keyed rules: personal and private network names, matched by domain-separated
  HMAC-SHA256 digests, so the rule file never holds or reveals a name;
- a private IPv4 detector for the RFC 1918, CGNAT and link-local ranges, which
  needs no rule and no key.

It reads either the worktree or, with --tree, a commit's tree as git objects,
so CI can scan a pull request's head without checking out or running its code.
Diagnostics never print a matched value: a matching path component is shown as
an opaque keyed id, and errors never echo input text.
"""

from __future__ import annotations

import argparse
import codecs
import functools
import hashlib
import hmac
import ipaddress
import json
import os
import re
import stat
import subprocess
import sys
import unicodedata
from collections.abc import Iterable, Iterator, Mapping, Sequence
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
    ".github/workflows/public-scan.yml",
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
DEFAULT_EXCEPTIONS_PATH = Path("tests/public_scan_exceptions.json")
DEFAULT_LOCAL_KEY_PATH = Path(".secrets/public-scan-hmac")
SYNTHETIC_KEY_PATH = Path("tests/fixtures/public_scan/synthetic_key.txt")
SYNTHETIC_RULES_PATH = Path("tests/fixtures/public_scan/synthetic_rules.json")
SYNTHETIC_CLEAN_FIXTURE_PATH = Path("tests/fixtures/public_scan/clean_fixture.txt")

KEY_ENV_VAR = "PUBLIC_SCAN_HMAC_KEY"
KEY_FILE_ENV_VAR = "PUBLIC_SCAN_HMAC_KEY_FILE"
SYNTHETIC_ENV_VAR = "PUBLIC_SCAN_SYNTHETIC"
GITHUB_ACTIONS_ENV_VAR = "GITHUB_ACTIONS"

KEY_BYTES = 32
KEY_HEX_RE = re.compile(r"^[0-9a-f]{64}$")
MAX_KEY_FILE_BYTES = 4096
MAX_SCAN_FILE_BYTES = 20 * 1024 * 1024
MAX_TREE_ENTRIES = 100_000
MAX_TREE_BYTES = 512 * 1024 * 1024
MAX_LISTING_BYTES = 64 * 1024 * 1024
MAX_FINDINGS = 10_000
# Caches hold only lines up to CACHE_LINE_CHARS, at most CACHE_LINES of them,
# and the candidate cache is cleared above CANDIDATE_CACHE_BYTES of keys.
CACHE_LINE_CHARS = 2048
CACHE_LINES = 4096
CANDIDATE_CACHE_BYTES = 32 * 1024 * 1024
RULES_SCHEMA_VERSION = 1
RULES_ALGORITHM = "hmac-sha256"
HMAC_DOMAIN_PREFIX = "wskit-public-scan:v1"
PATH_ID_DOMAIN = "wskit-public-scan:path-id:v1"
ALLOWED_RULE_IDS = frozenset({"personal-name", "private-network-name"})
DIGEST_RE = re.compile(r"^[0-9a-f]{64}$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
COMMIT_RE = re.compile(r"^(?:[0-9a-f]{40}|[0-9a-f]{64})$")

COMPOUND_TOKEN_RE = re.compile(r"[^\W_](?:[\w.-]*[^\W_])?", re.UNICODE)
WORD_RUN_RE = re.compile(r"[^\W_]+", re.UNICODE)
CAMEL_SPLIT_RE = re.compile(
    r"[A-Z]+(?=[A-Z][a-z]|\b)|[A-Z]?[a-z]+|[0-9]+|[^\W_A-Za-z0-9]+",
    re.UNICODE,
)
WHITESPACE_RE = re.compile(r"\s+")
PUNCTUATION_RE = re.compile(r"[^\w\s]|_", re.UNICODE)
JSON_ESCAPE_RE = re.compile(r"\\u([0-9a-fA-F]{4})")
APOSTROPHES = "'‘’ʼʹ`´"
MAX_DOT_SLICE_LABELS = 6
LONG_DOT_CHAIN_THRESHOLD = 16
LONG_DOT_CHAIN_WINDOW = 4
WRAP_WINDOW_CHARS = 64
# No name or host is longer; longer candidates are skipped and refused as values.
MAX_CANDIDATE_CHARS = 128

# Dotted decimal runs of two to four groups. A digit or a digit-dot before the
# run, or a digit or dot-digit after it, means it is part of a longer number,
# so it is not an address. A letter glued to either end is a break.
DOTTED_DECIMAL_RE = re.compile(r"(?<!\d)(?<!\d\.)(\d{1,3}(?:\.\d{1,3}){1,3})(?!\d)(?!\.\d)")
# A prefix continues when followed by a trailing dot, a CIDR suffix or a
# wildcard octet, for example "10.20." or "10.20.0/16" or "10.20.x".
PREFIX_CONTINUATION_RE = re.compile(r"\.(?!\d)|/\d|\.[x*](?![\w])")

PRIVATE_RANGES = (
    ("10/8", ipaddress.ip_network("10.0.0.0/8")),
    ("172.16/12", ipaddress.ip_network("172.16.0.0/12")),
    ("192.168/16", ipaddress.ip_network("192.168.0.0/16")),
    ("100.64/10", ipaddress.ip_network("100.64.0.0/10")),
    ("169.254/16", ipaddress.ip_network("169.254.0.0/16")),
)
# Two-group prefixes that are rare outside addresses are flagged bare; the
# others need a continuation, because "10.5" and "100.75" are common numbers.
DISTINCT_TWO_GROUP_RANGES = frozenset({"172.16/12", "192.168/16", "169.254/16"})

# Latin lookalikes from other scripts, folded before tokenising. This covers
# the letters a name or host can be spelled with; NFKC already folds
# fullwidth and compatibility forms.
CONFUSABLES = str.maketrans(
    {
        # Cyrillic
        "а": "a", "А": "A", "в": "b", "В": "B",
        "с": "c", "С": "C", "ԁ": "d", "е": "e",
        "Е": "E", "һ": "h", "Н": "H", "і": "i",
        "І": "I", "ј": "j", "Ј": "J", "к": "k",
        "К": "K", "ӏ": "l", "м": "m", "М": "M",
        "о": "o", "О": "O", "р": "p", "Р": "P",
        "ԛ": "q", "ѕ": "s", "Ѕ": "S", "т": "t",
        "Т": "T", "х": "x", "Х": "X", "у": "y",
        "Ү": "Y", "ԝ": "w",
        # Greek
        "α": "a", "Α": "A", "Β": "B", "Ε": "E",
        "Ζ": "Z", "Η": "H", "ι": "i", "Ι": "I",
        "Κ": "K", "Μ": "M", "Ν": "N", "ο": "o",
        "Ο": "O", "ρ": "p", "Ρ": "P", "Τ": "T",
        "υ": "u", "Υ": "Y", "Χ": "X", "ν": "v",
        # Latin variants
        "ı": "i", "ȷ": "j", "ɡ": "g", "ɪ": "i",
    }
)


class ScanConfigError(ValueError):
    """Raised when scan configuration, key material, rules or input are invalid.

    Messages never contain input text.
    """


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

    @property
    def empty_rule_ids(self) -> tuple[str, ...]:
        return tuple(sorted(rule.rule_id for rule in self.rules if not rule.digests))


@dataclass(frozen=True)
class Exceptions:
    """Reviewed exceptions, read from the trusted checkout.

    binary maps a path to the sha256 of the exact content allowed to be
    non-text. private_ipv4 maps a path to the canonical addresses or prefixes
    allowed in it.
    """

    binary: Mapping[str, str]
    private_ipv4: Mapping[str, frozenset[str]]


EMPTY_EXCEPTIONS = Exceptions(binary={}, private_ipv4={})


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


@dataclass(frozen=True)
class SourceFile:
    """One tracked entry: its path, and its content or a reason it has none."""

    path: str
    content: bytes | None
    symlink: bool = False
    problem: str | None = None
    object_id: str | None = None


# --- normalisation and candidates -------------------------------------------


def _decode_json_escapes(text: str) -> str:
    def replace(match: re.Match[str]) -> str:
        return chr(int(match.group(1), 16))

    decoded = JSON_ESCAPE_RE.sub(replace, text)
    # Join escaped surrogate pairs into one character; drop lone surrogates.
    return decoded.encode("utf-16", "surrogatepass").decode("utf-16", "ignore")


_APOSTROPHE_TABLE = {ord(ch): None for ch in APOSTROPHES}


def normalize_text(text: str, separate: bool = False) -> str:
    """Canonicalise text before tokenising (cached for short lines)."""
    if len(text) <= CACHE_LINE_CHARS:
        return _normalize_text_cached(text, separate)
    return _normalize_text(text, separate)


@functools.lru_cache(maxsize=CACHE_LINES)
def _normalize_text_cached(text: str, separate: bool) -> str:
    return _normalize_text(text, separate)


def _normalize_text(text: str, separate: bool) -> str:
    """Canonicalise text before tokenising, keeping case for camelCase splits.

    JSON unicode escapes are decoded, then NFKC folds compatibility forms (a
    fullwidth dot becomes "."), format and zero-width characters and combining
    marks are removed (so accents fold: "José" matches "Jose"), Latin
    lookalikes are folded and apostrophes are dropped, so "O'Brien" and
    "OBrien" give the same token. With separate true, the text is composed
    first and each remaining format character or combining mark becomes a
    space instead, so one placed between a name and a suffix still separates
    them; the scanner tokenises both forms.
    """
    if text.isascii() and "\\u" not in text:
        # NFKC, format characters and lookalikes cannot change ASCII text.
        return text.translate(_APOSTROPHE_TABLE)
    decoded = _decode_json_escapes(text)
    if not decoded.isascii():
        if separate:
            # Compose, so ordinary accented letters stay whole, then turn each
            # remaining format character or mark into a word break.
            decoded = unicodedata.normalize("NFKC", decoded)
            decoded = "".join(" " if unicodedata.category(ch) in ("Cf", "Mn") else ch for ch in decoded)
            decoded = decoded.translate(CONFUSABLES)
        else:
            # Decompose, drop format characters and combining marks (so an
            # accent or a mark between octets cannot split a token), then
            # recompose.
            decoded = unicodedata.normalize("NFKD", decoded)
            decoded = "".join(ch for ch in decoded if unicodedata.category(ch) not in ("Cf", "Mn"))
            decoded = unicodedata.normalize("NFKC", decoded).translate(CONFUSABLES)
    return decoded.translate(_APOSTROPHE_TABLE)


def normalize_value(value: str) -> str:
    """Normalise one value for keyed HMAC matching; the owner hashes this form."""
    normalized = normalize_text(value).strip().casefold()
    return WHITESPACE_RE.sub(" ", normalized)


def canonical_octets(groups: str) -> tuple[int, ...] | None:
    """Return the decimal octets of a dotted run, or None if one exceeds 255."""
    octets = tuple(int(part, 10) for part in groups.split("."))
    if any(octet > 255 for octet in octets):
        return None
    return octets


def compute_digest(key: bytes, rule_id: str, value: str) -> str:
    """Compute a domain-separated HMAC-SHA256 digest for one normalised value."""
    if len(key) != KEY_BYTES:
        raise ScanConfigError(f"HMAC scan key must be {KEY_BYTES} bytes")
    if rule_id not in ALLOWED_RULE_IDS:
        raise ScanConfigError("unknown keyed scan rule id")
    normalized = normalize_value(value)
    if not normalized:
        raise ScanConfigError("value to hash must not be empty")
    if len(normalized) > MAX_CANDIDATE_CHARS:
        raise ScanConfigError(f"value to hash must be at most {MAX_CANDIDATE_CHARS} characters")
    payload = f"{HMAC_DOMAIN_PREFIX}:{rule_id}:{normalized}".encode("utf-8")
    return hmac.new(key, payload, hashlib.sha256).hexdigest()


def _fold(value: str) -> str:
    """Fold already-normalised text: casefold and collapse whitespace."""
    return WHITESPACE_RE.sub(" ", value.strip().casefold())


def _add(candidates: set[str], value: str) -> None:
    folded = _fold(value)
    if folded and len(folded) <= MAX_CANDIDATE_CHARS:
        candidates.add(folded)


@functools.lru_cache(maxsize=CACHE_LINES)
def _cached_candidates(line: str) -> frozenset[str]:
    return frozenset(_extract_candidates(line))


def extract_candidates(line: str) -> frozenset[str]:
    """Extract candidate values from one line of text (cached for short lines)."""
    if len(line) <= CACHE_LINE_CHARS:
        return _cached_candidates(line)
    return frozenset(_extract_candidates(line))


def _extract_candidates(line: str) -> set[str]:
    """Extract candidate values from one line of text.

    Candidates are tokens, dot and hyphen slices, camelCase parts, two- and
    three-word runs, canonical dotted-decimal runs and their prefixes, the
    whole line, and the whole line with punctuation removed.
    """
    candidates: set[str] = set()
    _candidates_from(normalize_text(line), candidates)
    if not line.isascii() or "\\u" in line:
        # Also tokenise with format characters and marks as word breaks
        # (decided on the decoded text, so an escaped one counts too):
        # deleting one that sits between two words would otherwise join them.
        separated = normalize_text(line, separate=True)
        if separated != normalize_text(line):
            _candidates_from(separated, candidates)
    return candidates


def _candidates_from(text: str, candidates: set[str]) -> None:
    words: list[str] = []

    _add(candidates, text)
    _add(candidates, PUNCTUATION_RE.sub(" ", text))
    _add(candidates, PUNCTUATION_RE.sub("", text))
    # Punctuation deleted inside each whitespace chunk, alone and in runs of
    # two and three, so "ab!cd" gives "abcd".
    chunks = [c for c in (PUNCTUATION_RE.sub("", chunk) for chunk in text.split()) if c]
    for idx in range(len(chunks)):
        _add(candidates, chunks[idx])
        if idx + 1 < len(chunks):
            _add(candidates, f"{chunks[idx]} {chunks[idx + 1]}")
        if idx + 2 < len(chunks):
            _add(candidates, f"{chunks[idx]} {chunks[idx + 1]} {chunks[idx + 2]}")

    for match in DOTTED_DECIMAL_RE.finditer(text):
        octets = canonical_octets(match.group(1))
        if octets is None:
            continue
        for width in range(1, len(octets) + 1):
            _add(candidates, ".".join(str(octet) for octet in octets[:width]))

    for match in COMPOUND_TOKEN_RE.finditer(text):
        raw_token = match.group(0)
        _add(candidates, raw_token)

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
                    _add(candidates, ".".join(dot_parts[start : start + width]))

        if "-" in raw_token:
            for dot_seg in raw_token.split("."):
                if "-" not in dot_seg:
                    continue
                dash_parts = [p for p in dot_seg.split("-") if p]
                n_dash = len(dash_parts)
                for start in range(n_dash):
                    for width in range(1, min(LONG_DOT_CHAIN_WINDOW, n_dash - start) + 1):
                        _add(candidates, "-".join(dash_parts[start : start + width]))

        for run_match in WORD_RUN_RE.finditer(raw_token):
            run = run_match.group(0)
            norm_run = _fold(run)
            if norm_run:
                _add(candidates, norm_run)
            camel_parts = CAMEL_SPLIT_RE.findall(run)
            if len(camel_parts) > 1:
                for part in camel_parts:
                    norm_part = _fold(part)
                    if norm_part:
                        _add(candidates, norm_part)
                        words.append(norm_part)
            elif norm_run:
                words.append(norm_run)

    for idx in range(len(words)):
        if idx + 1 < len(words):
            _add(candidates, f"{words[idx]} {words[idx + 1]}")
        if idx + 2 < len(words):
            _add(candidates, f"{words[idx]} {words[idx + 1]} {words[idx + 2]}")



def private_ipv4_hits(line: str) -> list[tuple[str, str]]:
    """Return (range label, canonical value) for each private address or prefix.

    A full address is flagged when it lies in a private range. A three-group
    prefix is flagged when it lies in one (a 10.x prefix needs a
    continuation). A two-group prefix is flagged bare for 172.16/12,
    192.168/16 and 169.254/16, and with a continuation for 10/8 and 100.64/10.
    """
    text = normalize_text(line)
    hits: list[tuple[str, str]] = []
    for match in DOTTED_DECIMAL_RE.finditer(text):
        octets = canonical_octets(match.group(1))
        if octets is None:
            continue
        continued = bool(PREFIX_CONTINUATION_RE.match(text, match.end()))
        padded = octets + (0,) * (4 - len(octets))
        address = ipaddress.ip_address(".".join(str(octet) for octet in padded))
        for label, network in PRIVATE_RANGES:
            if address not in network:
                continue
            # The prefix must pin the range: a lone "172.16" pins 172.16/12,
            # but a lone "100.64" only pins it with its second octet in range,
            # which padding already checked.
            if len(octets) == 4:
                flagged = True
            elif len(octets) == 3:
                flagged = continued or label != "10/8"
            else:
                flagged = continued or label in DISTINCT_TWO_GROUP_RANGES
            if flagged:
                hits.append((label, ".".join(str(octet) for octet in octets)))
            break
    return hits


# --- rules, exceptions and keys ---------------------------------------------


def _strict_json_object_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    obj: dict[str, object] = {}
    for key, value in pairs:
        if key in obj:
            raise ScanConfigError("duplicate JSON key in a scan configuration file")
        obj[key] = value
    return obj


def _read_config_json(path: Path, label: str) -> object:
    if path.is_symlink():
        raise ScanConfigError(f"{label} must not be a symlink")
    try:
        st = os.lstat(path)
    except OSError as exc:
        raise ScanConfigError(f"{label} is missing or unreadable") from exc
    if not stat.S_ISREG(st.st_mode):
        raise ScanConfigError(f"{label} is not a regular file")
    try:
        raw_text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise ScanConfigError(f"{label} could not be read as UTF-8") from exc
    try:
        return json.loads(raw_text, object_pairs_hook=_strict_json_object_pairs)
    except json.JSONDecodeError as exc:
        raise ScanConfigError(f"{label} is not valid JSON") from exc


def load_rules(path: Path, *, require_nonempty_digests: bool = False) -> RuleSet:
    """Load and strictly validate a digest-only keyed-scan rule file."""
    data = _read_config_json(path, "rule file")
    if not isinstance(data, dict):
        raise ScanConfigError("rule file root must be a JSON object")
    if set(data.keys()) != {"schema_version", "algorithm", "rules"}:
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
        digests: set[str] = set()
        for item in raw_digests:
            if not isinstance(item, str) or not DIGEST_RE.fullmatch(item):
                raise ScanConfigError(
                    f"rule '{rule_id}' contains an entry that is not a lowercase 64-hex HMAC-SHA256 digest"
                )
            if item in digests:
                raise ScanConfigError(f"rule '{rule_id}' contains a duplicate HMAC digest")
            digests.add(item)
        parsed_rules.append(KeyedRule(rule_id=rule_id, digests=frozenset(digests)))

    if seen_ids != ALLOWED_RULE_IDS:
        missing = sorted(ALLOWED_RULE_IDS - seen_ids)
        raise ScanConfigError("rule file is missing required rule(s): " + ", ".join(missing))

    ruleset = RuleSet(schema_version=schema_version, algorithm=algorithm, rules=tuple(parsed_rules))
    if require_nonempty_digests and ruleset.empty_rule_ids:
        raise ScanConfigError(
            "no keyed rules populated for: "
            + ", ".join(ruleset.empty_rule_ids)
            + "; every keyed rule needs at least one digest (see docs/13-public-release-checklist.md)"
        )
    return ruleset


def _valid_relative_path(value: object) -> bool:
    if not isinstance(value, str) or not value or "\0" in value:
        return False
    path = Path(value)
    return not path.is_absolute() and ".." not in path.parts


def load_exceptions(path: Path) -> Exceptions:
    """Load the reviewed exception list; a missing file means no exceptions."""
    if not path.exists() and not path.is_symlink():
        return EMPTY_EXCEPTIONS
    data = _read_config_json(path, "exception file")
    if not isinstance(data, dict) or set(data.keys()) != {"schema_version", "binary", "private_ipv4"}:
        raise ScanConfigError(
            "exception file must contain only schema_version, binary, and private_ipv4"
        )
    if data["schema_version"] != 1 or type(data["schema_version"]) is not int:
        raise ScanConfigError("exception file schema_version must be integer 1")

    binary: dict[str, str] = {}
    if not isinstance(data["binary"], list):
        raise ScanConfigError("exception file binary must be a list")
    for entry in data["binary"]:
        if not isinstance(entry, dict) or set(entry.keys()) != {"path", "sha256", "reason"}:
            raise ScanConfigError("each binary exception must contain only path, sha256, and reason")
        if not _valid_relative_path(entry["path"]) or entry["path"] in binary:
            raise ScanConfigError("a binary exception has an invalid or duplicate path")
        if not isinstance(entry["sha256"], str) or not SHA256_RE.fullmatch(entry["sha256"]):
            raise ScanConfigError("a binary exception sha256 must be lowercase 64-hex")
        if not isinstance(entry["reason"], str) or not entry["reason"].strip():
            raise ScanConfigError("a binary exception needs a reason")
        binary[entry["path"]] = entry["sha256"]

    private: dict[str, frozenset[str]] = {}
    if not isinstance(data["private_ipv4"], list):
        raise ScanConfigError("exception file private_ipv4 must be a list")
    for entry in data["private_ipv4"]:
        if not isinstance(entry, dict) or set(entry.keys()) != {"path", "values", "reason"}:
            raise ScanConfigError("each private_ipv4 exception must contain only path, values, and reason")
        if not _valid_relative_path(entry["path"]) or entry["path"] in private:
            raise ScanConfigError("a private_ipv4 exception has an invalid or duplicate path")
        values = entry["values"]
        if not isinstance(values, list) or not values:
            raise ScanConfigError("a private_ipv4 exception needs a non-empty values list")
        canonical: set[str] = set()
        for value in values:
            octets = canonical_octets(value) if isinstance(value, str) and re.fullmatch(r"\d{1,3}(?:\.\d{1,3}){1,3}", value) else None
            if octets is None or ".".join(str(o) for o in octets) != value:
                raise ScanConfigError("a private_ipv4 exception value is not a canonical dotted address or prefix")
            canonical.add(value)
        if not isinstance(entry["reason"], str) or not entry["reason"].strip():
            raise ScanConfigError("a private_ipv4 exception needs a reason")
        private[entry["path"]] = frozenset(canonical)
    return Exceptions(binary=binary, private_ipv4=private)


def _parse_key_text(raw_text: str) -> bytes:
    cleaned = raw_text.strip()
    if not cleaned:
        raise ScanConfigError("HMAC scan key is missing or empty")
    if not KEY_HEX_RE.fullmatch(cleaned):
        raise ScanConfigError(
            "HMAC scan key must be exactly 64 lowercase hex characters (32 random bytes; "
            "create one with: openssl rand -hex 32)"
        )
    return bytes.fromhex(cleaned)


def load_synthetic_key(root: Path) -> bytes:
    """Load the public, non-secret synthetic key fixture."""
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
    """Load a private HMAC key from an untracked, owner-only regular file.

    The mode check covers the permission bits only; it does not inspect ACLs.
    """
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
            if resolved_key.relative_to(resolved_root).as_posix() in tracked_set:
                raise ScanConfigError("HMAC scan key file must not be tracked by Git")
        if key_path.is_relative_to(root):
            if key_path.relative_to(root).as_posix() in tracked_set:
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
        raise ScanConfigError(f"invalid {SYNTHETIC_ENV_VAR} value; expected '1' or unset")
    synthetic = synthetic_flag or (synthetic_env == "1")

    has_cli_key_file = key_file is not None
    has_env_key_file = KEY_FILE_ENV_VAR in env
    has_env_key = KEY_ENV_VAR in env
    explicit_private_sources = int(has_cli_key_file) + int(has_env_key_file) + int(has_env_key)

    if synthetic and explicit_private_sources > 0:
        raise ScanConfigError("cannot combine synthetic scan mode with a private HMAC key source")

    if synthetic:
        return KeyMaterial(key=load_synthetic_key(root), synthetic=True, source=str(SYNTHETIC_KEY_PATH))

    if explicit_private_sources > 1:
        raise ScanConfigError("specify only one HMAC key source")

    if has_cli_key_file:
        assert key_file is not None
        key_bytes = load_key_from_file(key_file, root=root, tracked=tracked)
        source = "--key-file"
    elif has_env_key_file:
        env_path_raw = env[KEY_FILE_ENV_VAR].strip()
        if not env_path_raw:
            raise ScanConfigError(f"{KEY_FILE_ENV_VAR} is set but empty")
        key_bytes = load_key_from_file(Path(env_path_raw), root=root, tracked=tracked)
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
                f"{DEFAULT_LOCAL_KEY_PATH} (mode 0600), or pass --synthetic for contributor checks"
            )

    # The published synthetic key must never be accepted as the private key.
    # If its fixture cannot be loaded the comparison cannot run, so fail.
    try:
        public_synthetic = load_synthetic_key(root)
    except ScanConfigError as exc:
        raise ScanConfigError(
            "cannot verify the private key differs from the public synthetic key: " + str(exc)
        ) from exc
    if hmac.compare_digest(key_bytes, public_synthetic):
        raise ScanConfigError("refusing to use the public synthetic key as a private HMAC key")

    return KeyMaterial(key=key_bytes, synthetic=False, source=source)


# --- sources ----------------------------------------------------------------


def _split_nul_paths(raw: bytes) -> list[str]:
    """Decode NUL-separated Git paths; any non-UTF-8 path fails the listing."""
    try:
        return [item.decode("utf-8") for item in raw.split(b"\0") if item]
    except UnicodeDecodeError as exc:
        raise ScanConfigError("tracked file listing contains a path that is not valid UTF-8") from exc


def tracked_files(root: Path) -> tuple[list[str] | None, str | None]:
    """Return tracked paths, or a concise error when listing fails or is empty."""
    try:
        result = subprocess.run(
            ["git", "ls-files", "-z"],
            cwd=root,
            check=True,
            capture_output=True,
        )
    except FileNotFoundError:
        return None, "Git is unavailable"
    except subprocess.CalledProcessError:
        return None, "the directory is not a readable Git worktree"
    except OSError as exc:
        return None, f"Git could not be executed: {exc.strerror or type(exc).__name__}"
    try:
        paths = _split_nul_paths(result.stdout)
    except ScanConfigError as exc:
        return None, str(exc)
    if not paths:
        return None, "tracked file listing is empty"
    return paths, None


def worktree_sources(root: Path, tracked: Sequence[str]) -> Iterator[SourceFile]:
    """Yield each tracked worktree entry, reading symlinks as their target text."""
    root_resolved = root.resolve()
    for relative in tracked:
        rel_path = Path(relative)
        if rel_path.is_absolute() or ".." in rel_path.parts:
            yield SourceFile(relative, None, problem="invalid tracked path")
            continue
        full_path = root / rel_path
        if full_path.is_symlink():
            try:
                target = os.readlink(full_path)
            except OSError:
                yield SourceFile(relative, None, problem="tracked symlink is unreadable")
                continue
            yield SourceFile(relative, os.fsencode(target), symlink=True)
            continue
        if not full_path.exists():
            yield SourceFile(relative, None, problem="tracked file is missing from worktree")
            continue
        if not full_path.is_file():
            yield SourceFile(relative, None, problem="tracked path is not a readable file")
            continue
        if not full_path.resolve().is_relative_to(root_resolved):
            yield SourceFile(relative, None, problem="tracked file resolves outside repository root")
            continue
        try:
            size = full_path.stat().st_size
            if size > MAX_SCAN_FILE_BYTES:
                yield SourceFile(relative, None, problem="tracked file is too large to scan")
                continue
            content = full_path.read_bytes()
        except OSError:
            yield SourceFile(relative, None, problem="tracked file is unreadable")
            continue
        yield SourceFile(relative, content)


def _git(root: Path, args: Sequence[str], *, stdin: bytes | None = None) -> bytes:
    try:
        result = subprocess.run(
            ["git", *args],
            cwd=root,
            input=stdin,
            check=True,
            capture_output=True,
        )
    except FileNotFoundError as exc:
        raise ScanConfigError("Git is unavailable") from exc
    except subprocess.CalledProcessError as exc:
        raise ScanConfigError("Git could not read the requested tree") from exc
    except OSError as exc:
        raise ScanConfigError("Git could not be executed") from exc
    return result.stdout


def _stream_listing(root: Path, revision: str) -> Iterator[bytes]:
    """Yield NUL-separated ls-tree records as they arrive, capping total bytes."""
    try:
        process = subprocess.Popen(
            ["git", "ls-tree", "-r", "-z", "--full-tree", revision],
            cwd=root, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        )
    except OSError as exc:
        raise ScanConfigError("Git could not be executed") from exc
    assert process.stdout is not None
    pending = b""
    total = 0
    finished = False
    try:
        while True:
            chunk = process.stdout.read(65536)
            if not chunk:
                break
            total += len(chunk)
            if total > MAX_LISTING_BYTES:
                raise ScanConfigError(f"tree listing is larger than {MAX_LISTING_BYTES} bytes; refusing to scan")
            pending += chunk
            *records, pending = pending.split(b"\0")
            for record in records:
                if record:
                    yield record
        if pending:
            raise ScanConfigError("Git returned a malformed tree listing")
        finished = True
    finally:
        process.stdout.close()
        if not finished:
            process.kill()
        if process.wait() != 0 and finished:
            raise ScanConfigError("Git could not read the requested tree")


def tree_sources(root: Path, revision: str) -> tuple[list[str], Iterator[SourceFile]]:
    """Read every entry of a commit's tree as git objects, never as a checkout.

    The revision must be a full commit SHA, so the scan is bound to an exact
    commit and not to a name the requester controls.
    """
    if not COMMIT_RE.fullmatch(revision):
        raise ScanConfigError("--tree needs a full lowercase commit SHA")
    _git(root, ["cat-file", "-e", f"{revision}^{{commit}}"])
    entries: list[tuple[str, str, str, str]] = []
    for record in _stream_listing(root, revision):
        meta, sep, raw_path = record.partition(b"\t")
        fields = meta.split(b" ")
        if not sep or len(fields) != 3:
            raise ScanConfigError("Git returned a malformed tree listing")
        try:
            path = raw_path.decode("utf-8")
            mode, kind, sha = (field.decode("ascii") for field in fields)
        except UnicodeDecodeError as exc:
            raise ScanConfigError("tracked file listing contains a path that is not valid UTF-8") from exc
        entries.append((mode, kind, sha, path))
        if len(entries) > MAX_TREE_ENTRIES:
            raise ScanConfigError(f"tree has more than {MAX_TREE_ENTRIES} entries; refusing to scan")
    if not entries:
        raise ScanConfigError("tracked file listing is empty")

    # One size check per distinct object, so many paths sharing a blob cost
    # one read; the total of distinct blob sizes is capped.
    unique = sorted({sha for _, kind, sha, _ in entries if kind == "blob"})
    sizes: dict[str, int] = {}
    if unique:
        listing = _git(root, ["cat-file", "--batch-check"], stdin="".join(f"{sha}\n" for sha in unique).encode("ascii"))
        for line in listing.splitlines():
            parts = line.split(b" ")
            if len(parts) != 3 or parts[1] != b"blob" or not parts[2].isdigit():
                raise ScanConfigError("Git returned a malformed object listing")
            sizes[parts[0].decode("ascii")] = int(parts[2])
        if set(sizes) != set(unique):
            raise ScanConfigError("Git returned a malformed object listing")
    readable = sum(size for size in sizes.values() if size <= MAX_SCAN_FILE_BYTES)
    if readable > MAX_TREE_BYTES:
        raise ScanConfigError(f"tree holds more than {MAX_TREE_BYTES} bytes of distinct content; refusing to scan")

    paths = [path for _, _, _, path in entries]
    return paths, _stream_tree(root, entries, sizes)


def _stream_tree(root: Path, entries: list[tuple[str, str, str, str]], sizes: Mapping[str, int]) -> Iterator[SourceFile]:
    """Yield entries grouped by object, reading one blob at a time."""
    by_object: dict[str, list[tuple[str, str]]] = {}
    for mode, kind, sha, path in entries:
        if kind != "blob":
            yield SourceFile(path, None, problem="tracked entry is not a file (submodule or other object)")
            continue
        if sizes[sha] > MAX_SCAN_FILE_BYTES:
            yield SourceFile(path, None, problem="tracked file is too large to scan")
            continue
        by_object.setdefault(sha, []).append((mode, path))
    if not by_object:
        return
    try:
        process = subprocess.Popen(
            ["git", "cat-file", "--batch"], cwd=root, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL
        )
    except OSError as exc:
        raise ScanConfigError("Git could not be executed") from exc
    assert process.stdin is not None and process.stdout is not None
    try:
        for sha, users in by_object.items():
            process.stdin.write(f"{sha}\n".encode("ascii"))
            process.stdin.flush()
            header = process.stdout.readline().rstrip(b"\n").split(b" ")
            if len(header) != 3 or header[0].decode("ascii", "replace") != sha or header[1] != b"blob" or int(header[2]) != sizes[sha]:
                raise ScanConfigError("Git returned a malformed object stream")
            content = process.stdout.read(sizes[sha])
            if len(content) != sizes[sha] or process.stdout.read(1) != b"\n":
                raise ScanConfigError("Git returned a truncated object stream")
            for mode, path in users:
                yield SourceFile(path, content, symlink=(mode == "120000"), object_id=sha)
    finally:
        process.stdin.close()
        process.stdout.close()
        process.wait()


# --- decoding and scanning --------------------------------------------------


def _looks_utf16(data: bytes) -> str | None:
    """Return a UTF-16 codec name when data looks like NUL-interleaved ASCII."""
    sample = data[:4096]
    if len(sample) < 4:
        return None
    pairs = len(sample) // 2
    even_nuls = sum(1 for i in range(0, pairs * 2, 2) if sample[i] == 0)
    odd_nuls = sum(1 for i in range(1, pairs * 2, 2) if sample[i] == 0)
    if odd_nuls >= pairs * 0.3 and even_nuls == 0:
        return "utf-16-le"
    if even_nuls >= pairs * 0.3 and odd_nuls == 0:
        return "utf-16-be"
    return None


def decode_content(data: bytes) -> str:
    """Decode file content strictly, raising ScanConfigError when it is not text.

    UTF-8, with or without a BOM, is the expected encoding. UTF-16 is decoded
    when a BOM or NUL-interleaved ASCII shows it. Anything else is not text.
    """
    try:
        if data.startswith((codecs.BOM_UTF16_LE, codecs.BOM_UTF16_BE)):
            text = data.decode("utf-16")
        else:
            body = data[len(codecs.BOM_UTF8):] if data.startswith(codecs.BOM_UTF8) else data
            codec = _looks_utf16(body)
            text = body.decode(codec) if codec else body.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ScanConfigError("tracked file is not valid UTF-8 or UTF-16 text") from exc
    # Whatever the branch, the decoded text must not hide tokens behind NULs.
    if "\0" in text:
        raise ScanConfigError("tracked file is binary, not text")
    return text


def escape_for_display(text: str) -> str:
    """Escape control, format and separator characters for one-line output."""
    out = []
    for ch in text:
        if unicodedata.category(ch)[0] in ("C", "Z") and ch != " ":
            code = ord(ch)
            if code < 0x100:
                out.append(f"\\x{code:02x}")
            elif code < 0x10000:
                out.append(f"\\u{code:04x}")
            else:
                out.append(f"\\U{code:08x}")
        else:
            out.append(ch)
    return "".join(out)


def path_component_id(key: bytes, component: str) -> str:
    """Return an opaque id for a path component; only the key holder can link it."""
    digest = hmac.new(key, f"{PATH_ID_DOMAIN}:{component}".encode("utf-8"), hashlib.sha256)
    return "<redacted-" + digest.hexdigest()[:12] + ">"


class Scanner:
    """Scan sources for keyed-rule matches and private addresses."""

    def __init__(self, *, rules: RuleSet, key: bytes, exceptions: Exceptions = EMPTY_EXCEPTIONS) -> None:
        self.rules = rules
        self.key = key
        self.exceptions = exceptions
        self.active_rules = tuple(
            (rule.rule_id, f"{HMAC_DOMAIN_PREFIX}:{rule.rule_id}:".encode("utf-8"), frozenset(bytes.fromhex(d) for d in rule.digests))
            for rule in rules.rules
            if rule.digests
        )
        self.cache: dict[str, tuple[str, ...]] = {}
        self.cache_bytes = 0
        self.object_findings: dict[str, tuple[str | None, list[tuple[int, str]], tuple[list[tuple[int, str, str]], bool]]] = {}

    def keyed_hits(self, candidates: Iterable[str]) -> set[str]:
        matched: set[str] = set()
        for candidate in candidates:
            cached = self.cache.get(candidate)
            if cached is None:
                encoded = candidate.encode("utf-8")
                if self.cache_bytes + len(encoded) > CANDIDATE_CACHE_BYTES:
                    self.cache.clear()
                    self.cache_bytes = 0
                self.cache_bytes += len(encoded)
                cached = tuple(
                    rule_id
                    for rule_id, prefix, digests in self.active_rules
                    if hmac.digest(self.key, prefix + encoded, "sha256") in digests
                )
                self.cache[candidate] = cached
            matched.update(cached)
        return matched

    def path_flagged(self, relative: str) -> bool:
        if self.keyed_hits(extract_candidates(relative)):
            return True
        return bool(private_ipv4_hits(relative))

    def display_path(self, relative: str) -> str:
        """Return a printable path: an opaque id when anything in it matches.

        A value can span components (a folder and a file name), so a match
        anywhere hides the whole path. Other paths are printed with control
        and format characters escaped, so a name cannot forge a log line.
        """
        if self.path_flagged(relative):
            return path_component_id(self.key, relative)
        return escape_for_display(relative)

    def scan_lines(self, text: str) -> list[tuple[int, str]]:
        """Return (line number, label) findings for text, without any value.

        Lines are scanned in one pass, and the finding limit is checked as
        findings are produced, line and wrap findings alike. Past the limit
        the list holds MAX_FINDINGS + 1 entries, which fails the scan.
        """
        lines = text.replace("\r\n", "\n").replace("\r", "\n").split("\n")
        found: list[tuple[int, str]] = []
        previous_hits: set[str] = set()
        for index, line in enumerate(lines):
            hits = self.keyed_hits(extract_candidates(line)) if line else set()
            for rule_id in sorted(hits):
                found.append((index + 1, f"matched keyed rule '{rule_id}'"))
            # Join this line with the previous one to catch a name split by a
            # wrap: a trailing hyphen joins without a space, any other wrap
            # with one. A bounded window from each side.
            if index > 0:
                left = lines[index - 1].rstrip()[-WRAP_WINDOW_CHARS:]
                right = line.lstrip()[:WRAP_WINDOW_CHARS]
                if left and right:
                    joined = left[:-1] + right if left.endswith("-") else left + " " + right
                    wrapped = self.keyed_hits(extract_candidates(joined)) - previous_hits - hits
                    for rule_id in sorted(wrapped):
                        found.append((index, f"matched keyed rule '{rule_id}' across a line wrap"))
            if len(found) > MAX_FINDINGS:
                return found[: MAX_FINDINGS + 1]
            previous_hits = hits
        return found

    def allowed_private(self, relative: str) -> frozenset[str]:
        # The exception file may hold exactly the values it lists, nothing else.
        if relative == DEFAULT_EXCEPTIONS_PATH.as_posix():
            return frozenset().union(*self.exceptions.private_ipv4.values())
        return self.exceptions.private_ipv4.get(relative, frozenset())

    def private_ipv4_values(self, text: str) -> tuple[list[tuple[int, str, str]], bool]:
        """Return the raw address hits and whether collection overflowed.

        Exceptions are applied later, per path, so an overflow is a failure
        whatever the exceptions: otherwise repeating an allowed value past the
        limit would hide every address after it.
        """
        found: list[tuple[int, str, str]] = []
        for number, line in enumerate(text.replace("\r\n", "\n").replace("\r", "\n").split("\n"), start=1):
            for label, value in private_ipv4_hits(line):
                found.append((number, label, value))
            if len(found) > MAX_FINDINGS:
                return found[:MAX_FINDINGS], True
        return found, False

    def scan_private_ipv4(self, relative: str, text: str) -> list[tuple[int, str]]:
        allowed = self.allowed_private(relative)
        found: list[tuple[int, str]] = []
        lines = text.replace("\r\n", "\n").replace("\r", "\n").split("\n")
        for number, line in enumerate(lines, start=1):
            for label, value in private_ipv4_hits(line):
                if value not in allowed:
                    found.append((number, f"private IPv4 address or prefix in {label}"))
        return found

    def scan_source(self, source: SourceFile) -> list[str]:
        shown = self.display_path(source.path)
        failures: list[str] = []
        path_rules = sorted(self.keyed_hits(extract_candidates(source.path)))
        for rule_id in path_rules:
            failures.append(f"{shown}:0: matched keyed rule '{rule_id}'")
        allowed = self.allowed_private(source.path)
        for label, value in private_ipv4_hits(source.path):
            if value not in allowed:
                failures.append(f"{shown}:0: private IPv4 address or prefix in {label}")

        if source.problem is not None:
            failures.append(f"{source.problem}: {shown}")
            return failures
        assert source.content is not None

        expected_binary = self.exceptions.binary.get(source.path)
        if expected_binary is not None:
            if hashlib.sha256(source.content).hexdigest() != expected_binary:
                failures.append(f"binary exception content changed; review and update its sha256: {shown}")
            return failures

        cached = self.object_findings.get(source.object_id) if source.object_id else None
        if cached is None:
            try:
                text = decode_content(source.content)
            except ScanConfigError as exc:
                cached = (str(exc), [], ([], False))
            else:
                cached = (None, self.scan_lines(text), self.private_ipv4_values(text))
            if source.object_id:
                self.object_findings[source.object_id] = cached
        error, line_findings, (address_hits, address_overflow) = cached
        if error is not None:
            failures.append(f"{error}: {shown}")
            return failures
        if address_overflow:
            failures.append(f"more than {MAX_FINDINGS} private address matches; the scan cannot check the rest: {shown}")

        allowed_values = self.allowed_private(source.path)
        findings = line_findings + [
            (number, f"private IPv4 address or prefix in {label}")
            for number, label, value in address_hits
            if value not in allowed_values
        ]
        for number, label in sorted(findings)[: MAX_FINDINGS + 1]:
            failures.append(f"{shown}:{number}: {label}")
        return failures


def scan_sources(
    sources: Iterable[SourceFile],
    *,
    rules: RuleSet,
    key: bytes,
    exceptions: Exceptions = EMPTY_EXCEPTIONS,
) -> list[str]:
    """Scan sources; an empty source list is a failure."""
    scanner = Scanner(rules=rules, key=key, exceptions=exceptions)
    failures: list[str] = []
    count = 0
    for source in sources:
        count += 1
        failures.extend(scanner.scan_source(source))
        if len(failures) > MAX_FINDINGS:
            # Fail closed: stop, keep the first findings and say so.
            del failures[MAX_FINDINGS:]
            failures.append(f"more than {MAX_FINDINGS} findings; the scan stopped after {count} entries")
            if hasattr(sources, "close"):
                sources.close()
            return failures
    if count == 0:
        return ["tracked file listing is missing or empty"]
    return failures


def scan_text(
    relative_path: str,
    text: str,
    *,
    rules: RuleSet,
    key: bytes,
    cache: dict[str, tuple[str, ...]] | None = None,
) -> list[ScanFinding]:
    """Scan one path and its text against the keyed rules only."""
    scanner = Scanner(rules=rules, key=key)
    if cache is not None:
        scanner.cache = cache
    findings = [
        ScanFinding(path=scanner.display_path(relative_path), line=0, rule_id=rule_id)
        for rule_id in sorted(scanner.keyed_hits(extract_candidates(relative_path)))
    ]
    shown = scanner.display_path(relative_path)
    for number, label in scanner.scan_lines(text):
        rule_id = label.split("'")[1]
        findings.append(ScanFinding(path=shown, line=number, rule_id=rule_id))
    return findings


def scan_tracked_files(
    root: Path,
    tracked: Sequence[str] | None,
    *,
    rules: RuleSet,
    key: bytes,
    exceptions: Exceptions = EMPTY_EXCEPTIONS,
) -> list[str]:
    """Scan every tracked worktree file; fail closed on gaps."""
    if not tracked:
        return ["tracked file listing is missing or empty"]
    return scan_sources(worktree_sources(root, tracked), rules=rules, key=key, exceptions=exceptions)


def verify_synthetic_fixtures(root: Path, *, rules: RuleSet, key: bytes) -> list[str]:
    """Verify the synthetic clean and positive fixtures against the synthetic key."""
    failures: list[str] = []
    clean_path = root / SYNTHETIC_CLEAN_FIXTURE_PATH
    if clean_path.is_symlink() or not clean_path.is_file():
        return [f"synthetic clean fixture is missing: {SYNTHETIC_CLEAN_FIXTURE_PATH}"]
    try:
        clean_text = clean_path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return [f"synthetic clean fixture is unreadable: {SYNTHETIC_CLEAN_FIXTURE_PATH}"]

    if scan_text(str(SYNTHETIC_CLEAN_FIXTURE_PATH), clean_text, rules=rules, key=key):
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
    if not expected.issubset(set(positive_findings)) or any(f.line > 3 for f in positive_findings):
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
        print("Unsupported rule id; use personal-name or private-network-name")
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


def _select_rules(
    root: Path,
    key_material: KeyMaterial,
    rules_file: Path | None,
    failures: list[str],
) -> RuleSet | None:
    """Pick the rules for this run, appending any configuration failure."""
    prod_rules_path = rules_file if rules_file is not None else root / DEFAULT_RULES_PATH
    if not key_material.synthetic:
        # Keyed mode: every production rule must carry digests, or the scan
        # would pass while checking nothing.
        try:
            return load_rules(prod_rules_path, require_nonempty_digests=True)
        except ScanConfigError as exc:
            failures.append(str(exc))
            return None

    # Synthetic mode still validates the production rule file's format.
    try:
        load_rules(prod_rules_path)
    except ScanConfigError as exc:
        failures.append(str(exc))
    synthetic_rules_path = (
        root / SYNTHETIC_RULES_PATH
        if key_material.local_fallback or rules_file is None
        else rules_file
    )
    try:
        rules = load_rules(synthetic_rules_path, require_nonempty_digests=True)
    except ScanConfigError as exc:
        failures.append(str(exc))
        return None
    if key_material.local_fallback or rules_file is None:
        failures.extend(verify_synthetic_fixtures(root, rules=rules, key=key_material.key))
    return rules


def _report(failures: list[str], key_material: KeyMaterial | None, passed: str) -> int:
    local = key_material is not None and key_material.local_fallback
    if local:
        print("WARNING: keyed private-name rules not checked locally; CI runs them.")
    if failures:
        print("Public release checks failed:")
        for failure in failures:
            print(f"- {failure}")
        return 1
    print(passed)
    return 0


def _run(args: argparse.Namespace) -> int:
    root = args.root.resolve()

    if args.hash_rule is not None:
        return _run_hash_mode(
            root=root,
            rule_id=args.hash_rule,
            key_file=args.key_file,
            synthetic=args.synthetic,
        )

    failures: list[str] = []
    try:
        exceptions = load_exceptions(root / DEFAULT_EXCEPTIONS_PATH)
    except ScanConfigError as exc:
        failures.append(str(exc))
        exceptions = EMPTY_EXCEPTIONS

    if args.tree is not None:
        # Scan a commit's tree as git objects with this checkout's scanner,
        # rules and exceptions; repository metadata checks do not apply.
        tree_paths: list[str] | None = None
        sources: Iterable[SourceFile] = []
        try:
            tree_paths, sources = tree_sources(root, args.tree)
        except ScanConfigError as exc:
            failures.append(f"tree listing failed: {exc}")
        key_material: KeyMaterial | None = None
        try:
            key_material = resolve_key_material(
                root,
                tracked=tree_paths,
                key_file=args.key_file,
                synthetic_flag=args.synthetic,
            )
        except ScanConfigError as exc:
            failures.append(str(exc))
        if key_material is not None:
            rules = _select_rules(root, key_material, args.rules_file, failures)
            if rules is not None and tree_paths:
                try:
                    failures.extend(scan_sources(sources, rules=rules, key=key_material.key, exceptions=exceptions))
                except ScanConfigError as exc:
                    failures.append(f"tree read failed: {exc}")
        mode = "public synthetic scan" if key_material is not None and key_material.synthetic else "keyed scan"
        return _report(failures, key_material, f"Public-safety scan of the tree passed ({mode}).")

    tracked, git_error = tracked_files(root)
    failures.extend(
        deterministic_failures(
            root,
            require_license=not args.allow_missing_license,
            tracked=tracked,
        )
    )
    if git_error:
        failures = [item for item in failures if item != "tracked file listing is missing or empty"]
        failures.append(f"tracked file listing failed: {git_error}")

    key_material = None
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

    if key_material is not None:
        rules = _select_rules(root, key_material, args.rules_file, failures)
        if tracked and rules is not None:
            failures.extend(
                scan_tracked_files(root, tracked, rules=rules, key=key_material.key, exceptions=exceptions)
            )

    qualifier = "draft " if args.allow_missing_license else ""
    if key_material is not None and key_material.local_fallback:
        passed = (
            f"Deterministic public-release {qualifier}checks passed (local synthetic scan); "
            "complete docs/13-public-release-checklist.md before publication."
        )
    elif key_material is not None and key_material.synthetic:
        passed = (
            f"Deterministic public-release {qualifier}checks passed (public synthetic scan); "
            "the public-scan workflow runs the keyed scan."
        )
    else:
        passed = (
            f"Deterministic public-release {qualifier}checks passed; "
            "complete docs/13-public-release-checklist.md before publication."
        )
    return _report(failures, key_material, passed)


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
        help="run with the public synthetic key and fixtures (contributor checks)",
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
        "--tree",
        default=None,
        metavar="COMMIT_SHA",
        help="scan this commit's tree as git objects (no checkout); skips metadata checks",
    )
    parser.add_argument(
        "--hash-rule",
        default=None,
        help="compute keyed HMAC digests for one value per line from stdin for RULE_ID",
    )
    args = parser.parse_args(argv)
    try:
        return _run(args)
    except Exception as exc:  # noqa: BLE001 - never print exception text, which may hold input
        print("Public release checks failed:")
        print(f"- internal error ({type(exc).__name__}); rerun locally to diagnose")
        return 1


if __name__ == "__main__":
    sys.exit(main())
