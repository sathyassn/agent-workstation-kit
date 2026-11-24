#!/usr/bin/env python3
"""Check repository prerequisites and the deterministic public-safety scan.

The public-safety scan blocks personal and identifying details without any
secret or name list, using deterministic detectors:

- email addresses
- phone numbers
- IPv4 addresses (private and public) and partial private IPv4 prefixes
- IPv6 addresses (private and public)
- MAC addresses
- home-folder paths with real-looking usernames
- tailnet and private DNS names (*.ts.net, *.internal, *.local)
- cloud account and project IDs
- chat and tracker user IDs
- secrets and private key headers

Before detection, text is decoded (UTF-8, UTF-16 with or without BOM, JSON
escapes, URL escapes) and folded (NFKD/NFKC, format and combining marks,
confusable lookalikes, case, zero-padded octets). Files containing NUL bytes
are refused as binary unless listed by exact SHA-256 hash in the binary
exceptions file.

Findings print only the file, line, and rule, never the matched text.
Tracked exemptions store only unkeyed SHA-256 digests of folded values,
with reason, approver, and expiry date, and are read only from the trusted
base checkout when scanning a commit tree with --tree.
"""

from __future__ import annotations

import argparse
import codecs
import datetime
import functools
import hashlib
import ipaddress
import json
import os
import re
import stat
import subprocess
import sys
import unicodedata
from collections.abc import Callable, Iterable, Iterator, Mapping, Sequence
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
    ".github/CODEOWNERS",
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
    "tests/public_scan_modes.json",
    "tests/public_scan_exemptions.json",
    "tests/public_scan_binary_exceptions.json",
)
CONDUCT_CONTACT_PLACEHOLDER = "owner must add a monitored private conduct-report"
DEPENDABOT_COMMIT_POLICY = (
    "    commit-message:\n",
    "      prefix: build\n",
    "      prefix-development: build\n",
    "      include: scope\n",
)

DEFAULT_MODES_PATH = Path("tests/public_scan_modes.json")
DEFAULT_EXEMPTIONS_PATH = Path("tests/public_scan_exemptions.json")
DEFAULT_BINARY_EXCEPTIONS_PATH = Path("tests/public_scan_binary_exceptions.json")

MAX_SCAN_FILE_BYTES = 20 * 1024 * 1024
MAX_TREE_ENTRIES = 100_000
MAX_TREE_BYTES = 512 * 1024 * 1024
MAX_LISTING_BYTES = 64 * 1024 * 1024
MAX_FINDINGS = 10_000
CACHE_LINE_CHARS = 2048
CACHE_LINES = 4096

SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
COMMIT_RE = re.compile(r"^(?:[0-9a-f]{40}|[0-9a-f]{64})$")
ISO_DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")

DETECTOR_IDS = (
    "email",
    "phone",
    "ipv4",
    "ipv6",
    "mac-address",
    "home-path",
    "private-dns",
    "cloud-id",
    "chat-tracker-id",
    "secret",
)
VALID_MODES = frozenset({"block", "warn", "off"})
DEFAULT_MODES: Mapping[str, str] = {rule_id: "block" for rule_id in DETECTOR_IDS}

# --- decoding and folding tables --------------------------------------------

JSON_ESCAPE_RE = re.compile(r"\\u([0-9a-fA-F]{4})")
URL_ESCAPE_RUN_RE = re.compile(r"(?:%[0-9a-fA-F]{2})+")
WHITESPACE_RE = re.compile(r"\s+")

# Latin lookalikes from Cyrillic, Greek, Latin variants, and punctuation
# homoglyphs. NFKC already folds fullwidth forms (e.g. fullwidth @, ., digits).
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
        # Homoglyph punctuation
        "\u2024": ".", "\u3002": ".", "\uff61": ".",
        "\u2010": "-", "\u2011": "-", "\u2012": "-",
        "\u2013": "-", "\u2014": "-", "\u2212": "-",
        "\u2215": "/", "\u2044": "/",
    }
)


# --- documented exact allow lists (matched strictly after folding) ----------

ALLOWED_EMAIL_DOMAINS = frozenset({
    "example.com",
    "example.org",
    "example.net",
    "example.invalid",
})

NON_EMAIL_TLDS = frozenset({
    "service",
    "socket",
    "slice",
    "scope",
    "target",
    "timer",
    "mount",
    "path",
    "swap",
    "device",
    "instructionfiles",
})

ALLOWED_PHONES = frozenset({
    "+15555550100",
    "+15555550101",
    "+15555550199",
    "5555550100",
    "5555550101",
    "5555550199",
    "+442079460000",
})

ALLOWED_IPV4_NETWORKS = (
    ipaddress.IPv4Network("192.0.2.0/24"),
    ipaddress.IPv4Network("198.51.100.0/24"),
    ipaddress.IPv4Network("203.0.113.0/24"),
)
ALLOWED_IPV4_EXACT = frozenset({
    "0.0.0.0",
    "127.0.0.1",
    "127.0.1.1",
    "255.255.255.255",
})


def _net(*octets: int, prefix: int) -> ipaddress.IPv4Network:
    return ipaddress.IPv4Network(".".join(str(o) for o in octets) + f"/{prefix}")


PRIVATE_IPV4_RANGES = (
    ("rfc1918-a", _net(10, 0, 0, 0, prefix=8)),
    ("rfc1918-b", _net(172, 16, 0, 0, prefix=12)),
    ("rfc1918-c", _net(192, 168, 0, 0, prefix=16)),
    ("cgnat", _net(100, 64, 0, 0, prefix=10)),
    ("link-local", _net(169, 254, 0, 0, prefix=16)),
)
DISTINCT_TWO_GROUP_RANGES = frozenset({"rfc1918-b", "rfc1918-c", "link-local"})

ALLOWED_IPV6_NETWORKS = (
    ipaddress.IPv6Network("2001:db8::/32"),
)
ALLOWED_IPV6_EXACT = frozenset({
    "::",
    "::1",
})

# RFC 7042 documentation MAC addresses (00:00:5e:00:53:00 through 00:00:5e:00:53:ff)
# plus null/broadcast addresses.
ALLOWED_MACS = frozenset(
    {f"00:00:5e:00:53:{octet:02x}" for octet in range(256)}
    | {"00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff"}
)

# Neutral placeholder usernames from AGENTS.md and documentation.
ALLOWED_HOME_USERS = frozenset({
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
})

# Exact documentation private-DNS placeholders from AGENTS.md.
ALLOWED_PRIVATE_DNS = frozenset({
    "host-a.ts.net",
    "host-a.internal",
    "host-a.local",
    "host-a.example.ts.net",
    "example.ts.net",
    "example.internal",
    "example.local",
    "localhost.local",
})

ALLOWED_CLOUD_IDS = frozenset({
    "123456789012",
    "000000000000",
    "111122223333",
    "example-project",
    "acme-example-project",
    "00000000-0000-0000-0000-000000000000",
    "11111111-1111-1111-1111-111111111111",
    "000000-000000-000000",
})

ALLOWED_CHAT_TRACKER_IDS = frozenset({
    "u00000000",
    "u00example",
    "w00000000",
    "t00000000",
    "000000000000000000",
    "000000:00000000-0000-0000-0000-000000000000",
    "example-user-id",
})

ALLOWED_SECRETS = frozenset({
    "akiaiosfodnn7example",
})


# --- detector regular expressions -------------------------------------------

EMAIL_RE = re.compile(
    r"(?<![A-Za-z0-9._%+-])"
    r"([A-Za-z0-9](?:[A-Za-z0-9._%+-]{0,62}[A-Za-z0-9])?"
    r"@"
    r"(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,24})"
    r"(?![A-Za-z0-9.-])"
)

PHONE_E164_RE = re.compile(
    r"(?<![A-Za-z0-9+])(\+[1-9](?:[ \t.-]?\d){7,14})(?![A-Za-z0-9])"
)
PHONE_INTL_PAREN_RE = re.compile(
    r"(?<![A-Za-z0-9+])(\+[1-9]\d{0,2}[ \t.-]?\(\d{2,4}\)[ \t.-]?\d{3,4}[ \t.-]\d{3,4})(?![A-Za-z0-9])"
)
PHONE_PAREN_RE = re.compile(
    r"(?<![A-Za-z0-9])(\(\d{3}\)[ \t.-]?\d{3}[ \t.-]\d{4})(?![A-Za-z0-9])"
)
PHONE_SEP_RE = re.compile(
    r"(?<![A-Za-z0-9.-])(\d{3}([-. ])\d{3}\2\d{4})(?![A-Za-z0-9.-])"
)

DOTTED_DECIMAL_RE = re.compile(
    r"(?<!\d)(?<!\d\.)(\d{1,3}(?:\.\d{1,3}){1,3})(?!\d)(?!\.\d)"
)
PREFIX_CONTINUATION_RE = re.compile(r"\.(?!\d)|/\d|\.[x*](?![\w])")

IPV6_RE = re.compile(
    r"(?<![0-9A-Fa-f:])([0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){2,7}(?:\.\d{1,3}){0,3})(?![0-9A-Za-z:])"
)
IPV6_GLUED_RE = re.compile(
    r"(?<![A-Za-z0-9:])[A-Za-z]{1,24}"
    r"((?:fe80|fc[0-9a-f]{2}|fd[0-9a-f]{2}|2[0-9a-f]{3})::[0-9a-f:]*|"
    r"(?:fe80|fc[0-9a-f]{2}|fd[0-9a-f]{2}|2[0-9a-f]{3})(?::[0-9a-f]{1,4}){2,7})"
    r"(?![0-9A-Za-z:])",
    re.IGNORECASE,
)

MAC_COLON_RE = re.compile(
    r"(?<![0-9A-Fa-f:])([0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5})(?![0-9A-Fa-f:])"
)
MAC_HYPHEN_RE = re.compile(
    r"(?<![0-9A-Fa-f-])([0-9A-Fa-f]{2}(?:-[0-9A-Fa-f]{2}){5})(?![0-9A-Fa-f-])"
)
MAC_CISCO_RE = re.compile(
    r"(?<![0-9A-Fa-f.])([0-9A-Fa-f]{4}\.[0-9A-Fa-f]{4}\.[0-9A-Fa-f]{4})(?![0-9A-Fa-f.])"
)

HOME_PATH_RE = re.compile(
    r"(?<![A-Za-z0-9._-])"
    r"(?:/(Users|home)/([A-Za-z0-9._-]{2,64})(?=/|$|[\s\"'`<>()\[\]{},;:])"
    r"|[A-Za-z]:[\\/](Users)[\\/]([A-Za-z0-9._-]{2,64})(?=[\\/]|$|[\s\"'`<>()\[\]{},;:]))"
)

PRIVATE_DNS_RE = re.compile(
    r"(?<![A-Za-z0-9.-])"
    r"([A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?"
    r"(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*"
    r"\.(?:ts\.net|internal|local))"
    r"(?!\.[A-Za-z0-9-])(?![A-Za-z0-9-])",
    re.IGNORECASE,
)

AWS_ARN_RE = re.compile(
    r"arn:aws(?:-[a-z]+)?:[a-z0-9-]+:[a-z0-9-]*:(\d{12}):[^\s\"'`<>]*",
    re.IGNORECASE,
)
AWS_ECR_RE = re.compile(
    r"(?<!\d)(\d{12})\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com",
    re.IGNORECASE,
)
AWS_ACCOUNT_KV_RE = re.compile(
    r"\b(?:aws[_-]?account(?:[_-]?id)?|account[_-]?id)[ \t]*[:=][ \t]*[\"']?(\d{12})[\"']?(?!\d)",
    re.IGNORECASE,
)
GCP_PROJECT_KV_RE = re.compile(
    r"\b(?:gcp[_-]?project(?:[_-]?id)?|google[_-]?cloud[_-]?project)[ \t]*[:=][ \t]*[\"']?([a-z][a-z0-9-]{4,28}[a-z0-9])[\"']?",
    re.IGNORECASE,
)
GCP_URL_PROJECT_RE = re.compile(
    r"googleapis\.com/[^\s\"'`<>]*?/projects/([a-z][a-z0-9-]{4,28}[a-z0-9])(?=/|$|[\s\"'`<>?,;])",
    re.IGNORECASE,
)
GCP_BILLING_RE = re.compile(
    r"\bbillingAccounts/([0-9A-Fa-f]{6}-[0-9A-Fa-f]{6}-[0-9A-Fa-f]{6})\b"
)
AZURE_SUB_URL_RE = re.compile(
    r"/subscriptions/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b",
    re.IGNORECASE,
)
AZURE_KV_RE = re.compile(
    r"\b(?:azure[_-]?(?:subscription|tenant)[_-]?id|subscription[_-]?id|tenant[_-]?id)[ \t]*[:=][ \t]*[\"']?([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})[\"']?",
    re.IGNORECASE,
)
CF_ACCOUNT_RE = re.compile(
    r"\bcloudflare[_-]?account[_-]?id[ \t]*[:=][ \t]*[\"']?([0-9a-f]{32})[\"']?",
    re.IGNORECASE,
)

SLACK_MENTION_RE = re.compile(r"<@([UW][A-Z0-9]{8,12})>", re.IGNORECASE)
SLACK_BARE_RE = re.compile(r"(?<![A-Za-z0-9_\\-])([UWT]0[0-9A-Z]{7,10})(?![A-Za-z0-9_-])")
DISCORD_MENTION_RE = re.compile(r"<@!?(\d{17,20})>")
ATLASSIAN_ACCOUNT_RE = re.compile(
    r"\b(\d{6}:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b",
    re.IGNORECASE,
)
CHAT_TRACKER_KV_RE = re.compile(
    r"\b(?:slack|discord|jira|atlassian|linear|telegram|mattermost|teams|tracker|chat)"
    r"[_-]?(?:account|user|member|workspace|team)[_-]?id"
    r"[ \t]*[:=][ \t]*[\"']?([A-Za-z0-9:._-]{5,64})[\"']?",
    re.IGNORECASE,
)

DIGIT_RE = re.compile(r"\d")
IPV6_HINT_RE = re.compile(r":[0-9A-Fa-f]{0,4}:")
MAC_COLON_HINT_RE = re.compile(r":[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}:")
MAC_HYPHEN_HINT_RE = re.compile(r"-[0-9A-Fa-f]{2}-[0-9A-Fa-f]{2}-")
MAC_CISCO_HINT_RE = re.compile(r"\.[0-9A-Fa-f]{4}\.")
PHONE_PAREN_HINT_RE = re.compile(r"\(\d{3}\)")
PHONE_E164_HINT_RE = re.compile(r"\+[1-9]")
ATLASSIAN_HINT_RE = re.compile(r":[0-9a-fA-F]{8}-")
SECRET_PATTERNS = (
    (("AKIA", "ASIA"), re.compile(r"(?<![A-Za-z0-9])(?:AKIA|ASIA)[0-9A-Z]{16}(?![A-Za-z0-9])")),
    (("ghp_", "gho_", "ghu_", "ghs_", "ghr_"), re.compile(r"(?<![A-Za-z0-9_])gh[pousr]_[A-Za-z0-9]{30,}(?![A-Za-z0-9_])")),
    (("github_pat_",), re.compile(r"(?<![A-Za-z0-9_])github_pat_[A-Za-z0-9_]{22,}(?![A-Za-z0-9_])")),
    (("glpat-",), re.compile(r"(?<![A-Za-z0-9_-])glpat-[A-Za-z0-9_-]{20,}(?![A-Za-z0-9_-])")),
    (("sk-",), re.compile(r"(?<![A-Za-z0-9_-])sk-[A-Za-z0-9_-]{24,}(?![A-Za-z0-9_-])")),
    (("xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-"), re.compile(r"(?<![A-Za-z0-9_-])xox[baprs]-[0-9A-Za-z-]{10,}(?![A-Za-z0-9_-])")),
    (("tskey-",), re.compile(r"(?<![A-Za-z0-9_-])tskey-(?:auth|api|client)-[A-Za-z0-9_-]{16,}(?![A-Za-z0-9_-])")),
    (("AIza",), re.compile(r"(?<![A-Za-z0-9_-])AIza[0-9A-Za-z_-]{35}(?![A-Za-z0-9_-])")),
    (("PRIVATE KEY",), re.compile(r"-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----")),
)


# --- types ------------------------------------------------------------------


class ScanConfigError(ValueError):
    """Raised when scan configuration, exemptions, or input are invalid.

    Messages never contain input text.
    """


@dataclass(frozen=True)
class ExemptionRecord:
    path: str
    rule: str
    digest: str
    reason: str
    approved_by: str
    expires: str

    @property
    def key(self) -> tuple[str, str, str]:
        return (self.path, self.rule, self.digest)


@dataclass(frozen=True)
class BinaryException:
    path: str
    sha256: str
    reason: str


@dataclass(frozen=True)
class SourceFile:
    """One tracked entry: its path, and its content or a reason it has none."""

    path: str
    content: bytes | None
    symlink: bool = False
    problem: str | None = None
    object_id: str | None = None


@dataclass(frozen=True)
class ScanResult:
    failures: list[str]
    warnings: list[str]
    applied_exemptions: frozenset[tuple[str, str, str]]


# --- decoding and normalisation ---------------------------------------------


def _decode_json_escapes(text: str) -> str:
    if "\\u" not in text and "\\/" not in text:
        return text
    decoded = text.replace("\\/", "/")
    if "\\u" in decoded:
        def replace(match: re.Match[str]) -> str:
            code = int(match.group(1), 16)
            if code in (0x0A, 0x0D):
                return " "
            return chr(code)

        decoded = JSON_ESCAPE_RE.sub(replace, decoded)
        # Join escaped surrogate pairs into one character; drop lone surrogates.
        decoded = decoded.encode("utf-16", "surrogatepass").decode("utf-16", "ignore")
    return decoded


def _decode_url_escapes(text: str) -> str:
    if "%" not in text:
        return text

    def replace(match: re.Match[str]) -> str:
        raw = match.group(0)
        hex_bytes = bytes(int(raw[i + 1 : i + 3], 16) for i in range(0, len(raw), 3))
        if b"\x00" in hex_bytes:
            return raw
        try:
            decoded = hex_bytes.decode("utf-8")
        except UnicodeDecodeError:
            return raw
        return decoded.replace("\r", " ").replace("\n", " ")

    return URL_ESCAPE_RUN_RE.sub(replace, text)


def normalize_text(text: str, *, separate: bool = False, decode_url: bool = True) -> str:
    """Decode and fold text before running detectors (cached for short lines)."""
    if len(text) <= CACHE_LINE_CHARS:
        return _normalize_text_cached(text, separate, decode_url)
    return _normalize_text(text, separate, decode_url)


@functools.lru_cache(maxsize=CACHE_LINES)
def _normalize_text_cached(text: str, separate: bool, decode_url: bool) -> str:
    return _normalize_text(text, separate, decode_url)


def _normalize_text(text: str, separate: bool, decode_url: bool) -> str:
    if (
        text.isascii()
        and "\\u" not in text
        and "\\/" not in text
        and (not decode_url or "%" not in text)
    ):
        return text
    decoded = _decode_json_escapes(text)
    if decode_url:
        decoded = _decode_url_escapes(decoded)
    if not decoded.isascii():
        decoded = unicodedata.normalize("NFKD", decoded)
        if separate:
            decoded = "".join(
                " " if unicodedata.category(ch) in ("Cf", "Mn") else ch
                for ch in decoded
            )
        else:
            decoded = "".join(
                ch for ch in decoded if unicodedata.category(ch) not in ("Cf", "Mn")
            )
        decoded = unicodedata.normalize("NFKC", decoded).translate(CONFUSABLES)
    return decoded


def _line_variants(line: str) -> tuple[str, ...]:
    """Return normalized text variants for a line without redundant work."""
    if line.isascii() and "\\u" not in line and "\\/" not in line and "%" not in line:
        return (line,)
    primary = normalize_text(line, separate=False, decode_url=True)
    variants = [primary]
    if not line.isascii() or "\\u" in line or "%" in line:
        separated = normalize_text(line, separate=True, decode_url=True)
        if separated != primary:
            variants.append(separated)
    if "%" in line:
        no_url = normalize_text(line, separate=False, decode_url=False)
        if no_url not in variants:
            variants.append(no_url)
    return tuple(variants)


def canonical_octets(groups: str) -> tuple[int, ...] | None:
    """Return the decimal octets of a dotted run, or None if any exceeds 255."""
    octets = tuple(int(part, 10) for part in groups.split("."))
    if any(octet > 255 for octet in octets):
        return None
    return octets


def fold_value_for_rule(rule_id: str, value: str) -> str:
    """Fold one matched value into the canonical form hashed for exemptions."""
    if rule_id not in DETECTOR_IDS:
        raise ScanConfigError("unknown detector rule id")
    normalized = normalize_text(value).strip()
    if not normalized:
        raise ScanConfigError("value to hash must not be empty")
    if rule_id == "ipv4":
        octets = canonical_octets(normalized) if re.fullmatch(r"\d{1,3}(?:\.\d{1,3}){1,3}", normalized) else None
        if octets is None:
            raise ScanConfigError("invalid ipv4 value for exemption digest")
        return ".".join(str(o) for o in octets)
    if rule_id == "ipv6":
        try:
            return str(ipaddress.IPv6Address(normalized.split("%", 1)[0]))
        except ValueError as exc:
            raise ScanConfigError("invalid ipv6 value for exemption digest") from exc
    if rule_id == "mac-address":
        hex_only = re.sub(r"[^0-9A-Fa-f]", "", normalized).lower()
        if len(hex_only) != 12:
            raise ScanConfigError("invalid mac-address value for exemption digest")
        return ":".join(hex_only[i : i + 2] for i in range(0, 12, 2))
    if rule_id == "phone":
        digits = "".join(ch for ch in normalized if ch.isdigit())
        prefix = "+" if normalized.startswith("+") else ""
        if not digits:
            raise ScanConfigError("invalid phone value for exemption digest")
        return prefix + digits
    if rule_id == "home-path":
        cleaned = normalized.replace("\\", "/")
        match = re.search(r"/(users|home)/([^/\s]+)", cleaned, re.IGNORECASE)
        if match:
            return f"/{match.group(1).casefold()}/{match.group(2).casefold()}"
        return cleaned.casefold()
    return WHITESPACE_RE.sub(" ", normalized).casefold()


def compute_exemption_digest(rule_id: str, value: str) -> str:
    """Return the unkeyed SHA-256 hex digest of a folded matched value."""
    folded = fold_value_for_rule(rule_id, value)
    return hashlib.sha256(folded.encode("utf-8")).hexdigest()


# --- deterministic detectors ------------------------------------------------
# Each detector accepts one normalized line and returns canonical folded
# matched values that are NOT on the documented exact allow list.


def detect_email(text: str) -> list[str]:
    if "@" not in text:
        return []
    hits: list[str] = []
    for match in EMAIL_RE.finditer(text):
        folded = match.group(1).casefold()
        _, _, domain = folded.partition("@")
        tld = domain.rsplit(".", 1)[-1]
        if tld in NON_EMAIL_TLDS:
            continue
        if domain in ALLOWED_EMAIL_DOMAINS:
            continue
        hits.append(folded)
    return hits


def detect_phone(text: str) -> list[str]:
    has_plus = "+" in text
    has_paren = "(" in text
    has_sep = "-" in text or "." in text or " " in text
    if not (has_plus or has_paren or has_sep):
        return []
    patterns: list[re.Pattern[str]] = []
    if has_plus and PHONE_E164_HINT_RE.search(text) is not None:
        if has_paren:
            patterns.append(PHONE_INTL_PAREN_RE)
        patterns.append(PHONE_E164_RE)
    if has_paren and PHONE_PAREN_HINT_RE.search(text) is not None:
        patterns.append(PHONE_PAREN_RE)
    if has_sep and DIGIT_RE.search(text) is not None:
        patterns.append(PHONE_SEP_RE)
    if not patterns:
        return []
    seen: set[str] = set()
    hits: list[str] = []
    for pattern in patterns:
        for match in pattern.finditer(text):
            raw = match.group(1)
            digits = "".join(ch for ch in raw if ch.isdigit())
            if raw.startswith("+"):
                if not (8 <= len(digits) <= 15):
                    continue
                folded = "+" + digits
            else:
                if len(digits) != 10:
                    continue
                if digits == "0" * 10:
                    continue
                folded = digits
            if folded in ALLOWED_PHONES:
                continue
            if folded not in seen:
                seen.add(folded)
                hits.append(folded)
    return hits


def detect_ipv4(text: str) -> list[str]:
    if "." not in text:
        return []
    hits: list[str] = []
    for match in DOTTED_DECIMAL_RE.finditer(text):
        octets = canonical_octets(match.group(1))
        if octets is None:
            continue
        canonical = ".".join(str(octet) for octet in octets)
        if len(octets) == 4:
            if canonical in ALLOWED_IPV4_EXACT:
                continue
            address = ipaddress.IPv4Address(canonical)
            if any(address in network for network in ALLOWED_IPV4_NETWORKS):
                continue
            hits.append(canonical)
            continue

        # Two- or three-group partial private prefixes.
        continued = bool(PREFIX_CONTINUATION_RE.match(text, match.end()))
        padded = octets + (0,) * (4 - len(octets))
        address = ipaddress.IPv4Address(".".join(str(octet) for octet in padded))
        for label, network in PRIVATE_IPV4_RANGES:
            if address not in network:
                continue
            if len(octets) == 3:
                flagged = continued or label != "rfc1918-a"
            else:
                flagged = continued or label in DISTINCT_TWO_GROUP_RANGES
            if flagged:
                hits.append(canonical)
            break
    return hits


def _validate_ipv6_candidate(raw: str) -> str | None:
    if "::" not in raw and raw.count(":") != 7:
        return None
    try:
        address = ipaddress.IPv6Address(raw)
    except ValueError:
        return None
    canonical = str(address)
    if canonical in ALLOWED_IPV6_EXACT:
        return None
    if any(address in network for network in ALLOWED_IPV6_NETWORKS):
        return None
    return canonical


def detect_ipv6(text: str) -> list[str]:
    if ":" not in text or IPV6_HINT_RE.search(text) is None:
        return []
    seen: set[str] = set()
    hits: list[str] = []
    for match in IPV6_RE.finditer(text):
        raw = match.group(1)
        # Skip single-group trailing-:: fragments that are pure letters (e.g. CSS).
        if raw.endswith("::") and raw.count(":") == 2 and raw[:-2].isalpha():
            continue
        canonical = _validate_ipv6_candidate(raw)
        if canonical is not None and canonical not in seen:
            seen.add(canonical)
            hits.append(canonical)
    for match in IPV6_GLUED_RE.finditer(text):
        canonical = _validate_ipv6_candidate(match.group(1))
        if canonical is not None and canonical not in seen:
            seen.add(canonical)
            hits.append(canonical)
    return hits


def detect_mac_address(text: str) -> list[str]:
    patterns: list[re.Pattern[str]] = []
    if ":" in text and MAC_COLON_HINT_RE.search(text) is not None:
        patterns.append(MAC_COLON_RE)
    if "-" in text and MAC_HYPHEN_HINT_RE.search(text) is not None:
        patterns.append(MAC_HYPHEN_RE)
    if "." in text and MAC_CISCO_HINT_RE.search(text) is not None:
        patterns.append(MAC_CISCO_RE)
    if not patterns:
        return []
    seen: set[str] = set()
    hits: list[str] = []
    for pattern in patterns:
        for match in pattern.finditer(text):
            hex_only = re.sub(r"[^0-9A-Fa-f]", "", match.group(1)).lower()
            folded = ":".join(hex_only[i : i + 2] for i in range(0, 12, 2))
            if folded in ALLOWED_MACS:
                continue
            if folded not in seen:
                seen.add(folded)
                hits.append(folded)
    return hits


def detect_home_path(text: str) -> list[str]:
    if "/" not in text and "\\" not in text:
        return []
    lower = text.casefold()
    if "users" not in lower and "/home/" not in lower:
        return []
    hits: list[str] = []
    for match in HOME_PATH_RE.finditer(text):
        if match.group(1) is not None:
            kind, user = match.group(1).casefold(), match.group(2).casefold()
        else:
            kind, user = match.group(3).casefold(), match.group(4).casefold()
        if user in ALLOWED_HOME_USERS:
            continue
        hits.append(f"/{kind}/{user}")
    return hits


def detect_private_dns(text: str) -> list[str]:
    if "." not in text:
        return []
    lower = text.casefold()
    if ".ts.net" not in lower and ".internal" not in lower and ".local" not in lower:
        return []
    hits: list[str] = []
    for match in PRIVATE_DNS_RE.finditer(text):
        folded = match.group(1).casefold()
        if folded in ALLOWED_PRIVATE_DNS:
            continue
        hits.append(folded)
    return hits


def detect_cloud_id(text: str) -> list[str]:
    lower = text.casefold()
    patterns: list[re.Pattern[str]] = []
    if "arn:aws" in lower:
        patterns.append(AWS_ARN_RE)
    if "amazonaws.com" in lower:
        patterns.append(AWS_ECR_RE)
    if "account" in lower:
        patterns.append(AWS_ACCOUNT_KV_RE)
        if "cloudflare" in lower:
            patterns.append(CF_ACCOUNT_RE)
        if "billingaccounts/" in lower:
            patterns.append(GCP_BILLING_RE)
    if "project" in lower:
        if "gcp" in lower or "google" in lower:
            patterns.append(GCP_PROJECT_KV_RE)
        if "googleapis.com" in lower:
            patterns.append(GCP_URL_PROJECT_RE)
    if "subscriptions/" in lower:
        patterns.append(AZURE_SUB_URL_RE)
    if "subscription" in lower or "tenant" in lower:
        patterns.append(AZURE_KV_RE)
    if not patterns:
        return []
    seen: set[str] = set()
    hits: list[str] = []
    for pattern in patterns:
        for match in pattern.finditer(text):
            folded = match.group(1).casefold()
            if folded in ALLOWED_CLOUD_IDS:
                continue
            if folded not in seen:
                seen.add(folded)
                hits.append(folded)
    return hits


def detect_chat_tracker_id(text: str) -> list[str]:
    patterns: list[re.Pattern[str]] = []
    if "<@" in text:
        patterns.append(SLACK_MENTION_RE)
        patterns.append(DISCORD_MENTION_RE)
    if "U0" in text or "W0" in text or "T0" in text:
        patterns.append(SLACK_BARE_RE)
    if ":" in text and ATLASSIAN_HINT_RE.search(text) is not None:
        patterns.append(ATLASSIAN_ACCOUNT_RE)
    lower = text.casefold()
    if "_id" in lower or "-id" in lower:
        if any(
            needle in lower
            for needle in (
                "slack",
                "discord",
                "jira",
                "atlassian",
                "linear",
                "telegram",
                "mattermost",
                "teams",
                "tracker",
                "chat",
            )
        ):
            patterns.append(CHAT_TRACKER_KV_RE)
    if not patterns:
        return []
    seen: set[str] = set()
    hits: list[str] = []
    for pattern in patterns:
        for match in pattern.finditer(text):
            folded = match.group(1).casefold()
            if folded in ALLOWED_CHAT_TRACKER_IDS:
                continue
            if folded not in seen:
                seen.add(folded)
                hits.append(folded)
    return hits


def detect_secret(text: str) -> list[str]:
    hits: list[str] = []
    for prefixes, pattern in SECRET_PATTERNS:
        if not any(prefix in text for prefix in prefixes):
            continue
        for match in pattern.finditer(text):
            folded = match.group(0).casefold()
            if folded in ALLOWED_SECRETS:
                continue
            hits.append(folded)
    return hits


DETECTORS: dict[str, Callable[[str], list[str]]] = {
    "email": detect_email,
    "phone": detect_phone,
    "ipv4": detect_ipv4,
    "ipv6": detect_ipv6,
    "mac-address": detect_mac_address,
    "home-path": detect_home_path,
    "private-dns": detect_private_dns,
    "cloud-id": detect_cloud_id,
    "chat-tracker-id": detect_chat_tracker_id,
    "secret": detect_secret,
}


# --- configuration loading (strict, fail closed) ----------------------------


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


def _valid_relative_path(value: object) -> bool:
    if not isinstance(value, str) or not value or "\0" in value:
        return False
    if "\\" in value:
        return False
    path = Path(value)
    return not path.is_absolute() and ".." not in path.parts


def load_modes(path: Path) -> dict[str, str]:
    """Load and strictly validate per-detector modes."""
    data = _read_config_json(path, "mode configuration file")
    if not isinstance(data, dict) or set(data.keys()) != {"schema_version", "modes"}:
        raise ScanConfigError("mode configuration file must contain only schema_version and modes")
    if type(data["schema_version"]) is not int or data["schema_version"] != 1:
        raise ScanConfigError("mode configuration schema_version must be integer 1")
    raw_modes = data["modes"]
    if not isinstance(raw_modes, dict):
        raise ScanConfigError("mode configuration modes must be a JSON object")
    if set(raw_modes.keys()) != set(DETECTOR_IDS):
        raise ScanConfigError("mode configuration must declare every detector and no unknown detectors")
    modes: dict[str, str] = {}
    for rule_id in DETECTOR_IDS:
        mode = raw_modes[rule_id]
        if not isinstance(mode, str) or mode not in VALID_MODES:
            raise ScanConfigError(f"invalid detector mode for rule '{rule_id}'")
        modes[rule_id] = mode
    return modes


def load_exemptions(
    path: Path,
    *,
    today: datetime.date | None = None,
) -> tuple[ExemptionRecord, ...]:
    """Load and strictly validate tracked digest-bound exemption records."""
    data = _read_config_json(path, "exemption file")
    if not isinstance(data, dict) or set(data.keys()) != {"schema_version", "exemptions"}:
        raise ScanConfigError("exemption file must contain only schema_version and exemptions")
    if type(data["schema_version"]) is not int or data["schema_version"] != 1:
        raise ScanConfigError("exemption file schema_version must be integer 1")
    raw_entries = data["exemptions"]
    if not isinstance(raw_entries, list):
        raise ScanConfigError("exemption file exemptions must be a list")

    current_date = datetime.date.today() if today is None else today
    required_keys = {"path", "rule", "digest", "reason", "approved_by", "expires"}
    records: list[ExemptionRecord] = []
    seen: set[tuple[str, str, str]] = set()
    for index, entry in enumerate(raw_entries):
        if not isinstance(entry, dict) or set(entry.keys()) != required_keys:
            raise ScanConfigError(
                f"exemption record #{index + 1} is incomplete or has unexpected fields"
            )
        rel_path = entry["path"]
        rule = entry["rule"]
        digest = entry["digest"]
        reason = entry["reason"]
        approved_by = entry["approved_by"]
        expires = entry["expires"]

        if not _valid_relative_path(rel_path):
            raise ScanConfigError(f"exemption record #{index + 1} has an invalid path")
        if not isinstance(rule, str) or rule not in DETECTOR_IDS:
            raise ScanConfigError(f"exemption record #{index + 1} has an unknown rule")
        if not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
            raise ScanConfigError(
                f"exemption record #{index + 1} digest must be lowercase 64-hex SHA-256"
            )
        if not isinstance(reason, str) or not reason.strip():
            raise ScanConfigError(f"exemption record #{index + 1} is missing a reason")
        if not isinstance(approved_by, str) or not approved_by.strip():
            raise ScanConfigError(f"exemption record #{index + 1} is missing approved_by")
        if not isinstance(expires, str) or not ISO_DATE_RE.fullmatch(expires):
            raise ScanConfigError(
                f"exemption record #{index + 1} has a missing or malformed expires date (expected YYYY-MM-DD)"
            )
        try:
            expires_date = datetime.date.fromisoformat(expires)
        except ValueError as exc:
            raise ScanConfigError(
                f"exemption record #{index + 1} has an invalid calendar expires date"
            ) from exc
        if expires_date < current_date:
            raise ScanConfigError(
                f"exemption record #{index + 1} for {escape_for_display(rel_path)} "
                f"rule '{rule}' digest {digest[:12]} expired on {expires}"
            )
        record = ExemptionRecord(
            path=rel_path,
            rule=rule,
            digest=digest,
            reason=reason.strip(),
            approved_by=approved_by.strip(),
            expires=expires,
        )
        if record.key in seen:
            raise ScanConfigError(f"exemption record #{index + 1} is a duplicate")
        seen.add(record.key)
        records.append(record)
    return tuple(records)


def load_binary_exceptions(path: Path) -> dict[str, BinaryException]:
    """Load and strictly validate hash-bound binary exceptions."""
    data = _read_config_json(path, "binary exception file")
    if not isinstance(data, dict) or set(data.keys()) != {"schema_version", "binary"}:
        raise ScanConfigError("binary exception file must contain only schema_version and binary")
    if type(data["schema_version"]) is not int or data["schema_version"] != 1:
        raise ScanConfigError("binary exception file schema_version must be integer 1")
    raw_entries = data["binary"]
    if not isinstance(raw_entries, list):
        raise ScanConfigError("binary exception file binary must be a list")

    exceptions: dict[str, BinaryException] = {}
    for index, entry in enumerate(raw_entries):
        if not isinstance(entry, dict) or set(entry.keys()) != {"path", "sha256", "reason"}:
            raise ScanConfigError(
                f"binary exception #{index + 1} must contain only path, sha256, and reason"
            )
        rel_path = entry["path"]
        digest = entry["sha256"]
        reason = entry["reason"]
        if not _valid_relative_path(rel_path) or rel_path in exceptions:
            raise ScanConfigError(
                f"binary exception #{index + 1} has an invalid or duplicate path"
            )
        if not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
            raise ScanConfigError(
                f"binary exception #{index + 1} sha256 must be lowercase 64-hex"
            )
        if not isinstance(reason, str) or not reason.strip():
            raise ScanConfigError(f"binary exception #{index + 1} needs a reason")
        exceptions[rel_path] = BinaryException(path=rel_path, sha256=digest, reason=reason.strip())
    return exceptions


# --- sources (worktree and git tree objects) --------------------------------


def _split_nul_paths(raw: bytes) -> list[str]:
    """Decode NUL-separated Git paths; any non-UTF-8 path fails the listing."""
    if len(raw) > MAX_LISTING_BYTES:
        raise ScanConfigError(
            f"tracked file listing is larger than {MAX_LISTING_BYTES} bytes; refusing to scan"
        )
    try:
        paths = [item.decode("utf-8") for item in raw.split(b"\0") if item]
    except UnicodeDecodeError as exc:
        raise ScanConfigError("tracked file listing contains a path that is not valid UTF-8") from exc
    if len(paths) > MAX_TREE_ENTRIES:
        raise ScanConfigError(
            f"tracked file listing has more than {MAX_TREE_ENTRIES} entries; refusing to scan"
        )
    return paths


def tracked_files(root: Path) -> tuple[list[str] | None, str | None]:
    """Return tracked paths, or a concise error when listing fails or is empty."""
    try:
        result = subprocess.run(
            ["git", "--no-optional-locks", "ls-files", "-z"],
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
    if len(tracked) > MAX_TREE_ENTRIES:
        raise ScanConfigError(
            f"worktree has more than {MAX_TREE_ENTRIES} entries; refusing to scan"
        )
    root_resolved = root.resolve()
    total_bytes = 0
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
            total_bytes += size
            if total_bytes > MAX_TREE_BYTES:
                raise ScanConfigError(
                    f"worktree holds more than {MAX_TREE_BYTES} bytes; refusing to scan"
                )
            content = full_path.read_bytes()
        except OSError:
            yield SourceFile(relative, None, problem="tracked file is unreadable")
            continue
        yield SourceFile(relative, content, object_id=hashlib.blake2b(content, digest_size=16).hexdigest())


def _git(root: Path, args: Sequence[str], *, stdin: bytes | None = None) -> bytes:
    try:
        result = subprocess.run(
            ["git", "--no-optional-locks", *args],
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
            ["git", "--no-optional-locks", "ls-tree", "-r", "-z", "--full-tree", revision],
            cwd=root,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
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
                raise ScanConfigError(
                    f"tree listing is larger than {MAX_LISTING_BYTES} bytes; refusing to scan"
                )
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
            raise ScanConfigError(
                "tracked file listing contains a path that is not valid UTF-8"
            ) from exc
        entries.append((mode, kind, sha, path))
        if len(entries) > MAX_TREE_ENTRIES:
            raise ScanConfigError(
                f"tree has more than {MAX_TREE_ENTRIES} entries; refusing to scan"
            )
    if not entries:
        raise ScanConfigError("tracked file listing is empty")

    unique = sorted({sha for _, kind, sha, _ in entries if kind == "blob"})
    sizes: dict[str, int] = {}
    if unique:
        listing = _git(
            root,
            ["cat-file", "--batch-check"],
            stdin="".join(f"{sha}\n" for sha in unique).encode("ascii"),
        )
        for line in listing.splitlines():
            parts = line.split(b" ")
            if len(parts) != 3 or parts[1] != b"blob" or not parts[2].isdigit():
                raise ScanConfigError("Git returned a malformed object listing")
            sizes[parts[0].decode("ascii")] = int(parts[2])
        if set(sizes) != set(unique):
            raise ScanConfigError("Git returned a malformed object listing")
    readable = sum(size for size in sizes.values() if size <= MAX_SCAN_FILE_BYTES)
    if readable > MAX_TREE_BYTES:
        raise ScanConfigError(
            f"tree holds more than {MAX_TREE_BYTES} bytes of distinct content; refusing to scan"
        )

    paths = [path for _, _, _, path in entries]
    return paths, _stream_tree(root, entries, sizes)


def _stream_tree(
    root: Path,
    entries: list[tuple[str, str, str, str]],
    sizes: Mapping[str, int],
) -> Iterator[SourceFile]:
    """Yield entries grouped by object, reading one blob at a time."""
    by_object: dict[str, list[tuple[str, str]]] = {}
    for mode, kind, sha, path in entries:
        if kind != "blob":
            yield SourceFile(
                path,
                None,
                problem="tracked entry is not a file (submodule or other object)",
            )
            continue
        if sizes[sha] > MAX_SCAN_FILE_BYTES:
            yield SourceFile(path, None, problem="tracked file is too large to scan")
            continue
        by_object.setdefault(sha, []).append((mode, path))
    if not by_object:
        return
    try:
        process = subprocess.Popen(
            ["git", "--no-optional-locks", "cat-file", "--batch"],
            cwd=root,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
        )
    except OSError as exc:
        raise ScanConfigError("Git could not be executed") from exc
    assert process.stdin is not None and process.stdout is not None
    try:
        for sha, users in by_object.items():
            process.stdin.write(f"{sha}\n".encode("ascii"))
            process.stdin.flush()
            header = process.stdout.readline().rstrip(b"\n").split(b" ")
            if (
                len(header) != 3
                or header[0].decode("ascii", "replace") != sha
                or header[1] != b"blob"
                or int(header[2]) != sizes[sha]
            ):
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


# --- decoding and display safety --------------------------------------------


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

    UTF-8 (with or without BOM) and UTF-16 (with BOM or NUL-interleaved ASCII)
    are decoded. Any remaining NUL byte or decode error is refused as binary.
    """
    try:
        if data.startswith((codecs.BOM_UTF16_LE, codecs.BOM_UTF16_BE)):
            text = data.decode("utf-16")
        else:
            body = data[len(codecs.BOM_UTF8) :] if data.startswith(codecs.BOM_UTF8) else data
            codec = _looks_utf16(body)
            text = body.decode(codec) if codec else body.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ScanConfigError("tracked file is not valid UTF-8 or UTF-16 text") from exc
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


def redacted_path_id(path: str) -> str:
    """Return an opaque id for a path that triggered a detector."""
    digest = hashlib.sha256(path.encode("utf-8")).hexdigest()[:12]
    return f"<redacted-{digest}>"


# --- scanner engine ---------------------------------------------------------


class Scanner:
    """Scan sources with deterministic detectors, modes, and digest exemptions."""

    def __init__(
        self,
        *,
        modes: Mapping[str, str] | None = None,
        exemptions: Sequence[ExemptionRecord] = (),
        binary_exceptions: Mapping[str, BinaryException] | None = None,
        detectors: Mapping[str, Callable[[str], list[str]]] | None = None,
    ) -> None:
        self.modes: dict[str, str] = dict(DEFAULT_MODES if modes is None else modes)
        if set(self.modes.keys()) != set(DETECTOR_IDS):
            raise ScanConfigError("detector modes must cover every detector")
        for rule_id, mode in self.modes.items():
            if mode not in VALID_MODES:
                raise ScanConfigError(f"invalid detector mode for rule '{rule_id}'")

        self.detectors: Mapping[str, Callable[[str], list[str]]] = (
            DETECTORS if detectors is None else detectors
        )
        self.exemptions: tuple[ExemptionRecord, ...] = tuple(exemptions)
        self.exemption_index: dict[tuple[str, str], set[str]] = {}
        for record in self.exemptions:
            self.exemption_index.setdefault((record.path, record.rule), set()).add(record.digest)
        self.applied_exemptions: set[tuple[str, str, str]] = set()
        self.binary_exceptions: Mapping[str, BinaryException] = (
            {} if binary_exceptions is None else binary_exceptions
        )
        self.active_detectors: tuple[tuple[str, str, Callable[[str], list[str]]], ...] = self._resolve_active()
        # Cache raw content hits per git object_id: (error, hits, overflow)
        # where hits is list of (line_no, rule_id, folded_value).
        self.object_findings: dict[str, tuple[str | None, list[tuple[int, str, str]], bool]] = {}

    def _resolve_active(self) -> tuple[tuple[str, str, Callable[[str], list[str]]], ...]:
        active: list[tuple[str, str, Callable[[str], list[str]]]] = []
        for rule_id in DETECTOR_IDS:
            mode = self.modes[rule_id]
            if mode == "off":
                continue
            fn = self.detectors.get(rule_id)
            if fn is None or not callable(fn):
                raise ScanConfigError(f"detector '{rule_id}' is missing or skipped; failing closed")
            active.append((rule_id, mode, fn))
        return tuple(active)

    def _text_variants(self, text: str) -> tuple[str, ...]:
        """Return normalized full-text variants with line count preserved.

        Only lines containing non-ASCII characters or escape markers are
        normalised, so large plain-ASCII files with a few punctuation marks
        stay fast.
        """
        if text.isascii() and "\\u" not in text and "\\/" not in text and "%" not in text:
            return (text,)
        lines = text.split("\n")
        primary_lines: list[str] = []
        sep_lines: list[str] = []
        no_url_lines: list[str] = []
        has_sep_diff = False
        has_url_diff = False
        for line in lines:
            if line.isascii() and "\\u" not in line and "\\/" not in line and "%" not in line:
                primary_lines.append(line)
                sep_lines.append(line)
                no_url_lines.append(line)
                continue
            p = normalize_text(line, separate=False, decode_url=True)
            primary_lines.append(p)
            if not line.isascii() or "\\u" in line or "%" in line:
                s = normalize_text(line, separate=True, decode_url=True)
                sep_lines.append(s)
                if s != p:
                    has_sep_diff = True
            else:
                sep_lines.append(p)
            if "%" in line:
                n = normalize_text(line, separate=False, decode_url=False)
                no_url_lines.append(n)
                if n != p:
                    has_url_diff = True
            else:
                no_url_lines.append(p)
        primary = "\n".join(primary_lines)
        variants = [primary]
        if has_sep_diff:
            variants.append("\n".join(sep_lines))
        if has_url_diff:
            no_url = "\n".join(no_url_lines)
            if no_url not in variants:
                variants.append(no_url)
        return tuple(variants)

    def _run_detector_checked(
        self,
        rule_id: str,
        detector_fn: Callable[[str], list[str]],
        variants: tuple[str, ...],
    ) -> tuple[list[str], str | None]:
        hits: list[str] = []
        try:
            for variant in variants:
                result = detector_fn(variant)
                if not isinstance(result, list):
                    raise TypeError("detector did not return a list")
                hits.extend(result)
        except Exception as exc:  # noqa: BLE001 - fail closed without leaking input
            return [], f"detector '{rule_id}' failed ({type(exc).__name__}); failing closed"
        return hits, None

    def _run_detectors_on_line(self, line: str) -> tuple[list[tuple[str, str]], list[str]]:
        """Run every enabled detector on one line, returning (hits, errors)."""
        variants = _line_variants(line)
        hits: list[tuple[str, str]] = []
        errors: list[str] = []
        executed: set[str] = set()
        for rule_id, _mode, detector_fn in self.active_detectors:
            det_hits, err = self._run_detector_checked(rule_id, detector_fn, variants)
            if err is not None:
                errors.append(err)
                continue
            executed.add(rule_id)
            seen_values: set[str] = set()
            for value in det_hits:
                if value not in seen_values:
                    seen_values.add(value)
                    hits.append((rule_id, value))
        expected = {rule_id for rule_id, _mode, _fn in self.active_detectors}
        for rule_id in sorted(expected - executed):
            if not any(err.startswith(f"detector '{rule_id}' failed") for err in errors):
                errors.append(f"detector '{rule_id}' was skipped; failing closed")
        return hits, errors

    def _is_exempt(self, relative: str, rule_id: str, folded_value: str) -> bool:
        digests = self.exemption_index.get((relative, rule_id))
        if not digests:
            return False
        value_digest = hashlib.sha256(folded_value.encode("utf-8")).hexdigest()
        if value_digest in digests:
            self.applied_exemptions.add((relative, rule_id, value_digest))
            return True
        return False

    def _scan_text_raw(self, text: str) -> tuple[str | None, list[tuple[int, str, str]], bool]:
        """Collect raw (line_no, rule_id, folded_value) hits for text.

        Runs each active detector once over the full file first; only detectors
        that report hits resolve exact line numbers line-by-line.
        """
        unified = text.replace("\r\n", "\n").replace("\r", "\n")
        if not unified:
            return None, [], False
        file_variants = self._text_variants(unified)
        triggered: list[tuple[str, Callable[[str], list[str]]]] = []
        executed: set[str] = set()
        for rule_id, _mode, detector_fn in self.active_detectors:
            det_hits, err = self._run_detector_checked(rule_id, detector_fn, file_variants)
            if err is not None:
                return err, [], False
            executed.add(rule_id)
            if det_hits:
                if len(det_hits) > MAX_FINDINGS:
                    return None, [], True
                triggered.append((rule_id, detector_fn))
        expected = {rule_id for rule_id, _mode, _fn in self.active_detectors}
        missing = sorted(expected - executed)
        if missing:
            return f"detector '{missing[0]}' was skipped; failing closed", [], False
        if not triggered:
            return None, [], False

        raw_hits: list[tuple[int, str, str]] = []
        lines = unified.split("\n")
        for number, line in enumerate(lines, start=1):
            if not line:
                continue
            line_variants = _line_variants(line)
            for rule_id, detector_fn in triggered:
                det_hits, err = self._run_detector_checked(rule_id, detector_fn, line_variants)
                if err is not None:
                    return err, [], False
                seen_values: set[str] = set()
                for value in det_hits:
                    if value not in seen_values:
                        seen_values.add(value)
                        raw_hits.append((number, rule_id, value))
                        if len(raw_hits) > MAX_FINDINGS:
                            return None, raw_hits[:MAX_FINDINGS], True
        return None, raw_hits, False

    def scan_source(self, source: SourceFile) -> tuple[list[str], list[str]]:
        """Scan one source file, returning (failures, warnings) with redacted output."""
        failures: list[str] = []
        warnings: list[str] = []

        # Scan the path itself first so sensitive values cannot hide in filenames.
        path_hits, path_errors = self._run_detectors_on_line(source.path)
        unexempt_path_hits: list[tuple[str, str]] = []
        for rule_id, value in path_hits:
            if not self._is_exempt(source.path, rule_id, value):
                unexempt_path_hits.append((rule_id, value))

        shown = redacted_path_id(source.path) if path_hits else escape_for_display(source.path)

        for err in path_errors:
            failures.append(f"{shown}:0: {err}")
        for rule_id in sorted({r for r, _ in unexempt_path_hits}):
            msg = f"{shown}:0: matched rule '{rule_id}'"
            if self.modes[rule_id] == "warn":
                warnings.append(msg)
            else:
                failures.append(msg)

        if source.problem is not None:
            failures.append(f"{source.problem}: {shown}")
            return failures, warnings
        assert source.content is not None

        expected_binary = self.binary_exceptions.get(source.path)
        if expected_binary is not None:
            actual_sha = hashlib.sha256(source.content).hexdigest()
            if actual_sha != expected_binary.sha256:
                failures.append(
                    f"binary exception content changed; review and update its sha256: {shown}"
                )
            return failures, warnings

        cached = self.object_findings.get(source.object_id) if source.object_id else None
        if cached is None:
            try:
                text = decode_content(source.content)
            except ScanConfigError as exc:
                cached = (str(exc), [], False)
            else:
                cached = self._scan_text_raw(text)
            if source.object_id:
                self.object_findings[source.object_id] = cached

        error, raw_hits, overflow = cached
        if error is not None:
            failures.append(f"{error}: {shown}")
            return failures, warnings
        if overflow:
            failures.append(
                f"more than {MAX_FINDINGS} matches; the scan cannot check the rest: {shown}"
            )

        # Deduplicate per (line, rule_id) after checking exemptions so one line
        # does not spam duplicate messages while every exempt digest is tracked.
        emitted_block: set[tuple[int, str]] = set()
        emitted_warn: set[tuple[int, str]] = set()
        for line_no, rule_id, value in raw_hits:
            if self._is_exempt(source.path, rule_id, value):
                continue
            key = (line_no, rule_id)
            if self.modes[rule_id] == "warn":
                if key not in emitted_warn:
                    emitted_warn.add(key)
                    warnings.append(f"{shown}:{line_no}: matched rule '{rule_id}'")
            else:
                if key not in emitted_block:
                    emitted_block.add(key)
                    failures.append(f"{shown}:{line_no}: matched rule '{rule_id}'")
        return failures, warnings


def scan_sources(
    sources: Iterable[SourceFile],
    *,
    modes: Mapping[str, str] | None = None,
    exemptions: Sequence[ExemptionRecord] = (),
    binary_exceptions: Mapping[str, BinaryException] | None = None,
    detectors: Mapping[str, Callable[[str], list[str]]] | None = None,
) -> ScanResult:
    """Scan sources; an empty source list or exceeded finding cap fails closed."""
    scanner = Scanner(
        modes=modes,
        exemptions=exemptions,
        binary_exceptions=binary_exceptions,
        detectors=detectors,
    )
    failures: list[str] = []
    warnings: list[str] = []
    count = 0
    for source in sources:
        count += 1
        src_failures, src_warnings = scanner.scan_source(source)
        failures.extend(src_failures)
        warnings.extend(src_warnings)
        if len(failures) > MAX_FINDINGS:
            del failures[MAX_FINDINGS:]
            failures.append(
                f"more than {MAX_FINDINGS} findings; the scan stopped after {count} entries"
            )
            if hasattr(sources, "close"):
                sources.close()
            return ScanResult(
                failures=failures,
                warnings=warnings,
                applied_exemptions=frozenset(scanner.applied_exemptions),
            )
    if count == 0:
        return ScanResult(
            failures=["tracked file listing is missing or empty"],
            warnings=warnings,
            applied_exemptions=frozenset(scanner.applied_exemptions),
        )
    return ScanResult(
        failures=failures,
        warnings=warnings,
        applied_exemptions=frozenset(scanner.applied_exemptions),
    )


def scan_tracked_files(
    root: Path,
    tracked: Sequence[str] | None,
    *,
    modes: Mapping[str, str] | None = None,
    exemptions: Sequence[ExemptionRecord] = (),
    binary_exceptions: Mapping[str, BinaryException] | None = None,
    detectors: Mapping[str, Callable[[str], list[str]]] | None = None,
) -> ScanResult:
    """Scan every tracked worktree file; fail closed on gaps."""
    if not tracked:
        return ScanResult(
            failures=["tracked file listing is missing or empty"],
            warnings=[],
            applied_exemptions=frozenset(),
        )
    return scan_sources(
        worktree_sources(root, tracked),
        modes=modes,
        exemptions=exemptions,
        binary_exceptions=binary_exceptions,
        detectors=detectors,
    )


# --- repository metadata checks ---------------------------------------------


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
        if any(fragment not in dependabot_text for fragment in DEPENDABOT_COMMIT_POLICY):
            failures.append("Dependabot must emit conventional build(scope) commit subjects")

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


# --- reporting and CLI ------------------------------------------------------


def _print_non_default_modes(modes: Mapping[str, str] | None) -> None:
    if not modes:
        return
    non_default = [(rule_id, modes[rule_id]) for rule_id in DETECTOR_IDS if modes.get(rule_id) != "block"]
    if non_default:
        print("Non-default detector modes:")
        for rule_id, mode in non_default:
            print(f"- {rule_id}: {mode}")


def _print_exemptions(
    exemptions: Sequence[ExemptionRecord],
    applied: frozenset[tuple[str, str, str]],
) -> None:
    if not exemptions:
        print("Configured exemptions: none")
        return
    print("Configured exemptions:")
    for record in exemptions:
        status = "applied" if record.key in applied else "unused"
        shown = escape_for_display(record.path)
        print(
            f"- [{status}] {shown} rule={record.rule} digest={record.digest[:12]} expires={record.expires}"
        )


def _report(
    *,
    failures: list[str],
    warnings: list[str],
    modes: Mapping[str, str] | None,
    exemptions: Sequence[ExemptionRecord],
    applied_exemptions: frozenset[tuple[str, str, str]],
    passed_message: str,
) -> int:
    _print_non_default_modes(modes)
    _print_exemptions(exemptions, applied_exemptions)
    if warnings:
        print("Public-safety scan warnings:")
        for warning in warnings:
            print(f"- {warning}")
    if failures:
        print("Public release checks failed:")
        for failure in failures:
            print(f"- {failure}")
        return 1
    print(passed_message)
    return 0


def _run_exemption_digest(rule_id: str) -> int:
    if rule_id not in DETECTOR_IDS:
        print("Unsupported rule id; expected one of: " + ", ".join(DETECTOR_IDS))
        return 1
    digests: list[str] = []
    for raw_line in sys.stdin:
        stripped = raw_line.strip()
        if not stripped:
            continue
        try:
            digests.append(compute_exemption_digest(rule_id, stripped))
        except ScanConfigError as exc:
            print(f"Cannot compute exemption digest: {exc}")
            return 1
    if not digests:
        print("Cannot compute exemption digest: no non-empty values provided on stdin")
        return 1
    for digest in digests:
        print(digest)
    return 0


def _run(args: argparse.Namespace) -> int:
    if args.exemption_digest is not None:
        return _run_exemption_digest(args.exemption_digest)

    root = args.root.resolve()
    modes_path = args.modes_file if args.modes_file is not None else root / DEFAULT_MODES_PATH
    exemptions_path = (
        args.exemptions_file if args.exemptions_file is not None else root / DEFAULT_EXEMPTIONS_PATH
    )
    binary_path = (
        args.binary_exceptions_file
        if args.binary_exceptions_file is not None
        else root / DEFAULT_BINARY_EXCEPTIONS_PATH
    )

    failures: list[str] = []
    warnings: list[str] = []
    modes: dict[str, str] | None = None
    exemptions: tuple[ExemptionRecord, ...] = ()
    binary_exceptions: dict[str, BinaryException] = {}
    applied_exemptions: frozenset[tuple[str, str, str]] = frozenset()

    try:
        modes = load_modes(modes_path)
    except ScanConfigError as exc:
        failures.append(str(exc))

    try:
        exemptions = load_exemptions(exemptions_path)
    except ScanConfigError as exc:
        failures.append(str(exc))

    try:
        binary_exceptions = load_binary_exceptions(binary_path)
    except ScanConfigError as exc:
        failures.append(str(exc))

    if args.tree is not None:
        # Scan a commit's tree as git objects using the trusted checkout's
        # scanner, detectors, modes, exemptions, and binary exceptions.
        tree_paths: list[str] | None = None
        sources: Iterable[SourceFile] = []
        try:
            tree_paths, sources = tree_sources(root, args.tree)
        except ScanConfigError as exc:
            failures.append(f"tree listing failed: {exc}")
        if modes is not None and tree_paths:
            try:
                result = scan_sources(
                    sources,
                    modes=modes,
                    exemptions=exemptions,
                    binary_exceptions=binary_exceptions,
                )
            except ScanConfigError as exc:
                failures.append(f"tree read failed: {exc}")
            else:
                failures.extend(result.failures)
                warnings.extend(result.warnings)
                applied_exemptions = result.applied_exemptions
        return _report(
            failures=failures,
            warnings=warnings,
            modes=modes,
            exemptions=exemptions,
            applied_exemptions=applied_exemptions,
            passed_message="Deterministic public-safety scan of the tree passed.",
        )

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

    if modes is not None and tracked:
        try:
            result = scan_tracked_files(
                root,
                tracked,
                modes=modes,
                exemptions=exemptions,
                binary_exceptions=binary_exceptions,
            )
        except ScanConfigError as exc:
            failures.append(f"worktree scan failed: {exc}")
        else:
            failures.extend(result.failures)
            warnings.extend(result.warnings)
            applied_exemptions = result.applied_exemptions

    qualifier = "draft " if args.allow_missing_license else ""
    passed_message = (
        f"Deterministic public-release {qualifier}checks passed; "
        "complete docs/13-public-release-checklist.md before publication."
    )
    return _report(
        failures=failures,
        warnings=warnings,
        modes=modes,
        exemptions=exemptions,
        applied_exemptions=applied_exemptions,
        passed_message=passed_message,
    )


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--allow-missing-license",
        action="store_true",
        help="validate a private draft while retaining fail-closed file-listing and privacy-scan gates",
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
        "--modes-file",
        type=Path,
        default=None,
        help="override detector mode configuration file path",
    )
    parser.add_argument(
        "--exemptions-file",
        type=Path,
        default=None,
        help="override tracked exemption records file path",
    )
    parser.add_argument(
        "--binary-exceptions-file",
        type=Path,
        default=None,
        help="override hash-bound binary exceptions file path",
    )
    parser.add_argument(
        "--exemption-digest",
        default=None,
        metavar="RULE_ID",
        help="compute unkeyed SHA-256 exemption digests for one value per line from stdin",
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
