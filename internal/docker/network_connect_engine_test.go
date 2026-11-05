// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build docker

package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"os"
	"testing"
	"time"
)

// Run with: go test -tags docker ./internal/docker -run Engine
// ENCLAVE_TEST_ENGINE=podman selects podman; ENCLAVE_TEST_IMAGE overrides busybox.

func engineTestSetup(t *testing.T) (context.Context, string) {
	t.Helper()
	if engine := os.Getenv("ENCLAVE_TEST_ENGINE"); engine != "" {
		previous := dockerBinary
		SetBinary(engine)
		t.Cleanup(func() { dockerBinary = previous })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	if _, err := capture(ctx, "version"); err != nil {
		t.Fatalf("%s is not usable: %v", Binary(), err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	return ctx, "a2a-a6-" + hex.EncodeToString(suffix)
}

func engineTestImage() string {
	if image := os.Getenv("ENCLAVE_TEST_IMAGE"); image != "" {
		return image
	}
	return "busybox:latest"
}

// createTestNetwork tries candidate /28 subnets until one does not overlap.
func createTestNetwork(t *testing.T, ctx context.Context, name string, candidates []string) netip.Prefix {
	t.Helper()
	for _, candidate := range candidates {
		_, err := NetworkCreate(ctx, NetworkCreateOptions{Name: name, Driver: "bridge", Subnet: candidate})
		if err != nil {
			t.Logf("network create %s with %s: %v", name, candidate, err)
			continue
		}
		t.Cleanup(func() {
			if err := NetworkRemove(context.Background(), name); err != nil && !IsNotFound(err) {
				t.Errorf("remove network %s: %v", name, err)
			}
		})
		return netip.MustParsePrefix(candidate)
	}
	t.Fatalf("no free /28 subnet for %s", name)
	return netip.Prefix{}
}

func startTestContainer(t *testing.T, ctx context.Context, name, network string) {
	t.Helper()
	t.Cleanup(func() {
		zero := time.Duration(0)
		_ = ContainerStop(context.Background(), name, &zero)
		if err := ContainerRemove(context.Background(), name, true, false); err != nil && !IsNotFound(err) {
			t.Errorf("remove container %s: %v", name, err)
		}
	})
	if _, err := capture(ctx, "run", "--detach", "--name", name, "--network", network, engineTestImage(), "sleep", "600"); err != nil {
		t.Fatalf("start container %s: %v", name, err)
	}
}

func hostAddr(prefix netip.Prefix, host byte) string {
	b := prefix.Addr().As4()
	b[3] += host
	return netip.AddrFrom4(b).String()
}

func liveIPv4(t *testing.T, ctx context.Context, container, network string) (string, bool) {
	t.Helper()
	endpoints, err := ContainerNetworkEndpoints(ctx, container)
	if err != nil {
		t.Fatalf("inspect %s: %v", container, err)
	}
	endpoint, ok := findEndpoint(endpoints, network)
	return endpoint.IPAddress, ok
}

func TestEngineNetworkConnectStaticAddressLifecycle(t *testing.T) {
	ctx, prefix := engineTestSetup(t)
	net := prefix + "-net"
	home := prefix + "-home"
	subnet := createTestNetwork(t, ctx, net, []string{"10.250.66.0/28", "10.250.76.0/28", "10.250.86.0/28"})
	createTestNetwork(t, ctx, home, []string{"10.250.67.0/28", "10.250.77.0/28", "10.250.87.0/28"})
	hub := prefix + "-hub"
	other := prefix + "-other"
	startTestContainer(t, ctx, hub, home)
	startTestContainer(t, ctx, other, home)

	hubIP := hostAddr(subnet, 5)
	if err := NetworkConnect(ctx, net, hub, hubIP); err != nil {
		t.Fatalf("NetworkConnect() error = %v", err)
	}
	if ip, ok := liveIPv4(t, ctx, hub, net); !ok || ip != hubIP {
		t.Fatalf("hub address on %s = %q (attached %v), want %s", net, ip, ok, hubIP)
	}
	if err := NetworkConnect(ctx, net, hub, hubIP); err != nil {
		t.Fatalf("repeated NetworkConnect() error = %v, want idempotent success", err)
	}
	var conflict *EndpointConflictError
	if err := NetworkConnect(ctx, net, hub, hostAddr(subnet, 6)); !errors.As(err, &conflict) || conflict.Have != hubIP {
		t.Fatalf("NetworkConnect() with a new address error = %v, want *EndpointConflictError with %s", err, hubIP)
	}

	// A failed attach must leave the container connectable and restartable.
	err := NetworkConnect(ctx, net, other, "10.250.99.5")
	t.Logf("out-of-subnet error: %v", err)
	if !IsStaticAddressRejected(err) {
		t.Fatalf("out-of-subnet NetworkConnect() error = %v, want static-address rejection", err)
	}
	err = NetworkConnect(ctx, net, other, hubIP)
	t.Logf("address-in-use error: %v (stale endpoint: %v)", err, errors.Is(err, ErrStaleEndpoint))
	if !IsAddressInUse(err) {
		t.Fatalf("duplicate-address NetworkConnect() error = %v, want address-in-use", err)
	}
	if _, listed := liveIPv4(t, ctx, other, net); listed != errors.Is(err, ErrStaleEndpoint) {
		t.Fatalf("%s listed on %s = %v, but error reports stale endpoint = %v", other, net, listed, errors.Is(err, ErrStaleEndpoint))
	}
	otherIP := hostAddr(subnet, 6)
	if err := NetworkConnect(ctx, net, other, otherIP); err != nil {
		t.Fatalf("NetworkConnect() after failed attempts error = %v", err)
	}
	zero := time.Duration(0)
	if err := ContainerStop(ctx, other, &zero); err != nil {
		t.Fatalf("stop %s: %v", other, err)
	}
	if _, err := capture(ctx, "start", other); err != nil {
		t.Fatalf("restart %s after a failed attach: %v", other, err)
	}
	if ip, ok := liveIPv4(t, ctx, other, net); !ok || ip != otherIP {
		t.Fatalf("%s address after restart = %q (attached %v), want %s", other, ip, ok, otherIP)
	}

	if err := SignalContainer(ctx, hub, SignalReload); err != nil {
		t.Fatalf("SignalContainer() error = %v", err)
	}

	if err := ContainerStop(ctx, hub, &zero); err != nil {
		t.Fatalf("stop hub: %v", err)
	}
	err = SignalContainer(ctx, hub, SignalReload)
	t.Logf("stopped-container signal error: %v", err)
	if !IsContainerNotRunning(err) {
		t.Fatalf("SignalContainer() on a stopped container error = %v, want not-running", err)
	}
	if _, err := capture(ctx, "start", hub); err != nil {
		t.Fatalf("restart hub: %v", err)
	}
	if ip, ok := liveIPv4(t, ctx, hub, net); !ok || ip != hubIP {
		t.Fatalf("hub address after restart = %q (attached %v), want %s", ip, ok, hubIP)
	}

	if err := NetworkDisconnect(ctx, net, hub, false); err != nil {
		t.Fatalf("NetworkDisconnect() error = %v", err)
	}
	if _, ok := liveIPv4(t, ctx, hub, net); ok {
		t.Fatalf("hub still attached to %s after disconnect", net)
	}
	for _, force := range []bool{false, true} {
		if err := NetworkDisconnect(ctx, net, hub, force); err != nil {
			t.Fatalf("repeated NetworkDisconnect(force=%v) error = %v, want success", force, err)
		}
		if err := NetworkDisconnect(ctx, net, prefix+"-missing", force); err != nil {
			t.Fatalf("NetworkDisconnect(force=%v) of a missing container error = %v, want success", force, err)
		}
		if err := NetworkDisconnect(ctx, prefix+"-missing-net", hub, force); err != nil {
			t.Fatalf("NetworkDisconnect(force=%v) from a missing network error = %v, want success", force, err)
		}
	}
	if err := NetworkDisconnect(ctx, net, other, true); err != nil {
		t.Fatalf("forced NetworkDisconnect() error = %v", err)
	}

	err = NetworkConnect(ctx, prefix+"-missing-net", hub, hubIP)
	t.Logf("missing-network connect error: %v", err)
	if !IsNetworkNotFound(err) {
		t.Fatalf("NetworkConnect() to a missing network error = %v, want network not found", err)
	}
	err = NetworkConnect(ctx, net, prefix+"-missing", hubIP)
	t.Logf("missing-container connect error: %v", err)
	if !IsNotFound(err) {
		t.Fatalf("NetworkConnect() of a missing container error = %v, want not found", err)
	}
}

func TestEngineNetworkConnectStaticAddressOnDefaultIPAM(t *testing.T) {
	ctx, prefix := engineTestSetup(t)
	net := prefix + "-auto"
	if _, err := NetworkCreate(ctx, NetworkCreateOptions{Name: net, Driver: "bridge"}); err != nil {
		t.Fatalf("create network: %v", err)
	}
	t.Cleanup(func() { _ = NetworkRemove(context.Background(), net) })
	home := prefix + "-home"
	createTestNetwork(t, ctx, home, []string{"10.250.68.0/28", "10.250.78.0/28", "10.250.88.0/28"})
	hub := prefix + "-hub"
	startTestContainer(t, ctx, hub, home)

	info, err := NetworkInspect(ctx, net)
	if err != nil {
		t.Fatalf("inspect network: %v", err)
	}
	subnets := info.SubnetConfigs()
	if len(subnets) == 0 {
		t.Fatalf("network %s reports no subnet", net)
	}
	subnet := netip.MustParsePrefix(subnets[0].Subnet).Masked()
	ip := hostAddr(subnet, 9)
	err = NetworkConnect(ctx, net, hub, ip)
	t.Logf("%s: static %s on engine-allocated %s: %v", Binary(), ip, subnet, err)
	if err != nil {
		t.Fatalf("NetworkConnect() on default IPAM error = %v", err)
	}
	if got, ok := liveIPv4(t, ctx, hub, net); !ok || got != ip {
		t.Fatalf("hub address = %q (attached %v), want %s", got, ok, ip)
	}
	if err := NetworkDisconnect(ctx, net, hub, false); err != nil {
		t.Fatalf("NetworkDisconnect() error = %v", err)
	}
}

func TestEngineNetworkConnectRejectsUnattachableNetworkMode(t *testing.T) {
	ctx, prefix := engineTestSetup(t)
	net := prefix + "-net"
	subnet := createTestNetwork(t, ctx, net, []string{"10.250.69.0/28", "10.250.79.0/28", "10.250.89.0/28"})
	isolated := prefix + "-none"
	startTestContainer(t, ctx, isolated, "none")
	err := NetworkConnect(ctx, net, isolated, hostAddr(subnet, 5))
	t.Logf("network-mode none connect error: %v", err)
	if !IsUnattachableNetworkMode(err) {
		t.Fatalf("NetworkConnect() of a --network none container error = %v, want unattachable network mode", err)
	}
}
