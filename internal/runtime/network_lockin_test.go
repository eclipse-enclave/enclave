// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

// sessionNetworkView is what the runtime hands the backend for a session's
// network, plus the restricted-only and gateway-only side effects decided
// elsewhere in the runtime.
type sessionNetworkView struct {
	network       backend.NetworkPolicy
	env           []string
	ports         []backend.PortMapping
	hostname      string
	gatewayCA     bool
	secretRelease bool
}

func lockinNetworkRuntime(t *testing.T, tool string, allowAll bool, persistedMode string) *Runtime {
	t.Helper()
	home := t.TempDir()
	if persistedMode != "" {
		policyPath := config.HostNetworkPolicyPath(home)
		if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(policyPath, []byte(`{"mode":"`+persistedMode+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Runtime{
		host:           model.Host{Home: home},
		project:        model.Project{Name: "project", Hash: "abc123abc123", Dir: "/work/project"},
		profile:        model.Profile{Name: tool},
		handler:        resolveHandler(nil),
		run:            model.RunOptions{AllowAllNetwork: allowAll, Ports: []string{"127.0.0.1:13000:3000"}},
		build:          model.BuildOptions{ImageName: "image"},
		ideBridgePorts: []string{"9800"},
	}
}

func viewSessionNetwork(t *testing.T, r *Runtime) sessionNetworkView {
	t.Helper()
	result, err := newNetworkManager(r).Prepare("enclave-session", model.AuthState{}, SecretMapping{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	req := r.backendRequest(&ExecutionContext{ContainerName: "enclave-session", Network: result.Network, Ports: result.Ports}, true, false)
	mounts := newMountAccumulator(nil, nil)
	if err := r.addGatewayCAMount(mounts); err != nil {
		t.Fatalf("addGatewayCAMount() error = %v", err)
	}
	_, gatewayCA := findMountByTarget(mounts.Mounts(), model.AgentGatewayCACertPath)
	releasing := []activeSecret{{ID: "api-key", EnvVars: []string{"API_KEY"}, ReleaseHTTP: &model.HTTPSecretReleaseConfig{Hosts: []string{"api.example.com"}, Header: "x-api-key"}}}
	return sessionNetworkView{
		network:       result.Network,
		env:           result.Env,
		ports:         result.Ports,
		hostname:      req.Hostname,
		gatewayCA:     gatewayCA,
		secretRelease: newAuthManager(r).shouldUseSecretReleases(releasing),
	}
}

// TestLockinUnrestrictedSessionNetwork pins the gateway-less unrestricted
// session: no gateway, no DNS-gateway signal, real hostname, no CA, no secret release. All
// routes to unrestricted mode and both policy-resolution paths (with and
// without a tool name) must produce it.
func TestLockinUnrestrictedSessionNetwork(t *testing.T) {
	want := sessionNetworkView{
		network: backend.NetworkPolicy{
			Mode:           backend.NetworkModeUnrestricted,
			IdeBridgePorts: []string{"9800"},
		},
		env:      []string{model.EnvIdeBridgePorts + "=9800"},
		ports:    []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "13000", ContainerPort: "3000", Protocol: "tcp"}},
		hostname: model.HostnamePrefix + "project",
	}
	for _, tool := range []string{"codex", ""} {
		for _, tc := range []struct {
			name          string
			allowAll      bool
			persistedMode string
		}{
			{"allow-all-network", true, ""},
			{"persisted unrestricted", false, model.NetworkModeUnrestricted},
			{"allow-all over restricted", true, model.NetworkModeRestricted},
		} {
			t.Run("tool="+tool+"/"+tc.name, func(t *testing.T) {
				got := viewSessionNetwork(t, lockinNetworkRuntime(t, tool, tc.allowAll, tc.persistedMode))
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("unrestricted session network drifted:\n got  %+v\n want %+v", got, want)
				}
			})
		}
	}
}

// TestLockinRestrictedSessionNetwork pins the restricted session: gateway
// with the effective egress policy, DNS-gateway signal, no session hostname
// (the session joins the gateway's namespace), CA mounted, secret release on.
func TestLockinRestrictedSessionNetwork(t *testing.T) {
	for _, tool := range []string{"codex", ""} {
		t.Run("tool="+tool, func(t *testing.T) {
			got := viewSessionNetwork(t, lockinNetworkRuntime(t, tool, false, ""))
			if got.network.Mode != backend.NetworkModeRestricted {
				t.Fatalf("mode = %q, want restricted", got.network.Mode)
			}
			if !got.network.Gateway {
				t.Fatal("restricted session must run a gateway")
			}
			if !slices.Equal(got.network.IdeBridgePorts, []string{"9800"}) {
				t.Fatalf("IDE bridge ports = %v", got.network.IdeBridgePorts)
			}
			if !slices.Equal(got.env, []string{model.EnvDNSGateway + "=1"}) {
				t.Fatalf("env = %v, want only the DNS-gateway signal", got.env)
			}
			if got.hostname != "" {
				t.Fatalf("hostname = %q, want none for a session joining the gateway namespace", got.hostname)
			}
			if !got.gatewayCA {
				t.Fatal("restricted session must mount the gateway CA")
			}
			if !got.secretRelease {
				t.Fatal("restricted session must release HTTP secrets through the gateway")
			}
			if len(got.ports) != 1 || got.ports[0].ContainerPort != "3000" {
				t.Fatalf("ports = %+v", got.ports)
			}
		})
	}
}
