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
	"path/filepath"
	"testing"
)

func TestContainerListToleratesOnlyListedContainerVanishing(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "docker")
	script := `#!/bin/sh
if [ "$1" = "ps" ]; then
  printf 'gone\n'
  exit 0
fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  printf 'Error: No such container: gone\n' >&2
  exit 1
fi
exit 2
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	orig := dockerBinary
	dockerBinary = stub
	t.Cleanup(func() { dockerBinary = orig })

	got, err := ContainerList(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("container list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no containers after listed ID vanished, got %v", got)
	}
}

func TestContainerListDecodesPortBindings(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "docker")
	script := `#!/bin/sh
if [ "$1" = "ps" ]; then
  printf 'abc\n'
  exit 0
fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  printf '{"Id":"abc","Name":"/enclave-codex-abc123abc123","NetworkSettings":{"Ports":{"3000/tcp":[{"HostIp":"127.0.0.1","HostPort":"39123"}],"9229/tcp":null}}}\n'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	orig := dockerBinary
	dockerBinary = stub
	t.Cleanup(func() { dockerBinary = orig })

	got, err := ContainerList(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("container list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one container, got %v", got)
	}
	bindings := got[0].Ports["3000/tcp"]
	if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort != "39123" {
		t.Fatalf("unexpected bindings for 3000/tcp: %v", got[0].Ports)
	}
	if unbound, ok := got[0].Ports["9229/tcp"]; !ok || unbound != nil {
		t.Fatalf("expected exposed-but-unpublished port with nil bindings, got %v", got[0].Ports)
	}
}
