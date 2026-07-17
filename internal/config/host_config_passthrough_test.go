// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"reflect"
	"testing"

	"enclave/internal/model"
)

func TestHostConfigPassthroughDefaultsFailClosed(t *testing.T) {
	profile := model.Profile{
		PassthroughPaths: []string{"settings.json", "history.jsonl", "commands/"},
		Providers: []model.ProviderConfig{
			{Name: "anthropic", AuthFiles: []string{"config.json"}},
		},
		HostOAuthJSON: ".claude.json",
	}

	got := HostConfigPassthroughDefaults(profile)
	want := []string{"commands/", "settings.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected passthrough defaults: got %v want %v", got, want)
	}
}

func TestResolveHostConfigPathsSupportsDefaultKeywordAndDirectives(t *testing.T) {
	profile := model.Profile{
		PassthroughPaths: []string{"commands/", "settings.json", "skills/"},
	}

	got := ResolveHostConfigPaths(profile, []string{"default", "-skills/", "+agents/"})
	want := []string{"agents/", "commands/", "settings.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected resolved passthrough paths: got %v want %v", got, want)
	}
}

func TestResolveHostConfigPathsSupportsExplicitOverride(t *testing.T) {
	profile := model.Profile{
		PassthroughPaths: []string{"commands/", "settings.json"},
	}

	got := ResolveHostConfigPaths(profile, []string{"agents/"})
	want := []string{"agents/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected explicit passthrough override: got %v want %v", got, want)
	}
}

func TestResolveHostConfigPathsAdditiveAgainstDefaults(t *testing.T) {
	profile := model.Profile{
		PassthroughPaths: []string{"commands/", "settings.json", "skills/"},
	}

	got := ResolveHostConfigPaths(profile, []string{"-skills/", "+statusbar/"})
	want := []string{"commands/", "settings.json", "statusbar/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected additive passthrough resolution: got %v want %v", got, want)
	}
}

func TestHostConfigPassthroughDefaultsBlocksHostCredentialsFile(t *testing.T) {
	profile := model.Profile{
		PassthroughPaths:    []string{"commands/", ".credentials.json"},
		HostCredentialsFile: ".credentials.json",
	}

	got := HostConfigPassthroughDefaults(profile)
	want := []string{"commands/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected passthrough defaults: got %v want %v", got, want)
	}
}

func TestHostConfigPathMatchesGlobAgainstBasename(t *testing.T) {
	if !HostConfigPathMatches("nested/claude.history", "*.history") {
		t.Fatal("expected glob backstop to match nested basename")
	}
}

func TestHostConfigPassthroughBlocksPiAgentSessions(t *testing.T) {
	profile := model.Profile{
		PassthroughPaths: []string{"agent/settings.json", "agent/sessions/"},
	}

	got := HostConfigPassthroughDefaults(profile)
	want := []string{"agent/settings.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected passthrough defaults: got %v want %v", got, want)
	}
}
