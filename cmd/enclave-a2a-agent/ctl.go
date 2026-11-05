// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent/server"
)

const (
	ctlUsage        = "usage: enclave-a2a-agent ctl [depart|release-admission|release-quarantine|status] < request.json"
	ctlTimeout      = 2 * time.Minute
	ctlMaxStdinSize = 64 << 10
)

// runCtl reads an a2a.CtlRequest from stdin, applies it to the running
// adapter and writes an a2a.CtlResponse to stdout. Owned by A10.
func runCtl(args []string) int {
	return ctl(args, os.Stdin, os.Stdout, server.CtlSocketPath)
}

// ctl exits 0 when the response is OK, 1 when it is not or the adapter is unreachable, 2 on usage errors.
func ctl(args []string, stdin io.Reader, stdout io.Writer, socket string) int {
	respond := func(resp a2a.CtlResponse, code int) int {
		_ = json.NewEncoder(stdout).Encode(resp)
		return code
	}
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, ctlUsage)
		return 2
	}
	data, err := io.ReadAll(io.LimitReader(stdin, ctlMaxStdinSize))
	if err != nil {
		return respond(a2a.CtlResponse{Error: "read stdin: " + err.Error()}, 1)
	}
	var req a2a.CtlRequest
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Fprintln(os.Stderr, ctlUsage)
		return respond(a2a.CtlResponse{Error: "stdin is not a ctl request"}, 2)
	}
	if len(args) == 1 {
		if req.Op != "" && req.Op != args[0] {
			fmt.Fprintln(os.Stderr, ctlUsage)
			return respond(a2a.CtlResponse{Op: req.Op, Error: fmt.Sprintf("op %q on argv contradicts op %q on stdin", args[0], req.Op)}, 2)
		}
		req.Op = args[0]
	}
	switch req.Op {
	case a2a.CtlDepart, a2a.CtlReleaseAdmission, a2a.CtlReleaseQuarantine, a2a.CtlStatus:
	default:
		fmt.Fprintln(os.Stderr, ctlUsage)
		return respond(a2a.CtlResponse{Op: req.Op, Error: fmt.Sprintf("unknown ctl op %q", req.Op)}, 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctlTimeout)
	defer cancel()
	resp, err := server.CallCtl(ctx, socket, req)
	if err != nil {
		return respond(a2a.CtlResponse{Op: req.Op, Error: err.Error()}, 1)
	}
	if !resp.OK {
		return respond(resp, 1)
	}
	return respond(resp, 0)
}
