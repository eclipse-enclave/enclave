// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"enclave/internal/util"
)

func TestNetworkConnectBuildsStaticAddressArgs(t *testing.T) {
	logPath := installNetworkDockerStub(t, `exit 0`)
	if err := NetworkConnect(context.Background(), "session-net", "hub", " 10.0.0.5 ", "hub.a2a", " "); err != nil {
		t.Fatalf("NetworkConnect() error = %v", err)
	}
	want := []string{"network", "connect", "--ip", "10.0.0.5", "--alias", "hub.a2a", "session-net", "hub"}
	if got := readNetworkDockerArgs(t, logPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("docker args = %v, want %v", got, want)
	}
}

func TestNetworkConnectRejectsInvalidInput(t *testing.T) {
	logPath := installNetworkDockerStub(t, `exit 0`)
	for _, tc := range []struct{ network, container, ip string }{
		{"net", "c", ""},
		{"net", "c", "fd00::5"},
		{"net", "c", "10.0.0.5/28"},
		{"net", "c", "--ip"},
		{"", "c", "10.0.0.5"},
		{"net", " ", "10.0.0.5"},
	} {
		if err := NetworkConnect(context.Background(), tc.network, tc.container, tc.ip); err == nil {
			t.Errorf("NetworkConnect(%q, %q, %q) succeeded, want an error", tc.network, tc.container, tc.ip)
		}
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("invalid input reached the engine CLI")
	}
}

const dockerAlreadyConnectedStub = `
if [ "$1" = "network" ] && [ "$2" = "connect" ]; then
  printf '%s\n' 'Error response from daemon: endpoint with name hub already exists in network session-net' >&2
  exit 1
fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  printf '%s\n' "$INSPECT"
  exit 0
fi
exit 2
`

func TestNetworkConnectAcceptsExistingSameAddress(t *testing.T) {
	t.Setenv("INSPECT", `{"session-net":{"IPAMConfig":{"IPv4Address":"10.0.0.5"},"NetworkID":"abc","IPAddress":"10.0.0.5"}}`)
	installNetworkDockerStub(t, dockerAlreadyConnectedStub)
	if err := NetworkConnect(context.Background(), "session-net", "hub", "10.0.0.5"); err != nil {
		t.Fatalf("NetworkConnect() error = %v, want idempotent success", err)
	}
}

func TestNetworkConnectMatchesExistingEndpointByNetworkID(t *testing.T) {
	t.Setenv("INSPECT", `{"session-net":{"IPAMConfig":{"IPv4Address":"10.0.0.5"},"NetworkID":"0123456789abcdef0123"}}`)
	installNetworkDockerStub(t, dockerAlreadyConnectedStub)
	if err := NetworkConnect(context.Background(), "0123456789ab", "hub", "10.0.0.5"); err != nil {
		t.Fatalf("NetworkConnect() error = %v, want match by network ID prefix", err)
	}
}

func TestNetworkConnectReportsConflictingAddress(t *testing.T) {
	for name, inspect := range map[string]string{
		"different static": `{"session-net":{"IPAMConfig":{"IPv4Address":"10.0.0.6"},"IPAddress":"10.0.0.6"}}`,
		"dynamic docker":   `{"session-net":{"IPAMConfig":null,"IPAddress":"10.0.0.5"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("INSPECT", inspect)
			installNetworkDockerStub(t, dockerAlreadyConnectedStub)
			err := NetworkConnect(context.Background(), "session-net", "hub", "10.0.0.5")
			var conflict *EndpointConflictError
			if !errors.As(err, &conflict) || conflict.Want != "10.0.0.5" {
				t.Fatalf("NetworkConnect() error = %v, want *EndpointConflictError", err)
			}
		})
	}
}

func TestNetworkConnectRollsBackFailedDockerAttach(t *testing.T) {
	logPath := installNetworkDockerStub(t, `
if [ "$2" = "connect" ]; then
  printf '%s\n' 'Error response from daemon: Address already in use' >&2
  exit 1
fi
exit 0
`)
	err := NetworkConnect(context.Background(), "session-net", "hub", "10.0.0.5")
	if !IsAddressInUse(err) || errors.Is(err, ErrStaleEndpoint) {
		t.Fatalf("NetworkConnect() error = %v, want address-in-use without a stale endpoint", err)
	}
	want := []string{
		"network", "connect", "--ip", "10.0.0.5", "session-net", "hub",
		"network", "disconnect", "session-net", "hub",
		"container", "inspect", "--format", "{{json .NetworkSettings.Networks}}", "hub",
	}
	if got := readNetworkDockerArgs(t, logPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("docker args = %v, want %v", got, want)
	}
}

func TestNetworkConnectReportsStaleEndpointAfterFailedRollback(t *testing.T) {
	installNetworkDockerStub(t, `
if [ "$2" = "connect" ]; then
  printf '%s\n' 'Error response from daemon: Address already in use' >&2
  exit 1
fi
if [ "$2" = "disconnect" ]; then
  printf '%s\n' 'Error response from daemon: container 0123 is not connected to network session-net' >&2
  exit 1
fi
printf '%s\n' '{"session-net":{"IPAMConfig":{"IPv4Address":"10.0.0.5"},"NetworkID":"","EndpointID":""}}'
`)
	err := NetworkConnect(context.Background(), "session-net", "hub", "10.0.0.5")
	if !IsAddressInUse(err) || !errors.Is(err, ErrStaleEndpoint) {
		t.Fatalf("NetworkConnect() error = %v, want address-in-use and ErrStaleEndpoint", err)
	}
}

func TestNetworkConnectRollsBackFailedPodmanAttach(t *testing.T) {
	logPath := installPodmanStub(t, `
if [ "$1" = "network" ] && [ "$2" = "connect" ]; then
  printf '%s\n' 'Error: IPAM error: requested ip address 10.0.0.5 is already allocated to container ID abc' >&2
  exit 125
fi
printf '%s\n' 'Error: tearing down network namespace configuration for container x: netavark: failed to delete container veth eth1: Netlink error: No such device (os error 19)' >&2
exit 125
`)
	err := NetworkConnect(context.Background(), "session-net", "hub", "10.0.0.5")
	if !IsAddressInUse(err) {
		t.Fatalf("NetworkConnect() error = %v, want the original address-in-use error", err)
	}
	want := []string{
		"network", "connect", "--ip", "10.0.0.5", "session-net", "hub",
		"network", "disconnect", "session-net", "hub",
		"container", "inspect", "--format", "{{json .NetworkSettings.Networks}}", "hub",
	}
	if got := readNetworkDockerArgs(t, logPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("podman args = %v, want %v", got, want)
	}
}

func TestNetworkConnectSkipsPodmanRollbackWhenTargetMissing(t *testing.T) {
	for name, stderr := range map[string]string{
		"container": `Error: no container with name or ID "hub" found: no such container`,
		"network":   `Error: unable to find network with name or ID session-net: network not found`,
	} {
		t.Run(name, func(t *testing.T) {
			logPath := installPodmanStub(t, "printf '%s\\n' '"+stderr+"' >&2\nexit 125\n")
			if err := NetworkConnect(context.Background(), "session-net", "hub", "10.0.0.5"); err == nil {
				t.Fatal("NetworkConnect() succeeded, want an error")
			}
			if got := readNetworkDockerArgs(t, logPath); len(got) != 6 {
				t.Fatalf("podman invoked beyond the connect: %v", got)
			}
		})
	}
}

func TestNetworkConnectAcceptsExistingSameAddressOnPodman(t *testing.T) {
	t.Setenv("INSPECT", `{"session-net":{"NetworkID":"session-net","IPAddress":"10.0.0.5","IPAMConfig":null}}`)
	installPodmanStub(t, `
if [ "$1" = "network" ] && [ "$2" = "connect" ]; then
  printf '%s\n' 'Error: container abc is already connected to network session-net: network is already connected' >&2
  exit 125
fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  printf '%s\n' "$INSPECT"
  exit 0
fi
exit 2
`)
	if err := NetworkConnect(context.Background(), "session-net", "hub", "10.0.0.5"); err != nil {
		t.Fatalf("NetworkConnect() error = %v, want idempotent success", err)
	}
}

func TestNetworkDisconnectBuildsArgs(t *testing.T) {
	logPath := installNetworkDockerStub(t, `exit 0`)
	if err := NetworkDisconnect(context.Background(), "session-net", "hub", true); err != nil {
		t.Fatalf("NetworkDisconnect() error = %v", err)
	}
	want := []string{"network", "disconnect", "--force", "session-net", "hub"}
	if got := readNetworkDockerArgs(t, logPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("docker args = %v, want %v", got, want)
	}
}

func TestNetworkDisconnectTreatsAbsentAttachmentAsSuccess(t *testing.T) {
	for _, stderr := range []string{
		"Error response from daemon: container 0123 is not connected to network session-net",
		"Error response from daemon: container 0123 is not connected to the network session-net",
		"Error response from daemon: No such container: hub",
		"Error response from daemon: endpoint hub not found",
		"Error response from daemon: network session-net not found",
		"Error: container hub is not connected to network session-net",
		`Error: no container with name or ID "hub" found: no such container`,
		"Error: unable to find network with name or ID session-net: network not found",
	} {
		installNetworkDockerStub(t, "printf '%s\\n' "+util.ShellQuote(stderr)+" >&2\nexit 1\n")
		if err := NetworkDisconnect(context.Background(), "session-net", "hub", false); err != nil {
			t.Errorf("NetworkDisconnect() with %q error = %v, want success", stderr, err)
		}
	}
}

func TestNetworkDisconnectReportsOtherFailures(t *testing.T) {
	installNetworkDockerStub(t, `
printf '%s\n' 'Error response from daemon: permission denied' >&2
exit 1
`)
	if err := NetworkDisconnect(context.Background(), "session-net", "hub", false); err == nil {
		t.Fatal("NetworkDisconnect() succeeded, want the engine failure")
	}
}

func TestNetworkConnectErrorClassifiers(t *testing.T) {
	cases := []struct {
		stderr   string
		classify func(error) bool
	}{
		{"Error response from daemon: Address already in use", IsAddressInUse},
		{"Error: IPAM error: requested ip address 10.0.0.5 is already allocated to container ID abc", IsAddressInUse},
		{"Error response from daemon: invalid endpoint settings:\nno configured subnet contains IP address 10.0.0.200", IsStaticAddressRejected},
		{"Error response from daemon: invalid endpoint settings:\n* user-specified IP address is supported on user-defined networks only", IsStaticAddressRejected},
		{"Error response from daemon: user specified IP address is supported only when connecting to networks with user configured subnets", IsStaticAddressRejected},
		{"Error: requested static ip 10.0.0.200 not in any subnet on network session-net", IsStaticAddressRejected},
		{"Error response from daemon: container cannot be connected to multiple networks with one of the networks in private (none) mode", IsUnattachableNetworkMode},
		{`Error: "slirp4netns" is not supported: invalid network mode`, IsUnattachableNetworkMode},
		{"Error response from daemon: network session-net not found", IsNetworkNotFound},
		{"Error: unable to find network with name or ID session-net: network not found", IsNetworkNotFound},
	}
	all := []func(error) bool{IsAddressInUse, IsStaticAddressRejected, IsUnattachableNetworkMode, IsNetworkNotFound}
	for _, tc := range cases {
		err := &cliError{args: []string{"network", "connect"}, stderr: tc.stderr}
		matched := 0
		for _, classify := range all {
			if classify(err) {
				matched++
			}
		}
		if !tc.classify(err) || matched != 1 {
			t.Errorf("%q: want exactly its own classifier to match, matched %d", tc.stderr, matched)
		}
	}
}

func TestDecodeContainerNetworkEndpointsAcceptsNull(t *testing.T) {
	endpoints, err := decodeContainerNetworkEndpoints("null")
	if err != nil || len(endpoints) != 0 {
		t.Fatalf("decodeContainerNetworkEndpoints(null) = %v, %v", endpoints, err)
	}
	if _, err := decodeContainerNetworkEndpoints("{"); err == nil {
		t.Fatal("malformed output decoded without error")
	}
}

func installPodmanStub(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.args")
	stub := filepath.Join(dir, "podman")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> " + util.ShellQuote(logPath) + "; done\n" + body
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write podman stub: %v", err)
	}
	orig := dockerBinary
	dockerBinary = stub
	t.Cleanup(func() { dockerBinary = orig })
	return logPath
}
