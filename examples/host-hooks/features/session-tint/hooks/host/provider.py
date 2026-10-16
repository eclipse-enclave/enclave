# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

import json
import re
import sys


def respond(request):
    settings = request["settings"]
    if set(settings) - {"color"}:
        raise ValueError("unknown tint settings")
    color = settings.get("color", "")
    if not isinstance(color, str) or not re.fullmatch(r"#[0-9a-fA-F]{6}", color):
        raise ValueError("settings.color must be #rrggbb")
    response = {"schemaVersion": "1"}
    if request["terminal"]["colorSuppressed"]:
        return response
    response["terminalEffects"] = [{"resource": "background", "enter": "\x1b]11;" + color + "\x07", "leave": "\x1b]111\x07"}]
    return response


if __name__ == "__main__":
    try:
        request = json.load(sys.stdin)
        print(json.dumps(respond(request)))
    except (ValueError, KeyError, TypeError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
