// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Command enclave-a2a-agent is the in-container A2A binary: the worker
// adapter (serve, ctl) and the orchestrator client (mcp, client).
package main

import (
	"fmt"
	"os"
)

const usage = "usage: enclave-a2a-agent serve|ctl|mcp|client [args]"

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches to the subcommand files: serve.go and ctl.go (A10), mcp.go
// and client.go (A11). Exit 2 is a usage error.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	rest := args[1:]
	switch args[0] {
	case "serve":
		return runServe(rest)
	case "ctl":
		return runCtl(rest)
	case "mcp":
		return runMCP(rest)
	case "client":
		return runClient(rest)
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
}
