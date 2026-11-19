"""Optional Rich presentation with a dependency-free, non-interactive fallback."""
from __future__ import annotations
import os
import re
import sys


def clean(value):
    # Paths and external error strings must not inject terminal control codes.
    text = re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", str(value))
    return "".join(character for character in text if character in "\n\t" or ord(character) >= 32 and ord(character) != 127)


class UI:
    def __init__(self, plain=False, stream=None):
        self.stream = sys.stdout if stream is None else stream
        self.console = None
        if not plain and self.stream.isatty():
            try:
                from rich.console import Console
                self.console = Console(file=self.stream, markup=False, highlight=False, safe_box=True)
            except ImportError:
                pass

    def say(self, message="", style=None):
        message = clean(message)
        if self.console:
            self.console.print(message, style=style, markup=False, highlight=False, soft_wrap=True)
        else:
            print(message, file=self.stream)

    def heading(self, message):
        self.say(message, "bold")

    def table(self, headers, rows):
        if self.console:
            from rich.table import Table
            table = Table(show_edge=False, pad_edge=False, expand=False)
            for header in headers:
                table.add_column(clean(header), overflow="fold")
            for row in rows:
                table.add_row(*(clean(value) for value in row))
            self.console.print(table)
        else:
            self.say(" | ".join(headers))
            for row in rows:
                self.say(" | ".join(str(value) for value in row))

    def ask(self, prompt, default=None):
        self.say(prompt + (" [" + str(default) + "]" if default is not None else ""))
        value = input("> ").strip()
        return value if value else default

    def choice(self, prompt, choices, default=None):
        while True:
            value = self.ask(prompt + " (" + "/".join(choices) + ")", default)
            if value in choices:
                return value
            self.say("Choose one of: " + ", ".join(choices), "yellow")

    def outcome(self, code):
        if code == 0:
            self.say("Completed the selected action.", "green")
        elif code == 3:
            self.say("Human action required. Complete the displayed checkpoint, then resume the same scope.", "yellow")
        elif code == 130:
            self.say("Interrupted. Previously verified receipts are retained; rerun the same scope to resume.", "yellow")
        else:
            self.say("Stopped. Resolve the first reported error, then rerun the same scope; verified earlier phases will be rechecked.", "red")
