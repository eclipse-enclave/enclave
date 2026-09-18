// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func vncSession(name string, ports ...backend.PortMapping) backend.Session {
	return backend.Session{Ref: backend.SessionRef{Name: name}, Ports: ports}
}

func rfbBinding(hostIP, hostPort string) backend.PortMapping {
	return backend.PortMapping{HostIP: hostIP, HostPort: hostPort, ContainerPort: "5900", Protocol: "tcp"}
}

// namedVNCSession mirrors a real container: the session name is the suffix of
// the container name, which is what makes it resolvable by either.
func namedVNCSession(container, sessionName string, ports ...backend.PortMapping) backend.Session {
	s := vncSession(container, ports...)
	s.Tool = "claude"
	s.Name = sessionName
	s.Status = "running"
	return s
}

func vncTargetOpts(args ...string) model.Options {
	opts := model.Options{Sources: model.DefaultOptionSources()}
	opts.CmdArgs = args
	return opts
}

func TestAutoSelectVNCSessionPicksSoleVNCSession(t *testing.T) {
	sessions := []backend.Session{
		vncSession("enclave-codex-1", backend.PortMapping{HostIP: "127.0.0.1", HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"}),
		vncSession("enclave-claude-1", rfbBinding("127.0.0.1", "43521")),
	}

	session, binding, err := autoSelectVNCSession(sessions)
	if err != nil {
		t.Fatalf("autoSelectVNCSession: %v", err)
	}
	if session.Ref.Name != "enclave-claude-1" {
		t.Errorf("session = %q, want enclave-claude-1", session.Ref.Name)
	}
	if binding.HostPort != "43521" {
		t.Errorf("host port = %q, want 43521", binding.HostPort)
	}
}

func TestAutoSelectVNCSessionRequiresExplicitNameWhenAmbiguous(t *testing.T) {
	sessions := []backend.Session{
		vncSession("enclave-claude-2", rfbBinding("127.0.0.1", "43522")),
		vncSession("enclave-claude-1", rfbBinding("127.0.0.1", "43521")),
	}

	if _, _, err := autoSelectVNCSession(sessions); err == nil {
		t.Fatal("autoSelectVNCSession = nil error, want ambiguity error")
	}
}

// A session running without the vnc feature must be distinguishable from no
// session at all: the fix differs (restart with the feature vs start one).
func TestAutoSelectVNCSessionDistinguishesMissingFeature(t *testing.T) {
	_, _, err := autoSelectVNCSession([]backend.Session{vncSession("enclave-claude-1")})
	if err == nil {
		t.Fatal("autoSelectVNCSession = nil error, want missing-feature error")
	}
	if !strings.Contains(err.Error(), "--features +vnc") || !strings.Contains(err.Error(), "enclave-claude-1") {
		t.Errorf("error = %q, want the vnc hint and the running container name", err)
	}

	_, _, err = autoSelectVNCSession(nil)
	if err == nil {
		t.Fatal("autoSelectVNCSession(nil) = nil error, want no-session error")
	}
	if strings.Contains(err.Error(), "has a VNC display") {
		t.Errorf("error = %q, want the no-container message", err)
	}
}

// The positional argument goes through the shared session resolver, so the
// `--name my-task` + `vnc-viewer my-task` flow works as it does for attach,
// stop, and theia rather than requiring the full container name.
func TestResolveVNCTargetResolvesSessionName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		namedVNCSession("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", rfbBinding("127.0.0.1", "43521")),
	}}

	session, binding, err := resolveVNCTarget(context.Background(), be, "", vncTargetOpts("my-task"))
	if err != nil {
		t.Fatalf("resolveVNCTarget: %v", err)
	}
	if session.Ref.Name != "enclave-claude-aaaaaaaaaaaa-my-task" {
		t.Errorf("session = %q, want the container of session my-task", session.Ref.Name)
	}
	if binding.HostPort != "43521" {
		t.Errorf("host port = %q, want 43521", binding.HostPort)
	}
}

func TestResolveVNCTargetNamedSessionErrors(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		namedVNCSession("enclave-claude-aaaaaaaaaaaa-gui", "gui", rfbBinding("127.0.0.1", "43521")),
		namedVNCSession("enclave-claude-aaaaaaaaaaaa-plain", "plain"),
	}}

	_, _, err := resolveVNCTarget(context.Background(), be, "", vncTargetOpts("plain"))
	if err == nil || !strings.Contains(err.Error(), "without the vnc feature") {
		t.Errorf("named VNC-less session: error = %v, want the vnc-feature hint", err)
	}

	_, _, err = resolveVNCTarget(context.Background(), be, "", vncTargetOpts("absent"))
	if err == nil || !strings.Contains(err.Error(), "no enclave session named") {
		t.Errorf("unknown session: error = %v, want the unknown-name error", err)
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
