// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

type configCell struct {
	Value string
	Set   bool
}

type configRow struct {
	Name         string
	Default      configCell
	Global       configCell
	Project      configCell
	ToolOverride configCell
	CLI          configCell
	Source       model.OptionSource
	Effective    string
}

func runConfig(
	paths model.Paths,
	opts model.Options,
	base model.Options,
	globalDefaults config.Defaults,
	projectDefaults config.Defaults,
	toolOverrideDefaults config.Defaults,
	hasToolOverride bool,
	view model.ConfigView,
	projectDir string,
) int {
	opts, warnings := normalizeOptions(opts)
	for _, warning := range warnings {
		logx.Warnf(warning)
	}
	hostConfigDefaults := []string{}
	if profile, err := config.LoadProfile(paths, opts.Tool); err == nil {
		hostConfigDefaults = config.HostConfigPassthroughDefaults(profile)
		opts.HostConfigPaths = config.ResolveHostConfigPaths(profile, opts.HostConfigPaths)
	} else {
		logx.Debugf("Skipping host_config_paths resolution for config output: %v", err)
	}

	rows := buildConfigRows(opts, base, globalDefaults, projectDefaults, toolOverrideDefaults, hasToolOverride, hostConfigDefaults)
	globalPath, globalErr := config.GlobalConfigPath()
	projectPath := config.ProjectConfigJSONPath(projectDir)
	globalExists := false
	projectExists := false
	if globalErr == nil && strings.TrimSpace(globalPath) != "" {
		if _, err := os.Stat(globalPath); err == nil {
			globalExists = true
		}
	}
	if strings.TrimSpace(projectPath) != "" {
		if _, err := os.Stat(projectPath); err == nil {
			projectExists = true
		}
	}
	if view.Mode == "diff" {
		rows = filterConfigDiff(rows)
	}

	if view.JSON {
		return renderConfigJSON(rows, view, globalPath, projectPath, globalExists, projectExists, globalErr)
	}

	if globalErr != nil {
		fmt.Printf("Global Config: unavailable (%v)\n", globalErr)
	} else if globalExists {
		fmt.Printf("Global Config: %s\n", globalPath)
	} else {
		fmt.Printf("Global Config: %s (missing)\n", globalPath)
	}
	if projectExists {
		fmt.Printf("Project Config: %s\n", projectPath)
	} else {
		fmt.Printf("Project Config: %s (missing)\n", projectPath)
	}
	fmt.Println()
	fmt.Printf("Config sources (highest wins): cli > tool_override > project > global > default\n\n")
	if view.Mode == "effective" {
		renderConfigEffective(rows)
		return 0
	}
	if view.Mode == "source" {
		renderConfigSources(rows)
		return 0
	}
	renderConfigMatrix(rows)
	return 0
}

func buildConfigRows(
	opts model.Options,
	base model.Options,
	globalDefaults config.Defaults,
	projectDefaults config.Defaults,
	toolOverrideDefaults config.Defaults,
	hasToolOverride bool,
	hostConfigDefaults []string,
) []configRow {
	defaults := base

	defs := config.OptionDefs()
	rows := make([]configRow, 0, len(defs))
	for _, def := range defs {
		toolOverrideCell := configCell{}
		if hasToolOverride {
			toolOverrideCell = cellFrom(config.OptionDefaultsValue(def, toolOverrideDefaults))
		}
		row := configRow{
			Name:         def.Name,
			Default:      cellFrom(config.OptionDefaultValue(def, defaults)),
			Global:       cellFrom(config.OptionDefaultsValue(def, globalDefaults)),
			Project:      cellFrom(config.OptionDefaultsValue(def, projectDefaults)),
			ToolOverride: toolOverrideCell,
			CLI:          cellFrom(config.OptionCLIValue(def, opts)),
			Source:       config.OptionEffectiveSource(def, opts.Sources),
			Effective:    config.OptionEffectiveValue(def, opts),
		}
		switch row.Name {
		case "host_config_paths":
			row.Default = configCell{Value: config.FormatSlice(hostConfigDefaults), Set: true}
			row.Effective = config.FormatSlice(opts.HostConfigPaths)
		case "features":
			if defaults.Features == nil && !row.Default.Set {
				row.Default = configCell{Value: model.SelectionDefault, Set: true}
			}
			if opts.Features == nil {
				row.Effective = model.SelectionDefault
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func filterConfigDiff(rows []configRow) []configRow {
	filtered := make([]configRow, 0, len(rows))
	for _, row := range rows {
		if row.Source != model.SourceDefault ||
			row.Global.Set ||
			row.Project.Set ||
			row.ToolOverride.Set ||
			row.CLI.Set {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func renderConfigMatrix(rows []configRow) {
	headers := []string{"Option", "default", "global", "project", "tool_override", "cli"}
	widths := []int{len(headers[0]), len(headers[1]), len(headers[2]), len(headers[3]), len(headers[4]), len(headers[5])}

	type rendered struct {
		name   string
		values []string
	}
	renderedRows := make([]rendered, 0, len(rows))
	for _, row := range rows {
		values := []string{
			renderCell(row.Default, row, model.SourceDefault),
			renderCell(row.Global, row, model.SourceGlobal),
			renderCell(row.Project, row, model.SourceProject),
			renderCell(row.ToolOverride, row, model.SourceToolOverride),
			renderCell(row.CLI, row, model.SourceCLI),
		}
		renderedRows = append(renderedRows, rendered{name: row.Name, values: values})
		widths[0] = max(widths[0], len(row.Name))
		for i, value := range values { // #nosec G602 -- widths has 6 elements; values always has 5 (indices 0-4, offset by 1 = 1-5).
			widths[i+1] = max(widths[i+1], len(value))
		}
	}

	format := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds\n",
		widths[0], widths[1], widths[2], widths[3], widths[4], widths[5])
	fmt.Printf(format, headers[0], headers[1], headers[2], headers[3], headers[4], headers[5])
	for _, row := range renderedRows {
		fmt.Printf(format, row.name, row.values[0], row.values[1], row.values[2], row.values[3], row.values[4])
	}
}

func renderConfigEffective(rows []configRow) {
	nameWidth := 0
	for _, row := range rows {
		nameWidth = max(nameWidth, len(row.Name))
	}
	format := fmt.Sprintf("%%-%ds  %%s\n", nameWidth)
	for _, row := range rows {
		fmt.Printf(format, row.Name, formatEffective(row))
	}
}

func renderConfigSources(rows []configRow) {
	nameWidth := 0
	for _, row := range rows {
		nameWidth = max(nameWidth, len(row.Name))
	}
	format := fmt.Sprintf("%%-%ds  %%s\n", nameWidth)
	for _, row := range rows {
		value := formatEffective(row)
		fmt.Printf(format, row.Name, fmt.Sprintf("%s (%s)", sourceLabel(row.Source), value))
	}
}

type configJSONOutput struct {
	Effective map[string]string            `json:"effective,omitempty"`
	Sources   map[string]configJSONSources `json:"sources,omitempty"`
	Paths     map[string]string            `json:"paths,omitempty"`
	Status    map[string]string            `json:"status,omitempty"`
}

type configJSONSources struct {
	Default      *string `json:"default,omitempty"`
	Global       *string `json:"global,omitempty"`
	Project      *string `json:"project,omitempty"`
	ToolOverride *string `json:"tool_override,omitempty"`
	CLI          *string `json:"cli,omitempty"`
	Effective    string  `json:"effective"`
	Source       string  `json:"source"`
}

func renderConfigJSON(rows []configRow, view model.ConfigView, globalPath string, projectPath string, globalExists bool, projectExists bool, globalErr error) int {
	if view.Mode == "effective" {
		out := configJSONOutput{Effective: map[string]string{}}
		out.Paths, out.Status = configJSONPaths(globalPath, projectPath, globalExists, projectExists, globalErr)
		for _, row := range rows {
			out.Effective[row.Name] = row.Effective
		}
		return writeConfigJSON(out)
	}
	if view.Mode == "source" {
		out := configJSONOutput{Sources: map[string]configJSONSources{}}
		out.Paths, out.Status = configJSONPaths(globalPath, projectPath, globalExists, projectExists, globalErr)
		for _, row := range rows {
			out.Sources[row.Name] = configJSONSources{
				Effective: row.Effective,
				Source:    sourceLabel(row.Source),
			}
		}
		return writeConfigJSON(out)
	}

	out := configJSONOutput{
		Effective: map[string]string{},
		Sources:   map[string]configJSONSources{},
	}
	out.Paths, out.Status = configJSONPaths(globalPath, projectPath, globalExists, projectExists, globalErr)
	for _, row := range rows {
		out.Effective[row.Name] = row.Effective
		out.Sources[row.Name] = configJSONSources{
			Default:      maybeString(renderCell(row.Default, row, model.SourceDefault), row.Default.Set, row.Source == model.SourceDefault),
			Global:       maybeString(renderCell(row.Global, row, model.SourceGlobal), row.Global.Set, row.Source == model.SourceGlobal),
			Project:      maybeString(renderCell(row.Project, row, model.SourceProject), row.Project.Set, row.Source == model.SourceProject),
			ToolOverride: maybeString(renderCell(row.ToolOverride, row, model.SourceToolOverride), row.ToolOverride.Set, row.Source == model.SourceToolOverride),
			CLI:          maybeString(renderCell(row.CLI, row, model.SourceCLI), row.CLI.Set, row.Source == model.SourceCLI),
			Effective:    row.Effective,
			Source:       sourceLabel(row.Source),
		}
	}
	return writeConfigJSON(out)
}

func configJSONPaths(globalPath string, projectPath string, globalExists bool, projectExists bool, globalErr error) (map[string]string, map[string]string) {
	paths := map[string]string{}
	status := map[string]string{}
	if globalErr == nil && strings.TrimSpace(globalPath) != "" {
		paths["global"] = globalPath
		if globalExists {
			status["global"] = "present"
		} else {
			status["global"] = "missing"
		}
	} else if globalErr != nil {
		status["global"] = "unavailable"
	}
	if strings.TrimSpace(projectPath) != "" {
		paths["project"] = projectPath
		if projectExists {
			status["project"] = "present"
		} else {
			status["project"] = "missing"
		}
	}
	if len(paths) == 0 {
		paths = nil
	}
	if len(status) == 0 {
		status = nil
	}
	return paths, status
}

func writeConfigJSON(out configJSONOutput) int {
	payload, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		logx.Errorf("Failed to render config JSON: %v", err)
		return 1
	}
	fmt.Printf("%s\n", payload)
	return 0
}

func maybeString(value string, set bool, effective bool) *string {
	if !set && !effective {
		return nil
	}
	out := strings.TrimSuffix(value, "*")
	return &out
}

func renderCell(cell configCell, row configRow, source model.OptionSource) string {
	value := cell.Value
	set := cell.Set
	star := row.Source == source
	if star {
		if row.Effective != "" && value != row.Effective {
			value = row.Effective
		}
		if !set {
			value = row.Effective
			set = true
		}
	}
	if !set || strings.TrimSpace(value) == "" {
		value = "-"
	}
	if star {
		value += "*"
	}
	return value
}

func formatEffective(row configRow) string {
	if strings.TrimSpace(row.Effective) == "" {
		return "-"
	}
	return row.Effective
}

func cellFrom(value string, set bool) configCell {
	return configCell{Value: value, Set: set}
}

func sourceLabel(source model.OptionSource) string {
	switch source {
	case model.SourceCLI:
		return "cli"
	case model.SourceProject:
		return "project"
	case model.SourceToolOverride:
		return "tool_override"
	case model.SourceGlobal:
		return "global"
	default:
		return "default"
	}
}
