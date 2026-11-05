// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Command enclave-a2a-hub is the A2A hub binary: the unprivileged proxy, the
// rule renderer the root supervisor calls, and host access over loopback.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"enclave/internal/a2a"
	"enclave/internal/a2ahub"
	"enclave/internal/peergroup"
)

const usage = "usage: enclave-a2a-hub proxy | render-rules [--snapshot-dir <dir>] | call <path>"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

// run dispatches the subcommands; exit 2 is a usage error.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	rest := args[1:]
	switch args[0] {
	case "proxy":
		return runProxy(rest, stderr, getenv)
	case "render-rules":
		return runRenderRules(rest, stdout, stderr, getenv)
	case "call":
		return runCall(rest, stdin, stdout, stderr, getenv)
	default:
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
}

func runProxy(args []string, stderr io.Writer, getenv func(string) string) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	logger := log.New(stderr, "enclave-a2a-hub proxy: ", log.LstdFlags|log.LUTC)
	cfg, err := a2ahub.ConfigFromEnv(getenv)
	if err != nil {
		logger.Print(err)
		return 1
	}
	if cfg.ProxyUID >= 0 && os.Geteuid() != cfg.ProxyUID {
		logger.Printf("running as uid %d, expected %s=%d", os.Geteuid(), a2a.EnvHubProxyUID, cfg.ProxyUID)
		return 1
	}
	// Registered before the first status write: the supervisor may signal as soon as it sees it.
	reloads := make(chan os.Signal, 1)
	signal.Notify(reloads, syscall.SIGHUP)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := a2ahub.Listen(ctx, cfg.Port, cfg.UserTimeout)
	if err != nil {
		logger.Printf("listen: %v", err)
		return 1
	}
	p, err := a2ahub.New(cfg, logger)
	if err != nil {
		logger.Print(err)
		return 1
	}
	logger.Printf("epoch %s listening on %s", p.Epoch(), ln.Addr())
	if err := p.Serve(ctx, ln, reloads); err != nil && ctx.Err() == nil {
		logger.Print(err)
		return 1
	}
	return 0
}

func runRenderRules(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("render-rules", flag.ContinueOnError)
	fs.SetOutput(stderr)
	defaultDir := getenv(a2a.EnvHubSnapshotDir)
	if defaultDir == "" {
		defaultDir = peergroup.HubSnapshotMountPath
	}
	dir := fs.String("snapshot-dir", defaultDir, "snapshot root holding current")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	gen := peergroup.Generation(strings.TrimSpace(getenv(a2a.EnvGroupGeneration)))
	port := 0
	if v := strings.TrimSpace(getenv(a2a.EnvHubPort)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "render-rules: %s %q is not a port\n", a2a.EnvHubPort, v)
			return 1
		}
		port = n
	}
	rules, err := a2ahub.RenderRules(*dir, strings.TrimSpace(getenv(a2a.EnvGroup)), gen, port, nil)
	if err != nil {
		// CR-A4-3: the revision line comes first, the reason is the last line.
		if rules.Revision != 0 {
			_, _ = fmt.Fprintf(stderr, "revision=%d\n", rules.Revision)
		}
		_, _ = fmt.Fprintf(stderr, "render-rules: %v\n", err)
		return 1
	}
	for _, w := range rules.Warnings {
		_, _ = fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	if _, err := io.WriteString(stdout, rules.Restore); err != nil {
		_, _ = fmt.Fprintf(stderr, "render-rules: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "revision=%d\n", rules.Revision)
	return 0
}

func runCall(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	port := strconv.Itoa(peergroup.HubPort)
	if v := strings.TrimSpace(getenv(a2a.EnvHubPort)); v != "" {
		port = v
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ok, err := a2ahub.Call(ctx, "http://127.0.0.1:"+port, args[0], stdin, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "call: %v\n", err)
		return 1
	}
	if !ok {
		return 1
	}
	return 0
}
