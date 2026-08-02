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
	"sync"
	"testing"
)

func TestSystemInfoHasSecurityOption(t *testing.T) {
	info := SystemInfo{SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless", "name=cgroupns"}}
	for _, name := range []string{"rootless", "seccomp", "cgroupns"} {
		if !info.HasSecurityOption(name) {
			t.Fatalf("expected security option %q to be reported", name)
		}
	}
	if info.HasSecurityOption("userns") {
		t.Fatalf("did not expect userns to be reported")
	}
}

// stubDockerInfo points the docker CLI at a script reporting securityOptions and
// clears the memoized info so the next lookup goes through the stub.
func stubDockerInfo(t *testing.T, securityOptions string) {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
if [ "$1" = "info" ]; then
  printf '{"DockerRootDir":"/var/lib/docker","SecurityOptions":[` + securityOptions + `]}\n'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	origBinary := dockerBinary
	dockerBinary = stub
	resetCachedInfo()
	t.Cleanup(func() {
		dockerBinary = origBinary
		resetCachedInfo()
	})
}

func resetCachedInfo() {
	cachedInfoOnce = sync.Once{}
	cachedInfo = SystemInfo{}
	cachedInfoErr = nil
}

func TestIsRootlessDetectsRootlessDaemon(t *testing.T) {
	stubDockerInfo(t, `"name=seccomp,profile=builtin","name=rootless"`)
	rootless, err := IsRootless(context.Background())
	if err != nil {
		t.Fatalf("is rootless: %v", err)
	}
	if !rootless {
		t.Fatalf("expected rootless daemon to be detected")
	}
}

func TestIsRootlessDetectsRootfulDaemon(t *testing.T) {
	stubDockerInfo(t, `"name=seccomp,profile=builtin","name=apparmor"`)
	rootless, err := IsRootless(context.Background())
	if err != nil {
		t.Fatalf("is rootless: %v", err)
	}
	if rootless {
		t.Fatalf("expected rootful daemon to be detected")
	}
}

func TestCachedInfoQueriesDaemonOnce(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "docker")
	counter := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf 'x' >> ` + counter + `
printf '{"SecurityOptions":["name=rootless"]}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	origBinary := dockerBinary
	dockerBinary = stub
	resetCachedInfo()
	t.Cleanup(func() {
		dockerBinary = origBinary
		resetCachedInfo()
	})

	for range 3 {
		if _, err := CachedInfo(context.Background()); err != nil {
			t.Fatalf("cached info: %v", err)
		}
	}
	calls, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read call counter: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected a single docker info call, got %d", len(calls))
	}
}
