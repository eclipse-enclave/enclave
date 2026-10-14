// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

type AuthState struct {
	Providers map[string]ProviderAuthState
}

type ProviderAuthState struct {
	HasEnvCredential bool
	HasSession       bool
}

// SecretReleaseEntry is the wire format written by the host runtime and
// read by the gateway proxy. Both sides must agree on this shape.
type SecretReleaseEntry struct {
	SecretID    string   `json:"secret_id"`
	Placeholder string   `json:"placeholder"`
	Value       string   `json:"value"`
	Hosts       []string `json:"hosts"`
	Header      string   `json:"header"`
	Format      string   `json:"format,omitempty"`
	// ExactHosts matches Hosts by equality instead of the allowlist rule where
	// a bare pattern also covers its subdomains. Set for hosts selected at
	// runtime through serviceAuth.hostsFromCredential: that value names one
	// instance the token belongs to, so releasing it to every host beneath it
	// would widen exactly what naming the instance is meant to narrow.
	ExactHosts bool `json:"exact_hosts,omitempty"`
}

// ProviderInstance is a host that serviceAuth.hostsFromCredential selected in
// place of a service's declared release hosts, such as a GitLab instance
// named by GITLAB_HOST. A protected gateway mediates it like the provider the
// declared hosts belong to, whether or not a token for it is configured.
type ProviderInstance struct {
	Host          string   `json:"host"`
	DeclaredHosts []string `json:"declared_hosts"`
}

func (s AuthState) HasAnyEnvCredential() bool {
	for _, provider := range s.Providers {
		if provider.HasEnvCredential {
			return true
		}
	}
	return false
}

func (s AuthState) HasAnySession() bool {
	for _, provider := range s.Providers {
		if provider.HasSession {
			return true
		}
	}
	return false
}
