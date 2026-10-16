# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

import json
import os
from pathlib import Path
import subprocess
import sys


def catalog():
    try:
        output = subprocess.run(
            [os.environ.get("HERDR_BIN_PATH") or "herdr", "server", "agent-manifests", "--json"],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            timeout=1, check=True,
        ).stdout
        if len(output) > 65536:
            raise ValueError("catalog exceeds limit")
        result = json.loads(output)["result"]
        if result["type"] != "agent_manifest_status":
            raise ValueError("unexpected catalog type")
        return {item["agent"] for item in result["manifests"] if isinstance(item["agent"], str)}
    except (OSError, subprocess.SubprocessError, ValueError, KeyError, TypeError) as error:
        print("Herdr catalog unavailable: " + str(error), file=sys.stderr)
        return set()


def settings_policy(settings):
    if set(settings) - {"agents", "execRules"}:
        raise ValueError("unknown Herdr settings")
    agents = settings.get("agents", {})
    rules = settings.get("execRules", [])
    if not isinstance(agents, dict) or not all(isinstance(k, str) and isinstance(v, str) for k, v in agents.items()):
        raise ValueError("agents must map tool names to labels (empty disables)")
    if not isinstance(rules, list):
        raise ValueError("execRules must be an array")
    for rule in rules:
        if not isinstance(rule, dict) or set(rule) != {"argv", "agent"}:
            raise ValueError("execRules entries require argv and agent")
        if not isinstance(rule["agent"], str) or not isinstance(rule["argv"], list) or not rule["argv"] or not all(isinstance(v, str) and v for v in rule["argv"]):
            raise ValueError("execRules require nonempty argv strings and a string agent")
    return agents, rules


def matches(prefix, argv):
    return len(argv) >= len(prefix) and Path(prefix[0]).name == Path(argv[0]).name and prefix[1:] == argv[1:len(prefix)]


def respond(request):
    agents, rules = settings_policy(request["settings"])
    noop = {"schemaVersion": "1"}
    if os.environ.get("HERDR_ENV") != "1" or "HERDR_AGENT" in os.environ:
        return noop
    action = request["action"]
    if request["background"] or request["shell"] or action not in ("run", "continue", "resume", "exec"):
        return noop
    label = None
    if action == "exec":
        if not request["command"]["explicit"]:
            return noop
        argv = request["command"]["argv"]
        matching = [rule for rule in rules if matches(rule["argv"], argv)]
        if matching:
            longest = max(len(rule["argv"]) for rule in matching)
            labels = {rule["agent"] for rule in matching if len(rule["argv"]) == longest}
            if len(labels) != 1:
                print("ambiguous Herdr execRules; no hint", file=sys.stderr)
                return noop
            label = labels.pop()
    else:
        argv = request["tool"]["entrypoint"]
        label = agents.get(request["tool"]["name"])
    if label is None and argv:
        candidate = Path(argv[0]).name
        if candidate in catalog():
            label = candidate
    if not label:
        return noop
    return {"schemaVersion": "1", "hostEnvironment": {"setIfAbsent": {"HERDR_AGENT": label}}, "restartHostProcess": True}


if __name__ == "__main__":
    try:
        print(json.dumps(respond(json.load(sys.stdin))))
    except (ValueError, KeyError, TypeError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
