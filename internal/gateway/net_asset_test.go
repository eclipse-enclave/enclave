// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// resolveDockerHostIPv4 sources the shipped net.sh and runs the helper against
// a fixture hosts file. getent is stubbed so the DNS fallback is deterministic.
func resolveDockerHostIPv4(t *testing.T, hostsContent, getentOutput string) (string, bool) {
	t.Helper()

	dir := t.TempDir()
	hostsFile := filepath.Join(dir, "hosts")
	if err := os.WriteFile(hostsFile, []byte(hostsContent), 0o644); err != nil {
		t.Fatalf("write hosts fixture: %v", err)
	}

	stubDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("create stub dir: %v", err)
	}
	stub := "#!/bin/sh\nexit 1\n"
	if getentOutput != "" {
		stub = "#!/bin/sh\ncat <<'STUB_EOF'\n" + getentOutput + "STUB_EOF\n"
	}
	if err := os.WriteFile(filepath.Join(stubDir, "getent"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write getent stub: %v", err)
	}

	netLib, err := filepath.Abs(filepath.Join("..", "..", "runtime-assets", "net.sh"))
	if err != nil {
		t.Fatalf("resolve net.sh: %v", err)
	}

	cmd := exec.Command("sh", "-c", ". \"$0\"; enclave_resolve_docker_host_ipv4", netLib)
	cmd.Env = append(os.Environ(),
		"ENCLAVE_HOSTS_FILE="+hostsFile,
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err == nil
}

func TestResolveDockerHostIPv4FromHosts(t *testing.T) {
	tests := []struct {
		name  string
		hosts string
		want  string
	}{
		{
			name:  "plain entry",
			hosts: "127.0.0.1\tlocalhost\n192.168.65.254\thost.docker.internal\n",
			want:  "192.168.65.254",
		},
		{
			name:  "skips ipv6 entry",
			hosts: "::1\thost.docker.internal\n192.168.65.254\thost.docker.internal\n",
			want:  "192.168.65.254",
		},
		{
			name:  "ignores suffix lookalike hostname",
			hosts: "10.1.2.3\tbuild.host.docker.internal.example\n192.168.65.254\thost.docker.internal\n",
			want:  "192.168.65.254",
		},
		{
			name:  "ignores commented entry",
			hosts: "#192.0.2.9 host.docker.internal\n192.168.65.254\thost.docker.internal\n",
			want:  "192.168.65.254",
		},
		{
			name:  "matches alias beyond the first hostname",
			hosts: "192.168.65.254\tgateway.docker.internal host.docker.internal\n",
			want:  "192.168.65.254",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveDockerHostIPv4(t, tc.hosts, "")
			if !ok {
				t.Fatalf("helper failed, output %q", got)
			}
			if got != tc.want {
				t.Errorf("resolved %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveDockerHostIPv4FallsBackToGetent(t *testing.T) {
	hosts := "127.0.0.1\tlocalhost\n"

	got, ok := resolveDockerHostIPv4(t, hosts, "192.168.65.254 STREAM host.docker.internal\n192.168.65.254 DGRAM\n")
	if !ok {
		t.Fatalf("helper failed, output %q", got)
	}
	if got != "192.168.65.254" {
		t.Errorf("resolved %q, want 192.168.65.254", got)
	}
}

func TestResolveDockerHostIPv4FailsWhenUnresolvable(t *testing.T) {
	got, ok := resolveDockerHostIPv4(t, "127.0.0.1\tlocalhost\n", "")
	if ok {
		t.Fatalf("expected failure, got %q", got)
	}
	if got != "" {
		t.Errorf("expected no output, got %q", got)
	}
}
