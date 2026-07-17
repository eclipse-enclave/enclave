// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"strings"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func formatSource(source model.OptionSource, projectDir string) string {
	switch source {
	case model.SourceCLI:
		return "cli"
	case model.SourceProject:
		return config.ProjectConfigJSONPath(projectDir)
	case model.SourceToolOverride:
		return "tool override"
	case model.SourceGlobal:
		if path, err := config.GlobalConfigPath(); err == nil {
			return path
		}
		return "global config"
	default:
		return ""
	}
}

func loadProfileOrReport(paths model.Paths, tool string) (model.Profile, error) {
	profile, err := config.LoadProfile(paths, tool)
	if err != nil {
		logx.Errorf("%v", err)
		if available, listErr := config.ListProfiles(paths); listErr == nil {
			logx.Infof("Available tools: %s", strings.Join(available, " "))
		}
	}
	return profile, err
}

func runTools(ctx *AppContext, opts model.Options, _ model.OptionSources) int {
	toolExts, err := listToolExtensions(ctx.Paths)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	// Images are per-tool, so every profile is selectable with --tool; mark the
	// one that would run by default for this invocation.
	active := strings.TrimSpace(opts.Tool)
	for _, profile := range toolNameList(toolExts) {
		if profile == active {
			fmt.Printf("✓ %s (selected)\n", profile)
		} else {
			fmt.Printf("✓ %s\n", profile)
		}
	}
	return 0
}
