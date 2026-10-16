# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

import os
from pathlib import Path
import runpy
import unittest
from unittest.mock import patch

provider = runpy.run_path(str(Path(__file__).parent / "hooks/host/provider.py"))
respond = provider["respond"]


class HerdrPolicyTests(unittest.TestCase):
    def request(self, action="run", **extra):
        request = {"settings": {}, "action": action, "background": False, "shell": False,
                   "tool": {"name": "custom", "entrypoint": ["/bin/claude"]},
                   "command": {"explicit": True, "argv": ["codex", "space value"]}}
        request.update(extra)
        return request

    def test_eligibility_and_empty_opt_out(self):
        with patch.dict(os.environ, {"HERDR_ENV": "1"}, clear=True):
            with patch.dict(respond.__globals__, {"catalog": lambda: {"claude", "codex"}}):
                for action in ("run", "continue", "resume", "exec"):
                    self.assertTrue(respond(self.request(action))["restartHostProcess"])
                for request in (self.request("attach"), self.request(shell=True),
                                self.request(background=True), self.request("exec", command={"explicit": False})):
                    self.assertNotIn("hostEnvironment", respond(request))
                os.environ["HERDR_AGENT"] = ""
                self.assertNotIn("hostEnvironment", respond(self.request()))

    def test_configured_alias_without_catalog_and_disabled(self):
        with patch.dict(os.environ, {"HERDR_ENV": "1"}, clear=True):
            with patch.dict(respond.__globals__, {"catalog": lambda: set()}):
                result = respond(self.request(settings={"agents": {"custom": "company-alias"}}))
                self.assertEqual(result["hostEnvironment"]["setIfAbsent"]["HERDR_AGENT"], "company-alias")
                self.assertNotIn("hostEnvironment", respond(self.request(settings={"agents": {"custom": ""}})))
                self.assertNotIn("hostEnvironment", respond(self.request("exec", settings={"agents": {"custom": "wrong"}})))

    def test_longest_prefix_ambiguity_and_literal_arguments(self):
        with patch.dict(os.environ, {"HERDR_ENV": "1"}, clear=True):
            settings = {"execRules": [{"argv": ["codex"], "agent": "short"},
                                      {"argv": ["codex", "space value"], "agent": "long"}]}
            result = respond(self.request("exec", settings=settings))
            self.assertEqual(result["hostEnvironment"]["setIfAbsent"]["HERDR_AGENT"], "long")
            settings["execRules"].append({"argv": ["codex", "space value"], "agent": "conflict"})
            self.assertNotIn("hostEnvironment", respond(self.request("exec", settings=settings)))

    def test_no_static_catalog_allowlist(self):
        request = self.request(tool={"name": "future", "entrypoint": ["future-agent"]})
        with patch.dict(os.environ, {"HERDR_ENV": "1"}, clear=True):
            with patch.dict(respond.__globals__, {"catalog": lambda: {"future-agent"}}):
                self.assertEqual(respond(request)["hostEnvironment"]["setIfAbsent"]["HERDR_AGENT"], "future-agent")


if __name__ == "__main__":
    unittest.main()
