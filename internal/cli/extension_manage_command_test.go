// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"testing"

	"enclave/internal/extinstall"
	"enclave/internal/model"
)

// The rejection messages are user-visible, so they are asserted verbatim.
func TestParseExtensionFlagCombinations(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "add --json without --yes",
			args: []string{"features", "add", "acme/kits", "--json"},
			want: "--json cannot prompt; pass --yes",
		},
		{
			name: "update --json without --yes",
			args: []string{"features", "update", "--json"},
			want: "--json cannot prompt; pass --yes",
		},
		{
			name: "remove --json without --yes",
			args: []string{"tools", "remove", "demo", "--json"},
			want: "--json cannot prompt; pass --yes",
		},
		{
			name: "add --all with --name",
			args: []string{"features", "add", "acme/kits", "--all", "--name", "foo"},
			want: "--all and --name are mutually exclusive",
		},
		{
			name: "add --path with --name",
			args: []string{"features", "add", "acme/kits", "--path", "extensions/features/foo", "--name", "foo"},
			want: "--path and --name are mutually exclusive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.args, model.Options{})
			if err == nil {
				t.Fatalf("Parse(%v) was accepted, request = %+v", tc.args, res.ExtRequest)
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err.Error(), tc.want)
			}
			if res.Action != ActionExtensionManage || res.ExtRequest == nil {
				t.Fatalf("action = %q, request = %+v: a rejected request must still be published", res.Action, res.ExtRequest)
			}
		})
	}
}

// Interactive is derived at the CLI boundary from --yes and --json.
func TestParseExtensionInteractivity(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "plain add prompts", args: []string{"features", "add", "acme/kits"}, want: true},
		{name: "--yes does not prompt", args: []string{"features", "add", "acme/kits", "--yes"}, want: false},
		{name: "--json --yes does not prompt", args: []string{"features", "add", "acme/kits", "--json", "--yes"}, want: false},
		{name: "plain update prompts", args: []string{"tools", "update"}, want: true},
		{name: "remove --yes does not prompt", args: []string{"tools", "remove", "demo", "--yes"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.args, model.Options{})
			if err != nil {
				t.Fatalf("Parse(%v): %v", tc.args, err)
			}
			if res.ExtRequest == nil {
				t.Fatal("ExtRequest is nil")
			}
			if res.ExtRequest.Interactive != tc.want {
				t.Fatalf("Interactive = %v, want %v", res.ExtRequest.Interactive, tc.want)
			}
		})
	}
}

func TestParseCommandsVerbs(t *testing.T) {
	res, err := Parse([]string{"commands"}, model.Options{})
	if err != nil || res.Action != "commands" {
		t.Fatalf("Parse(commands) = %q, %v", res.Action, err)
	}
	res, err = Parse([]string{"commands", "list", "--json"}, model.Options{})
	if err != nil || res.Action != "commands" || res.ExtRequest == nil || !res.ExtRequest.JSON {
		t.Fatalf("Parse(commands list --json) = %+v, %v", res, err)
	}
	res, err = Parse([]string{"commands", "add", "acme/kits", "--yes"}, model.Options{})
	if err != nil || res.Action != ActionExtensionManage || res.ExtRequest.Kind != model.KindCommand ||
		res.ExtRequest.Op != extinstall.OpAdd {
		t.Fatalf("Parse(commands add) = %+v, %v", res.ExtRequest, err)
	}
}

func TestParseFiltersVerbs(t *testing.T) {
	res, err := Parse([]string{"filters"}, model.Options{})
	if err != nil || res.Action != "filters" {
		t.Fatalf("Parse(filters) = %q, %v", res.Action, err)
	}
	res, err = Parse([]string{"filters", "list", "--json"}, model.Options{})
	if err != nil || res.Action != "filters" || res.ExtRequest == nil || !res.ExtRequest.JSON || res.ExtRequest.Kind != model.KindFilter {
		t.Fatalf("Parse(filters list --json) = %+v, %v", res, err)
	}
	// Like features list, --tool selects whose tool_overrides apply.
	res, err = Parse([]string{"filters", "list", "--tool", "codex", "--filters", "gws"}, model.Options{})
	if err != nil || res.Options.Tool != "codex" || res.Sources.Tool != model.SourceCLI || len(res.Options.Filters) != 1 {
		t.Fatalf("Parse(filters list --tool codex) = %+v, %v", res.Options, err)
	}
	for _, op := range []extinstall.Op{extinstall.OpAdd, extinstall.OpUpdate, extinstall.OpRemove} {
		args := []string{"filters", string(op), "gws", "--yes"}
		res, err = Parse(args, model.Options{})
		if err != nil || res.Action != ActionExtensionManage || res.ExtRequest.Kind != model.KindFilter || res.ExtRequest.Op != op {
			t.Fatalf("Parse(%v) = %+v, %v", args, res.ExtRequest, err)
		}
	}
}

func TestParseFilterVerbs(t *testing.T) {
	res, err := Parse([]string{"filters", "login", "gws", "--preset", "readonly", "--client-secret", "c.json", "--no-browser", "--grant", "g"}, model.Options{})
	if err != nil || res.Action != ActionFiltersLogin || res.FilterRequest == nil {
		t.Fatalf("Parse(filters login) = %+v, %v", res, err)
	}
	if got := *res.FilterRequest; got.Name != "gws" || got.Preset != "readonly" || got.ClientSecret != "c.json" || !got.NoBrowser || got.Grant != "g" {
		t.Fatalf("login request = %+v", got)
	}
	res, err = Parse([]string{"filters", "login", "gws", "--scopes", "drive, gmail.readonly", "--import", "u.json"}, model.Options{})
	if err != nil || res.FilterRequest.Import != "u.json" || len(res.FilterRequest.Scopes) != 2 || res.FilterRequest.Scopes[1] != "gmail.readonly" {
		t.Fatalf("Parse(filters login --import) = %+v, %v", res.FilterRequest, err)
	}
	for _, args := range [][]string{
		{"filters", "login", "gws", "--preset", "p", "--scopes", "s"},
		{"filters", "login", "gws", "--import", "u.json", "--client-secret", "c.json"},
		{"filters", "logout", "gws", "--grant", "g", "--all"},
		{"filters", "show"},
	} {
		if _, err := Parse(args, model.Options{}); err == nil {
			t.Errorf("Parse(%v) succeeded", args)
		}
	}
	res, err = Parse([]string{"filters", "logout", "gws", "--all"}, model.Options{})
	if err != nil || res.Action != ActionFiltersLogout || !res.FilterRequest.All {
		t.Fatalf("Parse(filters logout --all) = %+v, %v", res, err)
	}
	res, err = Parse([]string{"filters", "logout", "gws", "--grant", "g", "--no-revoke"}, model.Options{})
	if err != nil || res.FilterRequest.Grant != "g" || !res.FilterRequest.NoRevoke || res.FilterRequest.All {
		t.Fatalf("Parse(filters logout --no-revoke) = %+v, %v", res.FilterRequest, err)
	}
	res, err = Parse([]string{"filters", "grants", "gws"}, model.Options{})
	if err != nil || res.Action != ActionFiltersGrants || res.FilterRequest.Name != "gws" {
		t.Fatalf("Parse(filters grants) = %+v, %v", res, err)
	}
	res, err = Parse([]string{"filters", "show", "gws", "--preset", "readonly"}, model.Options{})
	if err != nil || res.Action != ActionFiltersShow || res.FilterRequest.Preset != "readonly" {
		t.Fatalf("Parse(filters show) = %+v, %v", res, err)
	}
}

func TestParseFiltersOption(t *testing.T) {
	for _, args := range [][]string{
		{"--filters", "gws:readonly"},
		{"run", "--filters", "gws:readonly"},
		{"network", "print", "--filters", "gws:readonly"},
		{"network", "status", "--filters", "gws:readonly"},
		{"network", "diff", "--filters", "gws:readonly"},
		{"filters", "list", "--filters", "gws:readonly"},
	} {
		res, err := Parse(args, model.Options{})
		if err != nil || len(res.Options.Filters) != 1 || res.Options.Filters[0] != "gws:readonly" || res.Sources.Filters != model.SourceCLI {
			t.Errorf("Parse(%v) = %v, %v, %v", args, res.Options.Filters, res.Sources.Filters, err)
		}
	}
	if _, err := Parse([]string{"--filters", "Bad Name"}, model.Options{}); err == nil {
		t.Error("Parse accepted an invalid filter name")
	}
}

// A request built without the CLI must not be able to block on a prompt.
func TestExtensionRequestZeroValueIsNonInteractive(t *testing.T) {
	if (extinstall.Request{}).Interactive {
		t.Error("a zero-value Request is interactive")
	}
}
