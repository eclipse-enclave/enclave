// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestValidateAuthOptions_CLIOverridesToolOverride(t *testing.T) {
	tests := []struct {
		name           string
		opts           model.Options
		sources        model.OptionSources
		wantErr        bool
		wantNoAPIKey   bool
		wantPassAPIKey bool
		wantEphemeral  bool
		wantResetAuth  bool
	}{
		{
			name:           "CLI pass-api-key clears tool-override no-api-key",
			opts:           optsWithAuth(false, false, true, true),
			sources:        withAuthSources(model.SourceToolOverride, model.SourceCLI, 0, 0),
			wantNoAPIKey:   false,
			wantPassAPIKey: true,
		},
		{
			name:           "CLI no-api-key clears tool-override pass-api-key",
			opts:           optsWithAuth(false, false, true, true),
			sources:        withAuthSources(model.SourceCLI, model.SourceToolOverride, 0, 0),
			wantNoAPIKey:   true,
			wantPassAPIKey: false,
		},
		{
			name:    "same source conflicts are errors",
			opts:    optsWithAuth(false, false, true, true),
			sources: withAuthSources(model.SourceCLI, model.SourceCLI, 0, 0),
			wantErr: true,
		},
		{
			name:          "CLI ephemeral clears config reset-auth",
			opts:          optsWithRun(true, true),
			sources:       withAuthSources(0, 0, model.SourceCLI, model.SourceGlobal),
			wantEphemeral: true,
			wantResetAuth: false,
		},
		{
			name:          "config ephemeral cleared by CLI reset-auth",
			opts:          optsWithRun(true, true),
			sources:       withAuthSources(0, 0, model.SourceGlobal, model.SourceCLI),
			wantEphemeral: false,
			wantResetAuth: true,
		},
		{
			name:           "no conflict passes through unchanged",
			opts:           optsWithFields(false, true, true, false),
			sources:        withAuthSources(0, model.SourceCLI, model.SourceCLI, 0),
			wantPassAPIKey: true,
			wantEphemeral:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotSources, err := validateAuthOptions(tt.opts, tt.sources)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.NoAPIKey != tt.wantNoAPIKey {
				t.Errorf("NoAPIKey = %v, want %v", got.NoAPIKey, tt.wantNoAPIKey)
			}
			if got.PassAPIKey != tt.wantPassAPIKey {
				t.Errorf("PassAPIKey = %v, want %v", got.PassAPIKey, tt.wantPassAPIKey)
			}
			if got.Ephemeral != tt.wantEphemeral {
				t.Errorf("Ephemeral = %v, want %v", got.Ephemeral, tt.wantEphemeral)
			}
			if got.ResetAuth != tt.wantResetAuth {
				t.Errorf("ResetAuth = %v, want %v", got.ResetAuth, tt.wantResetAuth)
			}
			// Cleared options should have SourceUnset
			if !tt.wantNoAPIKey && tt.opts.NoAPIKey && gotSources.NoAPIKey != model.SourceUnset {
				t.Errorf("cleared NoAPIKey source = %v, want SourceUnset", gotSources.NoAPIKey)
			}
			if !tt.wantPassAPIKey && tt.opts.PassAPIKey && gotSources.PassAPIKey != model.SourceUnset {
				t.Errorf("cleared PassAPIKey source = %v, want SourceUnset", gotSources.PassAPIKey)
			}
			if !tt.wantEphemeral && tt.opts.Ephemeral && gotSources.Ephemeral != model.SourceUnset {
				t.Errorf("cleared Ephemeral source = %v, want SourceUnset", gotSources.Ephemeral)
			}
			if !tt.wantResetAuth && tt.opts.ResetAuth && gotSources.ResetAuth != model.SourceUnset {
				t.Errorf("cleared ResetAuth source = %v, want SourceUnset", gotSources.ResetAuth)
			}
		})
	}
}

// optsWithAuth sets NoAPIKey and PassAPIKey (and optionally Ephemeral/ResetAuth).
func optsWithAuth(ephemeral, resetAuth, noAPIKey, passAPIKey bool) model.Options {
	return model.Options{
		RunOptions:  model.RunOptions{Ephemeral: ephemeral},
		AuthOptions: model.AuthOptions{ResetAuth: resetAuth, NoAPIKey: noAPIKey, PassAPIKey: passAPIKey},
	}
}

// optsWithRun sets Ephemeral and ResetAuth.
func optsWithRun(ephemeral, resetAuth bool) model.Options {
	return model.Options{
		RunOptions:  model.RunOptions{Ephemeral: ephemeral},
		AuthOptions: model.AuthOptions{ResetAuth: resetAuth},
	}
}

// optsWithFields sets all four fields independently.
func optsWithFields(noAPIKey, passAPIKey, ephemeral, resetAuth bool) model.Options {
	return model.Options{
		RunOptions:  model.RunOptions{Ephemeral: ephemeral},
		AuthOptions: model.AuthOptions{ResetAuth: resetAuth, NoAPIKey: noAPIKey, PassAPIKey: passAPIKey},
	}
}

func withAuthSources(noAPIKey, passAPIKey, ephemeral, resetAuth model.OptionSource) model.OptionSources {
	s := model.DefaultOptionSources()
	s.NoAPIKey = noAPIKey
	s.PassAPIKey = passAPIKey
	s.Ephemeral = ephemeral
	s.ResetAuth = resetAuth
	return s
}

func TestEnsureFeature(t *testing.T) {
	tests := []struct {
		name     string
		features []string
		add      string
		want     []string
	}{
		{
			name:     "nil becomes default plus feature",
			features: nil,
			add:      "playwright",
			want:     []string{model.SelectionDefault, "playwright"},
		},
		{
			name:     "already present is a no-op",
			features: []string{"default", "playwright"},
			add:      "playwright",
			want:     []string{"default", "playwright"},
		},
		{
			name:     "appends when missing",
			features: []string{"default", "devtools"},
			add:      "playwright",
			want:     []string{"default", "devtools", "playwright"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureFeature(tt.features, tt.add)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestValidateOptions_NoRebuildConflicts(t *testing.T) {
	tests := []struct {
		name    string
		opts    model.Options
		wantErr string
	}{
		{
			name: "conflicts with rebuild",
			opts: model.Options{
				BuildOptions: model.BuildOptions{
					NoRebuild:    true,
					ForceRebuild: true,
					ImageName:    "enclave:latest",
				},
			},
			wantErr: "--no-rebuild is incompatible with --rebuild",
		},
	}

	ctx := ValidationContext{Action: "run"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := ValidateOptions(tt.opts, model.DefaultOptionSources(), ctx)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: got %q want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateOptions_BuildOptionConflicts(t *testing.T) {
	tests := []struct {
		name    string
		opts    model.Options
		sources model.OptionSources
		wantErr string
	}{
		{
			name: "invalid build uid",
			opts: model.Options{
				BuildOptions: model.BuildOptions{
					BuildUID:  "-1",
					ImageName: "enclave:latest",
				},
			},
			sources: model.DefaultOptionSources(),
			wantErr: "--build-uid requires a non-negative numeric value",
		},
	}

	ctx := ValidationContext{Action: "run"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := ValidateOptions(tt.opts, tt.sources, ctx)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: got %q want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateOptions_ProjectMount(t *testing.T) {
	ctx := ValidationContext{Action: "run"}

	opts := model.Options{
		RunOptions:   model.RunOptions{ProjectMount: "READONLY"},
		BuildOptions: model.BuildOptions{ImageName: "enclave:latest"},
	}
	got, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ctx)
	if err != nil {
		t.Fatalf("ValidateOptions returned error: %v", err)
	}
	if got.ProjectMount != model.ProjectMountReadonly {
		t.Fatalf("ProjectMount = %q, want %q", got.ProjectMount, model.ProjectMountReadonly)
	}

	opts.ProjectMount = "bad"
	_, _, _, err = ValidateOptions(opts, model.DefaultOptionSources(), ctx)
	if err == nil {
		t.Fatal("expected invalid project mount error, got nil")
	}
	if !strings.Contains(err.Error(), "--project-mount must be") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateOptions_WorktreeMetadata(t *testing.T) {
	ctx := ValidationContext{Action: "run"}

	opts := model.Options{
		RunOptions:   model.RunOptions{WorktreeMetadata: "NONE"},
		BuildOptions: model.BuildOptions{ImageName: "enclave:latest"},
	}
	got, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ctx)
	if err != nil {
		t.Fatalf("ValidateOptions returned error: %v", err)
	}
	if got.WorktreeMetadata != model.WorktreeMetadataNone {
		t.Fatalf("WorktreeMetadata = %q, want %q", got.WorktreeMetadata, model.WorktreeMetadataNone)
	}

	opts.WorktreeMetadata = ""
	got, _, _, err = ValidateOptions(opts, model.DefaultOptionSources(), ctx)
	if err != nil {
		t.Fatalf("ValidateOptions returned error: %v", err)
	}
	if got.WorktreeMetadata != model.WorktreeMetadataFollow {
		t.Fatalf("WorktreeMetadata = %q, want default %q", got.WorktreeMetadata, model.WorktreeMetadataFollow)
	}

	opts.WorktreeMetadata = "bad"
	_, _, _, err = ValidateOptions(opts, model.DefaultOptionSources(), ctx)
	if err == nil {
		t.Fatal("expected invalid worktree metadata error, got nil")
	}
	if !strings.Contains(err.Error(), "--worktree-metadata must be") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRunOptions_RuntimeUIDRemapConflictsWithDevcontainerRemoteUserShell(t *testing.T) {
	projectDir := t.TempDir()
	devcontainerDir := filepath.Join(projectDir, model.DevcontainerDir)
	if err := os.MkdirAll(devcontainerDir, 0o755); err != nil {
		t.Fatalf("mkdir devcontainer dir: %v", err)
	}
	devcontainerJSON := `{"image":"debian:trixie-slim","remoteUser":"node"}`
	if err := os.WriteFile(filepath.Join(devcontainerDir, model.DevcontainerFilename), []byte(devcontainerJSON), 0o644); err != nil {
		t.Fatalf("write devcontainer: %v", err)
	}

	opts := model.Options{
		RunOptions: model.RunOptions{
			Tool:  "claude",
			Shell: true,
		},
		BuildOptions: model.BuildOptions{
			Devcontainer:    true,
			RuntimeUIDRemap: true,
			ImageName:       "enclave:latest",
		},
	}
	project := model.Project{Dir: projectDir, RealDir: projectDir, Hash: "abc123def456", Name: "project"}
	ctx := ValidationContext{
		Paths:  model.Paths{AppRoot: projectDir},
		Action: "shell",
	}

	_, _, _, _, err := ValidateRunOptions(opts, model.DefaultOptionSources(), ctx, project)
	if err == nil {
		t.Fatal("expected runtime uid remap/devcontainer remoteUser conflict")
	}
	want := "--runtime-uid-remap is incompatible with devcontainer remoteUser in shell mode"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("unexpected error: got %q want substring %q", err.Error(), want)
	}
}

func TestValidatePlaywrightMCP_NonClaude(t *testing.T) {
	opts := model.Options{
		RunOptions: model.RunOptions{PlaywrightMCP: true, Tool: "codex"},
		BuildOptions: model.BuildOptions{
			ImageName: "test",
		},
	}
	sources := model.DefaultOptionSources()
	ctx := ValidationContext{Action: "info"}

	got, _, warnings, err := ValidateOptions(opts, sources, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PlaywrightMCP {
		t.Error("PlaywrightMCP should be false for non-claude tool")
	}
	found := false
	for _, w := range warnings {
		if w == "--playwright-mcp is only supported with Claude; ignoring" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about playwright-mcp, got %v", warnings)
	}
}

func TestValidateAllowDomainsLowercases(t *testing.T) {
	opts := model.Options{
		RunOptions: model.RunOptions{
			Tool:         "claude",
			AllowDomains: []string{"API.DeepSeek.com", "  example.org  "},
		},
		BuildOptions: model.BuildOptions{
			ImageName: "test",
		},
	}
	sources := model.DefaultOptionSources()
	ctx := ValidationContext{Action: "info"}

	got, _, _, err := ValidateOptions(opts, sources, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"api.deepseek.com", "example.org"}
	if len(got.AllowDomains) != len(want) {
		t.Fatalf("AllowDomains = %v, want %v", got.AllowDomains, want)
	}
	for i := range want {
		if got.AllowDomains[i] != want[i] {
			t.Fatalf("AllowDomains[%d] = %q, want %q", i, got.AllowDomains[i], want[i])
		}
	}
}

func TestValidateAllowDomainsRejectsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		domain string
	}{
		{"scheme", "http://api.example.com"},
		{"path", "api.example.com/foo"},
		{"empty", ""},
		{"single label", "localhost"},
		{"trailing dot", "api.example.com."},
		{"underscore", "api_test.example.com"},
		{"port", "api.example.com:443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := model.Options{
				RunOptions: model.RunOptions{
					Tool:         "claude",
					AllowDomains: []string{tc.domain},
				},
				BuildOptions: model.BuildOptions{
					ImageName: "test",
				},
			}
			sources := model.DefaultOptionSources()
			ctx := ValidationContext{Action: "info"}

			if _, _, _, err := ValidateOptions(opts, sources, ctx); err == nil {
				t.Fatalf("expected error for invalid domain %q", tc.domain)
			}
		})
	}
}

func TestValidatePlaywrightMCP_Claude(t *testing.T) {
	opts := model.Options{
		RunOptions: model.RunOptions{PlaywrightMCP: true, Tool: "claude"},
		BuildOptions: model.BuildOptions{
			ImageName: "test",
		},
	}
	sources := model.DefaultOptionSources()
	ctx := ValidationContext{Action: "info"}

	got, _, warnings, err := ValidateOptions(opts, sources, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.PlaywrightMCP {
		t.Error("PlaywrightMCP should remain true for claude tool")
	}
	for _, w := range warnings {
		if w == "--playwright-mcp is only supported with Claude; ignoring" {
			t.Error("should not warn about playwright-mcp for claude tool")
		}
	}
}

func TestValidateOptionsQEMURejectsBackground(t *testing.T) {
	opts := model.Options{
		RunOptions:   model.RunOptions{Backend: "qemu", Tool: "codex", Background: true},
		BuildOptions: model.BuildOptions{ImageName: "test"},
	}
	_, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run"})
	if err == nil || !strings.Contains(err.Error(), "--background") {
		t.Fatalf("expected detached-session rejection, got %v", err)
	}
}

func TestValidateOptionsQEMUImpliesAllowAllNetworkAndSlim(t *testing.T) {
	// No --slim, no --allow-all-network: the qemu backend implies both rather
	// than erroring, and warns that network isolation is unavailable.
	opts := model.Options{
		RunOptions:   model.RunOptions{Backend: "qemu", Tool: "codex"},
		BuildOptions: model.BuildOptions{ImageName: "test"},
	}
	got, _, warnings, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run"})
	if err != nil {
		t.Fatalf("expected qemu options to be coerced, got error: %v", err)
	}
	if !got.AllowAllNetwork {
		t.Error("expected --allow-all-network to be implied for qemu")
	}
	if !got.Slim {
		t.Error("expected --slim to be implied for qemu")
	}
	if len(got.Features) != 0 {
		t.Errorf("expected no features for qemu, got %v", got.Features)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "network isolation is unavailable") {
		t.Errorf("expected a network-isolation notice, got warnings: %v", warnings)
	}
}

func TestValidateOptionsQEMULogsImplicitlyDisabledFeatures(t *testing.T) {
	featuresDir := t.TempDir()
	// No defaultEnabled key => enabled by default (would be installed on docker).
	writeFeatureFixture(t, featuresDir, "alpha", `{"schemaVersion":"1","kind":"mixin","name":"alpha","description":"a"}`, 0, false)
	// Explicitly off by default => not dropped by qemu, so not worth logging.
	writeFeatureFixture(t, featuresDir, "beta", `{"schemaVersion":"1","kind":"mixin","name":"beta","description":"b","defaultEnabled":false}`, 0, false)

	opts := model.Options{
		RunOptions:   model.RunOptions{Backend: "qemu", Tool: "codex"},
		BuildOptions: model.BuildOptions{ImageName: "test"},
	}
	_, _, warnings, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run", Paths: model.Paths{FeaturesDir: featuresDir}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "not installing features") || !strings.Contains(joined, "alpha") {
		t.Errorf("expected default-enabled feature alpha logged as dropped, got: %v", warnings)
	}
	if strings.Contains(joined, "beta") {
		t.Errorf("beta is off by default and should not be listed, got: %v", warnings)
	}
}

func TestValidateOptionsQEMUDropsConfigFeaturesWithoutError(t *testing.T) {
	featuresDir := t.TempDir()
	writeFeatureFixture(t, featuresDir, "sample-feature", `{"schemaVersion":"1","kind":"mixin","name":"sample-feature","description":"p","defaultEnabled":false}`, 0, false)

	// A feature enabled via config (e.g. "+sample-feature"), not the CLI, must not
	// block --backend qemu; it is dropped and reported instead.
	opts := model.Options{
		RunOptions:   model.RunOptions{Backend: "qemu", Tool: "codex"},
		BuildOptions: model.BuildOptions{ImageName: "test"},
	}
	opts.Features = []string{"+sample-feature"}
	sources := model.DefaultOptionSources()
	sources.Features = model.SourceGlobal
	got, _, warnings, err := ValidateOptions(opts, sources, ValidationContext{Action: "run", Paths: model.Paths{FeaturesDir: featuresDir}})
	if err != nil {
		t.Fatalf("config-sourced feature should not error on qemu: %v", err)
	}
	if !got.Slim || len(got.Features) != 0 {
		t.Errorf("expected coercion to slim/no-features, got Slim=%v Features=%v", got.Slim, got.Features)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "sample-feature") {
		t.Errorf("expected sample-feature reported as dropped, got: %v", warnings)
	}
}

func TestValidateOptionsQEMURejectsUnsatisfiableRequests(t *testing.T) {
	base := model.Options{
		RunOptions:   model.RunOptions{Backend: "qemu", Tool: "codex", AllowAllNetwork: true},
		BuildOptions: model.BuildOptions{ImageName: "test", Slim: true},
	}

	// Features passed on the command line cannot be honored by a tool-only bundle.
	withFeatures := base
	withFeatures.Features = []string{"node-dev"}
	cliSources := model.DefaultOptionSources()
	cliSources.Features = model.SourceCLI
	if _, _, _, err := ValidateOptions(withFeatures, cliSources, ValidationContext{Action: "run"}); err == nil {
		t.Fatal("expected qemu feature validation error")
	} else if !strings.Contains(err.Error(), "cannot install features") {
		t.Fatalf("unexpected error: %v", err)
	}

	// An allowlist implies restricted networking, which qemu cannot provide.
	withAllowlist := base
	withAllowlist.AllowDomains = []string{"example.com"}
	if _, _, _, err := ValidateOptions(withAllowlist, model.DefaultOptionSources(), ValidationContext{Action: "run"}); err == nil {
		t.Fatal("expected qemu allow-domain validation error")
	} else if !strings.Contains(err.Error(), "cannot restrict network access") {
		t.Fatalf("unexpected error: %v", err)
	}

	// --features none (explicit empty) is accepted.
	withNone := base
	withNone.Features = []string{}
	if _, _, _, err := ValidateOptions(withNone, model.DefaultOptionSources(), ValidationContext{Action: "run"}); err != nil {
		t.Fatalf("qemu no-feature validation failed: %v", err)
	}
}

func TestValidateOptionsNetworkLogRequests(t *testing.T) {
	opts := model.Options{
		RunOptions: model.RunOptions{
			Tool:       "claude",
			NetworkLog: model.NetworkLogRequests,
		},
		BuildOptions: model.BuildOptions{
			ImageName: "test",
		},
	}

	got, _, warnings, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "info"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.NetworkLog != model.NetworkLogRequests {
		t.Fatalf("NetworkLog = %q, want %q", got.NetworkLog, model.NetworkLogRequests)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
}

func TestValidateOptionsNetworkLogInvalid(t *testing.T) {
	opts := model.Options{
		RunOptions: model.RunOptions{
			Tool:       "claude",
			NetworkLog: "invalid",
		},
		BuildOptions: model.BuildOptions{
			ImageName: "test",
		},
	}

	_, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "info"})
	if err == nil {
		t.Fatal("expected invalid network log mode to fail validation")
	}
}
