// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/model"
)

// cliPorts returns parsed CLI options that set ports, as the -p flag does.
func cliPorts(values ...string) (model.Options, model.OptionSources) {
	opts := DefaultOptions()
	sources := model.OptionSources{}
	if values != nil {
		opts.Ports = values
		sources.Ports = model.SourceCLI
	}
	return opts, sources
}

func resolvePorts(t *testing.T, global, project Defaults, cli ...string) model.Options {
	t.Helper()
	opts, sources := cliPorts(cli...)
	resolved, _, _ := ResolveOptionsForTool(opts, sources, global, project, "claude")
	return resolved
}

func TestExtrasListMerge(t *testing.T) {
	global := Defaults{Ports: []string{"1000", "2000"}}
	cases := []struct {
		name    string
		project []string
		cli     []string
		want    []string
	}{
		{name: "config plain replaces", project: []string{"3000"}, want: []string{"3000"}},
		{name: "config + adds", project: []string{"+3000"}, want: []string{"1000", "2000", "3000"}},
		{name: "config - removes", project: []string{"-1000"}, want: []string{"2000"}},
		{name: "config +/- together amend", project: []string{"+3000", "-1000"}, want: []string{"2000", "3000"}},
		{name: "config [] clears", project: []string{}, want: []string{}},
		{name: "config none clears", project: []string{"none"}, want: []string{}},
		{name: "config unset inherits", project: nil, want: []string{"1000", "2000"}},
		{name: "cli plain replaces", cli: []string{"3000"}, want: []string{"3000"}},
		{name: "cli + adds", cli: []string{"+3000"}, want: []string{"1000", "2000", "3000"}},
		{name: "cli - removes", cli: []string{"-2000"}, want: []string{"1000"}},
		{name: "cli removing an absent entry is silent", cli: []string{"-9000"}, want: []string{"1000", "2000"}},
		{name: "cli none alone clears", cli: []string{"none"}, want: []string{}},
		{name: "cli + amends project replacement", project: []string{"3000"}, cli: []string{"+4000"}, want: []string{"3000", "4000"}},
		{name: "cli removal reaches config entry", project: []string{"+3000"}, cli: []string{"-1000"}, want: []string{"2000", "3000"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePorts(t, global, Defaults{Ports: tc.project}, tc.cli...)
			if !reflect.DeepEqual(got.Ports, tc.want) {
				t.Fatalf("ports = %#v, want %#v", got.Ports, tc.want)
			}
			if len(got.ListProblems) != 0 {
				t.Fatalf("unexpected problems: %+v", got.ListProblems)
			}
		})
	}
}

func TestExtrasListMergeRejectsMixedLists(t *testing.T) {
	global := Defaults{Ports: []string{"1000"}}
	cases := []struct {
		name    string
		project []string
		cli     []string
		want    string
	}{
		{name: "config plain with +", project: []string{"3000", "+4000"}, want: `"+4000" mixes a +/- entry into a plain list`},
		{name: "config plain with -", project: []string{"3000", "-1000"}, want: `"-1000" mixes a +/- entry into a plain list`},
		{name: "config none with +", project: []string{"none", "+3000"}, want: `"none" cannot be combined with other entries`},
		{name: "cli none with plain", cli: []string{"none", "3000"}, want: `"none" cannot be combined with other entries`},
		{name: "cli plain with +", cli: []string{"3000", "+4000"}, want: `"+4000" mixes a +/- entry into a plain list`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePorts(t, global, Defaults{Ports: tc.project}, tc.cli...)
			if len(got.ListProblems) != 1 || !strings.HasPrefix(got.ListProblems[0].Message, tc.want) {
				t.Fatalf("problems = %+v, want one error starting with %q", got.ListProblems, tc.want)
			}
			if !reflect.DeepEqual(got.Ports, []string{"1000"}) {
				t.Fatalf("ports = %v, want the inherited list unchanged", got.Ports)
			}
		})
	}
}

func TestExtrasListMergeToolOverridesAreSeparateLayers(t *testing.T) {
	global := Defaults{
		Ports:         []string{"1000"},
		ToolOverrides: map[string]Defaults{"claude": {Ports: []string{"+2000"}}},
	}
	project := Defaults{
		ToolOverrides: map[string]Defaults{"claude": {Ports: []string{"+3000"}}},
	}
	got := resolvePorts(t, global, project, "+4000")
	if want := []string{"1000", "2000", "3000", "4000"}; !reflect.DeepEqual(got.Ports, want) {
		t.Fatalf("ports = %v, want %v", got.Ports, want)
	}
	wantOrigins := map[string]model.EntryOrigin{
		"1000": {Source: model.SourceGlobal, Scope: model.ScopeGlobal},
		"2000": {Source: model.SourceToolOverride, Scope: model.ScopeGlobal, Tool: "claude"},
		"3000": {Source: model.SourceToolOverride, Scope: model.ScopeProject, Tool: "claude"},
		"4000": {Source: model.SourceCLI},
	}
	if !reflect.DeepEqual(got.Origins["ports"], wantOrigins) {
		t.Fatalf("origins = %+v, want %+v", got.Origins["ports"], wantOrigins)
	}
	if got.Sources.Ports != model.SourceCLI {
		t.Fatalf("source = %v, want cli", got.Sources.Ports)
	}

	// A plain list in the project file's override replaces the global file's.
	project.ToolOverrides["claude"] = Defaults{Ports: []string{"3000"}}
	got = resolvePorts(t, global, project)
	if want := []string{"3000"}; !reflect.DeepEqual(got.Ports, want) {
		t.Fatalf("ports = %v, want %v", got.Ports, want)
	}
}

func TestExtrasListMergeDeduplicatesByKey(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	cases := []struct {
		name   string
		global Defaults
		cli    model.Options
		field  func(model.Options) []string
		want   []string
	}{
		{
			name:   "port short and long form",
			global: Defaults{Ports: []string{"3000"}},
			cli:    model.Options{RunOptions: model.RunOptions{Ports: []string{"3000:3000", "127.0.0.1:3000:3000"}}},
			field:  func(o model.Options) []string { return o.Ports },
			want:   []string{"3000:3000"},
		},
		{
			name:   "domain case",
			global: Defaults{AllowDomains: []string{"Example.com"}},
			cli:    model.Options{RunOptions: model.RunOptions{AllowDomains: []string{"+example.COM", "-EXAMPLE.com"}}},
			field:  func(o model.Options) []string { return o.AllowDomains },
			want:   []string{},
		},
		{
			name:   "dir tilde and absolute",
			global: Defaults{AddDirs: []string{"~/work"}},
			cli:    model.Options{RunOptions: model.RunOptions{AddDirs: []string{filepath.Join(home, "work") + "/"}}},
			field:  func(o model.Options) []string { return o.AddDirs },
			want:   []string{filepath.Join(home, "work") + "/"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sources := model.OptionSources{}
			sources.Ports, sources.AllowDomains, sources.AddDirs = model.SourceCLI, model.SourceCLI, model.SourceCLI
			got, _, _ := ResolveOptionsForTool(tc.cli, sources, tc.global, Defaults{}, "claude")
			if !reflect.DeepEqual(tc.field(got), tc.want) {
				t.Fatalf("got %#v, want %#v", tc.field(got), tc.want)
			}
		})
	}
}

func TestExtrasListMergePortClashes(t *testing.T) {
	t.Run("higher layer replaces clashing entry", func(t *testing.T) {
		got := resolvePorts(t, Defaults{Ports: []string{"8080:80", "9000"}}, Defaults{}, "+8080:81")
		if want := []string{"8080:81", "9000"}; !reflect.DeepEqual(got.Ports, want) {
			t.Fatalf("ports = %v, want %v", got.Ports, want)
		}
	})
	t.Run("wildcard address overlaps", func(t *testing.T) {
		got := resolvePorts(t, Defaults{Ports: []string{"0.0.0.0:3000:3000"}}, Defaults{Ports: []string{"+3000:4000"}})
		if want := []string{"3000:4000"}; !reflect.DeepEqual(got.Ports, want) {
			t.Fatalf("ports = %v, want %v", got.Ports, want)
		}
	})
	t.Run("distinct addresses and port 0 coexist", func(t *testing.T) {
		got := resolvePorts(t, Defaults{Ports: []string{"127.0.0.1:3000:1", "0:5000"}}, Defaults{}, "+192.168.1.2:3000:2", "+0:5001")
		if want := []string{"127.0.0.1:3000:1", "0:5000", "192.168.1.2:3000:2", "0:5001"}; !reflect.DeepEqual(got.Ports, want) {
			t.Fatalf("ports = %v, want %v", got.Ports, want)
		}
	})
	t.Run("same layer is a problem", func(t *testing.T) {
		got := resolvePorts(t, Defaults{}, Defaults{}, "8080:80", "8080:81")
		if !reflect.DeepEqual(got.Ports, []string{"8080:80"}) {
			t.Fatalf("ports = %v", got.Ports)
		}
		if len(got.ListProblems) != 1 || got.ListProblems[0].Origin.Source != model.SourceCLI {
			t.Fatalf("problems = %+v, want one CLI error", got.ListProblems)
		}
	})
}

func TestExtrasListMergeCLIDirFlagsAdd(t *testing.T) {
	cli := DefaultOptions()
	cli.AddDirs = []string{"/var", "-/tmp"}
	cli.AddReadonlyDirs = []string{"/srv"}
	sources := model.OptionSources{}
	sources.AddDirs, sources.AddReadonlyDirs = model.SourceCLI, model.SourceCLI
	global := Defaults{AddDirs: []string{"/tmp", "/opt"}, AddReadonlyDirs: []string{"/usr"}}
	got, _, _ := ResolveOptionsForTool(cli, sources, global, Defaults{}, "claude")
	if want := []string{"/opt", "/var"}; !reflect.DeepEqual(got.AddDirs, want) {
		t.Fatalf("add_dirs = %v, want %v", got.AddDirs, want)
	}
	if want := []string{"/usr", "/srv"}; !reflect.DeepEqual(got.AddReadonlyDirs, want) {
		t.Fatalf("add_readonly_dirs = %v, want %v", got.AddReadonlyDirs, want)
	}
}

func TestExtrasListMergeEscapes(t *testing.T) {
	opts := DefaultOptions()
	opts.AddDirs = []string{"++plus", "./none", "./-minus"}
	sources := model.OptionSources{}
	sources.AddDirs = model.SourceCLI
	got, _, _ := ResolveOptionsForTool(opts, sources, Defaults{}, Defaults{}, "claude")
	if want := []string{"+plus", "./none", "./-minus"}; !reflect.DeepEqual(got.AddDirs, want) {
		t.Fatalf("add_dirs = %v, want %v", got.AddDirs, want)
	}
}

func TestSelectionListMerge(t *testing.T) {
	cases := []struct {
		name          string
		global        Defaults
		project       Defaults
		cli           []string
		want          []string
		wantUnchanged bool
	}{
		{name: "cli amends configured selection", global: Defaults{Features: []string{"node-dev"}}, cli: []string{"+debug-tools"}, want: []string{"node-dev", "debug-tools"}},
		{name: "cli amends explicit empty", global: Defaults{Features: []string{}}, cli: []string{"+debug-tools"}, want: []string{"debug-tools"}},
		{name: "cli plain replaces", global: Defaults{Features: []string{"node-dev"}}, cli: []string{"python-dev"}, want: []string{"python-dev"}},
		{name: "cli none clears", global: Defaults{Features: []string{"node-dev"}}, cli: []string{}, want: []string{}},
		{name: "higher layer wins per item", global: Defaults{Features: []string{"-node-dev"}}, project: Defaults{Features: []string{"+node-dev"}}, want: []string{"+node-dev"}},
		{name: "removal from explicit selection", global: Defaults{Features: []string{"node-dev", "python-dev"}}, cli: []string{"-python-dev"}, want: []string{"node-dev"}},
		{name: "removal kept against keyword", global: Defaults{Features: []string{"default"}}, cli: []string{"-node-dev"}, want: []string{"default", "-node-dev"}},
		{name: "directives without base stay directives", project: Defaults{Features: []string{"+debug-tools"}}, want: []string{"+debug-tools"}},
		{
			name:    "tool override files merge",
			global:  Defaults{ToolOverrides: map[string]Defaults{"claude": {Features: []string{"node-dev"}}}},
			project: Defaults{ToolOverrides: map[string]Defaults{"claude": {Features: []string{"+debug-tools"}}}},
			want:    []string{"node-dev", "debug-tools"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultOptions()
			sources := model.OptionSources{}
			if tc.cli != nil {
				opts.Features = tc.cli
				sources.Features = model.SourceCLI
			}
			got, _, _ := ResolveOptionsForTool(opts, sources, tc.global, tc.project, "claude")
			if !reflect.DeepEqual(got.Features, tc.want) {
				t.Fatalf("features = %#v, want %#v", got.Features, tc.want)
			}
		})
	}
}

func TestSelectionListMergeHostConfigPathsIgnoreTrailingSlash(t *testing.T) {
	global := Defaults{ToolOverrides: map[string]Defaults{"claude": {HostConfigPaths: []string{"skills/", "commands/"}}}}
	project := Defaults{ToolOverrides: map[string]Defaults{"claude": {HostConfigPaths: []string{"-skills"}}}}
	got, _, _ := ResolveOptionsForTool(DefaultOptions(), model.OptionSources{}, global, project, "claude")
	if want := []string{"commands/"}; !reflect.DeepEqual(got.HostConfigPaths, want) {
		t.Fatalf("host_config_paths = %v, want %v", got.HostConfigPaths, want)
	}
}

func TestMergeDefaultsCombinesToolOverrideLists(t *testing.T) {
	cases := []struct {
		name          string
		global, local []string
		want          []string
	}{
		{name: "one side kept as written", global: nil, local: []string{"+a"}, want: []string{"+a"}},
		{name: "directives stay directives", global: []string{"+3000"}, local: []string{"+4000"}, want: []string{"+3000", "+4000"}},
		{name: "later directive wins an entry", global: []string{"+3000"}, local: []string{"-3000"}, want: []string{"-3000"}},
		{name: "directive matches by key", global: []string{"+3000:3000"}, local: []string{"-3000"}, want: []string{"-3000"}},
		{name: "amending a replacing list stays bare", global: []string{"1000", "3000"}, local: []string{"+2000", "-3000"}, want: []string{"1000", "2000"}},
		{name: "removing the last entry of a replacing list clears", global: []string{"3000"}, local: []string{"-3000"}, want: []string{}},
		{name: "plain list replaces", global: []string{"+3000"}, local: []string{"4000"}, want: []string{"4000"}},
		{name: "amending none starts empty", global: []string{"none"}, local: []string{"+3000"}, want: []string{"3000"}},
		{name: "clash replaces the replacing list's entry", global: []string{"8080:80"}, local: []string{"+8080:81"}, want: []string{"8080:81"}},
		{name: "clash drops the earlier directive", global: []string{"+8080:80", "+3000"}, local: []string{"+8080:81"}, want: []string{"+3000", "+8080:81"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged := mergeDefaults(Defaults{Ports: tc.global}, Defaults{Ports: tc.local})
			if !reflect.DeepEqual(merged.Ports, tc.want) {
				t.Fatalf("ports = %#v, want %#v", merged.Ports, tc.want)
			}
		})
	}
}

// The combined display must resolve like the two layers a run folds.
func TestMergeDefaultsMatchesTheFold(t *testing.T) {
	base := Defaults{Ports: []string{"8080:80"}}
	for _, pair := range [][2][]string{
		{{"+3000"}, {"-3000"}},
		{{"+3000"}, {"+4000"}},
		{{"1000", "3000"}, {"+2000", "-3000"}},
		{{"3000"}, {"-3000"}},
		{{"-8080:80"}, {"+8080:81"}},
		{{"none"}, {"+3000"}},
		{{"8080:80"}, {"+8080:81"}},
		{{"+8080:80"}, {"+8080:81"}},
		{{"+3000"}, {"none"}},
		{{"-3000"}, {"+3000"}},
	} {
		global := base
		global.ToolOverrides = map[string]Defaults{"claude": {Ports: pair[0]}}
		project := Defaults{ToolOverrides: map[string]Defaults{"claude": {Ports: pair[1]}}}
		folded, _, _ := ResolveOptionsForTool(DefaultOptions(), model.OptionSources{}, global, project, "claude")

		combined := base
		combined.ToolOverrides = map[string]Defaults{"claude": {Ports: mergeDefaults(Defaults{Ports: pair[0]}, Defaults{Ports: pair[1]}).Ports}}
		once, _, _ := ResolveOptionsForTool(DefaultOptions(), model.OptionSources{}, combined, Defaults{}, "claude")
		if !reflect.DeepEqual(folded.Ports, once.Ports) {
			t.Errorf("%v then %v: folded %v, combined display resolves to %v", pair[0], pair[1], folded.Ports, once.Ports)
		}
	}
}

func TestListOriginsNameTheConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ports":["abc"],"tool_overrides":{"claude":{"add_dirs":["+/srv"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	global, _, err := readDefaults(path)
	if err != nil {
		t.Fatal(err)
	}
	opts, _, _ := ResolveOptionsForTool(DefaultOptions(), model.OptionSources{}, global, Defaults{}, "claude")
	err = CheckListProblems(opts)
	if want := `ports in ` + path + `: invalid entry "abc"`; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("error = %v, want prefix %q", err, want)
	}
	if got, want := ListEntryOriginLabel("add_dirs", opts.Origins, "/srv"), "tool_overrides.claude.add_dirs in "+path; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}
