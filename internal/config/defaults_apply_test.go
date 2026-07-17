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

func TestApplyDefaultsWithSources_MergeSlices(t *testing.T) {
	opts := DefaultOptions()
	sources := model.DefaultOptionSources()

	global := Defaults{
		Features:        []string{"github-cli"},
		HostConfigPaths: []string{"default", "-skills/"},
	}
	project := Defaults{
		Features:        []string{"+devtools"},
		HostConfigPaths: []string{"+commands/"},
	}

	opts = ApplyDefaultsWithSources(opts, global, model.SourceGlobal, &sources)
	opts = ApplyDefaultsWithSources(opts, project, model.SourceProject, &sources)

	wantFeatures := []string{"devtools", "github-cli"}
	if !reflect.DeepEqual(opts.Features, wantFeatures) {
		t.Fatalf("features mismatch: got %v want %v", opts.Features, wantFeatures)
	}
	if sources.Features != model.SourceProject {
		t.Fatalf("features source mismatch: got %v want %v", sources.Features, model.SourceProject)
	}

	wantHostConfigPaths := []string{"default", "-skills/", "+commands/"}
	if !reflect.DeepEqual(opts.HostConfigPaths, wantHostConfigPaths) {
		t.Fatalf("host_config_paths mismatch: got %v want %v", opts.HostConfigPaths, wantHostConfigPaths)
	}
	if sources.HostConfigPaths != model.SourceProject {
		t.Fatalf("host_config_paths source mismatch: got %v want %v", sources.HostConfigPaths, model.SourceProject)
	}
}

func TestApplyDefaultsWithSources_AdditiveAgainstImplicitDefaults(t *testing.T) {
	opts := DefaultOptions()
	sources := model.DefaultOptionSources()

	project := Defaults{
		Features:        []string{"+devtools"},
		HostConfigPaths: []string{"-skills/"},
	}

	opts = ApplyDefaultsWithSources(opts, project, model.SourceProject, &sources)

	wantFeatures := []string{"+devtools"}
	if !reflect.DeepEqual(opts.Features, wantFeatures) {
		t.Fatalf("features additive directives should be preserved when base is implicit default, got %v want %v", opts.Features, wantFeatures)
	}
	wantHostConfigPaths := []string{"-skills/"}
	if !reflect.DeepEqual(opts.HostConfigPaths, wantHostConfigPaths) {
		t.Fatalf("host_config_paths directives should be preserved against implicit defaults, got %v want %v", opts.HostConfigPaths, wantHostConfigPaths)
	}
}

func TestApplyDefaultsWithSources_SubtractiveAgainstImplicitAll(t *testing.T) {
	opts := DefaultOptions()
	sources := model.DefaultOptionSources()

	project := Defaults{
		Features: []string{"-node-dev"},
	}

	opts = ApplyDefaultsWithSources(opts, project, model.SourceProject, &sources)

	wantFeatures := []string{"-node-dev"}
	if !reflect.DeepEqual(opts.Features, wantFeatures) {
		t.Fatalf("features subtractive directives should be preserved when base is implicit default, got %v want %v", opts.Features, wantFeatures)
	}
}

// allow_domains widens the gateway egress allowlist, so it is a guarded
// project-config option (stripped by applyProjectOverrideGuardrails — see
// TestProjectOverrideGuardrails_StripsAllowDomains). It still flows through
// end-to-end from the trusted sources: global config (asserted here) and the
// --allow-domain CLI flag.
func TestApplyDefaultsWithSources_AllowDomainsFromGlobal(t *testing.T) {
	opts := DefaultOptions()
	sources := model.DefaultOptionSources()

	global := Defaults{
		AllowDomains: []string{"api.example.com", "api.deepseek.com"},
	}
	opts = ApplyDefaultsWithSources(opts, global, model.SourceGlobal, &sources)

	want := []string{"api.example.com", "api.deepseek.com"}
	if !reflect.DeepEqual(opts.AllowDomains, want) {
		t.Fatalf("allow_domains from global config should reach RunOptions: got %v want %v", opts.AllowDomains, want)
	}
	if sources.AllowDomains != model.SourceGlobal {
		t.Fatalf("allow_domains source should be global, got %v", sources.AllowDomains)
	}
}

func TestApplyDefaultsWithSources_HostConfigPathsAdditiveAgainstExplicitEmpty(t *testing.T) {
	opts := DefaultOptions()
	sources := model.DefaultOptionSources()

	global := Defaults{HostConfigPaths: []string{}}
	project := Defaults{HostConfigPaths: []string{"+statusbar/", "-skills/"}}

	opts = ApplyDefaultsWithSources(opts, global, model.SourceGlobal, &sources)
	opts = ApplyDefaultsWithSources(opts, project, model.SourceProject, &sources)

	want := []string{"statusbar/"}
	if !reflect.DeepEqual(opts.HostConfigPaths, want) {
		t.Fatalf("host_config_paths additive merge against explicit empty: got %v want %v", opts.HostConfigPaths, want)
	}
}
