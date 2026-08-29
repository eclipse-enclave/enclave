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
	"reflect"
	"strings"
	"testing"

	"enclave/internal/util"
)

func TestNetworkCreateBuildsBridgeOptions(t *testing.T) {
	logPath := installNetworkDockerStub(t, `
if [ "$1" = "network" ] && [ "$2" = "create" ]; then
  printf '%s\n' network-id
  exit 0
fi
exit 2
`)
	ipv6 := false
	id, err := NetworkCreate(context.Background(), NetworkCreateOptions{
		Name:       "session-net",
		Driver:     "bridge",
		Subnet:     "0.0.0.0/28",
		EnableIPv6: &ipv6,
		Labels:     map[string]string{"z": "last", "a": "first"},
		Options:    map[string]string{"host": "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("NetworkCreate() error = %v", err)
	}
	if id != "network-id" {
		t.Fatalf("NetworkCreate() ID = %q", id)
	}
	want := []string{
		"network", "create", "--driver", "bridge", "--ipv6=false",
		"--subnet", "0.0.0.0/28", "--opt", "host=127.0.0.1",
		"--label", "a=first", "--label", "z=last", "session-net",
	}
	if got := readNetworkDockerArgs(t, logPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("docker args = %v, want %v", got, want)
	}
}

func TestNetworkInspectAndListDecodeStructuredData(t *testing.T) {
	logPath := installNetworkDockerStub(t, `
if [ "$1" = "network" ] && [ "$2" = "ls" ]; then
  printf '%s\n' network-id
  exit 0
fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then
  printf '%s\n' '{"Name":"session-net","Id":"network-id","Created":"2026-08-27T10:00:00Z","Driver":"bridge","Labels":{"enclave.network":"true"},"IPAM":{"Config":[{"Subnet":"172.30.0.0/28","Gateway":"172.30.0.1"}]},"Containers":{"container-id":{"Name":"session","EndpointID":"endpoint-id","IPv4Address":"172.30.0.2/28"}}}'
  exit 0
fi
exit 2
`)
	filters := NewFilters()
	filters.Add("label", "enclave.network=true")
	networks, err := NetworkList(context.Background(), filters)
	if err != nil {
		t.Fatalf("NetworkList() error = %v", err)
	}
	if len(networks) != 1 || networks[0].Name != "session-net" || networks[0].IPAM.Config[0].Subnet != "172.30.0.0/28" {
		t.Fatalf("unexpected network list: %+v", networks)
	}
	if networks[0].Containers["container-id"].Name != "session" {
		t.Fatalf("unexpected endpoints: %+v", networks[0].Containers)
	}
	args := readNetworkDockerArgs(t, logPath)
	if !containsSequence(args, []string{"--filter", "label=enclave.network=true"}) {
		t.Fatalf("network list args missing label filter: %v", args)
	}
}

func TestNetworkInspectClassifiesMissingNetwork(t *testing.T) {
	installNetworkDockerStub(t, `
printf '%s\n' 'Error response from daemon: network session-net not found' >&2
exit 1
`)
	_, err := NetworkInspect(context.Background(), "session-net")
	if !IsNotFound(err) {
		t.Fatalf("NetworkInspect() error = %v, want not-found classification", err)
	}
}

func TestInfoDecodesFirewallAndVersionFields(t *testing.T) {
	installNetworkDockerStub(t, `
if [ "$1" = "info" ]; then
  printf '%s\n' '{"ServerVersion":"29.1.0","OSType":"linux","FirewallBackend":{"Driver":"iptables","Info":[["ReloadedAt","2026-08-27T10:00:00Z"]]},"Warnings":["network warning"]}'
  exit 0
fi
exit 2
`)
	info, err := Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if info.ServerVersion != "29.1.0" || info.OSType != "linux" || info.FirewallBackend == nil {
		t.Fatalf("unexpected Docker info: %+v", info)
	}
	if len(info.Warnings) != 1 {
		t.Fatalf("unexpected firewall details: %+v", info)
	}
}

func installNetworkDockerStub(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.args")
	stub := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> " + util.ShellQuote(logPath) + "; done\n" +
		"printf '%s\\n' -- >> " + util.ShellQuote(logPath) + "\n" + body
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write Docker stub: %v", err)
	}
	orig := dockerBinary
	dockerBinary = stub
	t.Cleanup(func() { dockerBinary = orig })
	return logPath
}

func readNetworkDockerArgs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Docker args: %v", err)
	}
	var args []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && line != "--" {
			args = append(args, line)
		}
	}
	return args
}

func containsSequence(values []string, want []string) bool {
	for i := 0; i+len(want) <= len(values); i++ {
		if reflect.DeepEqual(values[i:i+len(want)], want) {
			return true
		}
	}
	return false
}
