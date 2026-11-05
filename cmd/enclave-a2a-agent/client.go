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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/mcp"
)

const clientUsage = `usage: enclave-a2a-agent client [--journal-dir <dir>] <op> [flags] [args]

ops (flags before arguments):
  peers                                      list worker peers
  send [--context <ctx:...>] [--wait <dur>] [--retry] [--message-id <id>] <peer> <text|->
                                             send a task; "-" reads the text from stdin
  get [--wait <dur>] [<task:...>]            task state; without a handle, recent sends
  cancel <task:...>                          cancel a task
  hold                                       hold the orchestrator liveness connection until interrupted

Output is the JSON result of the matching MCP tool. Exit codes: 0 completed or
success, 1 error, 2 usage, 4 wait elapsed (the task continues), 5 another
terminal state, 130 interrupted.`

// loadConfig is replaced in tests.
var loadConfig = a2aagent.LoadConfig

// newClient builds the orchestrator client; the journal directory defaults to ENCLAVE_A2A_TABLE_DIR (CR-A11-1).
func newClient(journalDir string, environ []string, log io.Writer) (*mcp.Client, error) {
	cfg, err := loadConfig(environ)
	if err != nil {
		return nil, err
	}
	if journalDir == "" {
		journalDir = cfg.TableDir
	}
	if journalDir == "" {
		return nil, fmt.Errorf("send journal directory not set: pass --journal-dir or set %s", a2a.EnvTableDir)
	}
	return mcp.New(mcp.Options{Config: cfg, JournalDir: journalDir, Log: log})
}

// runClient is the plain CLI fallback for tools without MCP.
func runClient(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return clientMain(ctx, args, os.Environ(), os.Stdin, os.Stdout, os.Stderr)
}

func clientMain(ctx context.Context, args, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usageErr := func(msg string) int {
		if msg != "" {
			_, _ = fmt.Fprintln(stderr, "enclave-a2a-agent client:", msg) // #nosec G705 -- terminal output, not HTML.
		}
		_, _ = fmt.Fprintln(stderr, clientUsage)
		return 2
	}
	global := flag.NewFlagSet("client", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	journalDir := global.String("journal-dir", "", "")
	if err := global.Parse(args); err != nil {
		return usageErr(err.Error())
	}
	if global.NArg() == 0 {
		return usageErr("")
	}
	op, rest := global.Arg(0), global.Args()[1:]
	opFlags := flag.NewFlagSet(op, flag.ContinueOnError)
	opFlags.SetOutput(io.Discard)
	var (
		contextHandle, messageID string
		retry                    bool
		wait                     waitFlag
	)
	switch op {
	case "send":
		opFlags.StringVar(&contextHandle, "context", "", "")
		opFlags.Var(&wait, "wait", "")
		opFlags.BoolVar(&retry, "retry", false, "")
		opFlags.StringVar(&messageID, "message-id", "", "")
	case "get":
		opFlags.Var(&wait, "wait", "")
	case "peers", "cancel", "hold":
	default:
		return usageErr("unknown op " + op)
	}
	if err := opFlags.Parse(rest); err != nil {
		return usageErr(err.Error())
	}
	positional := opFlags.Args()
	want := map[string][2]int{"peers": {0, 0}, "send": {2, 2}, "get": {0, 1}, "cancel": {1, 1}, "hold": {0, 0}}[op]
	if len(positional) < want[0] || len(positional) > want[1] {
		return usageErr("wrong number of arguments for " + op)
	}

	client, err := newClient(*journalDir, environ, stderr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "enclave-a2a-agent client:", err)
		return 1
	}
	var res mcp.Result
	switch op {
	case "hold":
		client.HoldLiveness(ctx)
		return 0
	case "peers":
		res, err = client.ListPeers(ctx)
	case "send":
		text := positional[1]
		if text == "-" {
			data, readErr := io.ReadAll(io.LimitReader(stdin, mcp.DefaultMaxRequestBytes*4))
			if readErr != nil {
				_, _ = fmt.Fprintln(stderr, "enclave-a2a-agent client: read stdin:", readErr)
				return 1
			}
			text = string(data)
		}
		res, err = client.Send(ctx, mcp.SendRequest{
			Peer: positional[0], Text: text, Context: contextHandle,
			Wait: wait.value, Retry: retry, MessageID: messageID,
		}, nil)
	case "get":
		handle := ""
		if len(positional) == 1 {
			handle = positional[0]
		}
		res, err = client.Get(ctx, handle, wait.value, nil)
	case "cancel":
		res, err = client.Cancel(ctx, positional[0])
	}
	if errors.Is(err, mcp.ErrStopped) {
		return 130
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "enclave-a2a-agent client:", err)
		return 1
	}
	out, err := json.Marshal(res)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "enclave-a2a-agent client:", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, string(out)) // #nosec G705 -- JSON on stdout, not HTML.
	if op == "cancel" && res.State == string(a2a.TaskStateCanceled) {
		return 0
	}
	return res.ExitCode()
}

// waitFlag distinguishes an unset --wait from --wait 0.
type waitFlag struct{ value *time.Duration }

func (w *waitFlag) String() string {
	if w.value == nil {
		return ""
	}
	return w.value.String()
}

func (w *waitFlag) Set(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	if d < 0 {
		return errors.New("must not be negative")
	}
	w.value = &d
	return nil
}
