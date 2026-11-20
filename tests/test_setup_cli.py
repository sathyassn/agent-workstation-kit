from __future__ import annotations
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts/lib"))
import setup_cli
from terminal_ui import UI, clean


class SetupCLIUXTests(unittest.TestCase):
    def test_noninteractive_default_is_help_not_a_prompt_or_apply(self):
        with mock.patch.object(sys.stdin, "isatty", return_value=False), mock.patch("sys.stdout", new=io.StringIO()) as output, mock.patch.object(setup_cli, "call") as call:
            self.assertEqual(0, setup_cli.main([]))
            self.assertIn("one setup command", output.getvalue())
            call.assert_not_called()

    def test_verbs_route_to_fixed_helpers_with_no_shell_evaluation(self):
        for verb, helper in (("plan", "setup-workstation.py"), ("run", "execute-setup.py"), ("resume", "execute-setup.py"), ("status", "execute-setup.py"), ("check-access", "verify-remote-access.py")):
            with self.subTest(verb=verb), mock.patch.object(setup_cli, "call", return_value=0) as call:
                self.assertEqual(0, setup_cli.main([verb, "--help"]))
                self.assertEqual(helper, call.call_args.args[0])
                if verb == "status":
                    self.assertIn("--status", call.call_args.args[1])

    def test_invalid_action_explains_valid_choices(self):
        with mock.patch("sys.stdout", new=io.StringIO()) as output:
            self.assertEqual(2, setup_cli.main(["does-not-exist"]))
            self.assertIn("Unknown action", output.getvalue())
            self.assertIn("check-access", output.getvalue())

    def test_guide_refuses_pipe_and_handles_cancel_without_traceback(self):
        with mock.patch.object(sys.stdin, "isatty", return_value=False), mock.patch("sys.stdout", new=io.StringIO()) as output:
            self.assertEqual(2, setup_cli.main(["guide"]))
            self.assertIn("interactive terminal", output.getvalue())
        with mock.patch.object(setup_cli, "guide", side_effect=KeyboardInterrupt), mock.patch("sys.stdout", new=io.StringIO()) as output:
            self.assertEqual(130, setup_cli.main(["guide"]))
            self.assertIn("receipts are retained", output.getvalue())
        # End of input (Ctrl-D or a closed pipe) is a cancellation, not success.
        with mock.patch.object(setup_cli, "guide", side_effect=EOFError), mock.patch("sys.stdout", new=io.StringIO()) as output:
            self.assertEqual(130, setup_cli.main(["guide"]))
            self.assertIn("Cancelled", output.getvalue())

    def test_choice_retries_invalid_input_and_blank_uses_safe_default(self):
        output = io.StringIO()
        ui = UI(plain=True, stream=output)
        with mock.patch("builtins.input", side_effect=["invalid", ""]):
            self.assertEqual("no", ui.choice("Apply?", ("yes", "no"), "no"))
        self.assertIn("Choose one of", output.getvalue())

    def test_existing_private_output_and_control_characters_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            existing = Path(directory) / "plan.json"
            existing.write_text("preserved")
            new = Path(directory) / "new.json"
            ui = UI(plain=True, stream=io.StringIO())
            with mock.patch("builtins.input", side_effect=["bad\x1bpath", str(existing), str(new)]):
                self.assertEqual(new, setup_cli.path_input(ui, "output", new=True))
            self.assertEqual("preserved", existing.read_text())

    def test_rich_is_optional_and_plain_output_has_no_escape_codes(self):
        output = io.StringIO()
        ui = UI(plain=True, stream=output)
        ui.heading("Title")
        ui.table(("Phase", "Status"), [("base", "pending")])
        ui.say("\x1b[31muntrusted\x00value")
        text = output.getvalue()
        self.assertNotIn("\x1b", text)
        self.assertNotIn("\x00", text)
        self.assertIn("base | pending", text)

    @unittest.skipIf(os.geteuid() == 0, "needs a real non-root account for pwd lookups")
    def test_blank_guided_approval_does_not_apply_or_create_a_journal(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "plan.json"
            plan = {"profile": {"machine": {"hostname": "lab-mac-001", "platform": "macos"}, "accounts": {"agent": "agent-01"}}, "phases": [{"name": "base", "owner": "package-owner"}]}
            path.write_text(json.dumps(plan))
            journal = Path(directory) / "state/run.json"
            engine = mock.Mock()
            engine.SCOPES = {"macos": {"packages": ["base"]}}
            engine.approval_id.return_value = "reviewed"
            ui = UI(plain=True, stream=io.StringIO())
            with mock.patch.object(setup_cli, "path_input", side_effect=[path, journal]), mock.patch.object(ui, "choice", return_value="packages"), mock.patch.object(ui, "ask", return_value=""), mock.patch.object(setup_cli, "load", return_value=engine), mock.patch.object(setup_cli, "call", return_value=0) as call, mock.patch.object(setup_cli.os, "geteuid", return_value=os.geteuid()):
                self.assertEqual(0, setup_cli.run_guide(ui))
                self.assertEqual(1, call.call_count)
                self.assertNotIn("--apply", call.call_args.args[1])
                self.assertFalse(journal.exists())

    def test_bundle_changed_during_preview_never_reaches_approval(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "plan.json"
            path.write_text(json.dumps({"profile": {}, "phases": []}))
            engine = mock.Mock()
            engine.approval_id.return_value = "reviewed"
            def preview(*_):
                path.write_text("{}")
                return 0
            ui = UI(plain=True, stream=io.StringIO())
            with mock.patch.object(setup_cli, "path_input", side_effect=[path, Path(directory) / "run.json"]), mock.patch.object(ui, "choice", return_value="packages"), mock.patch.object(ui, "ask") as ask, mock.patch.object(setup_cli, "load", return_value=engine), mock.patch.object(setup_cli, "call", side_effect=preview):
                self.assertEqual(2, setup_cli.run_guide(ui))
                ask.assert_not_called()

    def test_system_python_can_show_native_help_without_tomllib_or_rich(self):
        if not Path("/usr/bin/python3").exists():
            self.skipTest("OS-owned Python not installed on this test host")
        result = subprocess.run(["/usr/bin/python3", str(ROOT / "scripts/lib/setup_cli.py"), "--help", "--plain"], text=True, capture_output=True)
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertIn("one setup command", result.stdout)


class TerminalRenderingTests(unittest.TestCase):
    def tty_stream(self):
        class Terminal(io.StringIO):
            def isatty(self):
                return True
        return Terminal()

    def test_missing_rich_keeps_the_full_walkthrough_available(self):
        output = self.tty_stream()
        with mock.patch.dict(sys.modules, {"rich.console": None}):
            ui = UI(stream=output)
            self.assertIsNone(ui.console)
            setup_cli.help_text(ui)
        self.assertIn("one setup command", output.getvalue())
        self.assertIn("resume", output.getvalue())

    def test_rich_table_folds_long_fields_at_narrow_terminal_width(self):
        output = self.tty_stream()
        ui = UI(stream=output)
        if ui.console is None:
            self.skipTest("optional Rich not installed; plain fallback covered separately")
        ui.console.width = 48
        ui.console.height = 25
        ui.table(("Phase", "Action"), [("remote-hardening", "Verify policy, apply the approved configuration, then check reconnect")])
        lines = clean(output.getvalue()).splitlines()
        self.assertTrue(lines)
        self.assertTrue(all(len(line) <= 48 for line in lines))
        self.assertIn("reconnect", " ".join(lines))


class DraftProfileCLITests(unittest.TestCase):
    def test_draft_preview_creates_no_profile_or_uuid(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch("sys.stdout", new=io.StringIO()) as output, mock.patch.object(setup_cli, "call") as call:
            args = ["init", "machines/lab-ws-001.toml", "--fleet-root", directory, "--context", "personal", "--namespace", "lab"]
            self.assertEqual(0, setup_cli.main(args))
            self.assertIn("No profile/UUID was allocated", output.getvalue())
            call.assert_not_called()
            self.assertEqual([], list(Path(directory).iterdir()))

    def test_write_delegates_existing_atomic_initializer_and_preserves_existing_profile(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(setup_cli, "call", return_value=0) as call:
            args = ["init", "machines/lab-ws-001.toml", "--fleet-root=" + directory, "--context", "personal", "--write"]
            self.assertEqual(0, setup_cli.main(args))
            self.assertEqual("fleetctl.py", call.call_args.args[0])
            self.assertEqual(["--fleet-root", str(Path(directory).resolve()), "init", "machines/lab-ws-001.toml", "--context", "personal"], call.call_args.args[1])
            folder = Path(directory) / "machines"
            folder.mkdir()
            profile = folder / "lab-ws-001.toml"
            profile.write_text("preserve")
            call.reset_mock()
            with mock.patch("sys.stdout", new=io.StringIO()):
                self.assertEqual(2, setup_cli.main(args))
            call.assert_not_called()
            self.assertEqual("preserve", profile.read_text())


class PrivateStorageBoundaryTests(unittest.TestCase):
    def test_private_outputs_cannot_land_in_another_public_worktree(self):
        import path_safety
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            current = parent / "feature"
            main = parent / "main checkout"
            current.mkdir()
            (current / ".git").write_text("gitdir: fixture")
            data = "worktree " + str(main) + "\0HEAD fixture\0\0worktree " + str(current) + "\0HEAD fixture\0\0"
            with mock.patch.object(path_safety.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, data, "")):
                self.assertFalse(path_safety.outside_kit(main / "private-plan.json", current))
                self.assertFalse(path_safety.outside_kit(current / "private-plan.json", current))
                self.assertTrue(path_safety.outside_kit(parent / "private-data/plan.json", current))


class NoWritesWithoutApplyTests(unittest.TestCase):
    def test_plan_refuses_a_fleet_root_inside_the_public_kit(self):
        result = subprocess.run(
            [sys.executable, str(ROOT / "scripts/setup-workstation.py"),
             "--fleet-root", str(ROOT / "templates/private-fleet"),
             "--profile", "machines/acme-ws-001.toml"],
            capture_output=True, text=True)
        self.assertEqual(2, result.returncode)
        self.assertIn("outside every public toolkit worktree", result.stderr)

    @unittest.skipIf(os.geteuid() == 0, "the walkthrough refuses root before asking")
    def test_guided_plan_export_creates_no_folders(self):
        with tempfile.TemporaryDirectory() as directory:
            fleet = Path(directory) / "fleet"
            (fleet / "machines").mkdir(parents=True)
            profile = fleet / "machines/acme-ws-001.toml"
            profile.write_text("")
            output = Path(directory) / "missing/plan.json"
            ui = UI(plain=True, stream=io.StringIO())
            with mock.patch.object(setup_cli, "path_input", side_effect=[fleet, profile, output]), \
                    mock.patch.object(ui, "choice", return_value="local-console"), \
                    mock.patch.object(setup_cli, "call") as call:
                self.assertEqual(2, setup_cli.plan_guide(ui))
            call.assert_not_called()
            self.assertFalse(output.parent.exists())

    def test_wrapper_disables_bytecode_writes(self):
        wrapper = (ROOT / "scripts/setup-workstation.sh").read_text()
        self.assertIn("export PYTHONDONTWRITEBYTECODE=1", wrapper)
        self.assertIn("PYTHONDONTWRITEBYTECODE=1 \\\n    /usr/bin/python3 -I", wrapper)
