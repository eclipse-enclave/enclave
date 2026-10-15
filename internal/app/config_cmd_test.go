// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

func TestBuildConfigRows_IncludesToolOverrideCell(t *testing.T) {
	opts := config.DefaultOptions()
	opts.NoAPIKey = true
	opts.Sources = model.DefaultOptionSources()
	opts.Sources.NoAPIKey = model.SourceToolOverride

	rows := buildConfigRows(
		opts,
		config.DefaultOptions(),
		config.Defaults{},
		config.Defaults{},
		config.Defaults{NoAPIKey: boolPtr(true)},
		true,
		nil,
	)

	foundIndex := -1
	for i := range rows {
		if rows[i].Name == "no_api_key" {
			foundIndex = i
			break
		}
	}
	if foundIndex < 0 {
		t.Fatalf("expected row for no_api_key")
	}
	row := rows[foundIndex]
	if !row.ToolOverride.Set {
		t.Fatalf("expected tool_override cell to be set")
	}
	if row.ToolOverride.Value != "true" {
		t.Fatalf("unexpected tool_override value: %q", row.ToolOverride.Value)
	}
}

func TestBuildConfigRows_UsesResolvedHostConfigDefaults(t *testing.T) {
	opts := config.DefaultOptions()
	opts.HostConfigPaths = []string{"commands/", "settings.json"}
	rows := buildConfigRows(
		opts,
		config.DefaultOptions(),
		config.Defaults{},
		config.Defaults{},
		config.Defaults{},
		false,
		[]string{"commands/", "settings.json"},
	)

	var found *configRow
	for i := range rows {
		if rows[i].Name == "host_config_paths" {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected row for host_config_paths")
		return
	}
	if !found.Default.Set || found.Default.Value != "[commands/,settings.json]" {
		t.Fatalf("unexpected host_config_paths default cell: %#v", found.Default)
	}
	if found.Effective != "[commands/,settings.json]" {
		t.Fatalf("unexpected host_config_paths effective value: %q", found.Effective)
	}
}

func TestRenderConfigMatrix_IncludesToolOverrideColumn(t *testing.T) {
	rows := []configRow{
		{
			Name:         "no_api_key",
			Default:      configCell{Value: "false", Set: true},
			ToolOverride: configCell{Value: "true", Set: true},
			Source:       model.SourceToolOverride,
			Effective:    "true",
		},
	}

	out := captureStdout(t, func() {
		renderConfigMatrix(rows)
	})
	if !strings.Contains(out, "tool_override") {
		t.Fatalf("expected matrix header to include tool_override, output:\n%s", out)
	}
	if !strings.Contains(out, "true*") {
		t.Fatalf("expected effective tool_override value marker in output:\n%s", out)
	}
}

func TestRenderConfigSources_UsesToolOverrideLabel(t *testing.T) {
	rows := []configRow{
		{
			Name:      "no_api_key",
			Source:    model.SourceToolOverride,
			Effective: "true",
		},
	}

	out := captureStdout(t, func() {
		renderConfigSources(rows)
	})
	if !strings.Contains(out, "tool_override (true)") {
		t.Fatalf("expected source output to include tool_override label, output:\n%s", out)
	}
}

func TestLegacyConfigJSON_IncludesToolOverrideFieldAndSource(t *testing.T) {
	rows := []configRow{
		{
			Name:         "no_api_key",
			Default:      configCell{Value: "false", Set: true},
			ToolOverride: configCell{Value: "true", Set: true},
			Source:       model.SourceToolOverride,
			Effective:    "true",
		},
	}

	out := captureStdout(t, func() {
		code := writeConfigJSON(configJSONDocument{configJSONOutput: legacyConfigJSON(rows, model.ConfigView{}, nil, nil)})
		if code != 0 {
			t.Fatalf("writeConfigJSON returned %d", code)
		}
	})

	var payload configJSONOutput
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal JSON output: %v\noutput:\n%s", err, out)
	}
	source, ok := payload.Sources["no_api_key"]
	if !ok {
		t.Fatalf("expected sources.no_api_key in JSON output")
	}
	if source.ToolOverride == nil || *source.ToolOverride != "true" {
		t.Fatalf("expected sources.no_api_key.tool_override=true, got %#v", source.ToolOverride)
	}
	if source.Source != "tool_override" {
		t.Fatalf("expected sources.no_api_key.source=tool_override, got %q", source.Source)
	}
}

func TestLegacyConfigJSON_SourceViewSupportsToolOverride(t *testing.T) {
	rows := []configRow{
		{
			Name:      "no_api_key",
			Source:    model.SourceToolOverride,
			Effective: "true",
		},
	}

	out := captureStdout(t, func() {
		code := writeConfigJSON(configJSONDocument{configJSONOutput: legacyConfigJSON(rows, model.ConfigView{Mode: "source"}, nil, nil)})
		if code != 0 {
			t.Fatalf("writeConfigJSON returned %d", code)
		}
	})

	var payload configJSONOutput
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal JSON output: %v\noutput:\n%s", err, out)
	}
	if payload.Sources["no_api_key"].Source != "tool_override" {
		t.Fatalf("expected source label tool_override, got %q", payload.Sources["no_api_key"].Source)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	outCh := drainPipe(reader)
	os.Stdout = writer

	defer func() {
		os.Stdout = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	res := <-outCh
	if res.err != nil {
		t.Fatalf("read stdout: %v", res.err)
	}
	return string(res.data)
}

func boolPtr(v bool) *bool {
	return &v
}

func configTestInput(t *testing.T, paths model.Paths, opts model.Options, configWarnings []config.Warning) *CommandInput {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if opts.Sources == (model.OptionSources{}) {
		opts.Sources = model.DefaultOptionSources()
	}
	return &CommandInput{
		Ctx:            NewAppContext(paths, t.TempDir()),
		Options:        opts,
		BaseOptions:    config.DefaultOptions(),
		ConfigView:     model.ConfigView{Mode: "effective", JSON: true},
		ConfigWarnings: configWarnings,
	}
}

func runConfigJSON(t *testing.T, input *CommandInput) map[string]any {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = runConfig(input) })
	if code != 0 {
		t.Fatalf("runConfig exit code = %d, want 0\n%s", code, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal config JSON: %v\n%s", err, out)
	}
	return doc
}

func configJSONOptionField(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	option, ok := doc["options"].(map[string]any)[name].(map[string]any)
	if !ok {
		t.Fatalf("options.%s missing", name)
	}
	return option
}

func TestConfigJSONEnvelope(t *testing.T) {
	opts := config.DefaultOptions()
	opts.Ports = []string{"3000"}
	opts.Sources = model.DefaultOptionSources()
	opts.Sources.Ports = model.SourceCLI
	warning := config.Warning{Code: config.WarningProjectGuardrail, Option: "allow_domains", Layer: "project", Path: "/p/config.json", Message: "Ignoring allow_domains"}

	doc := runConfigJSON(t, configTestInput(t, testPathsWithFeatures(t), opts, []config.Warning{warning}))

	for key, want := range map[string]any{"schemaVersion": "1", "kind": "config", "view": "effective", "valid": true, "error": ""} {
		if doc[key] != want {
			t.Fatalf("%s = %#v, want %#v", key, doc[key], want)
		}
	}
	if tool := doc["tool"].(map[string]any); tool["name"] != nil || tool["source"] != "default" {
		t.Fatalf("tool = %#v, want unresolved default", tool)
	}
	if doc["effective"].(map[string]any)["features"] != model.SelectionDefault {
		t.Fatalf("legacy effective.features = %#v, want %q", doc["effective"].(map[string]any)["features"], model.SelectionDefault)
	}

	warnings := doc["warnings"].([]any)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %#v, want guardrail and tool_unresolved", warnings)
	}
	want := map[string]any{"code": "project_guardrail", "option": "allow_domains", "layer": "project", "path": "/p/config.json", "message": "Ignoring allow_domains"}
	if !reflect.DeepEqual(warnings[0], want) {
		t.Fatalf("warnings[0] = %#v, want %#v", warnings[0], want)
	}
	if warnings[1].(map[string]any)["code"] != configWarningToolUnresolved {
		t.Fatalf("warnings[1] = %#v, want tool_unresolved", warnings[1])
	}

	for _, def := range config.OptionDefs() {
		option := configJSONOptionField(t, doc, def.Name)
		if _, ok := option["layers"].(map[string]any); !ok {
			t.Fatalf("options.%s.layers = %#v, want object", def.Name, option["layers"])
		}
		if def.Kind != config.OptionKindStringSlice || def.Name == "host_config_paths" {
			continue
		}
		if _, ok := option["value"].([]any); !ok {
			t.Fatalf("options.%s.value = %#v, want array", def.Name, option["value"])
		}
	}
	ports := configJSONOptionField(t, doc, "ports")
	if !reflect.DeepEqual(ports, map[string]any{"kind": "string_slice", "value": []any{"3000"}, "source": "cli", "layers": map[string]any{"cli": []any{"3000"}}}) {
		t.Fatalf("options.ports = %#v", ports)
	}
	for _, name := range []string{"yolo", "host_config_paths"} {
		if value := configJSONOptionField(t, doc, name)["value"]; value != nil {
			t.Fatalf("options.%s.value = %#v, want null without a tool", name, value)
		}
	}
	if kind := configJSONOptionField(t, doc, "yolo")["kind"]; kind != "bool" {
		t.Fatalf("options.yolo.kind = %#v, want bool", kind)
	}
}

func TestConfigJSONFeatures(t *testing.T) {
	paths := testPathsWithFeatures(t)
	writeAppFile(t, filepath.Join(paths.FeaturesDir, "playwright", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: playwright\ndefaultEnabled: false\n", 0o644)

	cases := []struct {
		name           string
		features       []string
		slim           bool
		playwrightMCP  bool
		wantValue      []any
		wantDirectives any
		wantUnknown    bool
	}{
		{name: "unset", wantValue: []any{"github-cli", "node-dev"}},
		{name: "additive with unknown", features: []string{"+shell-extras", "+ghost"}, wantValue: []any{"github-cli", "node-dev", "shell-extras"}, wantDirectives: []any{"+shell-extras", "+ghost"}, wantUnknown: true},
		{name: "none", features: []string{}, wantValue: []any{}, wantDirectives: []any{}},
		{name: "slim", slim: true, wantValue: []any{}},
		{name: "playwright_mcp", playwrightMCP: true, wantValue: []any{"github-cli", "node-dev", "playwright"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := config.DefaultOptions()
			opts.Features = tc.features
			opts.Slim = tc.slim
			opts.PlaywrightMCP = tc.playwrightMCP

			doc := runConfigJSON(t, configTestInput(t, paths, opts, nil))

			if doc["valid"] != true {
				t.Fatalf("valid = %#v, error = %#v", doc["valid"], doc["error"])
			}
			features := configJSONOptionField(t, doc, "features")
			if !reflect.DeepEqual(features["value"], tc.wantValue) {
				t.Fatalf("features.value = %#v, want %#v", features["value"], tc.wantValue)
			}
			if !reflect.DeepEqual(features["directives"], tc.wantDirectives) {
				t.Fatalf("features.directives = %#v, want %#v", features["directives"], tc.wantDirectives)
			}
			gotUnknown := false
			for _, w := range doc["warnings"].([]any) {
				if w.(map[string]any)["code"] == configWarningUnknownFeature {
					gotUnknown = true
				}
			}
			if gotUnknown != tc.wantUnknown {
				t.Fatalf("unknown_feature warning = %v, want %v: %#v", gotUnknown, tc.wantUnknown, doc["warnings"])
			}
		})
	}
}

func TestConfigJSONInvalidOptionsStillEmitEnvelope(t *testing.T) {
	opts := config.DefaultOptions()
	opts.Tool = "claude"
	opts.SkillsValidation = "bogus"

	doc := runConfigJSON(t, configTestInput(t, testPathsWithFeatures(t), opts, nil))

	if doc["valid"] != false || doc["error"] != "--skills-validation must be strict or agent" {
		t.Fatalf("valid/error = %#v/%#v", doc["valid"], doc["error"])
	}
	if doc["effective"].(map[string]any)["skills_validation"] != "bogus" {
		t.Fatalf("legacy effective.skills_validation = %#v, want unvalidated value", doc["effective"].(map[string]any)["skills_validation"])
	}
}

func TestConfigJSONYoloResolution(t *testing.T) {
	off := false
	opts := config.DefaultOptions()
	opts.Tool = "claude"
	opts.YoloOverride = &off
	opts.Sources = model.DefaultOptionSources()
	opts.Sources.Yolo = model.SourceCLI
	input := &CommandInput{Ctx: NewAppContext(testPathsWithFeatures(t), t.TempDir()), Options: opts}

	envelope := buildConfigEnvelope(input, opts, &model.Profile{Name: "claude"}, nil, nil)

	yolo := envelope.Options["yolo"]
	if yolo.Value != false || yolo.ResolvedBy != yoloResolvedByCLI || yolo.Layers["cli"] != false {
		t.Fatalf("options.yolo = %#v", yolo)
	}
}

func TestConfigJSONValuesMatchValidatedRun(t *testing.T) {
	opts := config.DefaultOptions()
	opts.AllowDomains = []string{"Example.com"}
	opts.Sources = model.DefaultOptionSources()
	opts.Sources.AllowDomains = model.SourceGlobal

	doc := runConfigJSON(t, configTestInput(t, testPathsWithFeatures(t), opts, nil))

	allowDomains := configJSONOptionField(t, doc, "allow_domains")
	if !reflect.DeepEqual(allowDomains["value"], []any{"example.com"}) || allowDomains["source"] != "global" {
		t.Fatalf("options.allow_domains = %#v, want validated lowercase value from global", allowDomains)
	}
	if value := configJSONOptionField(t, doc, "backend")["value"]; value != "auto" {
		t.Fatalf("options.backend.value = %#v, want configured auto", value)
	}
}

func TestSessionProvenanceMatchesConfigJSON(t *testing.T) {
	stubBackendResolution(t, []string{backend.NamePodman}, false)
	paths := testPathsWithFeatures(t)
	paths.AppRoot = t.TempDir()
	profile := model.Profile{Name: "claude", PassthroughPaths: []string{"settings.json"}}
	opts := config.DefaultOptions()
	opts.Tool = "claude"
	opts.Features = []string{"+shell-extras"}
	opts.PassEnv = []string{"GITHUB_TOKEN"}
	opts.HostConfig = model.HostConfigPassthrough
	opts.Sources = model.DefaultOptionSources()

	runInput := configTestInput(t, paths, opts, nil)
	configuredBackend, err := resolveActionBackend("run", &runInput.Options, false)
	if err != nil || runInput.Options.Backend != backend.NamePodman {
		t.Fatalf("backend = %q, err = %v; want detected podman", runInput.Options.Backend, err)
	}
	runInput.ConfiguredBackend = configuredBackend
	project := model.Project{Dir: paths.AppRoot, RealDir: paths.AppRoot, Hash: "abc123def456", Name: "project"}
	validated, _, _, _, err := ValidateRunOptions(runInput.Options, opts.Sources, ValidationContext{Paths: paths, Action: "run"}, project)
	if err != nil {
		t.Fatalf("ValidateRunOptions: %v", err)
	}
	validated.HostConfigPaths = config.ResolveHostConfigPaths(profile, validated.HostConfigPaths)
	runInput.Action = "run"
	_, recorded := sessionFeaturesAndProvenance(runInput, validated, profile)

	configInput := configTestInput(t, paths, opts, nil)
	envelope := buildConfigEnvelope(configInput, opts, &profile, nil, nil)

	if features := recorded["features"]; !reflect.DeepEqual(features, []string{"github-cli", "node-dev", "shell-extras"}) {
		t.Fatalf("features = %#v", features)
	}
	for _, def := range config.OptionDefs() {
		value, ok := recorded[def.Name]
		if ok != (def.AffectsSession == config.SessionAffected) {
			t.Fatalf("options.%s recorded = %v, want only session-affecting options", def.Name, ok)
		}
		if !ok {
			continue
		}
		got, _ := json.Marshal(value)
		want, _ := json.Marshal(envelope.Options[def.Name].Value)
		if string(got) != string(want) {
			t.Errorf("options.%s = %s, config --json reports %s", def.Name, got, want)
		}
	}
}

func TestConfigJSONQEMUValidatedAsRun(t *testing.T) {
	cases := []struct {
		name          string
		source        model.OptionSource
		wantValid     bool
		wantFeatures  []any
		wantAllowsAll bool
	}{
		{name: "cli features rejected", source: model.SourceCLI, wantValid: false, wantFeatures: []any{"github-cli", "node-dev"}},
		{name: "config features dropped", source: model.SourceGlobal, wantValid: true, wantFeatures: []any{}, wantAllowsAll: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := config.DefaultOptions()
			opts.Backend = "qemu"
			opts.Features = []string{"+node-dev"}
			opts.Sources = model.DefaultOptionSources()
			opts.Sources.Features = tc.source

			doc := runConfigJSON(t, configTestInput(t, testPathsWithFeatures(t), opts, nil))

			if doc["valid"] != tc.wantValid {
				t.Fatalf("valid = %#v (error %#v), want %v", doc["valid"], doc["error"], tc.wantValid)
			}
			if value := configJSONOptionField(t, doc, "features")["value"]; !reflect.DeepEqual(value, tc.wantFeatures) {
				t.Fatalf("features.value = %#v, want %#v", value, tc.wantFeatures)
			}
			if value := configJSONOptionField(t, doc, "allow_all_network")["value"]; value != tc.wantAllowsAll {
				t.Fatalf("allow_all_network.value = %#v, want %v", value, tc.wantAllowsAll)
			}
		})
	}
}

func TestConfigJSONUnavailableInputs(t *testing.T) {
	brokenFeatures := model.Paths{FeaturesDir: filepath.Join(t.TempDir(), "features")}
	writeAppFile(t, brokenFeatures.FeaturesDir, "not a directory", 0o644)

	cases := []struct {
		name      string
		paths     model.Paths
		tool      string
		wantCode  string
		wantValid bool
		option    string
	}{
		{name: "unknown tool", paths: testPathsWithFeatures(t), tool: "bogus", wantCode: configWarningToolUnavailable, option: "yolo"},
		{name: "unlistable features", paths: brokenFeatures, wantCode: configWarningFeaturesUnavailable, wantValid: true, option: "features"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := config.DefaultOptions()
			opts.Tool = tc.tool

			doc := runConfigJSON(t, configTestInput(t, tc.paths, opts, nil))

			found := false
			for _, w := range doc["warnings"].([]any) {
				found = found || w.(map[string]any)["code"] == tc.wantCode
			}
			if !found {
				t.Fatalf("missing %s warning: %#v", tc.wantCode, doc["warnings"])
			}
			if doc["valid"] != tc.wantValid {
				t.Fatalf("valid = %#v (error %#v), want %v", doc["valid"], doc["error"], tc.wantValid)
			}
			if value := configJSONOptionField(t, doc, tc.option)["value"]; value != nil {
				t.Fatalf("options.%s.value = %#v, want null", tc.option, value)
			}
		})
	}
}

func TestConfigJSONKeepsLegacyFields(t *testing.T) {
	global := config.Defaults{AllowDomains: []string{"Example.com"}}
	opts := config.DefaultOptions()
	opts.AllowDomains = global.AllowDomains
	opts.Sources = model.DefaultOptionSources()
	opts.Sources.AllowDomains = model.SourceGlobal
	input := configTestInput(t, testPathsWithFeatures(t), opts, nil)
	input.GlobalDefaults = global

	doc := runConfigJSON(t, input)

	if got := doc["effective"].(map[string]any)["allow_domains"]; got != "[Example.com]" {
		t.Fatalf("effective.allow_domains = %#v, want unvalidated display string", got)
	}
	normalized, _ := normalizeOptions(opts)
	rows := buildConfigRows(normalized, config.DefaultOptions(), global, config.Defaults{}, config.Defaults{}, false, []string{})
	globalPath, globalErr := config.GlobalConfigPath()
	paths, status := configJSONPaths(globalPath, config.ProjectConfigJSONPath(input.Ctx.ProjectDir), false, false, globalErr)
	legacy, err := json.Marshal(legacyConfigJSON(rows, input.ConfigView, paths, status))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(legacy, &want); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"effective", "paths", "status"} {
		if !reflect.DeepEqual(doc[key], want[key]) {
			t.Fatalf("%s = %#v, want %#v", key, doc[key], want[key])
		}
	}
}
