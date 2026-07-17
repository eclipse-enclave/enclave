// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import (
	"fmt"
	"strings"

	"enclave/internal/domainpattern"
)

func NormalizePolicyDomain(value string) (string, error) {
	domain, err := domainpattern.Normalize(value)
	if err != nil {
		return "", err
	}
	if domain == "" {
		return "", nil
	}
	if strings.HasPrefix(domain, "*.") {
		return "", fmt.Errorf("wildcard domains are not supported in network policy")
	}
	return domain, nil
}

func NormalizePolicy(policy Policy) (Policy, error) {
	global, err := normalizePolicyDomainList(policy.Domains.Global)
	if err != nil {
		return Policy{}, fmt.Errorf("normalize global domains: %w", err)
	}

	tools := make(map[string][]string, len(policy.Domains.Tools))
	for tool, domains := range policy.Domains.Tools {
		normalized, err := normalizePolicyDomainList(domains)
		if err != nil {
			return Policy{}, fmt.Errorf("normalize tool domains for %q: %w", tool, err)
		}
		if len(normalized) > 0 {
			tools[tool] = normalized
		}
	}

	policy.Domains.Global = global
	policy.Domains.Tools = tools
	return policy, nil
}

func normalizePolicyDomainList(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		domain, err := NormalizePolicyDomain(value)
		if err != nil {
			return nil, err
		}
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		normalized = append(normalized, domain)
	}
	return normalized, nil
}
