# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

from copy import deepcopy
import json
from pathlib import Path
import runpy
import subprocess
import unittest


ROOT = Path(__file__).resolve().parent
provider = runpy.run_path(str(ROOT / "hooks/host/provider.py"))
respond = provider["respond"]


def request():
    return {
        "schemaVersion": "1", "phase": "terminal-enter", "action": "run",
        "background": False, "settings": {},
        "tool": {"name": "codex"}, "project": {"path": "/work/payment-service"},
        "terminal": {"stdoutIsTTY": True, "colorSuppressed": False},
    }


class TitleTests(unittest.TestCase):
    def test_saves_sets_and_restores_window_title(self):
        self.assertEqual(respond(request()), {
            "schemaVersion": "1",
            "terminalEffects": [{
                "resource": "title",
                "enter": "\x1b[22;2t\x1b]2;Enclave · codex · payment-service\x07",
                "leave": "\x1b[23;2t",
            }],
        })

    def test_target_context_on_exec_and_attach(self):
        for action in ("run", "exec", "attach", "shell"):
            with self.subTest(action=action):
                value = request()
                value.update(action=action, settings={"prefix": "Sandbox"})
                value["tool"]["name"] = "claude"
                value["project"]["path"] = "/other/project/"
                self.assertIn("Sandbox · claude · project", respond(value)["terminalEffects"][0]["enter"])

    def test_title_is_not_a_color_effect(self):
        value = request()
        value["terminal"]["colorSuppressed"] = True
        self.assertIn("terminalEffects", respond(value))

    def test_non_tty_and_background_are_noops(self):
        for background, tty in ((True, True), (False, False)):
            with self.subTest(background=background, tty=tty):
                value = request()
                value["background"] = background
                value["terminal"]["stdoutIsTTY"] = tty
                self.assertEqual(respond(value), {"schemaVersion": "1"})

    def test_labels_cannot_inject_terminal_sequences(self):
        value = request()
        value["settings"]["prefix"] = "Demo\x1b]52;c;payload\x07"
        value["tool"]["name"] = "codex\x9c\nnext"
        value["project"]["path"] = "/work/evil\x1b\\\x07\x00\rname"
        enter = respond(value)["terminalEffects"][0]["enter"]
        text = enter.removeprefix("\x1b[22;2t\x1b]2;").removesuffix("\x07")
        self.assertTrue(text.isprintable())
        self.assertNotIn("\x1b", text)
        self.assertNotIn("\x07", text)
        self.assertNotIn("\x9c", text)

    def test_unicode_title_is_bounded_without_split_characters(self):
        value = request()
        value["project"]["path"] = "/work/" + "🍀" * 1000
        effect = respond(value)["terminalEffects"][0]
        title = effect["enter"][len("\x1b[22;2t\x1b]2;"):-1]
        self.assertLessEqual(len(title.encode("utf-8")), 512)
        self.assertNotIn("�", title)
        self.assertLess(len(effect["enter"].encode("utf-8")), 4096)

    def test_empty_metadata_fallback(self):
        value = request()
        value["tool"]["name"] = ""
        value["project"]["path"] = ""
        self.assertIn("Enclave · session · project", respond(value)["terminalEffects"][0]["enter"])

    def test_invalid_settings_and_wrong_phase_are_rejected(self):
        for settings in ({"unknown": True}, {"prefix": 3}, {"prefix": "\x1b\x07"}, []):
            with self.subTest(settings=settings):
                value = request()
                value["settings"] = settings
                with self.assertRaises(ValueError):
                    respond(value)
        for key, value in (("schemaVersion", "2"), ("phase", "prepare")):
            invalid = deepcopy(request())
            invalid[key] = value
            with self.assertRaises(ValueError):
                respond(invalid)

    def test_executable_returns_one_json_object_not_raw_terminal_output(self):
        result = subprocess.run(
            [str(ROOT / "hooks/host/terminal-enter")],
            input=json.dumps(request()), capture_output=True, text=True, timeout=2,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        self.assertNotIn("\x1b", result.stdout)
        self.assertEqual(json.loads(result.stdout), respond(request()))


if __name__ == "__main__":
    unittest.main()
