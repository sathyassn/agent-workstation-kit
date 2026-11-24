#!/usr/bin/env python3
"""Optional keyed HMAC-SHA256 digest list layer for private repositories.

Off by default in tests/public_scan_modes.json ("keyed-list": "off"). Whenever
tests/public_scan_keyed_list.json is present (even while mode is "off"), its
digest-only format is strictly validated so plaintext or malformed entries fail
closed. When set to "block" or "warn", resolves a 32-byte hex HMAC key from an
untracked owner-only file or CI secret environment variable, matches folded
candidate tokens via domain-separated HMAC-SHA256 using the exact same
normalization shared with the digest helper, and returns only matched digests
(never plaintext values).
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
import stat
from collections.abc import Callable, Mapping, Sequence
from pathlib import Path


KEYED_LIST_RULE_ID = "keyed-list"
DEFAULT_KEYED_LIST_PATH = Path("tests/public_scan_keyed_list.json")
DEFAULT_LOCAL_KEY_PATH = Path(".secrets/public-scan-hmac")
KEY_ENV_VAR = "PUBLIC_SCAN_HMAC_KEY"
KEY_FILE_ENV_VAR = "PUBLIC_SCAN_HMAC_KEY_FILE"

RULES_SCHEMA_VERSION = 1
RULES_ALGORITHM = "hmac-sha256"
HMAC_DOMAIN_PREFIX = "wskit-public-scan:v1"

KEY_HEX_RE = re.compile(r"^[0-9a-f]{64}$")
DIGEST_RE = re.compile(r"^[0-9a-f]{64}$")
MAX_KEY_FILE_BYTES = 4096
MAX_CANDIDATE_CHARS = 160
MAX_PHRASE_WORDS = 6
CANDIDATE_CACHE_BYTES = 32 * 1024 * 1024

# Backtrack-free compound token regex (trailing ._ - stripped in Python).
COMPOUND_TOKEN_RE = re.compile(r"[^\W_][\w.-]*", re.UNICODE)
WORD_RUN_RE = re.compile(r"[^\W_]+", re.UNICODE)
CAMEL_RUN_RE = re.compile(r"[A-Z]+|[a-z]+|[0-9]+|[^\WA-Za-z0-9_]+", re.UNICODE)
WHITESPACE_RE = re.compile(r"\s+")
DELIM_SPLIT_RE = re.compile(r"[\s\[\]()<>\"'=&,;{}?]+")
PUNCTUATION_RE = re.compile(r"[^\w\s]|_", re.UNICODE)
OUTER_WRAPPER_CHARS = "()[]{}<>\"'`,;:.!?*#~|\\"
MAX_DOT_SLICE_LABELS = 6
LONG_DOT_CHAIN_THRESHOLD = 16
LONG_DOT_CHAIN_WINDOW = 4


class KeyedListConfigError(ValueError):
    """Raised when keyed-list rules or HMAC key material are invalid or missing.

    Messages never echo rule entries or key contents.
    """


def _strict_json_object_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    obj: dict[str, object] = {}
    for key, value in pairs:
        if key in obj:
            raise KeyedListConfigError("duplicate JSON key in keyed-list configuration file")
        obj[key] = value
    return obj


def canonicalize_keyed_value(
    value: str,
    *,
    normalize_fn: Callable[[str], str],
    error_cls: type[Exception] = KeyedListConfigError,
) -> str:
    """Canonical folded representation shared by the digest helper and matcher."""
    normalized = WHITESPACE_RE.sub(" ", normalize_fn(value).strip()).casefold()
    if not normalized:
        raise error_cls("value to hash must not be empty")
    if len(normalized) > MAX_CANDIDATE_CHARS:
        raise error_cls(f"value to hash must be at most {MAX_CANDIDATE_CHARS} characters")
    if len(normalized.split(" ")) > MAX_PHRASE_WORDS:
        raise error_cls(f"value to hash must be at most {MAX_PHRASE_WORDS} words")
    return normalized


def compute_keyed_digest(
    key: bytes,
    value: str,
    *,
    normalize_fn: Callable[[str], str],
    error_cls: type[Exception] = KeyedListConfigError,
) -> str:
    """Compute the domain-separated HMAC-SHA256 hex digest of a folded value."""
    normalized = canonicalize_keyed_value(value, normalize_fn=normalize_fn, error_cls=error_cls)
    payload = f"{HMAC_DOMAIN_PREFIX}:{KEYED_LIST_RULE_ID}:{normalized}".encode("utf-8")
    return hmac.new(key, payload, hashlib.sha256).hexdigest()


def _validate_keyed_list_json_text(
    raw_text: str,
    *,
    require_nonempty: bool,
    error_cls: type[Exception],
) -> frozenset[str]:
    try:
        data = json.loads(raw_text, object_pairs_hook=_strict_json_object_pairs)
    except (json.JSONDecodeError, KeyedListConfigError) as exc:
        raise error_cls("keyed-list file is not valid JSON") from exc

    if not isinstance(data, dict) or set(data.keys()) != {"schema_version", "algorithm", "digests"}:
        raise error_cls("keyed-list file must contain only schema_version, algorithm, and digests")
    if type(data["schema_version"]) is not int or data["schema_version"] != RULES_SCHEMA_VERSION:
        raise error_cls(f"keyed-list schema_version must be integer {RULES_SCHEMA_VERSION}")
    if data["algorithm"] != RULES_ALGORITHM:
        raise error_cls(f"keyed-list algorithm must be '{RULES_ALGORITHM}'")
    raw_digests = data["digests"]
    if not isinstance(raw_digests, list):
        raise error_cls("keyed-list digests must be a list")
    if require_nonempty and not raw_digests:
        raise error_cls("keyed-list digests must be a non-empty list")

    digests: set[str] = set()
    for index, item in enumerate(raw_digests):
        if not isinstance(item, str) or not DIGEST_RE.fullmatch(item):
            raise error_cls(
                f"keyed-list entry #{index + 1} is malformed or plaintext; "
                "expected lowercase 64-hex HMAC-SHA256 digest"
            )
        digests.add(item)
    return frozenset(digests)


def validate_keyed_list_bytes(
    raw_bytes: bytes,
    *,
    require_nonempty: bool = True,
    error_cls: type[Exception] = KeyedListConfigError,
) -> frozenset[str]:
    """Strictly validate digest-only keyed-list content bytes (e.g. from a git tree)."""
    try:
        raw_text = raw_bytes.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise error_cls("keyed-list file could not be read as UTF-8") from exc
    return _validate_keyed_list_json_text(
        raw_text,
        require_nonempty=require_nonempty,
        error_cls=error_cls,
    )


def load_keyed_list(
    path: Path,
    *,
    require_nonempty: bool = True,
    error_cls: type[Exception] = KeyedListConfigError,
) -> frozenset[str]:
    """Load and strictly validate a digest-only keyed HMAC list file.

    Plaintext or malformed entries fail closed without echoing input.
    """
    if path.is_symlink():
        raise error_cls("keyed-list file must not be a symlink")
    try:
        st = os.lstat(path)
    except OSError as exc:
        raise error_cls("keyed-list file is missing or unreadable") from exc
    if not stat.S_ISREG(st.st_mode):
        raise error_cls("keyed-list file is not a regular file")
    try:
        raw_text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise error_cls("keyed-list file could not be read as UTF-8") from exc
    return _validate_keyed_list_json_text(
        raw_text,
        require_nonempty=require_nonempty,
        error_cls=error_cls,
    )


def _parse_key_text(raw_text: str, *, error_cls: type[Exception]) -> bytes:
    cleaned = raw_text.strip()
    if not cleaned:
        raise error_cls("HMAC scan key is missing or empty")
    if not KEY_HEX_RE.fullmatch(cleaned):
        raise error_cls(
            "HMAC scan key must be exactly 64 lowercase hex characters (32 random bytes; "
            "create one with: openssl rand -hex 32)"
        )
    return bytes.fromhex(cleaned)


def load_key_from_file(
    key_path: Path,
    *,
    root: Path,
    tracked: Sequence[str] | None,
    error_cls: type[Exception] = KeyedListConfigError,
) -> bytes:
    """Load an HMAC key from an untracked, owner-only regular file."""
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(key_path, flags)
    except OSError as exc:
        if exc.errno in (getattr(os, "ELOOP", -1), 62, 40) or key_path.is_symlink():
            raise error_cls("HMAC scan key file must not be a symlink") from exc
        raise error_cls("HMAC scan key file is missing or unreadable") from exc

    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode):
            raise error_cls("HMAC scan key file must be a regular file")
        if st.st_size > MAX_KEY_FILE_BYTES:
            raise error_cls(f"HMAC scan key file exceeds maximum size ({MAX_KEY_FILE_BYTES} bytes)")
        if os.name == "posix" and (st.st_mode & 0o077) != 0:
            raise error_cls(
                "HMAC scan key file permissions are too broad; restrict to owner only (chmod 0600)"
            )
        with os.fdopen(fd, "rb") as handle:
            fd = -1
            raw_bytes = handle.read(MAX_KEY_FILE_BYTES + 1)
    finally:
        if fd != -1:
            os.close(fd)

    if len(raw_bytes) > MAX_KEY_FILE_BYTES:
        raise error_cls(f"HMAC scan key file exceeds maximum size ({MAX_KEY_FILE_BYTES} bytes)")

    resolved_root = root.resolve()
    resolved_key = key_path.resolve()
    if tracked:
        tracked_set = set(tracked)
        if resolved_key.is_relative_to(resolved_root):
            if resolved_key.relative_to(resolved_root).as_posix() in tracked_set:
                raise error_cls("HMAC scan key file must not be tracked by Git")
        if key_path.is_relative_to(root):
            if key_path.relative_to(root).as_posix() in tracked_set:
                raise error_cls("HMAC scan key file must not be tracked by Git")

    try:
        raw_text = raw_bytes.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise error_cls("HMAC scan key file could not be read as UTF-8") from exc
    return _parse_key_text(raw_text, error_cls=error_cls)


def resolve_keyed_list_key(
    root: Path,
    *,
    tracked: Sequence[str] | None,
    key_file: Path | None = None,
    environ: Mapping[str, str] | None = None,
    error_cls: type[Exception] = KeyedListConfigError,
) -> bytes:
    """Resolve the HMAC key from CLI file, env file, env var, or default untracked file."""
    env = os.environ if environ is None else environ
    has_cli_key_file = key_file is not None
    has_env_key_file = KEY_FILE_ENV_VAR in env
    has_env_key = KEY_ENV_VAR in env
    explicit_sources = int(has_cli_key_file) + int(has_env_key_file) + int(has_env_key)
    if explicit_sources > 1:
        raise error_cls("specify only one HMAC key source")

    if has_cli_key_file:
        assert key_file is not None
        return load_key_from_file(key_file, root=root, tracked=tracked, error_cls=error_cls)
    if has_env_key_file:
        env_path = env[KEY_FILE_ENV_VAR].strip()
        if not env_path:
            raise error_cls(f"{KEY_FILE_ENV_VAR} is set but empty")
        return load_key_from_file(Path(env_path), root=root, tracked=tracked, error_cls=error_cls)
    if has_env_key:
        return _parse_key_text(env[KEY_ENV_VAR], error_cls=error_cls)

    default_path = root / DEFAULT_LOCAL_KEY_PATH
    if default_path.exists() or default_path.is_symlink():
        return load_key_from_file(default_path, root=root, tracked=tracked, error_cls=error_cls)
    raise error_cls(
        f"HMAC scan key is missing; set {KEY_ENV_VAR}, set {KEY_FILE_ENV_VAR}, "
        f"pass --keyed-list-key-file, or create untracked {DEFAULT_LOCAL_KEY_PATH} (chmod 0600)"
    )


def _fold_plain(value: str) -> str:
    return WHITESPACE_RE.sub(" ", value.strip().casefold())


def _add_candidate(candidates: set[str], value: str) -> None:
    if not value:
        return
    if len(value) > MAX_CANDIDATE_CHARS and value.isalnum():
        return
    folded = _fold_plain(value)
    if folded and len(folded) <= MAX_CANDIDATE_CHARS:
        candidates.add(folded)


def _split_camel_linear(run: str) -> list[str]:
    """Strictly O(n) camelCase and class-boundary splitter using disjoint C-level runs."""
    if len(run) <= 1:
        return [run] if len(run) == 1 else []
    raw_chunks = CAMEL_RUN_RE.findall(run)
    if len(raw_chunks) <= 1:
        return raw_chunks
    parts: list[str] = []
    idx = 0
    n = len(raw_chunks)
    while idx < n:
        chunk = raw_chunks[idx]
        if (
            idx + 1 < n
            and "A" <= chunk[0] <= "Z"
            and "a" <= raw_chunks[idx + 1][0] <= "z"
        ):
            if len(chunk) > 1:
                parts.append(chunk[:-1])
                parts.append(chunk[-1] + raw_chunks[idx + 1])
            else:
                parts.append(chunk + raw_chunks[idx + 1])
            idx += 2
        else:
            parts.append(chunk)
            idx += 1
    return parts


def _add_phrase_windows(candidates: set[str], items: Sequence[str]) -> None:
    n = len(items)
    for start in range(n):
        phrase = items[start]
        if not phrase or len(phrase) > MAX_CANDIDATE_CHARS:
            continue
        candidates.add(phrase)
        for end in range(start + 1, min(n, start + MAX_PHRASE_WORDS)):
            part = items[end]
            if not part:
                break
            phrase = f"{phrase} {part}"
            if len(phrase) > MAX_CANDIDATE_CHARS:
                break
            candidates.add(phrase)


def extract_candidates_from_normalized(text: str) -> set[str]:
    """Extract bounded folded candidate tokens from one already-normalized line.

    Strictly O(n) in line length and preserves internal punctuation (@, /, :, +,
    ., -, _) inside whitespace-delimited tokens so values accepted by
    compute_keyed_digest are matched inside prose.
    """
    candidates: set[str] = set()
    words: list[str] = []

    _add_candidate(candidates, text)
    if not (len(text) > MAX_CANDIDATE_CHARS and text.isalnum()):
        _add_candidate(candidates, PUNCTUATION_RE.sub(" ", text))
        _add_candidate(candidates, PUNCTUATION_RE.sub("", text))

    # Raw whitespace tokens with outer wrapper punctuation stripped (preserves emails,
    # paths, key:value tokens, and multi-word phrases up to MAX_PHRASE_WORDS).
    raw_stripped: list[str] = []
    cleaned_chunks: list[str] = []
    for raw_tok in text.split():
        stripped = raw_tok.strip(OUTER_WRAPPER_CHARS)
        if stripped:
            raw_stripped.append(stripped.casefold() if len(stripped) <= MAX_CANDIDATE_CHARS else "")
            if "=" in stripped:
                _, _, rhs = stripped.partition("=")
                rhs_clean = rhs.strip(OUTER_WRAPPER_CHARS)
                if rhs_clean:
                    _add_candidate(candidates, rhs_clean)
        if raw_tok.isalnum():
            cleaned_chunks.append(raw_tok.casefold() if len(raw_tok) <= MAX_CANDIDATE_CHARS else "")
        else:
            cleaned = PUNCTUATION_RE.sub("", raw_tok)
            if cleaned:
                cleaned_chunks.append(cleaned.casefold() if len(cleaned) <= MAX_CANDIDATE_CHARS else "")

    _add_phrase_windows(candidates, raw_stripped)
    _add_phrase_windows(candidates, cleaned_chunks)

    # Linear delimiter pass catches Markdown links, mailto:, JSON values, query params,
    # label:value tokens, and URLs without quadratic regex backtracking.
    if not (len(text) > MAX_CANDIDATE_CHARS and text.isalnum()):
        for raw_seg in DELIM_SPLIT_RE.split(text):
            seg = raw_seg.strip(OUTER_WRAPPER_CHARS)
            if not seg:
                continue
            if len(seg) <= MAX_CANDIDATE_CHARS:
                _add_candidate(candidates, seg)
            lower_seg = seg.casefold()
            if lower_seg.startswith("mailto:"):
                addr = seg[7:].split("?", 1)[0].strip(OUTER_WRAPPER_CHARS)
                if addr:
                    _add_candidate(candidates, addr)
            elif "://" in seg:
                _, _, after_scheme = seg.partition("://")
                no_frag = after_scheme.split("#", 1)[0]
                main_part, _, query_part = no_frag.partition("?")
                authority, sep_slash, url_path = main_part.partition("/")
                if authority:
                    _add_candidate(candidates, authority)
                    host_port = authority.rsplit("@", 1)[-1]
                    if host_port:
                        _add_candidate(candidates, host_port)
                        host_only = host_port.split(":", 1)[0]
                        if host_only:
                            _add_candidate(candidates, host_only)
                if sep_slash and url_path:
                    cleaned_path = url_path.strip("/")
                    if cleaned_path:
                        _add_candidate(candidates, cleaned_path)
                        for path_part in cleaned_path.split("/"):
                            if path_part:
                                _add_candidate(candidates, path_part)
                if query_part:
                    for q_item in query_part.split("&"):
                        if "=" in q_item:
                            _, _, q_val = q_item.partition("=")
                            if q_val:
                                _add_candidate(candidates, q_val.strip(OUTER_WRAPPER_CHARS))
                        elif q_item:
                            _add_candidate(candidates, q_item.strip(OUTER_WRAPPER_CHARS))
            elif ":" in seg:
                _, _, rhs = seg.partition(":")
                rhs_clean = rhs.strip(OUTER_WRAPPER_CHARS)
                if rhs_clean:
                    _add_candidate(candidates, rhs_clean)
                    if rhs_clean.casefold().startswith("mailto:"):
                        addr = rhs_clean[7:].split("?", 1)[0].strip(OUTER_WRAPPER_CHARS)
                        if addr:
                            _add_candidate(candidates, addr)

    for match in COMPOUND_TOKEN_RE.finditer(text):
        raw_token = match.group(0).rstrip("._-")
        if not raw_token:
            continue
        _add_candidate(candidates, raw_token)

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
                    _add_candidate(candidates, ".".join(dot_parts[start : start + width]))

        if "-" in raw_token:
            for dot_seg in raw_token.split("."):
                if "-" not in dot_seg:
                    continue
                dash_parts = [p for p in dot_seg.split("-") if p]
                n_dash = len(dash_parts)
                for start in range(n_dash):
                    for width in range(1, min(LONG_DOT_CHAIN_WINDOW, n_dash - start) + 1):
                        _add_candidate(candidates, "-".join(dash_parts[start : start + width]))

        for run_match in WORD_RUN_RE.finditer(raw_token):
            run = run_match.group(0)
            norm_run = _fold_plain(run) if len(run) <= MAX_CANDIDATE_CHARS else ""
            if norm_run:
                _add_candidate(candidates, norm_run)
            camel_parts = _split_camel_linear(run)
            if len(camel_parts) > 1:
                for part in camel_parts:
                    if len(part) > MAX_CANDIDATE_CHARS:
                        words.append("")
                        continue
                    norm_part = _fold_plain(part)
                    if norm_part:
                        _add_candidate(candidates, norm_part)
                        words.append(norm_part)
            elif norm_run:
                words.append(norm_run)
            else:
                words.append("")

    _add_phrase_windows(candidates, words)
    return candidates


class KeyedListMatcher:
    """Match normalized text against a keyed HMAC-SHA256 digest set.

    Returns matched 64-hex HMAC digests (never plaintext values).
    """

    def __init__(
        self,
        key: bytes,
        digests: frozenset[str],
        *,
        normalize_fn: Callable[[str], str],
    ) -> None:
        self._key = key
        self._digests = digests
        self._normalize_fn = normalize_fn
        self._cache: dict[str, str | None] = {}
        self._cache_bytes = 0

    def _hmac_if_hit(self, candidate: str) -> str | None:
        cached = self._cache.get(candidate, False)
        if cached is not False:
            return cached  # type: ignore[return-value]
        payload = f"{HMAC_DOMAIN_PREFIX}:{KEYED_LIST_RULE_ID}:{candidate}".encode("utf-8")
        digest = hmac.new(self._key, payload, hashlib.sha256).hexdigest()
        hit = digest if digest in self._digests else None
        if self._cache_bytes + len(candidate) > CANDIDATE_CACHE_BYTES:
            self._cache.clear()
            self._cache_bytes = 0
        self._cache[candidate] = hit
        self._cache_bytes += len(candidate)
        return hit

    def detect(self, text: str) -> list[str]:
        """Scan normalized text (single line or multi-line) and return matched HMAC digests."""
        if not self._digests or not text:
            return []
        hits: list[str] = []
        seen: set[str] = set()
        for line in text.split("\n"):
            if not line.strip():
                continue
            for candidate in extract_candidates_from_normalized(line):
                matched_digest = self._hmac_if_hit(candidate)
                if matched_digest is not None and matched_digest not in seen:
                    seen.add(matched_digest)
                    hits.append(matched_digest)
        return hits
