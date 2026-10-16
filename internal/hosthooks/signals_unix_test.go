// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package hosthooks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTerminalTerminationRestoresAndReraises(t *testing.T) {
	if os.Getenv("HOSTHOOK_SIGNAL_HELPER") == "1" {
		provider := Provider{ID: "builtin:test", Phases: map[string]string{Enter: ""}, Builtin: func(context.Context, Request) (Response, error) {
			return Response{SchemaVersion: Version, TerminalEffects: []Effect{{Resource: "test", Enter: "ENTER", Leave: "RESET"}}}, nil
		}}
		scope := (Runner{}).Begin(context.Background(), []Provider{provider}, Request{Terminal: Terminal{StdoutIsTTY: true}}, os.Environ(), os.Stdout)
		defer scope.Close(Outcome{})
		fmt.Println("READY")
		select {}
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalTerminationRestoresAndReraises$")
			cmd.Env = append(os.Environ(), "HOSTHOOK_SIGNAL_HELPER=1")
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(pipe)
			first, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if first != "ENTERREADY\n" {
				t.Fatal(first)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			rest, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("signal not preserved: %v", err)
			}
			status := exit.Sys().(syscall.WaitStatus)
			if !status.Signaled() || status.Signal() != sig || strings.Count(string(rest), "RESET") != 1 {
				t.Fatalf("status=%v output=%q", status, rest)
			}
		})
	}
}
