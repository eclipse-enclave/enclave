// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

export default function (pi) {
  pi.registerMcpServer("enclave-a2a", {
    command: "enclave-a2a-agent",
    args: ["mcp"],
    exposure: "direct",
    timeout: 120,
  });
}
