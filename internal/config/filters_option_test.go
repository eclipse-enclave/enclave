// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestFiltersListMerge(t *testing.T) {
	cases := []struct {
		name    string
		global  Defaults
		project Defaults
		cli     []string
		want    []string
	}{
		{name: "never enabled by default", want: nil},
		{name: "config selects", global: Defaults{Filters: []string{"gws:readonly"}}, want: []string{"gws:readonly"}},
		{name: "cli preset replaces the configured one", global: Defaults{Filters: []string{"gws:readonly", "x"}}, cli: []string{"+gws:send"}, want: []string{"x", "gws:send"}},
		{name: "cli removal drops any preset", global: Defaults{Filters: []string{"gws:readonly"}}, cli: []string{"-gws"}, want: []string{}},
		{name: "cli plain replaces", global: Defaults{Filters: []string{"gws"}}, cli: []string{"other"}, want: []string{"other"}},
		{name: "cli none clears", global: Defaults{Filters: []string{"gws"}}, cli: []string{}, want: []string{}},
		{name: "project amends global", global: Defaults{Filters: []string{"gws"}}, project: Defaults{Filters: []string{"+other:p"}}, want: []string{"gws", "other:p"}},
		{name: "project replaces global", global: Defaults{Filters: []string{"gws"}}, project: Defaults{Filters: []string{"none"}}, want: []string{"none"}},
		{
			name:   "tool override amends",
			global: Defaults{Filters: []string{"gws"}, ToolOverrides: map[string]Defaults{"claude": {Filters: []string{"+gws:edit"}}}},
			want:   []string{"gws:edit"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultOptions()
			sources := model.OptionSources{}
			if tc.cli != nil {
				opts.Filters = tc.cli
				sources.Filters = model.SourceCLI
			}
			got, _, _ := ResolveOptionsForTool(opts, sources, tc.global, tc.project, "claude")
			if !reflect.DeepEqual(got.Filters, tc.want) {
				t.Fatalf("filters = %#v, want %#v", got.Filters, tc.want)
			}
		})
	}
}

func TestApplyFiltersFlag(t *testing.T) {
	opts := model.Options{}
	if err := applyFilters(&opts, "gws:readonly, +other"); err != nil {
		t.Fatalf("applyFilters: %v", err)
	}
	if err := applyFilters(&opts, "-third"); err != nil {
		t.Fatalf("applyFilters: %v", err)
	}
	if want := []string{"gws:readonly", "+other", "-third"}; !reflect.DeepEqual(opts.Filters, want) {
		t.Fatalf("filters = %v, want %v", opts.Filters, want)
	}
	for _, value := range []string{"", "Bad", "gws:", "gws:Bad", "a/b", "gws:x:y"} {
		if err := applyFilters(&model.Options{}, value); err == nil {
			t.Errorf("applyFilters(%q) succeeded", value)
		}
	}
	for _, value := range []string{",", " , ,"} {
		if err := applyFilters(&model.Options{}, value); err == nil || err.Error() != "--filters requires at least one filter" {
			t.Errorf("applyFilters(%q) = %v", value, err)
		}
	}
	none := model.Options{}
	if err := applyFilters(&none, "none"); err != nil || none.Filters == nil || len(none.Filters) != 0 {
		t.Fatalf("applyFilters(none) = %v, %#v", err, none.Filters)
	}
	if err := applyFilters(&none, "gws"); err == nil || !strings.Contains(err.Error(), "none") {
		t.Fatalf("none then gws = %v", err)
	}
	if err := applyFilters(&model.Options{}, "gws,none"); err == nil {
		t.Fatal("gws,none succeeded")
	}
}

// Removals that merging consumes are kept, as for features, so validation
// can report one that names no installed filter.
func TestFiltersDroppedRemovals(t *testing.T) {
	global := Defaults{Filters: []string{"gws"}, path: "/g/config.json"}
	project := Defaults{Filters: []string{"-gws", "-nope"}, path: "/p/config.json"}
	got, _, _ := ResolveOptionsForTool(DefaultOptions(), model.OptionSources{}, global, project, "claude")
	if len(got.Filters) != 0 {
		t.Fatalf("filters = %#v, want none", got.Filters)
	}
	var names []string
	for _, removal := range got.DroppedRemovals {
		if removal.Option != "filters" || removal.Origin.Source != model.SourceProject {
			t.Fatalf("removal = %+v", removal)
		}
		names = append(names, removal.Name)
	}
	if want := []string{"gws", "nope"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("dropped removals = %v, want %v", names, want)
	}
}

func TestFiltersOptionDefinition(t *testing.T) {
	var def OptionDef
	for _, d := range OptionDefs() {
		if d.Name == "filters" {
			def = d
		}
	}
	if def.AffectsSession != SessionAffected || !def.AffectsNetwork || def.Apply != ApplySliceMergeFilter || !def.HasDirectives() {
		t.Fatalf("filters option = %+v", def)
	}
	if len(def.CLIFlags) != 1 || def.CLIFlags[0].Name != "--filters" {
		t.Fatalf("filters flags = %+v", def.CLIFlags)
	}
	var defaults Defaults
	if err := json.Unmarshal([]byte(`{"filters":["gws:readonly"]}`), &defaults); err != nil || !reflect.DeepEqual(defaults.Filters, []string{"gws:readonly"}) {
		t.Fatalf("config key filters = %v, %v", defaults.Filters, err)
	}
}
