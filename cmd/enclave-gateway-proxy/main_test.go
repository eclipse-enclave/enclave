// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/gateway/oauthbroker"
	"enclave/internal/model"
)

func TestLoadOAuthBrokerRequiresExplicitCompleteConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, mode, identity, session, incarnation string
		instances                                  string
		wantBroker, wantError                      bool
	}{
		{name: "disabled"},
		{name: "unsupported", mode: "unknown", identity: "fresh", session: "session", incarnation: "first", wantError: true},
		{name: "missing-identity", mode: "claude", session: "session", incarnation: "first", wantError: true},
		{name: "missing-session", mode: "claude", identity: "fresh", incarnation: "first", wantError: true},
		{name: "missing-incarnation", mode: "claude", identity: "fresh", session: "session", wantError: true},
		{name: "orphan-identity", identity: "fresh", wantError: true},
		{name: "protected", mode: "claude", identity: "fresh", session: "session", incarnation: "first", wantBroker: true},
		{name: "instances", mode: "codex", identity: "fresh", session: "session", incarnation: "first", wantBroker: true,
			instances: `[{"host":"gitlab.example.com","declared_hosts":["gitlab.com","*.gitlab.com"]}]`},
		{name: "malformed-instances", mode: "codex", identity: "fresh", session: "session", incarnation: "first", instances: "{", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(model.EnvOAuthBroker, tc.mode)
			t.Setenv(model.EnvOAuthIdentity, tc.identity)
			t.Setenv(model.EnvGatewaySession, tc.session)
			t.Setenv(model.EnvOAuthIncarnation, tc.incarnation)
			t.Setenv(model.EnvOAuthGitHub, "")
			t.Setenv(model.EnvOAuthGitHubIdentity, "")
			t.Setenv(model.EnvOAuthInstances, tc.instances)
			root := t.TempDir()
			config := oauthbroker.Config{StoreDir: filepath.Join(root, "grant"), RuntimeDir: filepath.Join(root, "runtime"), LockPath: filepath.Join(root, "identity.lock")}
			view := filepath.Join(root, "view")
			for _, dir := range []string{config.StoreDir, config.RuntimeDir, view} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(config.LockPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			broker, err := loadOAuthBroker(config, view)
			if (err != nil) != tc.wantError || (broker != nil) != tc.wantBroker {
				t.Fatalf("broker configured=%t, error=%v", broker != nil, err)
			}
			if tc.wantError && !errors.Is(err, oauthbroker.ErrState) {
				t.Fatal("unsafe broker configuration did not fail closed")
			}
			if broker != nil && broker.RequiresMITM("gitlab.example.com") != (tc.instances != "") {
				t.Fatal("selected GitLab instance not mediated")
			}
			if tc.wantBroker {
				if err := os.WriteFile(filepath.Join(view, ".credentials.json"), []byte("client-managed"), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := loadOAuthBroker(config, view); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(view, ".credentials.json"))
				if err != nil || string(data) != "client-managed" {
					t.Fatal("proxy reload rewrote the workload view")
				}
			}
		})
	}
}

func TestLoadGitHubBrokerRequiresBoundFeatureConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, mode, github, identity string
		wantError                    bool
	}{
		{"feature without provider", "", "1", "github-identity", true},
		{"missing identity", "claude", "1", "", true},
		{"orphan identity", "claude", "", "github-identity", true},
		{"unsupported mode", "claude", "other", "github-identity", true},
		{"enabled", "claude", "1", "github-identity", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(model.EnvOAuthBroker, tc.mode)
			t.Setenv(model.EnvOAuthIdentity, "claude-identity")
			t.Setenv(model.EnvOAuthIncarnation, "incarnation")
			t.Setenv(model.EnvGatewaySession, "session")
			t.Setenv(model.EnvOAuthGitHub, tc.github)
			t.Setenv(model.EnvOAuthGitHubIdentity, tc.identity)
			root := t.TempDir()
			cfg := oauthbroker.Config{
				StoreDir: filepath.Join(root, "grant"), RuntimeDir: filepath.Join(root, "runtime"), LockPath: filepath.Join(root, "identity.lock"),
				GitHub: &oauthbroker.GitHubConfig{
					StoreDir: filepath.Join(root, "github-grant"), RuntimeDir: filepath.Join(root, "github-runtime"),
					ViewDir: filepath.Join(root, "github-view"), LockPath: filepath.Join(root, "github.lock"),
				},
			}
			view := filepath.Join(root, "view")
			for _, dir := range []string{cfg.StoreDir, cfg.RuntimeDir, view, cfg.GitHub.StoreDir, cfg.GitHub.RuntimeDir, cfg.GitHub.ViewDir} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, lock := range []string{cfg.LockPath, cfg.GitHub.LockPath} {
				if err := os.WriteFile(lock, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			broker, err := loadOAuthBroker(cfg, view)
			if (err != nil) != tc.wantError {
				t.Fatalf("load feature broker: %v, wantError=%v", err, tc.wantError)
			}
			if !tc.wantError && (broker == nil || !broker.RequiresMITM("github.com") || !broker.RequiresMITM("api.github.com")) {
				t.Fatal("GitHub endpoints not intercepted before login")
			}
		})
	}
}
