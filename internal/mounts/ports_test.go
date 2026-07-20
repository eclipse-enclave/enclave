// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mounts

import (
	"testing"

	"enclave/internal/backend"
)

func singleMapping(t *testing.T, spec string) backend.PortMapping {
	t.Helper()
	mappings := ResolvePorts([]string{spec})
	if len(mappings) != 1 {
		t.Fatalf("ResolvePorts([%s]) = %+v, want one mapping", spec, mappings)
	}
	return mappings[0]
}

func TestResolvePortsDefaultsToLoopback(t *testing.T) {
	got := singleMapping(t, "3000")
	if got.HostIP != "127.0.0.1" || got.HostPort != "3000" || got.ContainerPort != "3000" {
		t.Fatalf("ResolvePorts([3000]) = %+v, want 127.0.0.1:3000->3000", got)
	}
}

func TestResolvePortsHostContainerMapping(t *testing.T) {
	got := singleMapping(t, "8080:80")
	if got.HostIP != "127.0.0.1" || got.HostPort != "8080" || got.ContainerPort != "80" {
		t.Fatalf("ResolvePorts([8080:80]) = %+v, want 127.0.0.1:8080->80", got)
	}
}

// A 3-part ip:host:container spec (the form the UI produces when it forces
// loopback) must parse and preserve the explicit host-IP. This guards the
// regression where such specs were dropped as "invalid port format".
func TestResolvePortsThreePartSpec(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:3000:3000": "127.0.0.1",
		"0.0.0.0:3000:3000":   "0.0.0.0", // explicit non-loopback opt-in
	}
	for spec, wantIP := range cases {
		got := singleMapping(t, spec)
		if got.HostIP != wantIP || got.HostPort != "3000" || got.ContainerPort != "3000" {
			t.Fatalf("ResolvePorts([%s]) = %+v, want %s:3000->3000", spec, got, wantIP)
		}
	}
}

// A host port of "0" requests an OS-assigned port; it must survive parsing
// with the sentinel intact (and its host-IP default applied) so the backend
// emits a Docker "--publish" that lets the daemon pick a free host port.
func TestResolvePortsAutoAssignedHostPort(t *testing.T) {
	cases := map[string]string{
		"0:5391":           "127.0.0.1", // host-IP defaults to loopback
		"127.0.0.1:0:5391": "127.0.0.1",
		"0.0.0.0:0:5391":   "0.0.0.0", // explicit non-loopback opt-in
	}
	for spec, wantIP := range cases {
		got := singleMapping(t, spec)
		if got.HostIP != wantIP || got.HostPort != "0" || got.ContainerPort != "5391" {
			t.Fatalf("ResolvePorts([%s]) = %+v, want %s:0->5391", spec, got, wantIP)
		}
	}
}

func TestResolvePortsIgnoresInvalidAndEmpty(t *testing.T) {
	mappings := ResolvePorts([]string{"", "  ", "bogus", "1:2:3"})
	if len(mappings) != 0 {
		t.Fatalf("ResolvePorts(invalid) = %+v, want empty", mappings)
	}
}
