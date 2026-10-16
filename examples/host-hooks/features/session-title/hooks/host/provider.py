# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

"""Demonstrate terminal ownership using only a v1 terminal-enter response."""

import json
from pathlib import Path
import sys


SAVE_TITLE = "\x1b[22;2t"
RESTORE_TITLE = "\x1b[23;2t"
MAX_TITLE_BYTES = 512


def label(value):
    if not isinstance(value, str):
        raise ValueError("title labels must be strings")
    # Never let project names or settings terminate OSC and inject terminal code.
    return " ".join("".join(c if c.isprintable() else " " for c in value).split())


def respond(request):
    if request.get("schemaVersion") != "1" or request.get("phase") != "terminal-enter":
        raise ValueError("expected a v1 terminal-enter request")
    settings = request["settings"]
    if not isinstance(settings, dict) or set(settings) - {"prefix"}:
        raise ValueError("settings accepts only an optional string prefix")
    prefix = label(settings.get("prefix", "Enclave"))
    if not prefix:
        raise ValueError("settings.prefix must contain printable text")

    response = {"schemaVersion": "1"}
    if request["background"] or not request["terminal"]["stdoutIsTTY"]:
        return response

    tool = label(request["tool"]["name"]) or "session"
    project_path = request["project"]["path"]
    if not isinstance(project_path, str):
        raise ValueError("project.path must be a string")
    project = label(Path(project_path).name) or "project"
    title = f"{prefix} · {tool} · {project}"
    title = title.encode("utf-8")[:MAX_TITLE_BYTES].decode("utf-8", errors="ignore")
    response["terminalEffects"] = [{
        "resource": "title",
        "enter": SAVE_TITLE + "\x1b]2;" + title + "\x07",
        "leave": RESTORE_TITLE,
    }]
    return response


if __name__ == "__main__":
    try:
        print(json.dumps(respond(json.load(sys.stdin))))
    except (ValueError, KeyError, TypeError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
