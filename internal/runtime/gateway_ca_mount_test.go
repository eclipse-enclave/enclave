// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"slices"
	"testing"

	"enclave/internal/model"
	"enclave/internal/network"
	"enclave/internal/policy"
)

func gatewayCATestRuntime(t *testing.T, mode string) *Runtime {
	t.Helper()
	// Keep the gateway CA store under the test home on Linux.
	t.Setenv("XDG_STATE_HOME", "")
	return &Runtime{
		host:           model.Host{Home: t.TempDir()},
		policyResolved: true,
		policyResult:   policy.ResolveResult{Effective: network.EffectivePolicy{Mode: mode}},
	}
}

// Processes started with `docker exec` (e.g. an attached IDE backend) only see
// the container environment, so the CA variables must be set there, not only
// exported by the entrypoint.
func TestAddGatewayCAMountSetsTrustEnvOnContainer(t *testing.T) {
	r := gatewayCATestRuntime(t, model.NetworkModeRestricted)

	mounts := newMountAccumulator(nil, nil)
	if err := r.addGatewayCAMount(mounts); err != nil {
		t.Fatalf("addGatewayCAMount: %v", err)
	}

	if _, ok := findMountByTarget(mounts.Mounts(), model.AgentGatewayCACertPath); !ok {
		t.Fatalf("expected gateway CA mount at %s", model.AgentGatewayCACertPath)
	}
	for _, want := range []string{
		model.EnvGatewayCACertPath + "=" + model.AgentGatewayCACertPath,
		model.EnvGatewayCABundlePath + "=" + model.AgentGatewayCABundlePath,
		"SSL_CERT_FILE=" + model.AgentGatewayCABundlePath,
		"REQUESTS_CA_BUNDLE=" + model.AgentGatewayCABundlePath,
		"NODE_EXTRA_CA_CERTS=" + model.AgentGatewayCACertPath,
	} {
		if !slices.Contains(mounts.Env(), want) {
			t.Errorf("container env missing %q; got %v", want, mounts.Env())
		}
	}
}

func TestAddGatewayCAMountSkipsUnrestrictedNetwork(t *testing.T) {
	r := gatewayCATestRuntime(t, model.NetworkModeUnrestricted)

	mounts := newMountAccumulator(nil, nil)
	if err := r.addGatewayCAMount(mounts); err != nil {
		t.Fatalf("addGatewayCAMount: %v", err)
	}
	if len(mounts.Mounts()) != 0 || len(mounts.Env()) != 0 {
		t.Fatalf("expected no gateway CA mount or env, got mounts %v env %v", mounts.Mounts(), mounts.Env())
	}
}
