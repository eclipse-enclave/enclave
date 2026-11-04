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
	"slices"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

// TestUnrestrictedSessionRunsObservingGateway covers an unrestricted session
// without a disabled gateway: a gateway runs and owns the session's ports, the
// session keeps Docker's resolver (no DNS-gateway signal) and its hostname,
// the CA is mounted, and secrets are injected as real values.
func TestUnrestrictedSessionRunsObservingGateway(t *testing.T) {
	for _, tool := range []string{"codex", ""} {
		for _, tc := range []struct {
			name          string
			allowAll      bool
			persistedMode string
		}{
			{"allow-all-network", true, ""},
			{"persisted unrestricted", false, model.NetworkModeUnrestricted},
		} {
			t.Run("tool="+tool+"/"+tc.name, func(t *testing.T) {
				got := viewSessionNetwork(t, lockinNetworkRuntime(t, tool, tc.allowAll, tc.persistedMode))
				if got.network.Mode != backend.NetworkModeUnrestricted || !got.network.Gateway {
					t.Fatalf("network = %+v, want unrestricted with a gateway", got.network)
				}
				if len(got.network.Egress.AllowedDomains) != 0 || len(got.network.Egress.DeniedDomains) != 0 {
					t.Fatalf("egress = %+v, want no policy", got.network.Egress)
				}
				if !slices.Equal(got.network.IdeBridgePorts, []string{"9800"}) {
					t.Fatalf("IDE bridge ports = %v", got.network.IdeBridgePorts)
				}
				if len(got.env) != 0 {
					t.Fatalf("env = %v, want none: the gateway forwards the ports and DNS stays Docker's", got.env)
				}
				if got.hostname != model.HostnamePrefix+"project" {
					t.Fatalf("hostname = %q, want the session hostname", got.hostname)
				}
				if !got.gatewayCA {
					t.Fatal("a session with a gateway must mount the gateway CA")
				}
				if got.secretRelease {
					t.Fatal("unrestricted session must inject real secret values")
				}
				if len(got.ports) != 1 || got.ports[0].ContainerPort != "3000" {
					t.Fatalf("ports = %+v", got.ports)
				}
			})
		}
	}
}

// The gateway image is shared by both modes, so an unrestricted gateway is
// built from the same allowlist as a restricted one; otherwise each mode
// would rebuild the image the other built.
func TestUnrestrictedGatewayUsesRestrictedAllowlistBuildInput(t *testing.T) {
	allowlistsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(allowlistsDir, "base.conf"), []byte("# base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withAllowlists := func(r *Runtime) *Runtime {
		r.paths = model.Paths{AllowlistsDir: allowlistsDir}
		return r
	}
	restricted := viewSessionNetwork(t, withAllowlists(lockinNetworkRuntime(t, "codex", false, "")))
	unrestricted := viewSessionNetwork(t, withAllowlists(lockinNetworkRuntime(t, "codex", true, "")))
	if restricted.network.Egress.AllowlistPath == "" {
		t.Fatal("restricted session has no allowlist path")
	}
	if unrestricted.network.Egress.AllowlistPath != restricted.network.Egress.AllowlistPath {
		t.Fatalf("unrestricted allowlist path = %q, want %q", unrestricted.network.Egress.AllowlistPath, restricted.network.Egress.AllowlistPath)
	}
}
