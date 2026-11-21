"""The unit catalog check finds duplicates, cycles and dangling per-OS edges."""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import catalog_tables as cat  # noqa: E402


def unit(uid, linux=(), macos=(), on_mac=True):
    return {"id": uid, "domain": "system", "epic": "EPC-001", "milestone": "alpha",
            "stage": {"linux": "s", "macos": "s" if on_mac else "-"},
            "privilege": {"linux": "root", "macos": "root" if on_mac else "-"},
            "depends": {"linux": list(linux), "macos": list(macos)}}


@unittest.skipIf(cat.tomllib is None, "needs tomllib")
class CatalogTests(unittest.TestCase):
    def test_real_catalog_is_clean(self) -> None:
        self.assertEqual(cat.problems(cat.load()), [])

    def test_cycle_is_found(self) -> None:
        found = cat.problems([unit("a", ["b"], ["b"]), unit("b", ["a"], ["a"])])
        self.assertTrue(any("cycle" in f for f in found))

    def test_dangling_macos_edge_is_found(self) -> None:
        found = cat.problems([unit("linux-only", on_mac=False), unit("b", [], ["linux-only"])])
        self.assertTrue(any("macos: b depends on linux-only" in f for f in found))

    def test_duplicate_id_is_found(self) -> None:
        self.assertIn("duplicate unit id: a", cat.problems([unit("a"), unit("a")]))

    def test_table_lists_every_unit(self) -> None:
        units = cat.load()
        body = cat.table(units)
        for u in units:
            self.assertIn("`" + u["id"] + "`", body)


if __name__ == "__main__":
    unittest.main()
