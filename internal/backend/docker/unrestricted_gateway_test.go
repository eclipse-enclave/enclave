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
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

func unrestrictedGatewayRequest(t *testing.T) backend.Request {
	t.Helper()
	req := lockinRequest(prepareRunSession(t))
	req.Network.Mode = backend.NetworkModeUnrestricted
	req.Network.Gateway = true
	req.Hostname = model.HostnamePrefix + "project"
	req.Env = []backend.EnvVar{{Name: "API_KEY", Value: "real-secret"}}
	return req
}

// engineInvocations splits the recorded engine calls into one string per call.
func engineInvocations(t *testing.T, logPath string, home string) []string {
	t.Helper()
	var calls []string
	for _, call := range strings.Split(lockinCalls(t, logPath, home), "---\n") {
		if call = strings.TrimSpace(call); call != "" {
			calls = append(calls, strings.ReplaceAll(call, "\n", " "))
		}
	}
	return calls
}

func runInvocation(t *testing.T, calls []string, name string) string {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, "run ") && strings.Contains(call, " --name "+name+" ") {
			return call
		}
	}
	t.Fatalf("no run invocation for %s in:\n%s", name, strings.Join(calls, "\n"))
	return ""
}

// TestUnrestrictedSessionStartsObservingGateway pins an unrestricted session
// with a gateway: the gateway carries the mode label, the session's hostname,
// ports and bridges, and gets no config bundle; the session joins its
// namespace without the DNS-gateway signal.
func TestUnrestrictedSessionStartsObservingGateway(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		t.Run(engine, func(t *testing.T) {
			b, home := lockinBackend(t)
			logPath := lockinEngine(t, engine)
			req := unrestrictedGatewayRequest(t)

			if _, err := b.Start(context.Background(), req); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			calls := engineInvocations(t, logPath, home)
			gatewayName := req.Session.Name + model.GatewayContainerSuffix
			gw := runInvocation(t, calls, gatewayName)
			session := runInvocation(t, calls, req.Session.Name)
			toolDir := config.HostProjectToolDir(home, req.Session.ProjectHash, req.Session.Tool)
			for _, want := range []string{
				"--label " + model.GatewayLabelMode + "=" + model.NetworkModeUnrestricted,
				"--hostname " + req.Hostname,
				"--publish 127.0.0.1:13000:3000/tcp",
				"--env ENCLAVE_LOOPBACK_PORTS=1455",
				"--env ENCLAVE_IDE_BRIDGE_PORTS=9800",
				"target=/tls/ca.crt",
			} {
				if !strings.Contains(gw, want) {
					t.Errorf("gateway invocation lacks %q:\n%s", want, gw)
				}
			}
			if strings.Contains(gw, "secret-releases.json") {
				t.Errorf("unrestricted gateway received secret releases:\n%s", gw)
			}
			if strings.Contains(gw, model.GatewayConfigDir) {
				t.Errorf("unrestricted gateway received a config bundle:\n%s", gw)
			}
			for _, want := range []string{"--network container:" + gatewayName} {
				if !strings.Contains(session, want) {
					t.Errorf("session invocation lacks %q:\n%s", want, session)
				}
			}
			for _, unwanted := range []string{"--hostname", "--publish", model.EnvDNSGateway} {
				if strings.Contains(session, unwanted) {
					t.Errorf("session invocation carries %q:\n%s", unwanted, session)
				}
			}
			for _, dir := range []string{"gateway-config", "gateway-config-unrestricted"} {
				if path := filepath.Join(toolDir, dir); util.PathExists(path) {
					t.Errorf("unrestricted session wrote a gateway config bundle to %s", path)
				}
			}
			assertLockinGolden(t, "unrestricted-gateway-session-start-"+engine, lockinCalls(t, logPath, home))
		})
	}
}

// The gateway takes its network mode from its container environment, which
// cannot change after the container is created, not from a file on the host.
func TestGatewayEnvironmentCarriesNetworkMode(t *testing.T) {
	for _, mode := range []backend.NetworkMode{backend.NetworkModeRestricted, backend.NetworkModeUnrestricted} {
		t.Run(string(mode), func(t *testing.T) {
			b, home := lockinBackend(t)
			logPath := lockinEngine(t, backend.NameDocker)
			req := lockinRequest(prepareRunSession(t))
			req.Network.Mode = mode
			req.Network.Gateway = true

			if _, err := b.Start(context.Background(), req); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			gw := runInvocation(t, engineInvocations(t, logPath, home), req.Session.Name+model.GatewayContainerSuffix)
			if want := "--env " + model.EnvGatewayNetworkMode + "=" + string(mode) + " "; !strings.Contains(gw, want) {
				t.Errorf("gateway invocation lacks %q:\n%s", want, gw)
			}
		})
	}
}

// Full mode in unrestricted sessions logs requests without a capture store.
func TestUnrestrictedGatewayReceivesRequestsLogMode(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		for _, mode := range []string{model.NetworkLogRequests, model.NetworkLogFull} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				b, home := lockinBackend(t)
				b.opts.NetworkLogMode = mode
				logPath := lockinEngine(t, engine)
				req := unrestrictedGatewayRequest(t)

				if _, err := b.Start(context.Background(), req); err != nil {
					t.Fatalf("Start() error = %v", err)
				}

				gw := runInvocation(t, engineInvocations(t, logPath, home), req.Session.Name+model.GatewayContainerSuffix)
				for _, want := range []string{"--env " + model.EnvNetworkLogMode + "=" + model.NetworkLogRequests, "target=/tls/ca.key"} {
					if !strings.Contains(gw, want) {
						t.Errorf("gateway invocation lacks %q:\n%s", want, gw)
					}
				}
				if strings.Contains(gw, model.EnvNetworkCaptureDir) || strings.Contains(gw, "target=/var/log/enclave/capture") {
					t.Errorf("unrestricted gateway received a capture store:\n%s", gw)
				}
				if dir := config.HostProjectNetworkCaptureDir(home, req.Session.ProjectHash, req.Session.Tool); util.PathExists(dir) {
					t.Errorf("unrestricted session created capture directory %s", dir)
				}
			})
		}
	}
}

// failingGatewayEngine answers like lockinEngine but fails every container run.
func failingGatewayEngine(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, backend.NameDocker)
	script := `#!/bin/sh
case "$1 $2" in
"image inspect") printf '%s\n' '{"Id":"sha256:gateway"}' ;;
"container inspect") printf 'Error: No such container\n' >&2; exit 1 ;;
"run "*) printf 'boom\n' >&2; exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil { // #nosec G306 -- test stub must be executable.
		t.Fatal(err)
	}
	previous := dockercmd.Binary()
	dockercmd.SetBinary(stub)
	t.Cleanup(func() { dockercmd.SetBinary(previous) })
}

// A failing observing gateway fails the session and names the way out.
func TestUnrestrictedGatewayStartFailureSuggestsDisableGateway(t *testing.T) {
	b, _ := lockinBackend(t)
	failingGatewayEngine(t)

	_, err := b.Start(context.Background(), unrestrictedGatewayRequest(t))
	if err == nil {
		t.Fatal("Start() succeeded although the gateway failed")
	}
	if !strings.Contains(err.Error(), "--disable-gateway") {
		t.Fatalf("error does not suggest --disable-gateway: %v", err)
	}
}

func TestRestrictedGatewayStartFailureDoesNotSuggestDisableGateway(t *testing.T) {
	b, _ := lockinBackend(t)
	failingGatewayEngine(t)
	req := lockinRequest(prepareRunSession(t))
	req.Network.Mode = backend.NetworkModeRestricted
	req.Network.Gateway = true

	_, err := b.Start(context.Background(), req)
	if err == nil {
		t.Fatal("Start() succeeded although the gateway failed")
	}
	if strings.Contains(err.Error(), "--disable-gateway") {
		t.Fatalf("restricted error suggests --disable-gateway: %v", err)
	}
}
