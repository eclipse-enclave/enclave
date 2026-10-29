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

func resolveConfigOnly(global, project Defaults) model.Options {
	opts, _, _ := ResolveOptionsForTool(DefaultOptions(), model.OptionSources{}, global, project, "claude")
	return opts
}

func TestResolveOptionsMergesSelections(t *testing.T) {
	global := Defaults{
		Features:      []string{"github-cli"},
		ToolOverrides: map[string]Defaults{"claude": {HostConfigPaths: []string{"default", "-skills/"}}},
	}
	project := Defaults{
		Features:      []string{"+devtools"},
		ToolOverrides: map[string]Defaults{"claude": {HostConfigPaths: []string{"+commands/"}}},
	}
	opts := resolveConfigOnly(global, project)

	if want := []string{"github-cli", "devtools"}; !reflect.DeepEqual(opts.Features, want) {
		t.Fatalf("features mismatch: got %v want %v", opts.Features, want)
	}
	if opts.Sources.Features != model.SourceProject {
		t.Fatalf("features source mismatch: got %v want %v", opts.Sources.Features, model.SourceProject)
	}
	if want := []string{"default", "-skills/", "commands/"}; !reflect.DeepEqual(opts.HostConfigPaths, want) {
		t.Fatalf("host_config_paths mismatch: got %v want %v", opts.HostConfigPaths, want)
	}
	if opts.Sources.HostConfigPaths != model.SourceToolOverride {
		t.Fatalf("host_config_paths source mismatch: got %v want %v", opts.Sources.HostConfigPaths, model.SourceToolOverride)
	}
}

func TestResolveOptionsKeepsDirectivesAgainstImplicitDefaults(t *testing.T) {
	project := Defaults{
		Features:      []string{"+devtools", "-node-dev"},
		ToolOverrides: map[string]Defaults{"claude": {HostConfigPaths: []string{"-skills/"}}},
	}
	opts := resolveConfigOnly(Defaults{}, project)

	if want := []string{"+devtools", "-node-dev"}; !reflect.DeepEqual(opts.Features, want) {
		t.Fatalf("features directives should be preserved when base is implicit default, got %v want %v", opts.Features, want)
	}
	if want := []string{"-skills/"}; !reflect.DeepEqual(opts.HostConfigPaths, want) {
		t.Fatalf("host_config_paths directives should be preserved against implicit defaults, got %v want %v", opts.HostConfigPaths, want)
	}
}

func TestResolveOptionsAllowDomainsFromGlobal(t *testing.T) {
	opts := resolveConfigOnly(Defaults{AllowDomains: []string{"api.example.com", "api.deepseek.com"}}, Defaults{})

	want := []string{"api.example.com", "api.deepseek.com"}
	if !reflect.DeepEqual(opts.AllowDomains, want) {
		t.Fatalf("allow_domains from global config should reach RunOptions: got %v want %v", opts.AllowDomains, want)
	}
	if opts.Sources.AllowDomains != model.SourceGlobal {
		t.Fatalf("allow_domains source should be global, got %v", opts.Sources.AllowDomains)
	}
}

func TestResolveOptionsHostConfigPathsAdditiveAgainstExplicitEmpty(t *testing.T) {
	global := Defaults{ToolOverrides: map[string]Defaults{"claude": {HostConfigPaths: []string{}}}}
	project := Defaults{ToolOverrides: map[string]Defaults{"claude": {HostConfigPaths: []string{"+statusbar/", "-skills/"}}}}
	opts := resolveConfigOnly(global, project)

	if want := []string{"statusbar/"}; !reflect.DeepEqual(opts.HostConfigPaths, want) {
		t.Fatalf("host_config_paths additive merge against explicit empty: got %v want %v", opts.HostConfigPaths, want)
	}
}

func TestResolveOptionsForTool_ExplicitFalseCLIBoolBeatsGlobal(t *testing.T) {
	opts := DefaultOptions()
	var sources model.OptionSources
	if err := CLIFlagIndex()["--no-cache"].Apply(&opts, &sources, "false"); err != nil {
		t.Fatal(err)
	}
	enabled := true
	resolved, _, _ := ResolveOptionsForTool(opts, sources, Defaults{NoCache: &enabled}, Defaults{}, "")
	if resolved.NoCache || resolved.Sources.NoCache != model.SourceCLI {
		t.Fatalf("no_cache = %v (source %v), want false from cli", resolved.NoCache, resolved.Sources.NoCache)
	}
}
