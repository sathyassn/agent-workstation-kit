import importlib.util
from pathlib import Path
import sys
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
spec = importlib.util.spec_from_file_location("setup_workstation", ROOT / "scripts/setup-workstation.py")
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)


class SetupPlanTests(unittest.TestCase):
    def test_changed_plan_invalidates_all_checkpoints(self):
        self.assertEqual([key for key, _ in plan.ACCEPTANCE], plan.remaining("new", {"plan_id": "old"}))
        self.assertNotEqual(plan.plan_id("source", "profile", "target", "commands"), plan.plan_id("changed", "profile", "target", "commands"))

    def test_evidence_change_invalidates_checkpoint_and_successors(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory) / "evidence.txt"
            evidence.write_text("operator observed result")
            journal = {"plan_id": "same", "checkpoints": {key: {"status": "verified", "evidence": [{"path": str(evidence), "sha256": plan.digest(evidence)}]} for key, _ in plan.ACCEPTANCE}}
            self.assertEqual([], plan.remaining("same", journal))
            journal["checkpoints"]["tcp"]["status"] = "failed"
            self.assertEqual("tcp", plan.remaining("same", journal)[0])
            evidence.write_text("changed result")
            self.assertEqual("os-recovery", plan.remaining("same", journal)[0])

    def test_absent_or_missing_evidence_is_not_verified(self):
        journal = {"plan_id": "same", "checkpoints": {"os-recovery": {"status": "verified", "evidence": [{"path": "/missing/evidence", "sha256": "0"}]}}}
        self.assertEqual("os-recovery", plan.remaining("same", journal)[0])

    def test_preview_cli_for_both_platforms_is_read_only(self):
        settings = {
            "asset_tag": "AC-10001", "tailscale_tailnet": "organization.example",
            "desktop_lock_mode": "dedicated-shared", "gws": "skip",
            "secrets_provider": "organization-vault", "antidote_ref": "v1.9.10",
            "atlassian_site": "company.atlassian.net", "atlassian_identity": "service-account",
            "atlassian_principal": "acagentdev", "atlassian_mcp_auth": "service-account-api-key",
            "endpoint_management": "mdm-and-edr", "target": "approved-backup",
            "update_window": "Sunday 02:00-04:00", "owner": "platform-team",
        }
        for template in ("work.example.toml", "macos-work.example.toml"):
            with self.subTest(template=template), tempfile.TemporaryDirectory() as directory:
                fleet = Path(directory)
                (fleet / "machines").mkdir()
                text = (ROOT / "config/profiles" / template).read_text().replace('state = "draft"', 'state = "approved"')
                lines = []
                for line in text.splitlines():
                    key = line.partition("=")[0].strip()
                    if key in settings and '"ask"' in line:
                        line = f'{key} = "{settings[key]}"'
                    lines.append(line)
                profile = fleet / "machines/ac-mac-001.toml"
                profile.write_text("\n".join(lines) + "\n")
                (fleet / "kit.lock").write_text((ROOT / "VERSION").read_text())
                before = {str(p): p.read_bytes() for p in fleet.rglob("*") if p.is_file()}
                result = subprocess.run([sys.executable, str(ROOT / "scripts/setup-workstation.py"), "--fleet-root", str(fleet), "--profile", str(profile)], capture_output=True, text=True)
                self.assertEqual(0, result.returncode, result.stderr)
                self.assertIn("PREVIEW ONLY", result.stdout)
                self.assertIn("PENDING listener:", result.stdout)
                self.assertIn("PENDING phase-base:", result.stdout)
                self.assertIn("Next: os-recovery", result.stdout)
                self.assertEqual(before, {str(p): p.read_bytes() for p in fleet.rglob("*") if p.is_file()})
