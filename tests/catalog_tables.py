#!/usr/bin/env python3
"""Generate the unit catalog tables from registry/units.toml and check them.

Each Markdown file listed in TARGETS holds the table between the markers
`<!-- catalog:units:begin -->` and `<!-- catalog:units:end -->`. With
`--write` the tables are rewritten; without it, the script exits 1 when any
table is stale, the dependency graph has a cycle or a dangling edge on an OS,
or a unit ID is duplicated. TSK-005 replaces this with `go generate`.
"""
from __future__ import annotations

import sys
from pathlib import Path

try:
    import tomllib
except ModuleNotFoundError:  # Python 3.9 and 3.10 have no tomllib
    tomllib = None

ROOT = Path(__file__).resolve().parents[1]
CATALOG = ROOT / "registry/units.toml"
PRESETS = ROOT / "registry/presets.toml"
TARGETS = (
    "docs/plan/roadmap.md",
    "docs/design/setup-architecture.md",
    "project-management/specs/SPC-001.md",
)
BEGIN = "<!-- catalog:units:begin -->"
END = "<!-- catalog:units:end -->"
OSES = ("linux", "macos")


def load() -> list[dict]:
    with CATALOG.open("rb") as handle:
        return tomllib.load(handle)["unit"]


def problems(units: list[dict]) -> list[str]:
    found: list[str] = []
    ids = [u["id"] for u in units]
    for unit_id in sorted({i for i in ids if ids.count(i) > 1}):
        found.append(f"duplicate unit id: {unit_id}")
    for os_name in OSES:
        present = {u["id"] for u in units if u["stage"][os_name] != "-"}
        edges = {
            u["id"]: list(u["depends"][os_name])
            + [d for cond in u.get("depends_if", {}).values() for d in cond.get(os_name, [])]
            for u in units if u["id"] in present
        }
        for unit_id, deps in edges.items():
            for dep in deps:
                if dep not in present:
                    found.append(f"{os_name}: {unit_id} depends on {dep}, which does not run on {os_name}")
        state: dict[str, int] = {}

        def visit(node: str, path: list[str]) -> None:
            if state.get(node) == 1:
                found.append(f"{os_name}: dependency cycle: {' -> '.join(path + [node])}")
                return
            if state.get(node) == 2 or node not in edges:
                return
            state[node] = 1
            for dep in edges[node]:
                visit(dep, path + [node])
            state[node] = 2

        for node in sorted(edges):
            visit(node, [])
    return found


def cell(value: str) -> str:
    return "none" if value in ("", "-") else value


def table(units: list[dict]) -> str:
    lines = [
        "| Unit | Domain | Linux stage | Linux runs as | macOS stage | macOS runs as | Depends on (Linux; macOS) | Epic | Milestone |",
        "|---|---|---|---|---|---|---|---|---|",
    ]
    for u in units:
        def deps_for(o: str) -> str:
            items = list(u["depends"][o])
            for cond, per_os in u.get("depends_if", {}).items():
                items += [f"{d} (if {cond})" for d in per_os.get(o, [])]
            return ", ".join(items) or "none"

        deps = "; ".join(deps_for(o) for o in OSES)
        if u.get("waits_for"):
            deps += "; waits for " + ", ".join(u["waits_for"])
        lines.append(
            f"| `{u['id']}` | {u['domain']} | {cell(u['stage']['linux'])} | {cell(u['privilege']['linux'])} "
            f"| {cell(u['stage']['macos'])} | {cell(u['privilege']['macos'])} | {deps} | {u['epic']} | {u['milestone']} |"
        )
    return "\n".join(lines)


def render(text: str, body: str) -> str:
    start = text.index(BEGIN) + len(BEGIN)
    end = text.index(END)
    return text[:start] + "\n" + body + "\n" + text[end:]


def main(argv: list[str]) -> int:
    if tomllib is None:
        print("SKIP catalog tables: needs Python 3.11 or newer (tomllib)")
        return 0
    units = load()
    errors = problems(units)
    if PRESETS.is_file():
        known = {u["id"] for u in units}
        with PRESETS.open("rb") as handle:
            presets = tomllib.load(handle)
        for group in ("tier", "purpose"):
            for name, entry in presets.get(group, {}).items():
                for unit_id in entry.get("units", []):
                    if unit_id not in known:
                        errors.append(f"presets: {group}.{name} names unknown unit {unit_id}")
    body = table(units)
    for name in TARGETS:
        path = ROOT / name
        if not path.is_file():
            continue
        text = path.read_text(encoding="utf-8")
        if BEGIN not in text or END not in text:
            continue
        updated = render(text, body)
        if updated != text:
            if "--write" in argv:
                path.write_text(updated, encoding="utf-8")
            else:
                errors.append(f"stale catalog table in {name}; run: python3 tests/catalog_tables.py --write")
    for error in errors:
        print("ERROR " + error)
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
