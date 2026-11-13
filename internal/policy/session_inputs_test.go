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

	"enclave/internal/domainpattern"
	"enclave/internal/filter"
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

// The allowlist entries generated from governed hosts admit exactly the names
// the filter governs: an entry's own name and every name below it.
func TestFilterHostAllowlistEntriesAreGoverned(t *testing.T) {
	governed := []string{"a.example.com", "*.b.example.org"}
	effective := SessionNetworkInputs{FilterHosts: governed}.Merge(network.MergeConfig{})
	if want := []string{"a.example.com", "b.example.org"}; !reflect.DeepEqual(effective.Domains, want) {
		t.Fatalf("domains = %v, want %v", effective.Domains, want)
	}
	if want := []string{"a.example.com", "b.example.org"}; !reflect.DeepEqual(effective.GovernedDomains, want) {
		t.Fatalf("governed domains = %v, want %v", effective.GovernedDomains, want)
	}
	for _, entry := range effective.Domains {
		for _, admitted := range []string{entry, "x." + entry, "y.x." + entry} {
			if !domainpattern.MatchNormalizedHost(admitted, entry) {
				t.Fatalf("allowlist entry %s does not admit %s", entry, admitted)
			}
			if !filter.HostGoverned(admitted, governed) {
				t.Errorf("allowlist entry %s admits %s, which no filter governs", entry, admitted)
			}
		}
	}
}
