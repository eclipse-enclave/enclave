// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"strings"
	"testing"

	"enclave/internal/backend"
)

func vncSession(name string, ports ...backend.PortMapping) backend.Session {
	return backend.Session{Ref: backend.SessionRef{Name: name}, Ports: ports}
}

func rfbBinding(hostIP, hostPort string) backend.PortMapping {
	return backend.PortMapping{HostIP: hostIP, HostPort: hostPort, ContainerPort: "5900", Protocol: "tcp"}
}

func TestResolveVNCSessionPicksSoleVNCSession(t *testing.T) {
	sessions := []backend.Session{
		vncSession("enclave-codex-1", backend.PortMapping{HostIP: "127.0.0.1", HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"}),
		vncSession("enclave-claude-1", rfbBinding("127.0.0.1", "43521")),
	}

	session, binding, err := resolveVNCSession(sessions, "")
	if err != nil {
		t.Fatalf("resolveVNCSession: %v", err)
	}
	if session.Ref.Name != "enclave-claude-1" {
		t.Errorf("session = %q, want enclave-claude-1", session.Ref.Name)
	}
	if binding.HostPort != "43521" {
		t.Errorf("host port = %q, want 43521", binding.HostPort)
	}
}

func TestResolveVNCSessionRequiresExplicitNameWhenAmbiguous(t *testing.T) {
	sessions := []backend.Session{
		vncSession("enclave-claude-2", rfbBinding("127.0.0.1", "43522")),
		vncSession("enclave-claude-1", rfbBinding("127.0.0.1", "43521")),
	}

	if _, _, err := resolveVNCSession(sessions, ""); err == nil {
		t.Fatal("resolveVNCSession = nil error, want ambiguity error")
	}

	session, binding, err := resolveVNCSession(sessions, "enclave-claude-2")
	if err != nil {
		t.Fatalf("resolveVNCSession(explicit): %v", err)
	}
	if session.Ref.Name != "enclave-claude-2" || binding.HostPort != "43522" {
		t.Errorf("session = %q, port = %q", session.Ref.Name, binding.HostPort)
	}
}

// A session running without the vnc feature must be distinguishable from no
// session at all: the fix differs (restart with the feature vs start one).
func TestResolveVNCSessionDistinguishesMissingFeature(t *testing.T) {
	_, _, err := resolveVNCSession([]backend.Session{vncSession("enclave-claude-1")}, "")
	if err == nil {
		t.Fatal("resolveVNCSession = nil error, want missing-feature error")
	}
	if !strings.Contains(err.Error(), "--features +vnc") || !strings.Contains(err.Error(), "enclave-claude-1") {
		t.Errorf("error = %q, want the vnc hint and the running container name", err)
	}

	_, _, err = resolveVNCSession(nil, "")
	if err == nil {
		t.Fatal("resolveVNCSession(nil) = nil error, want no-session error")
	}
	if strings.Contains(err.Error(), "has a VNC display") {
		t.Errorf("error = %q, want the no-container message", err)
	}
}

func TestResolveVNCSessionExplicitNameErrors(t *testing.T) {
	sessions := []backend.Session{
		vncSession("enclave-claude-1", rfbBinding("127.0.0.1", "43521")),
		vncSession("enclave-codex-1"),
	}

	_, _, err := resolveVNCSession(sessions, "enclave-codex-1")
	if err == nil || !strings.Contains(err.Error(), "without the vnc feature") {
		t.Errorf("named VNC-less container: error = %v, want the vnc-feature hint", err)
	}

	_, _, err = resolveVNCSession(sessions, "enclave-other")
	if err == nil || !strings.Contains(err.Error(), "no running enclave container named") {
		t.Errorf("unknown container: error = %v, want the unknown-name error", err)
	}
}

func TestVNCPortBindingIgnoresOtherPortsAndProtocols(t *testing.T) {
	cases := map[string]backend.Session{
		"other container port": vncSession("c", backend.PortMapping{HostIP: "127.0.0.1", HostPort: "5900", ContainerPort: "5901", Protocol: "tcp"}),
		"udp":                  vncSession("c", backend.PortMapping{HostIP: "127.0.0.1", HostPort: "43521", ContainerPort: "5900", Protocol: "udp"}),
		"unpublished":          vncSession("c", backend.PortMapping{HostIP: "127.0.0.1", ContainerPort: "5900", Protocol: "tcp"}),
	}
	for name, session := range cases {
		if _, ok := vncPortBinding(session); ok {
			t.Errorf("%s: vncPortBinding = ok, want no binding", name)
		}
	}

	// A binding recorded without an explicit protocol is TCP.
	if _, ok := vncPortBinding(vncSession("c", backend.PortMapping{HostIP: "127.0.0.1", HostPort: "43521", ContainerPort: "5900"})); !ok {
		t.Error("protocol-less binding: vncPortBinding = not ok, want the binding")
	}
}

func TestVNCDialHostPrefersLoopbackForWildcardBindings(t *testing.T) {
	cases := map[string]string{
		"":        "127.0.0.1",
		"0.0.0.0": "127.0.0.1",
		"::":      "127.0.0.1",
		"[::]":    "127.0.0.1",
		"1.2.3.4": "1.2.3.4",
	}
	for hostIP, want := range cases {
		if got := vncDialHost(backend.PortMapping{HostIP: hostIP}); got != want {
			t.Errorf("vncDialHost(%q) = %q, want %q", hostIP, got, want)
		}
	}
}
