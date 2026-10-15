// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package policy

import (
	"reflect"
	"testing"

	"enclave/internal/model"
	"enclave/internal/network"
)

func TestSessionNetworkInputsMergeDedupesAllowDomains(t *testing.T) {
	inputs := SessionNetworkInputs{
		Profile:      model.Profile{AllowedDomains: []string{"spec.example.com"}},
		AllowDomains: []string{"Adhoc.Example.com", "adhoc.example.com", "SPEC.example.com"},
	}
	effective := inputs.Merge(network.MergeConfig{})
	if want := []string{"spec.example.com", "adhoc.example.com"}; !reflect.DeepEqual(effective.Domains, want) {
		t.Fatalf("domains = %v, want %v", effective.Domains, want)
	}
}
