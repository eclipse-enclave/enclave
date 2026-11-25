// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tools

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
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

func TestOAuthPortValidateRunDetectsPortInUse(t *testing.T) {
	tests := []struct {
		name     string
		heldOn   string
		hostIP   string
		reported string
	}{
		{name: "held on loopback, default address", heldOn: "127.0.0.1", reported: "127.0.0.1"},
		{name: "held on loopback, published on loopback", heldOn: "127.0.0.1", hostIP: "127.0.0.1", reported: "127.0.0.1"},
		{name: "held on loopback, published on wildcard", heldOn: "127.0.0.1", hostIP: "0.0.0.0"},
		{name: "held on wildcard, default address", heldOn: "0.0.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", net.JoinHostPort(tt.heldOn, "0"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
			spec := port
			if tt.hostIP != "" {
				spec = tt.hostIP + ":" + port + ":" + port
			}

			err = OAuthPortValidateRun(oauthPortRunContext(port, spec))
			if err == nil || !strings.Contains(err.Error(), "already in use") {
				t.Fatalf("expected port-in-use error, got %v", err)
			}
			if tt.reported != "" && !strings.Contains(err.Error(), "in use on "+tt.reported+";") {
				t.Fatalf("expected the error to name %s, got %v", tt.reported, err)
			}
		})
	}
}

func TestOAuthPortValidateRunReportsUnusableAddress(t *testing.T) {
	// 192.0.2.1 is reserved for documentation, so no host has it.
	err := OAuthPortValidateRun(oauthPortRunContext("1455", "192.0.2.1:1455:1455"))
	if !errors.Is(err, syscall.EADDRNOTAVAIL) || strings.Contains(err.Error(), "already in use") {
		t.Fatalf("expected the listen error, got %v", err)
	}
}

func oauthPortRunContext(port string, spec string) model.RunContext {
	return model.RunContext{
		Profile: model.Profile{
			Providers: []model.ProviderConfig{
				{
					Name:       "openai-codex",
					OAuthPorts: []model.OAuthPortConfig{{Port: port}},
				},
			},
		},
		AuthState: model.AuthState{
			Providers: map[string]model.ProviderAuthState{
				"openai-codex": {},
			},
		},
		Run: model.RunOptions{Ports: []string{spec}},
	}
}

func TestPublishedHostIP(t *testing.T) {
	tests := []struct {
		ports []string
		want  string
	}{
		{ports: []string{"1455"}, want: "127.0.0.1"},
		{ports: []string{"1455:1455"}, want: "127.0.0.1"},
		{ports: []string{"0.0.0.0:1455:1455"}, want: "0.0.0.0"},
		{ports: []string{"[::1]:1455:1455"}, want: "::1"},
		{ports: []string{"0.0.0.0:8080:1455", "1455"}, want: "127.0.0.1"},
	}
	for _, tt := range tests {
		if got := publishedHostIP(tt.ports, "1455"); got != tt.want {
			t.Errorf("publishedHostIP(%v) = %q, want %q", tt.ports, got, tt.want)
		}
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
