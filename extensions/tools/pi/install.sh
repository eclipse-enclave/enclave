#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -e

# Upstream ships prebuilt native modules and needs no lifecycle scripts; their
# own installer and `pi update --self` pass --ignore-scripts too.
enclave-install-npm-tool --ignore-scripts @earendil-works/pi-coding-agent@latest pi Pi
