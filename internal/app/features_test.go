// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/extinstall"
	"enclave/internal/model"
)

// displayName is part of the documented `list --json` contract.
func TestRunFeaturesJSONPopulatesDisplayName(t *testing.T) {
	tmp := t.TempDir()
	featuresDir := filepath.Join(tmp, "extensions", "features")
	spec := "schemaVersion: \"1\"\nkind: mixin\nname: demo\ndisplayName: Demo Feature\n"
	writeAppFile(t, filepath.Join(featuresDir, "demo", "spec.yaml"), spec, 0o644)

	ctx := NewAppContext(model.Paths{FeaturesDir: featuresDir}, tmp)
	req := &extinstall.Request{Kind: model.KindFeature, JSON: true}

	out := captureStdout(t, func() {
		if code := runFeatures(ctx, req, config.DefaultOptions(), model.DefaultOptionSources()); code != 0 {
			t.Errorf("runFeatures exit code = %d", code)
		}
	})

	var decoded struct {
		Extensions []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(decoded.Extensions) != 1 || decoded.Extensions[0].DisplayName != "Demo Feature" {
		t.Fatalf("extensions = %+v, want displayName populated", decoded.Extensions)
	}
}

// `list` renders local modifications in both output shapes.
func TestRunFeaturesReportsLocalModification(t *testing.T) {
	tmp := t.TempDir()
	featuresDir := filepath.Join(tmp, "extensions", "features")
	userFeaturesDir := filepath.Join(tmp, "user", "extensions", "features")
	extDir := filepath.Join(userFeaturesDir, "demo")
	writeAppFile(t, filepath.Join(extDir, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: demo\n", 0o644)
	hash, err := extinstall.TreeHash(extDir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	if err := extinstall.WriteOrigin(extDir, extinstall.Origin{
		SchemaVersion: extinstall.OriginSchemaVersion,
		Kind:          string(model.KindFeature),
		Name:          "demo",
		Remote:        "https://github.com/acme/kits",
		Source:        "acme/kits",
		Ref:           "main",
		RefType:       extinstall.RefTypeBranch,
		Commit:        "a1b2c3d4e5f6a7b8",
		TreeHash:      hash,
	}); err != nil {
		t.Fatalf("WriteOrigin: %v", err)
	}
	writeAppFile(t, filepath.Join(extDir, "local.sh"), "echo mine\n", 0o644)

	paths := model.Paths{FeaturesDir: featuresDir, UserFeaturesDir: userFeaturesDir}
	ctx := NewAppContext(paths, tmp)

	text := captureStdout(t, func() {
		if code := runFeatures(ctx, nil, config.DefaultOptions(), model.DefaultOptionSources()); code != 0 {
			t.Errorf("runFeatures exit code = %d", code)
		}
	})
	if !strings.Contains(text, ", modified]") {
		t.Errorf("text listing does not mark the local edit:\n%s", text)
	}

	out := captureStdout(t, func() {
		req := &extinstall.Request{Kind: model.KindFeature, JSON: true}
		if code := runFeatures(ctx, req, config.DefaultOptions(), model.DefaultOptionSources()); code != 0 {
			t.Errorf("runFeatures --json exit code = %d", code)
		}
	})
	var decoded struct {
		Extensions []struct {
			Origin *struct {
				LocallyModified bool `json:"locallyModified"`
			} `json:"origin"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(decoded.Extensions) != 1 || decoded.Extensions[0].Origin == nil || !decoded.Extensions[0].Origin.LocallyModified {
		t.Fatalf("extensions = %+v, want locallyModified true", decoded.Extensions)
	}
}

func runFeaturesJSON(t *testing.T, paths model.Paths, opts model.Options) (selection map[string]any, enabled []string) {
	t.Helper()
	ctx := NewAppContext(paths, t.TempDir())
	req := &extinstall.Request{Kind: model.KindFeature, JSON: true}
	out := captureStdout(t, func() {
		if code := runFeatures(ctx, req, opts, opts.Sources); code != 0 {
			t.Errorf("runFeatures exit code = %d", code)
		}
	})
	var decoded struct {
		Selection  map[string]any `json:"selection"`
		Extensions []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	for _, ext := range decoded.Extensions {
		if ext.Enabled {
			enabled = append(enabled, ext.Name)
		}
	}
	return decoded.Selection, enabled
}

func TestRunFeaturesJSONHonoursToolOverride(t *testing.T) {
	global := config.Defaults{ToolOverrides: map[string]config.Defaults{"claude": {Features: []string{"+shell-extras", "-github-cli"}}}}

	cases := []struct {
		tool          string
		wantSelection map[string]any
		wantEnabled   []string
	}{
		{"claude", map[string]any{"directives": []any{"+shell-extras", "-github-cli"}, "source": "tool_override", "tool": "claude"}, []string{"node-dev", "shell-extras"}},
		{"", map[string]any{"directives": nil, "source": "default", "tool": nil}, []string{"github-cli", "node-dev"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			cliOpts, cliSources := config.DefaultOptions(), model.OptionSources{}
			if tc.tool != "" {
				cliOpts.Tool, cliSources.Tool = tc.tool, model.SourceCLI
			}
			opts, _, _ := config.ResolveOptionsForTool(cliOpts, cliSources, global, config.Defaults{}, "")

			selection, enabled := runFeaturesJSON(t, testPathsWithFeatures(t), opts)
			if !reflect.DeepEqual(selection, tc.wantSelection) {
				t.Errorf("selection = %#v, want %#v", selection, tc.wantSelection)
			}
			sort.Strings(enabled)
			if !reflect.DeepEqual(enabled, tc.wantEnabled) {
				t.Errorf("enabled = %v, want %v", enabled, tc.wantEnabled)
			}
		})
	}
}

// A devcontainer run without explicit features enables none, so the listing
// must not report the defaults as enabled.
func TestRunFeaturesJSONIncludesPlaywrightForPlaywrightMCP(t *testing.T) {
	paths := testPathsWithFeatures(t)
	writeAppFile(t, filepath.Join(paths.FeaturesDir, "playwright", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: playwright\n", 0o644)
	opts := config.DefaultOptions()
	opts.Tool = "claude"
	opts.PlaywrightMCP = true
	opts.Sources = model.DefaultOptionSources()

	selection, enabled := runFeaturesJSON(t, paths, opts)
	sort.Strings(enabled)
	if want := []string{"github-cli", "node-dev", "playwright"}; !reflect.DeepEqual(enabled, want) {
		t.Fatalf("enabled = %v, want %v", enabled, want)
	}
	if selection["directives"] != nil {
		t.Fatalf("selection.directives = %#v, want the configured null", selection["directives"])
	}
}

func TestRunFeaturesDisabledEnablesNone(t *testing.T) {
	cases := []struct {
		name   string
		set    func(*model.Options)
		reason string
	}{
		{name: "devcontainer without features", set: func(o *model.Options) { o.Devcontainer = true }, reason: "devcontainer"},
		{name: "qemu backend", set: func(o *model.Options) { o.Backend = backend.NameQEMU }, reason: "qemu backend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := config.DefaultOptions()
			tc.set(&opts)
			opts.Sources = model.DefaultOptionSources()

			if _, enabled := runFeaturesJSON(t, testPathsWithFeatures(t), opts); len(enabled) != 0 {
				t.Fatalf("enabled = %v, want none", enabled)
			}

			ctx := NewAppContext(testPathsWithFeatures(t), t.TempDir())
			text := captureStdout(t, func() { runFeatures(ctx, nil, opts, opts.Sources) })
			if want := "✗ node-dev (disabled by " + tc.reason + ")"; !strings.Contains(text, want) {
				t.Fatalf("text listing lacks %q:\n%s", want, text)
			}
		})
	}
}

// Invalid options still list, using the configured selection without the
// coercions a run would apply.
func TestRunFeaturesFallsBackWhenValidationFails(t *testing.T) {
	opts := config.DefaultOptions()
	opts.Backend = backend.NameQEMU
	opts.NetworkLog = "bogus"
	opts.Sources = model.DefaultOptionSources()

	_, enabled := runFeaturesJSON(t, testPathsWithFeatures(t), opts)
	sort.Strings(enabled)
	if want := []string{"github-cli", "node-dev"}; !reflect.DeepEqual(enabled, want) {
		t.Fatalf("enabled = %v, want %v", enabled, want)
	}

	ctx := NewAppContext(testPathsWithFeatures(t), t.TempDir())
	var code int
	text := captureStdout(t, func() { code = runFeatures(ctx, nil, opts, opts.Sources) })
	if code != 0 {
		t.Fatalf("runFeatures exit code = %d, want 0", code)
	}
	if strings.Contains(text, "qemu") {
		t.Fatalf("fallback listing attributes features to the qemu backend:\n%s", text)
	}
}
