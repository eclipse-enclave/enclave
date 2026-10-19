// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import "fmt"

// Warning codes are stable identifiers for machine consumers.
const (
	WarningInvalidConfigJSON     = "invalid_config_json"
	WarningConfigPathIsDirectory = "config_path_is_directory"
	WarningUnsupportedScope      = "unsupported_scope"
	WarningInvalidToolOverride   = "invalid_tool_override"
	WarningInvalidValue          = "invalid_value"
	WarningSuspiciousValue       = "suspicious_value"
	WarningProjectGuardrail      = "project_guardrail"
	WarningProjectSubtree        = "project_subtree"
)

const (
	warningLayerGlobal             = "global"
	warningLayerProject            = "project"
	warningLayerToolOverridePrefix = "tool_override:"
)

// Warning describes a config value that was ignored or looks wrong. Layer is
// "global", "project", or "tool_override:<tool>" for tool_overrides fields;
// Path is the config file that set the value.
type Warning struct {
	Code    string
	Option  string
	Layer   string
	Path    string
	Message string
}

func newWarning(code string, option string, toolName string, path string, format string, args ...any) Warning {
	w := Warning{Code: code, Option: option, Path: path, Message: fmt.Sprintf(format, args...)}
	if toolName != "" {
		w.Layer = warningLayerToolOverridePrefix + toolName
	}
	return w
}

func withFileLayer(warnings []Warning, layer string) []Warning {
	for i := range warnings {
		if warnings[i].Layer == "" {
			warnings[i].Layer = layer
		}
	}
	return warnings
}
