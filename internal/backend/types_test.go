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

func TestValidateAllowsDockerCapabilities(t *testing.T) {
	req := Request{
		Network: NetworkPolicy{Mode: NetworkModeRestricted},
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

func TestNetworkGatewayMediationIsIndependentOfRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy NetworkPolicy
		want   bool
	}{
		{"restricted", NetworkPolicy{Mode: NetworkModeRestricted}, true},
		{"legacy unrestricted bypass", NetworkPolicy{Mode: NetworkModeUnrestricted}, false},
		{"mediated unrestricted", NetworkPolicy{Mode: NetworkModeUnrestricted, RequireGateway: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.UsesGateway(); got != tc.want {
				t.Fatalf("UsesGateway() = %t, want %t", got, tc.want)
			}
		})
	}
	req := Request{Network: NetworkPolicy{Mode: NetworkModeUnrestricted, RequireGateway: true}}
	if err := Validate(req, Capabilities{}); err == nil {
		t.Fatal("accepted required credential mediation on unsupported backend")
	}
	if err := Validate(req, Capabilities{SecretHTTPRelease: true}); err != nil {
		t.Fatalf("destination restrictions unnecessarily required for mediation: %v", err)
	}
}
