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
MAX_RANGE_COMMITS = 5_000
MAX_DECODE_PASSES = 4
CACHE_LINE_CHARS = 2048
CACHE_LINES = 4096
SCAN_VERIFIED_MARKER = "PUBLIC_SCAN_VERIFIED=1"

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
    "15555550100",
    "15555550101",
    "15555550199",
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
    r"(?![A-Za-z0-9-])(?!\.[A-Za-z0-9-])"
)

PHONE_E164_RE = re.compile(
    r"(?<![A-Za-z0-9+])(\+[1-9](?:[ \t.-]?\d){7,14})(?![A-Za-z0-9])"
)
PHONE_INTL_PAREN_RE = re.compile(
    r"(?<![A-Za-z0-9+])(\+[1-9]\d{0,2}[ \t.-]?\(\d{2,4}\)[ \t.-]?\d{3,4}[ \t.-]\d{3,4})(?![A-Za-z0-9])"
)
PHONE_PAREN_RE = re.compile(
    r"(?<![A-Za-z0-9])((?:1[ \t.-]?)?\(\d{3}\)[ \t.-]?\d{3}[ \t.-]\d{4})(?![A-Za-z0-9])"
)
PHONE_SEP_RE = re.compile(
    r"(?<![A-Za-z0-9.-])((?:1[-. \t])?\d{3}([-. \t])\d{3}\2\d{4})(?![A-Za-z0-9.-])"
)
# Linear-time, token-boundary-anchored phone key matcher (no unanchored leading
# wildcard, avoiding ReDoS and avoiding false positives like hotel_id/intel_serial).
PHONE_KEY_RE = re.compile(
    r"(?:"
    r"(?<![A-Za-z])(?:[cC][eE][lL][lL][pP][hH][oO][nN][eE]|[sS][mM][aA][rR][tT][pP][hH][oO][nN][eE]|[tT][eE][lL][eE][pP][hH][oO][nN][eE]|[pP][hH][oO][nN][eE]|[mM][oO][bB][iI][lL][eE]|[tT][eE][lL]|[cC][eE][lL][lL]|[fF][aA][xX]|[sS][mM][sS]|[wW][hH][aA][tT][sS][aA][pP][pP])[sS]?(?![a-z])"
    r"|(?<=[a-z])(?:Phone|Mobile|Tel|Cell|Fax|Sms|Whatsapp)[sS]?(?![a-z])"
    r"|(?<![A-Za-z0-9_])(?:[cC][oO][nN][tT][aA][cC][tT]|[cC][aA][lL][lL])[sS]?(?![A-Za-z0-9_])"
    r")"
    r"(?:[ \t._-]{1,2}[A-Za-z0-9]{1,20}){0,2}"
    r"[\"']?[ \t]*(?:=>|[:=]|[ \t]+)[ \t]*"
)
PHONE_VALUE_AT_RE = re.compile(
    r"[\"']?(\+?1?\d{10}|\+[1-9]\d{7,14}|(?:1[ \t.-]?)?\(?\d{3}\)?[ \t.-]?\d{3}[ \t.-]\d{4})(?![A-Za-z0-9])"
)
PHONE_LIST_ITEM_RE = re.compile(
    r"(?<![A-Za-z0-9+])(\+?1?\d{10}|\+[1-9]\d{7,14}|(?:1[ \t.-]?)?\(?\d{3}\)?[ \t.-]?\d{3}[ \t.-]\d{4})(?![A-Za-z0-9])"
)
PHONE_TEL_URI_RE = re.compile(
    r"(?<![A-Za-z0-9])tel:(\+?1?\d{10}|\+[1-9]\d{7,14})(?![A-Za-z0-9])",
    re.IGNORECASE,
)
PHONE_CONTEXT_KEYWORDS = (
    "phone",
    "tel",
    "mobile",
    "cell",
    "fax",
    "sms",
    "whatsapp",
    "call",
    "contact",
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
    r"|[A-Za-z]:[\\/](Users)[\\/]([A-Za-z0-9._-]{2,64})(?=[\\/]|$|[\s\"'`<>()\[\]{},;:]))",
    re.IGNORECASE,
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
    r"\b(?:aws[_-]?account(?:[_-]?id)?|account[_-]?id)[\"']?[ \t]*[:=][ \t]*[\"']?(\d{12})[\"']?(?!\d)",
    re.IGNORECASE,
)
GCP_PROJECT_KV_RE = re.compile(
    r"\b(?:gcp[_-]?project(?:[_-]?id)?|google[_-]?cloud[_-]?project)[\"']?[ \t]*[:=][ \t]*[\"']?([a-z][a-z0-9-]{4,28}[a-z0-9])[\"']?",
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
    r"\b(?:azure[_-]?(?:subscription|tenant)[_-]?id|subscription[_-]?id|tenant[_-]?id)[\"']?[ \t]*[:=][ \t]*[\"']?([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})[\"']?",
    re.IGNORECASE,
)
CF_ACCOUNT_RE = re.compile(
    r"\bcloudflare[_-]?account[_-]?id[\"']?[ \t]*[:=][ \t]*[\"']?([0-9a-f]{32})[\"']?",
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
    r"[_-]?(?:account|user|member|workspace|team)(?:[_-]?id)?"
    r"[\"']?[ \t]*[:=][ \t]*[\"']?([A-Za-z0-9:._-]{5,64})[\"']?",
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
class ExemptionInventoryEntry:
    path: str
    rule: str
    digest_prefix: str
    expires: str
    status: str  # "valid" | "expired" | "invalid"
    record: ExemptionRecord | None = None


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


def _decode_escapes_once(text: str, *, decode_url: bool) -> str:
    decoded = _decode_json_escapes(text)
    if decode_url:
        decoded = _decode_url_escapes(decoded)
    return decoded


def _fold_unicode(text: str, *, separate: bool) -> str:
    if text.isascii():
        return text
    folded = unicodedata.normalize("NFKD", text)
    if separate:
        folded = "".join(
            " " if unicodedata.category(ch) in ("Cf", "Mn") else ch
            for ch in folded
        )
    else:
        folded = "".join(
            ch for ch in folded if unicodedata.category(ch) not in ("Cf", "Mn")
        )
    return unicodedata.normalize("NFKC", folded).translate(CONFUSABLES)


def _decode_stages(text: str, *, decode_url: bool = True) -> tuple[str, ...]:
    """Return original plus every intermediate and final bounded escape-decoded stage."""
    if "\\u" not in text and "\\/" not in text and (not decode_url or "%" not in text):
        return (text,)
    stages: list[str] = [text]
    current = text
    for _ in range(MAX_DECODE_PASSES):
        step_json = _decode_json_escapes(current)
        if step_json != current and step_json not in stages:
            stages.append(step_json)
        step_next = _decode_url_escapes(step_json) if decode_url else step_json
        if step_next != step_json and step_next not in stages:
            stages.append(step_next)
        if step_next == current:
            break
        current = step_next
    else:
        if _decode_escapes_once(current, decode_url=decode_url) != current:
            raise ScanConfigError("nested escape encoding exceeds maximum decode depth")
    return tuple(stages)


def _normalize_text(text: str, separate: bool, decode_url: bool) -> str:
    if (
        text.isascii()
        and "\\u" not in text
        and "\\/" not in text
        and (not decode_url or "%" not in text)
    ):
        return text
    stages = _decode_stages(text, decode_url=decode_url)
    return _fold_unicode(stages[-1], separate=separate)


def _line_variants(line: str) -> tuple[str, ...]:
    """Return folded variants for original, every intermediate decode stage, and final form."""
    if line.isascii() and "\\u" not in line and "\\/" not in line and "%" not in line:
        return (line,)
    variants: list[str] = []
    seen: set[str] = set()

    def add(candidate: str) -> None:
        if candidate not in seen:
            seen.add(candidate)
            variants.append(candidate)

    for stage in _decode_stages(line, decode_url=True):
        add(_fold_unicode(stage, separate=False))
        if not stage.isascii():
            add(_fold_unicode(stage, separate=True))
    if "%" in line:
        for stage in _decode_stages(line, decode_url=False):
            add(_fold_unicode(stage, separate=False))
            if not stage.isascii():
                add(_fold_unicode(stage, separate=True))
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


def _normalize_phone_candidate(raw: str) -> str | None:
    digits = "".join(ch for ch in raw if ch.isdigit())
    if raw.startswith("+"):
        if not (8 <= len(digits) <= 15):
            return None
        folded = "+" + digits
    else:
        if len(digits) not in (10, 11):
            return None
        if len(digits) == 11 and not digits.startswith("1"):
            return None
        if digits == "0" * len(digits):
            return None
        folded = digits
    if folded in ALLOWED_PHONES:
        return None
    return folded


def detect_phone(text: str) -> list[str]:
    has_plus = "+" in text
    has_paren = "(" in text
    has_sep = "-" in text or "." in text or " " in text or "\t" in text
    lower = text.casefold()
    has_ctx = ":" in text or "=" in text or " " in text or "\t" in text
    has_phone_kw = has_ctx and any(kw in lower for kw in PHONE_CONTEXT_KEYWORDS)
    if not (has_plus or has_paren or has_sep or has_phone_kw):
        return []
    if DIGIT_RE.search(text) is None:
        return []

    patterns: list[re.Pattern[str]] = []
    if has_plus and PHONE_E164_HINT_RE.search(text) is not None:
        if has_paren:
            patterns.append(PHONE_INTL_PAREN_RE)
        patterns.append(PHONE_E164_RE)
    if has_paren and PHONE_PAREN_HINT_RE.search(text) is not None:
        patterns.append(PHONE_PAREN_RE)
    if has_sep:
        patterns.append(PHONE_SEP_RE)
    if has_phone_kw and "tel:" in lower:
        patterns.append(PHONE_TEL_URI_RE)

    seen: set[str] = set()
    hits: list[str] = []

    def record(raw: str) -> None:
        folded = _normalize_phone_candidate(raw)
        if folded is not None and folded not in seen:
            seen.add(folded)
            hits.append(folded)

    for pattern in patterns:
        for match in pattern.finditer(text):
            record(match.group(1))

    if has_phone_kw:
        for key_match in PHONE_KEY_RE.finditer(text):
            pos = key_match.end()
            if pos >= len(text):
                continue
            if text[pos] == "[":
                end_bracket = text.find("]", pos, pos + 256)
                end_nl = text.find("\n", pos, pos + 256)
                stop = end_bracket if end_bracket != -1 else (end_nl if end_nl != -1 else pos + 256)
                for item in PHONE_LIST_ITEM_RE.finditer(text, pos + 1, stop):
                    record(item.group(1))
            else:
                val_match = PHONE_VALUE_AT_RE.match(text, pos)
                if val_match is not None:
                    record(val_match.group(1))
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
    if ":" in text or "=" in text:
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


def load_exemptions_inventory(
    path: Path,
    *,
    today: datetime.date | None = None,
) -> tuple[tuple[ExemptionRecord, ...], tuple[ExemptionInventoryEntry, ...], list[str]]:
    """Load tracked exemptions, returning (active_records, full_inventory, errors).

    An expired or malformed record fails closed via `errors`, while preserving
    every configured record in `full_inventory` so the output never wipes the
    inventory when one record expires.
    """
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
    active_records: list[ExemptionRecord] = []
    inventory: list[ExemptionInventoryEntry] = []
    errors: list[str] = []
    seen: set[tuple[str, str, str]] = set()

    for index, entry in enumerate(raw_entries):
        if not isinstance(entry, dict):
            errors.append(f"exemption record #{index + 1} is incomplete or has unexpected fields")
            inventory.append(
                ExemptionInventoryEntry(
                    path=f"<record-{index + 1}>",
                    rule="unknown",
                    digest_prefix="invalid",
                    expires="invalid",
                    status="invalid",
                )
            )
            continue

        raw_path = entry.get("path")
        raw_rule = entry.get("rule")
        raw_digest = entry.get("digest")
        raw_expires = entry.get("expires")
        safe_path = raw_path if _valid_relative_path(raw_path) else f"<record-{index + 1}>"
        safe_rule = raw_rule if isinstance(raw_rule, str) and raw_rule in DETECTOR_IDS else "unknown"
        safe_digest_prefix = (
            raw_digest[:12]
            if isinstance(raw_digest, str) and SHA256_RE.fullmatch(raw_digest)
            else "invalid"
        )
        safe_expires = (
            raw_expires
            if isinstance(raw_expires, str) and ISO_DATE_RE.fullmatch(raw_expires)
            else "invalid"
        )

        if set(entry.keys()) != required_keys:
            errors.append(f"exemption record #{index + 1} is incomplete or has unexpected fields")
            inventory.append(
                ExemptionInventoryEntry(safe_path, safe_rule, safe_digest_prefix, safe_expires, "invalid")
            )
            continue

        rel_path = entry["path"]
        rule = entry["rule"]
        digest = entry["digest"]
        reason = entry["reason"]
        approved_by = entry["approved_by"]
        expires = entry["expires"]

        record_error: str | None = None
        if not _valid_relative_path(rel_path):
            record_error = f"exemption record #{index + 1} has an invalid path"
        elif not isinstance(rule, str) or rule not in DETECTOR_IDS:
            record_error = f"exemption record #{index + 1} has an unknown rule"
        elif not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
            record_error = f"exemption record #{index + 1} digest must be lowercase 64-hex SHA-256"
        elif not isinstance(reason, str) or not reason.strip():
            record_error = f"exemption record #{index + 1} is missing a reason"
        elif not isinstance(approved_by, str) or not approved_by.strip():
            record_error = f"exemption record #{index + 1} is missing approved_by"
        elif not isinstance(expires, str) or not ISO_DATE_RE.fullmatch(expires):
            record_error = (
                f"exemption record #{index + 1} has a missing or malformed expires date (expected YYYY-MM-DD)"
            )
        else:
            try:
                expires_date = datetime.date.fromisoformat(expires)
            except ValueError:
                record_error = f"exemption record #{index + 1} has an invalid calendar expires date"
            else:
                if expires_date < current_date:
                    shown = display_path(rel_path)
                    errors.append(
                        f"exemption record #{index + 1} for {shown} "
                        f"rule '{rule}' digest {digest[:12]} expired on {expires}"
                    )
                    inventory.append(
                        ExemptionInventoryEntry(
                            path=rel_path,
                            rule=rule,
                            digest_prefix=digest[:12],
                            expires=expires,
                            status="expired",
                        )
                    )
                    continue

        if record_error is not None:
            errors.append(record_error)
            inventory.append(
                ExemptionInventoryEntry(safe_path, safe_rule, safe_digest_prefix, safe_expires, "invalid")
            )
            continue

        record = ExemptionRecord(
            path=rel_path,
            rule=rule,
            digest=digest,
            reason=reason.strip(),
            approved_by=approved_by.strip(),
            expires=expires,
        )
        if record.key in seen:
            errors.append(f"exemption record #{index + 1} is a duplicate")
            inventory.append(
                ExemptionInventoryEntry(rel_path, rule, digest[:12], expires, "invalid")
            )
            continue
        seen.add(record.key)
        active_records.append(record)
        inventory.append(
            ExemptionInventoryEntry(
                path=rel_path,
                rule=rule,
                digest_prefix=digest[:12],
                expires=expires,
                status="valid",
                record=record,
            )
        )
    return tuple(active_records), tuple(inventory), errors


def load_exemptions(
    path: Path,
    *,
    today: datetime.date | None = None,
) -> tuple[ExemptionRecord, ...]:
    """Load and strictly validate tracked digest-bound exemption records."""
    records, _inventory, errors = load_exemptions_inventory(path, today=today)
    if errors:
        raise ScanConfigError(errors[0])
    return records


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
    finished = False
    try:
        for sha, users in by_object.items():
            process.stdin.write(f"{sha}\n".encode("ascii"))
            process.stdin.flush()
            header = process.stdout.readline().rstrip(b"\n").split(b" ")
            try:
                header_size = int(header[2]) if len(header) == 3 else -1
            except ValueError:
                header_size = -1
            if (
                len(header) != 3
                or header[0].decode("ascii", "replace") != sha
                or header[1] != b"blob"
                or header_size != sizes[sha]
            ):
                raise ScanConfigError("Git returned a malformed object stream")
            content = process.stdout.read(sizes[sha])
            if len(content) != sizes[sha] or process.stdout.read(1) != b"\n":
                raise ScanConfigError("Git returned a truncated object stream")
            for mode, path in users:
                yield SourceFile(path, content, symlink=(mode == "120000"), object_id=sha)
        finished = True
    finally:
        process.stdin.close()
        process.stdout.close()
        if not finished:
            process.kill()
        rc = process.wait()
        if finished and rc != 0:
            raise ScanConfigError("Git cat-file failed while reading tree objects")


def range_commits(root: Path, base: str, head: str) -> list[str]:
    """Return all commits in base..head in chronological order, failing closed on gaps."""
    if not COMMIT_RE.fullmatch(base) or not COMMIT_RE.fullmatch(head):
        raise ScanConfigError("--range needs two full lowercase commit SHAs")
    _git(root, ["cat-file", "-e", f"{base}^{{commit}}"])
    _git(root, ["cat-file", "-e", f"{head}^{{commit}}"])
    shallow = _git(root, ["rev-parse", "--is-shallow-repository"]).decode("ascii", "replace").strip()
    if shallow == "true":
        # Verify base is an ancestor of head; shallow clones with truncated history fail closed.
        anc = subprocess.run(
            ["git", "--no-optional-locks", "merge-base", "--is-ancestor", base, head],
            cwd=root,
            capture_output=True,
            check=False,
        )
        if anc.returncode != 0:
            raise ScanConfigError("repository history is shallow/incomplete for the requested range")
    raw = _git(root, ["rev-list", "--reverse", f"{base}..{head}"])
    commits = [line.decode("ascii") for line in raw.splitlines() if line]
    if not commits:
        raise ScanConfigError("commit range is empty")
    if len(commits) > MAX_RANGE_COMMITS:
        raise ScanConfigError(
            f"commit range has more than {MAX_RANGE_COMMITS} commits; refusing to scan"
        )
    return commits


# --- decoding and display safety --------------------------------------------


_ALLOWED_TEXT_CONTROLS = frozenset({"\t", "\n", "\r", "\f", "\v"})


def _is_printable_utf16_text(text: str) -> bool:
    """Return True only when decoded BOM-less UTF-16 looks like genuine text."""
    if not text or "\0" in text:
        return False
    printable = 0
    for ch in text:
        if ch in _ALLOWED_TEXT_CONTROLS:
            printable += 1
            continue
        cat = unicodedata.category(ch)
        if cat in ("Cc", "Cs", "Co", "Cn"):
            return False
        if ch.isprintable() or cat.startswith(("L", "M", "N", "P", "S", "Z")):
            printable += 1
    return (printable / len(text)) >= 0.95


def _looks_utf16(data: bytes) -> str | None:
    """Return a UTF-16 codec name only when data is genuine NUL-interleaved UTF-16 text."""
    if len(data) < 4 or len(data) % 2 != 0:
        return None
    sample = data[:4096]
    pairs = len(sample) // 2
    even_nuls = sum(1 for i in range(0, pairs * 2, 2) if sample[i] == 0)
    odd_nuls = sum(1 for i in range(1, pairs * 2, 2) if sample[i] == 0)
    codec: str | None = None
    if odd_nuls >= pairs * 0.5 and even_nuls == 0:
        codec = "utf-16-le"
    elif even_nuls >= pairs * 0.5 and odd_nuls == 0:
        codec = "utf-16-be"
    if codec is None:
        return None
    try:
        decoded = data.decode(codec)
    except UnicodeDecodeError:
        return None
    if not _is_printable_utf16_text(decoded):
        return None
    return codec


def decode_content(data: bytes) -> str:
    """Decode file content strictly, raising ScanConfigError when it is not text.

    UTF-8 (with or without BOM) and genuine UTF-16 (with BOM or strictly
    validated NUL-interleaved text) are decoded. Opaque NUL-bearing binary
    data or decode errors are refused unless hash-excepted.
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


def display_path(path: str) -> str:
    """Return a safe display path, redacting any path that matches a detector."""
    try:
        variants = _line_variants(path)
        for detector_fn in DETECTORS.values():
            for variant in variants:
                if detector_fn(variant):
                    return redacted_path_id(path)
    except Exception:  # noqa: BLE001 - fail safe by redacting path on any error
        return redacted_path_id(path)
    return escape_for_display(path)


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
        """Return normalized full-text variants across every bounded decode stage.

        Only lines containing non-ASCII characters or escape markers are
        normalised, so large plain-ASCII files with a few punctuation marks
        stay fast while preserving line alignment across every intermediate stage.
        """
        if text.isascii() and "\\u" not in text and "\\/" not in text and "%" not in text:
            return (text,)
        lines = text.split("\n")
        per_line: list[tuple[str, ...]] = []
        max_variants = 1
        for line in lines:
            if line.isascii() and "\\u" not in line and "\\/" not in line and "%" not in line:
                per_line.append((line,))
                continue
            lv = _line_variants(line)
            per_line.append(lv)
            if len(lv) > max_variants:
                max_variants = len(lv)
        variants: list[str] = []
        seen: set[str] = set()
        for idx in range(max_variants):
            joined = "\n".join(lv[idx] if idx < len(lv) else lv[-1] for lv in per_line)
            if joined not in seen:
                seen.add(joined)
                variants.append(joined)
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
        try:
            variants = _line_variants(line)
        except ScanConfigError as exc:
            return [], [f"decode failed ({exc}); failing closed"]
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
        try:
            file_variants = self._text_variants(unified)
        except ScanConfigError as exc:
            return f"decode failed ({exc}); failing closed", [], False
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
            try:
                line_variants = _line_variants(line)
            except ScanConfigError as exc:
                return f"decode failed ({exc}); failing closed", [], False
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

        shown = (
            redacted_path_id(source.path)
            if (path_hits or path_errors)
            else display_path(source.path)
        )

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


def tracked_path_failures(
    tracked: Iterable[str] | None,
    *,
    root: Path | None = None,
) -> list[str]:
    """Fail closed on empty listings or tracked private, generated, or retired paths."""
    failures: list[str] = []
    tracked_list = list(tracked) if tracked is not None else None
    if not tracked_list:
        failures.append("tracked file listing is missing or empty")
        return failures
    for name in tracked_list:
        if (
            name.endswith(".local.toml")
            or name.startswith("reports/")
            or name.startswith("artifacts/")
            or name.startswith(".secrets/")
        ):
            failures.append(f"private/generated path is tracked: {display_path(name)}")
        if name.startswith("skills/setup-agent-dev-machine/"):
            if root is None or (root / name).exists():
                failures.append(f"retired skill path is tracked: {display_path(name)}")
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
        if any(fragment not in dependabot_text for fragment in DEPENDABOT_COMMIT_POLICY):
            failures.append("Dependabot must emit conventional build(scope) commit subjects")

    failures.extend(tracked_path_failures(tracked, root=root))
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
    exemptions: Sequence[ExemptionRecord | ExemptionInventoryEntry],
    applied: frozenset[tuple[str, str, str]],
) -> None:
    if not exemptions:
        print("Configured exemptions: none")
        return
    print("Configured exemptions:")
    for item in exemptions:
        if isinstance(item, ExemptionInventoryEntry):
            if item.status == "valid" and item.record is not None:
                status = "applied" if item.record.key in applied else "unused"
            else:
                status = item.status
            shown = display_path(item.path)
            print(
                f"- [{status}] {shown} rule={item.rule} digest={item.digest_prefix} expires={item.expires}"
            )
        else:
            status = "applied" if item.key in applied else "unused"
            shown = display_path(item.path)
            print(
                f"- [{status}] {shown} rule={item.rule} digest={item.digest[:12]} expires={item.expires}"
            )


def _report(
    *,
    failures: list[str],
    warnings: list[str],
    modes: Mapping[str, str] | None,
    exemptions: Sequence[ExemptionRecord | ExemptionInventoryEntry],
    applied_exemptions: frozenset[tuple[str, str, str]],
    passed_message: str,
    emit_verified_marker: bool = False,
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
    if emit_verified_marker:
        print(SCAN_VERIFIED_MARKER)
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


def _commit_message_bytes(root: Path, commit: str) -> bytes:
    """Return the raw commit message (%B) for a commit SHA."""
    return _git(root, ["show", "-s", "--format=%B", commit])


def _tag_message_bytes(root: Path, tag_oid: str) -> bytes:
    """Return the message body of an annotated tag object (excluding tagger header)."""
    if not COMMIT_RE.fullmatch(tag_oid):
        raise ScanConfigError("--tag-objects needs full lowercase object SHAs")
    raw = _git(root, ["cat-file", "tag", tag_oid])
    _header, sep, body = raw.partition(b"\n\n")
    return body if sep else b""


def _scan_commit_trees(
    root: Path,
    commits: Sequence[str],
    *,
    modes: Mapping[str, str] | None,
    exemptions: Sequence[ExemptionRecord],
    binary_exceptions: Mapping[str, BinaryException],
    scan_commit_messages: bool = False,
    tag_objects: Sequence[str] = (),
) -> tuple[list[str], list[str], frozenset[tuple[str, str, str]]]:
    """Scan commits (trees and optionally commit messages) plus annotated tag messages."""
    failures: list[str] = []
    warnings: list[str] = []
    if not commits and not tag_objects:
        return ["commit list is empty"], warnings, frozenset()
    if len(commits) > MAX_RANGE_COMMITS:
        return [f"commit list has more than {MAX_RANGE_COMMITS} commits; refusing to scan"], warnings, frozenset()

    scanner = (
        Scanner(modes=modes, exemptions=exemptions, binary_exceptions=binary_exceptions)
        if modes is not None
        else None
    )
    multi = len(commits) > 1
    for commit in commits:
        if scan_commit_messages and scanner is not None:
            try:
                msg_bytes = _commit_message_bytes(root, commit)
            except ScanConfigError as exc:
                failures.append(f"commit message read failed: {exc}")
            else:
                msg_failures, msg_warnings = scanner.scan_source(
                    SourceFile(f"commit:{commit[:12]}:message", msg_bytes)
                )
                failures.extend(msg_failures)
                warnings.extend(msg_warnings)
        try:
            tree_paths, sources = tree_sources(root, commit)
        except ScanConfigError as exc:
            failures.append(f"tree listing failed: {exc}")
            continue
        path_issues = tracked_path_failures(tree_paths)
        if multi:
            failures.extend(f"{item} (commit {commit[:12]})" for item in path_issues)
        else:
            failures.extend(path_issues)
        if scanner is None or not tree_paths:
            continue
        count = 0
        try:
            for source in sources:
                count += 1
                src_failures, src_warnings = scanner.scan_source(source)
                if multi:
                    failures.extend(f"{item} (commit {commit[:12]})" for item in src_failures)
                    warnings.extend(f"{item} (commit {commit[:12]})" for item in src_warnings)
                else:
                    failures.extend(src_failures)
                    warnings.extend(src_warnings)
                if len(failures) > MAX_FINDINGS:
                    del failures[MAX_FINDINGS:]
                    failures.append(
                        f"more than {MAX_FINDINGS} findings; the scan stopped after {count} entries"
                    )
                    if hasattr(sources, "close"):
                        sources.close()
                    return failures, warnings, frozenset(scanner.applied_exemptions)
        except ScanConfigError as exc:
            failures.append(f"tree read failed: {exc}")
            continue
        if count == 0:
            failures.append("tracked file listing is missing or empty")

    if scanner is not None:
        for tag_oid in tag_objects:
            try:
                tag_bytes = _tag_message_bytes(root, tag_oid)
            except ScanConfigError as exc:
                failures.append(f"tag message read failed: {exc}")
                continue
            tag_failures, tag_warnings = scanner.scan_source(
                SourceFile(f"tag:{tag_oid[:12]}:message", tag_bytes)
            )
            failures.extend(tag_failures)
            warnings.extend(tag_warnings)

    applied = frozenset(scanner.applied_exemptions) if scanner is not None else frozenset()
    return failures, warnings, applied


def _run(args: argparse.Namespace) -> int:
    if args.exemption_digest is not None:
        return _run_exemption_digest(args.exemption_digest)

    selected_modes = sum(
        1 for opt in (args.tree is not None, args.range is not None, args.commits is not None) if opt
    )
    if selected_modes > 1:
        print("Public release checks failed:")
        print("- specify at most one of --tree, --range, or --commits")
        return 1

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
    exemption_inventory: tuple[ExemptionInventoryEntry, ...] = ()
    binary_exceptions: dict[str, BinaryException] = {}
    applied_exemptions: frozenset[tuple[str, str, str]] = frozenset()

    try:
        modes = load_modes(modes_path)
    except ScanConfigError as exc:
        failures.append(str(exc))

    try:
        exemptions, exemption_inventory, exemption_errors = load_exemptions_inventory(exemptions_path)
    except ScanConfigError as exc:
        failures.append(str(exc))
    else:
        failures.extend(exemption_errors)

    try:
        binary_exceptions = load_binary_exceptions(binary_path)
    except ScanConfigError as exc:
        failures.append(str(exc))

    if args.tree is not None or args.range is not None or args.commits is not None or args.tag_objects is not None:
        commits_to_scan: list[str] = []
        tag_objects_to_scan: list[str] = list(args.tag_objects or ())
        scan_messages = args.range is not None or args.commits is not None
        if args.tree is not None:
            commits_to_scan = [args.tree]
        elif args.range is not None:
            try:
                commits_to_scan = range_commits(root, args.range[0], args.range[1])
            except ScanConfigError as exc:
                failures.append(f"commit range failed: {exc}")
        elif args.commits is not None:
            commits_to_scan = list(args.commits)
        if commits_to_scan or tag_objects_to_scan:
            tree_failures, tree_warnings, applied_exemptions = _scan_commit_trees(
                root,
                commits_to_scan,
                modes=modes,
                exemptions=exemptions,
                binary_exceptions=binary_exceptions,
                scan_commit_messages=scan_messages,
                tag_objects=tag_objects_to_scan,
            )
            failures.extend(tree_failures)
            warnings.extend(tree_warnings)
        label = (
            f"Deterministic public-safety scan of {len(commits_to_scan)} commit(s) passed."
            if (args.range is not None or args.commits is not None or args.tag_objects is not None)
            else "Deterministic public-safety scan of the tree passed."
        )
        return _report(
            failures=failures,
            warnings=warnings,
            modes=modes,
            exemptions=exemption_inventory,
            applied_exemptions=applied_exemptions,
            passed_message=label,
            emit_verified_marker=True,
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
        exemptions=exemption_inventory,
        applied_exemptions=applied_exemptions,
        passed_message=passed_message,
        emit_verified_marker=True,
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
        help="scan this commit's tree as git objects (no checkout); checks tracked paths and content",
    )
    parser.add_argument(
        "--range",
        nargs=2,
        default=None,
        metavar=("BASE_SHA", "HEAD_SHA"),
        help="scan every commit in BASE_SHA..HEAD_SHA as git objects (fails closed on empty/incomplete history)",
    )
    parser.add_argument(
        "--commits",
        nargs="+",
        default=None,
        metavar="COMMIT_SHA",
        help="scan each named commit SHA (tree and commit message) as git objects",
    )
    parser.add_argument(
        "--tag-objects",
        nargs="+",
        default=None,
        metavar="TAG_OID",
        help="scan each named annotated tag object's message",
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
