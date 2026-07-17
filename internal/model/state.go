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
