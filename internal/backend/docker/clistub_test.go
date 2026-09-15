// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	dockercmd "enclave/internal/docker"
)

// stubCLI installs a shell script as the container CLI for the test. The
// binary name selects the engine (IsPodman inspects it); the script body runs
// after every invocation has been appended to the returned call log.
func stubCLI(t *testing.T, binaryName string, script string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, binaryName)
	full := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$STUB_CALL_LOG\"\n" + script
	if err := os.WriteFile(stub, []byte(full), 0o700); err != nil { // #nosec G306 -- test stub must be executable.
		t.Fatalf("write cli stub: %v", err)
	}
	previous := dockercmd.Binary()
	dockercmd.SetBinary(stub)
	t.Cleanup(func() { dockercmd.SetBinary(previous) })
	t.Setenv("STUB_CALL_LOG", logPath)
	return logPath
}

func stubCalls(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read stub call log: %v", err)
	}
	var calls []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

func hasCallWithPrefix(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}
