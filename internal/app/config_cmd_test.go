// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

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

func TestRenderConfigJSON_IncludesToolOverrideFieldAndSource(t *testing.T) {
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
		code := renderConfigJSON(rows, model.ConfigView{}, "", "", false, false, nil)
		if code != 0 {
			t.Fatalf("renderConfigJSON returned %d", code)
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

func TestRenderConfigJSON_SourceViewSupportsToolOverride(t *testing.T) {
	rows := []configRow{
		{
			Name:      "no_api_key",
			Source:    model.SourceToolOverride,
			Effective: "true",
		},
	}

	out := captureStdout(t, func() {
		code := renderConfigJSON(rows, model.ConfigView{Mode: "source"}, "", "", false, false, nil)
		if code != 0 {
			t.Fatalf("renderConfigJSON returned %d", code)
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
	os.Stdout = writer

	defer func() {
		os.Stdout = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(data)
}

func boolPtr(v bool) *bool {
	return &v
}
