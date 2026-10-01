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

// cliFeatureOptions mirrors what parsing `--features <value>` leaves behind.
func cliFeatureOptions(t *testing.T, value string) (model.Options, model.OptionSources) {
	t.Helper()
	opts := DefaultOptions()
	if err := applyFeatures(&opts, value); err != nil {
		t.Fatalf("applyFeatures(%q): %v", value, err)
	}
	sources := model.DefaultOptionSources()
	sources.Features = model.SourceCLI
	return opts, sources
}

func resolvedFeatures(t *testing.T, cliValue string, global Defaults, project Defaults) ([]string, model.OptionSource) {
	t.Helper()
	cliOpts, cliSources := cliFeatureOptions(t, cliValue)
	opts, _, _ := ResolveOptionsForTool(cliOpts, cliSources, global, project, "")
	return opts.Features, opts.Sources.Features
}

func TestResolveOptionsForTool_AdditiveCLIFeaturesAmendGlobal(t *testing.T) {
	global := Defaults{Features: []string{"+shell-extras", "-node-dev"}}

	got, source := resolvedFeatures(t, "+playwright", global, Defaults{})

	want := []string{"+shell-extras", "-node-dev", "+playwright"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("additive --features should amend the global selection: got %v want %v", got, want)
	}
	if source != model.SourceCLI {
		t.Fatalf("features source mismatch: got %v want %v", source, model.SourceCLI)
	}
}

func TestResolveOptionsForTool_AdditiveCLIFeaturesAmendProjectAndToolOverride(t *testing.T) {
	global := Defaults{
		Tool:     "claude",
		Features: []string{"node-dev"},
	}
	project := Defaults{
		Features: []string{"+shell-extras"},
		ToolOverrides: map[string]Defaults{
			"claude": {Features: []string{"+devtools"}},
		},
	}

	got, _ := resolvedFeatures(t, "+playwright", global, project)

	want := []string{"node-dev", "+shell-extras", "+devtools", "+playwright"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("additive --features should amend every configured layer: got %v want %v", got, want)
	}
}

func TestResolveOptionsForTool_BareCLIFeaturesReplaceConfigured(t *testing.T) {
	global := Defaults{Features: []string{"+shell-extras", "-node-dev"}}

	got, _ := resolvedFeatures(t, "github-cli,python-dev", global, Defaults{})

	want := []string{"github-cli", "python-dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("an unprefixed --features list should replace the configured selection: got %v want %v", got, want)
	}
}

func TestResolveOptionsForTool_CLIFeaturesNoneReplacesConfigured(t *testing.T) {
	global := Defaults{Features: []string{"+shell-extras"}}

	got, _ := resolvedFeatures(t, "none", global, Defaults{})

	if got == nil || len(got) != 0 {
		t.Fatalf("--features none should select no features, got %v", got)
	}
}

func TestResolveOptionsForTool_AdditiveCLIFeaturesAgainstConfiguredNone(t *testing.T) {
	global := Defaults{Features: []string{}}

	got, _ := resolvedFeatures(t, "+playwright", global, Defaults{})

	want := []string{"playwright"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("additive --features on top of \"none\" should add only that feature: got %v want %v", got, want)
	}
}

func TestResolveOptionsForTool_HigherLayerWinsDirectiveConflict(t *testing.T) {
	cases := []struct {
		name      string
		cliValue  string
		global    Defaults
		project   Defaults
		want      []string
		wantAfter model.OptionSource
	}{
		{
			name:      "project re-enables what global removed",
			cliValue:  "+playwright",
			global:    Defaults{Features: []string{"-devtools"}},
			project:   Defaults{Features: []string{"+devtools"}},
			want:      []string{"+devtools", "+playwright"},
			wantAfter: model.SourceCLI,
		},
		{
			name:      "cli removes what project enabled",
			cliValue:  "-devtools",
			global:    Defaults{Features: []string{"+shell-extras"}},
			project:   Defaults{Features: []string{"+devtools"}},
			want:      []string{"+shell-extras", "-devtools"},
			wantAfter: model.SourceCLI,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, source := resolvedFeatures(t, tc.cliValue, tc.global, tc.project)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("features mismatch: got %v want %v", got, tc.want)
			}
			if source != tc.wantAfter {
				t.Fatalf("features source mismatch: got %v want %v", source, tc.wantAfter)
			}
		})
	}
}

func TestResolveToolOverrideDefaults_AdditiveFeaturesAmendGlobalOverride(t *testing.T) {
	global := Defaults{
		ToolOverrides: map[string]Defaults{
			"claude": {Features: []string{"node-dev"}},
		},
	}
	project := Defaults{
		ToolOverrides: map[string]Defaults{
			"claude": {Features: []string{"+devtools"}},
		},
	}

	merged, ok := ResolveToolOverrideDefaults(global, project, "claude")
	if !ok {
		t.Fatal("expected tool override defaults for claude")
	}

	want := []string{"node-dev", "+devtools"}
	if !reflect.DeepEqual(merged.Features, want) {
		t.Fatalf("project tool override should amend the global one: got %v want %v", merged.Features, want)
	}
}

func TestMergeFeatureSlice(t *testing.T) {
	cases := []struct {
		name     string
		base     []string
		override []string
		want     []string
	}{
		{name: "nil base keeps directives", base: nil, override: []string{"+devtools"}, want: []string{"+devtools"}},
		{name: "nil override keeps base", base: []string{"node-dev"}, override: nil, want: []string{"node-dev"}},
		{name: "bare override replaces", base: []string{"+devtools"}, override: []string{"node-dev"}, want: []string{"node-dev"}},
		{name: "selector override replaces", base: []string{"+devtools"}, override: []string{"all"}, want: []string{"all"}},
		{name: "explicit empty override replaces", base: []string{"+devtools"}, override: []string{}, want: []string{}},
		{name: "additive override amends", base: []string{"node-dev"}, override: []string{"+devtools"}, want: []string{"node-dev", "+devtools"}},
		{name: "override wins conflict", base: []string{"-devtools", "+shell-extras"}, override: []string{"+devtools"}, want: []string{"+shell-extras", "+devtools"}},
		{name: "conflicting bare base entry stays as anchor", base: []string{"node-dev"}, override: []string{"-node-dev"}, want: []string{"node-dev", "-node-dev"}},
		{name: "re-added bare base entry stays as anchor", base: []string{"node-dev"}, override: []string{"+node-dev"}, want: []string{"node-dev", "+node-dev"}},
		{name: "additive on explicit empty resolves", base: []string{}, override: []string{"+devtools", "-node-dev"}, want: []string{"devtools"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeFeatureSlice(tc.base, tc.override)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeFeatureSlice(%v, %v) = %v want %v", tc.base, tc.override, got, tc.want)
			}
		})
	}
}
