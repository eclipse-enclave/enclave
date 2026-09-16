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
	"time"

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

func TestNetworkInspectReportsMalformedOutput(t *testing.T) {
	installNetworkDockerStub(t, `
printf '%s\n' 'not json'
exit 0
`)
	_, err := NetworkInspect(context.Background(), "session-net")
	if err == nil || !strings.Contains(err.Error(), "decode network inspect output") {
		t.Fatalf("NetworkInspect() error = %v, want decode failure", err)
	}
	if IsNotFound(err) {
		t.Fatalf("NetworkInspect() classified malformed output as not-found: %v", err)
	}
}

func TestNetworkListReportsMalformedOutput(t *testing.T) {
	installNetworkDockerStub(t, `
if [ "$1" = "network" ] && [ "$2" = "ls" ]; then
  printf '%s\n' network-id
  exit 0
fi
printf '%s\n' '{"Name":"session-net"'
exit 0
`)
	_, err := NetworkList(context.Background(), NewFilters())
	if err == nil || !strings.Contains(err.Error(), "decode network inspect output") {
		t.Fatalf("NetworkList() error = %v, want decode failure", err)
	}
}

func TestNetworkInspectReportsEmptySuccessfulOutput(t *testing.T) {
	installNetworkDockerStub(t, `exit 0`)
	_, err := NetworkInspect(context.Background(), "session-net")
	if err == nil || !strings.Contains(err.Error(), "printed 0 objects for 1 networks") {
		t.Fatalf("NetworkInspect() error = %v, want an empty-output failure", err)
	}
	if IsNotFound(err) {
		t.Fatalf("NetworkInspect() classified empty output as not-found: %v", err)
	}
}

func TestNetworkListReportsFailureDespitePartialOutput(t *testing.T) {
	installNetworkDockerStub(t, `
if [ "$1" = "network" ] && [ "$2" = "ls" ]; then
  printf '%s\n' first-id second-id
  exit 0
fi
printf '%s\n' '{"Name":"first-net","Id":"first-id"}'
printf '%s\n' 'Error response from daemon: permission denied' >&2
exit 1
`)
	networks, err := NetworkList(context.Background(), NewFilters())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("NetworkList() = %+v, %v; want the engine failure, not partial results", networks, err)
	}
}

func TestNetworkListKeepsNetworksInspectedBeforeMissingOne(t *testing.T) {
	installNetworkDockerStub(t, `
if [ "$1" = "network" ] && [ "$2" = "ls" ]; then
  printf '%s\n' present-id gone-id
  exit 0
fi
printf '%s\n' '{"Name":"present-net","Id":"present-id"}'
printf '%s\n' 'Error response from daemon: network gone-id not found' >&2
exit 1
`)
	networks, err := NetworkList(context.Background(), NewFilters())
	if err != nil {
		t.Fatalf("NetworkList() error = %v", err)
	}
	if len(networks) != 1 || networks[0].ID != "present-id" {
		t.Fatalf("NetworkList() = %+v, want only the network still present", networks)
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

func TestDecodeNetworkInspectResponsesAcceptsPodmanShape(t *testing.T) {
	line := `{"name":"session-net","id":"0123abcd","driver":"bridge","created":"2026-09-14T10:00:00.123456789+02:00","subnets":[{"subnet":"10.89.3.0/24","gateway":"10.89.3.1"}],"labels":{"enclave.network":"true","enclave.network.container":"session"},"options":{"isolate":"strict"},"containers":{"c1":{"name":"session-gateway","interfaces":{"eth0":{}}}}}`
	results, err := decodeNetworkInspectResponses(line)
	if err != nil {
		t.Fatalf("decodeNetworkInspectResponses() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("decoded %d networks, want 1", len(results))
	}
	info := results[0]
	if info.Name != "session-net" || info.ID != "0123abcd" || info.Labels["enclave.network.container"] != "session" {
		t.Fatalf("podman identity fields not decoded: %+v", info)
	}
	if _, err := time.Parse(time.RFC3339Nano, info.Created); err != nil {
		t.Fatalf("podman created timestamp not decoded: %q", info.Created)
	}
	if subnets := info.SubnetConfigs(); len(subnets) != 1 || subnets[0].Subnet != "10.89.3.0/24" {
		t.Fatalf("podman subnets not decoded: %+v", subnets)
	}
	if len(info.Containers) != 1 || info.Containers["c1"].Name != "session-gateway" {
		t.Fatalf("podman endpoints not decoded: %+v", info.Containers)
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

// podman echoes the new network's name where Docker prints its ID. Handing that
// name back as an ID makes every later comparison against inspect output fail,
// which silently skips the network's removal and leaks it.
func TestNetworkCreateResolvesPodmanNameToID(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "podman")
	script := `#!/bin/sh
if [ "$1" = "network" ] && [ "$2" = "create" ]; then
  printf '%s\n' session-net
  exit 0
fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then
  printf '%s\n' '{"name":"session-net","id":"51cb64e853c090000e91","labels":{}}'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write podman stub: %v", err)
	}
	orig := dockerBinary
	dockerBinary = stub
	t.Cleanup(func() { dockerBinary = orig })

	id, err := NetworkCreate(context.Background(), NetworkCreateOptions{Name: "session-net", Driver: "bridge"})
	if err != nil {
		t.Fatalf("NetworkCreate() error = %v", err)
	}
	if id != "51cb64e853c090000e91" {
		t.Fatalf("NetworkCreate() = %q, want the inspected network ID", id)
	}
}
