from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts/lib"))
from readiness import source_evidence, execution_evidence


class ReadinessEvidenceTests(unittest.TestCase):
    def test_archive_inside_unrelated_checkout_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            subprocess.run(["git", "init", "-q", str(parent)], check=True)
            (parent / ".gitignore").write_text("staged/\n")
            (parent / "VERSION").write_text("1.0.0\n")
            def git(*args):
                return subprocess.run(["git", "-C", str(parent), *args], check=True, capture_output=True, text=True).stdout
            git("add", ".")
            git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "chore: fixture")
            staged = parent / "staged"
            staged.mkdir()
            (staged / "VERSION").write_text("1.0.0\n")
            # Previous readiness accepted this: git status is clean and HEAD resolves
            # through the unrelated parent, even though this is a staged archive.
            clean = subprocess.run(["git", "-C", str(staged), "status", "--porcelain"], check=True, capture_output=True, text=True)
            self.assertEqual("", clean.stdout)
            status, detail = source_evidence(staged)
            self.assertEqual("FAIL", status)
            self.assertIn("staged snapshot", detail)

    def test_source_reports_full_revision_without_claiming_freshness(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.run(["git", "-C", str(root), *args], check=True, capture_output=True, text=True).stdout.strip()
            git("init", "-q")
            (root / "VERSION").write_text("1.0.0\n")
            git("add", "VERSION")
            git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "chore: fixture")
            status, detail = source_evidence(root)
            self.assertEqual("PASS", status)
            self.assertIn(git("rev-parse", "HEAD"), detail)
            self.assertIn("remote freshness unverified", detail)
            (root / "VERSION").write_text("2.0.0\n")
            self.assertEqual("FAIL", source_evidence(root)[0])

    def test_unknown_and_sandbox_cannot_certify_host(self):
        self.assertEqual("FAIL", execution_evidence("unknown", {})[0])
        self.assertEqual("FAIL", execution_evidence("sandbox", {})[0])
        self.assertEqual("FAIL", execution_evidence("host", {"CODEX_SANDBOX": "seatbelt"})[0])
        self.assertEqual("PASS", execution_evidence("host", {})[0])
