// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"strings"

	"enclave/internal/config"
	"enclave/internal/extinstall"
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

// extensionInventory reads the provenance and local-modification state a
// listing line renders. Passing the already-resolved names spares the installer
// a second enumeration of the extension roots.
func extensionInventory(paths model.Paths, kind model.ExtensionKind, exts []model.Extension) (map[string]extinstall.Managed, error) {
	names := make([]string, 0, len(exts))
	for _, ext := range exts {
		names = append(names, ext.Name)
	}
	return extinstall.Inventory(paths, kind, names, extinstall.InventoryModifications)
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

func runTools(ctx *AppContext, req *extinstall.Request, opts model.Options, _ model.OptionSources) int {
	toolExts, err := listToolExtensions(ctx.Paths)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	inventory, err := extensionInventory(ctx.Paths, model.KindTool, toolExts)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	if req != nil && req.JSON {
		alwaysEnabled := func(string) bool { return true }
		entries := extensionListJSONEntries(toolExts, inventory, alwaysEnabled)
		if err := renderExtensionListJSON(os.Stdout, model.KindTool, entries); err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		return 0
	}

	// Images are per-tool, so every profile is selectable with --tool; mark the
	// one that would run by default for this invocation.
	active := strings.TrimSpace(opts.Tool)
	for _, tool := range toolExts {
		suffix := provenanceSuffix(inventory[tool.Name])
		if tool.Name == active {
			fmt.Printf("✓ %s%s (selected)\n", tool.Name, suffix)
		} else {
			fmt.Printf("✓ %s%s\n", tool.Name, suffix)
		}
	}
	return 0
}
