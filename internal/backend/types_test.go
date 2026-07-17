// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package backend

import "testing"

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
