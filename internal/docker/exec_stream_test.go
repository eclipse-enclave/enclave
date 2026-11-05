// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bufio"
	"context"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExecStreamDeliversOutputBeforeExit(t *testing.T) {
	logPath := installNetworkDockerStub(t, `cat > "$0.stdin"; printf 'first\n'; exec sleep 30`)
	stream, err := ExecStream(context.Background(), "hub", "65532", []string{"tool", "call", "/peers/w1"}, strings.NewReader("cred\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(stream).ReadString('\n')
		line <- s
	}()
	select {
	case got := <-line:
		if got != "first\n" {
			t.Fatalf("first line = %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("output was buffered until exit")
	}
	start := time.Now()
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Close took %s", elapsed)
	}
	if got, want := readNetworkDockerArgs(t, logPath), []string{"exec", "--interactive", "--user", "65532", "hub", "tool", "call", "/peers/w1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	stdin, err := os.ReadFile(dockerBinary + ".stdin")
	if err != nil || string(stdin) != "cred\nbody" {
		t.Fatalf("stdin = %q, %v", stdin, err)
	}
}

func TestExecStreamReportsFailureAfterOutput(t *testing.T) {
	installNetworkDockerStub(t, `printf 'partial'; echo boom >&2; exit 3`)
	stream, err := ExecStream(context.Background(), "hub", "", []string{"tool"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	out, err := io.ReadAll(stream)
	if string(out) != "partial" || err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("ReadAll() = %q, %v", out, err)
	}
}
