// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"path/filepath"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

const GatewayAllowlistsDirName = "gateway-allowlists"

func GatewayAllowlistOverrideForTool(tool string, home string, projectHash string) (string, string) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return "", ""
	}

	filename := tool + ".conf"
	projectOverride := filepath.Join(HostProjectGatewayAllowlistsDir(home, projectHash), filename)
	if projectHash != "" && util.PathExists(projectOverride) {
		return projectOverride, "project-specific"
	}

	globalOverride := filepath.Join(HostGatewayAllowlistsDir(home), filename)
	if util.PathExists(globalOverride) {
		return globalOverride, "global"
	}

	return "", ""
}

func GatewayAllowlistOverridePath(profile model.Profile, home string, projectHash string) (string, string) {
	return GatewayAllowlistOverrideForTool(profile.Name, home, projectHash)
}

func ResolveAllowlistPathWithScope(tool string, home string, projectHash string, fallbackPath string) (string, string) {
	if overridePath, scope := GatewayAllowlistOverrideForTool(tool, home, projectHash); overridePath != "" {
		return overridePath, scope
	}
	return fallbackPath, "built-in"
}

func ResolveAllowlistPath(tool string, home string, projectHash string, fallbackPath string) string {
	path, _ := ResolveAllowlistPathWithScope(tool, home, projectHash, fallbackPath)
	return path
}
