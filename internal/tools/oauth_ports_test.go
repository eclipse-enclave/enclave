// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tools

import (
	"testing"

	"enclave/internal/model"
)

func TestOAuthPortHintsDedupAndOverride(t *testing.T) {
	autoFalse := false
	ctx := model.RunContext{
		Profile: model.Profile{
			Providers: []model.ProviderConfig{
				{
					Name: "openai-codex",
					OAuthPorts: []model.OAuthPortConfig{
						{Port: "1455"},
					},
				},
				{
					Name: "other",
					OAuthPorts: []model.OAuthPortConfig{
						{Port: "1455", AutoHintWhenNoSession: &autoFalse},
					},
				},
			},
		},
		AuthState: model.AuthState{
			Providers: map[string]model.ProviderAuthState{
				"openai-codex": {HasSession: false},
				"other":        {HasSession: false},
			},
		},
	}

	hints := OAuthPortHints(ctx)
	if len(hints) != 1 || hints[0] != "1455" {
		t.Fatalf("expected one hint 1455, got %v", hints)
	}
}

func TestOAuthPortValidateRunInvalidPort(t *testing.T) {
	ctx := model.RunContext{
		Profile: model.Profile{
			Providers: []model.ProviderConfig{
				{
					Name: "openai-codex",
					OAuthPorts: []model.OAuthPortConfig{
						{Port: "not-a-port"},
					},
				},
			},
		},
		AuthState: model.AuthState{
			Providers: map[string]model.ProviderAuthState{
				"openai-codex": {},
			},
		},
	}

	if err := OAuthPortValidateRun(ctx); err == nil {
		t.Fatalf("expected error for invalid port")
	}
}

func TestOAuthPortValidateRunRequireMappingOverride(t *testing.T) {
	requireFalse := false
	ctx := model.RunContext{
		Profile: model.Profile{
			Providers: []model.ProviderConfig{
				{
					Name: "openai-codex",
					OAuthPorts: []model.OAuthPortConfig{
						{Port: "1455", RequireMappingWhenNoCredentials: &requireFalse},
					},
				},
			},
		},
		AuthState: model.AuthState{
			Providers: map[string]model.ProviderAuthState{
				"openai-codex": {},
			},
		},
	}

	if err := OAuthPortValidateRun(ctx); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestOAuthLoopbackPortsDedup(t *testing.T) {
	ctx := model.RunContext{
		Profile: model.Profile{
			Providers: []model.ProviderConfig{
				{
					Name: "openai-codex",
					OAuthPorts: []model.OAuthPortConfig{
						{Port: "1455"},
					},
				},
				{
					Name: "other",
					OAuthPorts: []model.OAuthPortConfig{
						{Port: "1455"},
					},
				},
			},
		},
		AuthState: model.AuthState{
			Providers: map[string]model.ProviderAuthState{
				"openai-codex": {},
				"other":        {},
			},
		},
		Run: model.RunOptions{
			Ports: []string{"1455"},
		},
	}

	ports := OAuthLoopbackPorts(ctx)
	if len(ports) != 1 || ports[0] != "1455" {
		t.Fatalf("expected one loopback port 1455, got %v", ports)
	}
}
