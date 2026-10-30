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

func TestHostEnvMergesKeysAcrossLayers(t *testing.T) {
	global := Defaults{
		HostEnv:       map[string]string{"A": "global", "B": "global"},
		ToolOverrides: map[string]Defaults{"claude": {HostEnv: map[string]string{"C": "global-claude"}}},
	}
	project := Defaults{
		HostEnv:       map[string]string{"B": "project"},
		ToolOverrides: map[string]Defaults{"claude": {HostEnv: map[string]string{"C": "project-claude"}}},
	}

	opts, _, _ := ResolveOptionsForTool(DefaultOptions(), model.DefaultOptionSources(), global, project, "claude")
	want := map[string]string{"A": "global", "B": "project", "C": "project-claude"}
	if !reflect.DeepEqual(opts.HostEnv, want) || opts.Sources.HostEnv != model.SourceToolOverride {
		t.Fatalf("host_env = %v from %v, want %v from tool override", opts.HostEnv, opts.Sources.HostEnv, want)
	}

	opts, _, _ = ResolveOptionsForTool(DefaultOptions(), model.DefaultOptionSources(), global, project, "codex")
	if want := (map[string]string{"A": "global", "B": "project"}); !reflect.DeepEqual(opts.HostEnv, want) {
		t.Fatalf("codex host_env = %v, want %v", opts.HostEnv, want)
	}
	if global.HostEnv["B"] != "global" {
		t.Fatal("resolving mutated the global layer")
	}
}

func TestHostEnvDropsInvalidNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"host_env":{"OK":"1","BAD NAME":"x","DOCKER_HOST":"tcp://x","PATH":"/x"},"tool_overrides":{"claude":{"host_env":{"1BAD":"x","CONTAINER_HOST":"x","HERDR_AGENT":"claude"}}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	defaults, warnings, err := readDefaults(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults.HostEnv, map[string]string{"OK": "1"}) || !reflect.DeepEqual(defaults.ToolOverrides["claude"].HostEnv, map[string]string{"HERDR_AGENT": "claude"}) {
		t.Fatalf("host_env = %v, claude = %v", defaults.HostEnv, defaults.ToolOverrides["claude"].HostEnv)
	}
	var got []string
	for _, warning := range warnings {
		got = append(got, warning.Message)
	}
	for i, want := range []string{`host_env entry "BAD NAME"`, `host_env entry "DOCKER_HOST"`, `host_env entry "PATH"`, `tool_overrides.claude.host_env entry "1BAD"`, `tool_overrides.claude.host_env entry "CONTAINER_HOST"`} {
		if i >= len(got) || !strings.Contains(got[i], want) {
			t.Fatalf("warnings = %q, want %d: %s", got, i, want)
		}
	}
	if len(got) != 5 {
		t.Fatalf("warnings = %q", got)
	}
}

func TestHostEnvTypedValueIsAlwaysAnObject(t *testing.T) {
	var def OptionDef
	for _, candidate := range OptionDefs() {
		if candidate.Name == "host_env" {
			def = candidate
		}
	}
	if got, ok := OptionTypedEffectiveValue(def, DefaultOptions()).(map[string]string); !ok || got == nil || len(got) != 0 {
		t.Fatalf("empty host_env = %#v, want an empty map", got)
	}
}
