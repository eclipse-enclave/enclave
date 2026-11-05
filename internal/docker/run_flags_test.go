// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestBuildRunArgsRendersHardeningAndEnvFiles(t *testing.T) {
	args := buildRunArgs(
		&ContainerConfig{Image: "img", Env: []string{"A=1"}, EnvFiles: []string{"/tmp/secret.env"}},
		&HostConfig{NetworkMode: "net", IPv4Address: "10.0.0.14", ReadonlyRootfs: true, LogDriver: "none"},
		"hub", runMode{Detach: true},
	)
	for _, want := range [][]string{
		{"--env", "A=1", "--env-file", "/tmp/secret.env"},
		{"--network", "net", "--ip", "10.0.0.14", "--read-only", "--log-driver", "none"},
	} {
		if !containsSequence(args, want) {
			t.Fatalf("args %v miss %v", args, want)
		}
	}
	for _, arg := range args {
		if strings.Contains(arg, "secret") && arg != "/tmp/secret.env" {
			t.Fatalf("env file content leaked into args: %v", args)
		}
	}
}

func TestExecWithInputPassesStdin(t *testing.T) {
	logPath := installNetworkDockerStub(t, `cat > "$0.stdin"; printf '{"ok":true}'`)
	out, err := ExecWithInput(context.Background(), "worker", []string{"tool", "ctl"}, []byte("payload"))
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("ExecWithInput() = %q, %v", out, err)
	}
	if got, want := readNetworkDockerArgs(t, logPath), []string{"exec", "--interactive", "worker", "tool", "ctl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	stdin, err := os.ReadFile(dockerBinary + ".stdin")
	if err != nil || string(stdin) != "payload" {
		t.Fatalf("stdin = %q, %v", stdin, err)
	}
}

func TestExecWithInputKeepsStdoutOnFailure(t *testing.T) {
	installNetworkDockerStub(t, `printf '{"ok":false}'; exit 3`)
	out, err := ExecWithInput(context.Background(), "worker", []string{"tool"}, nil)
	if err == nil || out != `{"ok":false}` {
		t.Fatalf("ExecWithInput() = %q, %v", out, err)
	}
}

func TestContainerStartAndUnshareArgs(t *testing.T) {
	logPath := installNetworkDockerStub(t, `exit 0`)
	if err := ContainerStart(context.Background(), "hub"); err != nil {
		t.Fatal(err)
	}
	if err := Unshare(context.Background(), "chown", "1:1", "/x"); err == nil {
		t.Fatal("Unshare succeeded on docker")
	}
	if got := readNetworkDockerArgs(t, logPath); !reflect.DeepEqual(got, []string{"start", "hub"}) {
		t.Fatalf("args = %v", got)
	}
}
