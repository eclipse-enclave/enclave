// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/model"
)

// The frozen v1 fixtures record behavior before the format migration. Compare
// both the loaded runtime models and the installer's permission summaries.
func TestV3BuiltinsPreserveLegacyBehavior(t *testing.T) {
	builtin := model.Paths{ToolsDir: "../../extensions/tools", FeaturesDir: "../../extensions/features"}
	legacy := model.Paths{ToolsDir: "testdata/legacy-spec/tools", FeaturesDir: "testdata/legacy-spec/features"}
	for _, kind := range []string{KindSandbox, KindMixin} {
		root := builtin.FeaturesDir
		legacyRoot := legacy.FeaturesDir
		if kind == KindSandbox {
			root, legacyRoot = builtin.ToolsDir, legacy.ToolsDir
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			t.Run(entry.Name(), func(t *testing.T) {
				name := entry.Name()
				current, err := LoadSpec(builtin, name, kind)
				if err != nil {
					t.Fatal(err)
				}
				previous, err := LoadSpec(legacy, name, kind)
				if err != nil {
					t.Fatal(err)
				}
				if current.SchemaVersion != "3" || previous.SchemaVersion != "1" {
					t.Fatal("fixtures must compare v3 built-ins against v1 baselines")
				}
				current.SchemaVersion = previous.SchemaVersion
				if !reflect.DeepEqual(current, previous) {
					t.Fatalf("normalized declarations changed for %s", name)
				}
				gotExt, err := loadSpecExtension(builtin, name, kind)
				if err != nil {
					t.Fatal(err)
				}
				wantExt, err := loadSpecExtension(legacy, name, kind)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotExt, wantExt) {
					t.Fatalf("extension behavior changed: got %+v, want %+v", gotExt, wantExt)
				}
				if kind == KindSandbox {
					got, err := LoadProfile(builtin, name)
					if err != nil {
						t.Fatal(err)
					}
					want, err := LoadProfile(legacy, name)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("profile behavior changed: got %+v, want %+v", got, want)
					}
				}
				gotSummary, err := SummarizeSpecDir(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				wantSummary, err := SummarizeSpecDir(filepath.Join(legacyRoot, name))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotSummary, wantSummary) {
					t.Fatalf("installer summary changed: got %+v, want %+v", gotSummary, wantSummary)
				}
			})
		}
	}
}

const minimalV3Spec = `schemaVersion: "3"
kind: workload
capabilities:
  - type: org.eclipse.enclave/runtime@1
    config:
      name: demo
      sandbox:
        entrypoint: {run: [demo]}
        configDir: .demo
`

func TestV3SpecRejectsUnsupportedOrMalformedDeclarations(t *testing.T) {
	cases := map[string]struct{ source, want string }{
		"multiple documents":    {minimalV3Spec + "---\nkind: mixin\n", "single YAML document"},
		"top-level typo":        {minimalV3Spec + "netwrok: {}\n", "netwrok"},
		"payload typo":          {strings.Replace(minimalV3Spec, "configDir", "configDr", 1), "configDr"},
		"top-level identity":    {minimalV3Spec + "name: demo\n", "name"},
		"payload metadata":      {strings.Replace(minimalV3Spec, "name: demo", "name: demo\n      kind: mixin", 1), "kind"},
		"duplicate payload key": {strings.Replace(minimalV3Spec, "name: demo", "name: demo\n      name: other", 1), "name"},
		"required unknown":      {minimalV3Spec + "  - type: example.org/custom@1\n", "unsupported required"},
		"runtime optional":      {strings.Replace(minimalV3Spec, "    config:", "    optional: true\n    config:", 1), "required"},
		"duplicate runtime":     {minimalV3Spec + "  - type: org.eclipse.enclave/runtime@1\n    config: {name: demo}\n", "exactly one"},
		"missing runtime":       {"schemaVersion: \"3\"\nkind: mixin\n", "required"},
		"missing identity":      {strings.Replace(minimalV3Spec, "      name: demo\n", "", 1), "config.name"},
		"dependencies":          {minimalV3Spec + "requires: [other]\n", "dependency"},
		"provides":              {minimalV3Spec + "provides: [demo]\n", "dependency"},
		"integrates":            {minimalV3Spec + "integrates: [other]\n", "dependency"},
		"conflicts":             {minimalV3Spec + "conflicts: [other]\n", "dependency"},
		"recipe":                {minimalV3Spec + "build: FROM scratch\n", "build recipes"},
		"args":                  {minimalV3Spec + "args: {version: {default: latest}}\n", "args"},
		"old kind":              {strings.Replace(minimalV3Spec, "kind: workload", "kind: sandbox", 1), "kind"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeSpecDocument([]byte(tc.source), "test/spec.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestV3OptionalUnknownAndJSON(t *testing.T) {
	source := minimalV3Spec + "  - type: example.org/custom@1\n    optional: true\n    config: {arbitrary: value}\n"
	doc, err := decodeSpecDocument([]byte(source), "test/spec.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Kind != KindSandbox || doc.Name != "demo" || doc.Sandbox.ConfigDir != ".demo" {
		t.Fatalf("incorrect mapping: %+v", doc)
	}
	jsonSource := `{"schemaVersion":"3","kind":"workload","capabilities":[{"type":"org.eclipse.enclave/runtime@1","config":{"name":"demo","sandbox":{"entrypoint":{"run":["demo"]},"configDir":".demo"}}}]}`
	var validJSON any
	if err := json.Unmarshal([]byte(jsonSource), &validJSON); err != nil {
		t.Fatal(err)
	}
	jsonDoc, err := decodeSpecDocument([]byte(jsonSource), "test/spec.json")
	if err != nil || !reflect.DeepEqual(jsonDoc, doc) {
		t.Fatalf("JSON mapping: %+v, %v", jsonDoc, err)
	}
}
