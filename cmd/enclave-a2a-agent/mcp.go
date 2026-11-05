// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"enclave/internal/a2aagent/mcp"
)

const mcpUsage = "usage: enclave-a2a-agent mcp [--journal-dir <dir>]"

// runMCP serves the orchestrator's stdio MCP server and holds the liveness connection while it runs.
func runMCP(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	journalDir := fs.String("journal-dir", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(os.Stderr, mcpUsage)
		return 2
	}
	client, err := newClient(*journalDir, os.Environ(), os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "enclave-a2a-agent mcp:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcp.NewServer(client).Serve(ctx, os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "enclave-a2a-agent mcp:", err)
		return 1
	}
	return 0
}
