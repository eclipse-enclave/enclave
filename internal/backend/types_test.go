// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package backend

import (
	"slices"
	"testing"
)

func TestValidateFailsClosedForRestrictedNetwork(t *testing.T) {
	req := Request{Network: NetworkPolicy{Mode: NetworkModeRestricted}}
	if err := Validate(req, Capabilities{}); err == nil {
		t.Fatal("expected restricted network validation to fail without capability")
	}
}

func TestValidateFailsClosedForHTTPSecretRelease(t *testing.T) {
	req := Request{
		Network: NetworkPolicy{Mode: NetworkModeRestricted},
		Secrets: []SecretRelease{{SecretID: "api-key", HTTP: &HTTPReleaseRule{Hosts: []string{"example.com"}, Header: "Authorization"}}},
	}
	caps := Capabilities{RestrictedNetwork: true}
	if err := Validate(req, caps); err == nil {
		t.Fatal("expected HTTP secret release validation to fail without capability")
	}
}

// Nothing but the gateway enforces a restricted session's policy, so no
// backend may start one without it, whatever its capabilities.
func TestValidateRequiresGatewayForRestrictedNetwork(t *testing.T) {
	caps := Capabilities{RestrictedNetwork: true, SecretHTTPRelease: true}
	for _, tc := range []struct {
		name    string
		network NetworkPolicy
		wantErr bool
	}{
		{"restricted without gateway", NetworkPolicy{Mode: NetworkModeRestricted}, true},
		{"restricted with gateway", NetworkPolicy{Mode: NetworkModeRestricted, Gateway: true}, false},
		{"unrestricted without gateway", NetworkPolicy{Mode: NetworkModeUnrestricted}, false},
		{"unrestricted with gateway", NetworkPolicy{Mode: NetworkModeUnrestricted, Gateway: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(Request{Network: tc.network}, caps)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateAllowsDockerCapabilities(t *testing.T) {
	req := Request{
		Network: NetworkPolicy{Mode: NetworkModeRestricted, Gateway: true},
		Secrets: []SecretRelease{{SecretID: "api-key", HTTP: &HTTPReleaseRule{Hosts: []string{"example.com"}, Header: "Authorization"}}},
	}
	caps := Capabilities{RestrictedNetwork: true, SecretHTTPRelease: true}
	if err := Validate(req, caps); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestAttachIOCommandEnv(t *testing.T) {
	if env := (AttachIO{}).CommandEnv(); env != nil {
		t.Fatalf("without HostEnv the session process must inherit, got %d entries", len(env))
	}
	t.Setenv("ENCLAVE_TEST_AMBIENT", "kept")
	env := AttachIO{HostEnv: map[string]string{"ENCLAVE_TEST_B": "2", "ENCLAVE_TEST_A": "1"}}.CommandEnv()
	if !slices.Contains(env, "ENCLAVE_TEST_AMBIENT=kept") || !slices.Equal(env[len(env)-2:], []string{"ENCLAVE_TEST_A=1", "ENCLAVE_TEST_B=2"}) {
		t.Fatalf("env = %v", env)
	}
}
