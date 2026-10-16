# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

from pathlib import Path
import runpy
import unittest

respond = runpy.run_path(str(Path(__file__).parent / "hooks/host/provider.py"))["respond"]


class TintTests(unittest.TestCase):
    def test_sequences_and_suppression(self):
        request = {"settings": {"color": "#2a0f12"}, "terminal": {"colorSuppressed": False}}
        effect = respond(request)["terminalEffects"][0]
        self.assertEqual(effect, {"resource": "background", "enter": "\x1b]11;#2a0f12\x07", "leave": "\x1b]111\x07"})
        request["terminal"]["colorSuppressed"] = True
        self.assertNotIn("terminalEffects", respond(request))
        request["settings"]["color"] = "\x1bBAD"
        with self.assertRaises(ValueError):
            respond(request)


if __name__ == "__main__":
    unittest.main()
