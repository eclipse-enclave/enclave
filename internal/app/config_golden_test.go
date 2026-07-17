// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

// updateConfigGolden regenerates the golden snapshot. Run:
//
//	go test ./internal/app/ -run TestBuildConfigRowsGolden -update-config-golden
var updateConfigGolden = flag.Bool("update-config-golden", false, "rewrite the config-rows golden file")

func cfgGoldenBool(b bool) *bool { return &b }

// TestBuildConfigRowsGolden characterizes the full output of buildConfigRows
// (every cell, source, and effective value for all options) across a spread of
// kinds (string/bool/slice/yolo) and sources (cli/global/project/tool_override/
// default). It guards the option-display logic so the reflection-based rewrite
// of the generated per-spec closures is provably behavior-preserving.
func TestBuildConfigRowsGolden(t *testing.T) {
	var opts model.Options
	opts.Tool = "codex"
	opts.Sources.Tool = model.SourceCLI
	opts.AuthScope = "project"
	opts.Sources.AuthScope = model.SourceProject
	opts.Ephemeral = true
	opts.Sources.Ephemeral = model.SourceCLI
	opts.NoCache = true
	opts.Sources.NoCache = model.SourceGlobal
	opts.PassEnv = []string{"FOO", "BAR"}
	opts.Sources.PassEnv = model.SourceCLI
	opts.ForceRebuild = true
	opts.Sources.ForceRebuild = model.SourceCLI
	opts.YoloOverride = cfgGoldenBool(true)
	opts.ConfigDefaultYolo = cfgGoldenBool(false)
	opts.Sources.Yolo = model.SourceCLI
	opts.HostConfigPaths = []string{"/a", "/b"}
	opts.Features = []string{"go", "node"}

	var base model.Options
	base.Tool = "claude"
	base.AuthScope = "shared"

	var global config.Defaults
	global.NoCache = cfgGoldenBool(true)

	var project config.Defaults
	project.AuthScope = "project"
	project.PassEnv = []string{"FOO", "BAR"}

	var toolOverride config.Defaults
	toolOverride.AuthScope = "shared"
	toolOverride.Ephemeral = cfgGoldenBool(true)

	rows := buildConfigRows(opts, base, global, project, toolOverride, true, []string{"/x", "/y"})
	got := formatConfigRowsForGolden(rows)

	goldenPath := filepath.Join("testdata", "config_rows.golden")
	if *updateConfigGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d rows)", goldenPath, len(rows))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run once with -update-config-golden): %v", err)
	}
	if got != string(want) {
		t.Errorf("buildConfigRows output changed:\n--- got ---\n%s\n--- want ---\n%s", got, string(want))
	}
}

func formatConfigRowsForGolden(rows []configRow) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s | default=%q/%t global=%q/%t project=%q/%t toolOverride=%q/%t cli=%q/%t | source=%v effective=%q\n",
			r.Name,
			r.Default.Value, r.Default.Set,
			r.Global.Value, r.Global.Set,
			r.Project.Value, r.Project.Set,
			r.ToolOverride.Value, r.ToolOverride.Set,
			r.CLI.Value, r.CLI.Set,
			r.Source, r.Effective,
		)
	}
	return b.String()
}
