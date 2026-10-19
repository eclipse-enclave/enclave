// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"io/fs"
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

func runConfig(input *CommandInput) int {
	paths := input.Ctx.Paths
	view := input.ConfigView
	opts, warnings := normalizeOptions(input.Options)
	for _, warning := range warnings {
		logx.Warnf(warning)
	}
	hostConfigDefaults := []string{}
	resolvedHostConfigPaths := opts.HostConfigPaths
	var profile *model.Profile
	var profileErr error
	if loaded, err := config.LoadProfile(paths, opts.Tool); err == nil {
		profile = &loaded
		hostConfigDefaults = config.HostConfigPassthroughDefaults(loaded)
		resolvedHostConfigPaths = config.ResolveHostConfigPaths(loaded, opts.HostConfigPaths)
	} else {
		profileErr = err
	}

	rowOpts := opts
	rowOpts.HostConfigPaths = resolvedHostConfigPaths
	rows := buildConfigRows(rowOpts, input.BaseOptions, input.GlobalDefaults, input.ProjectDefaults, input.ToolDefaults, input.HasToolDefaults, hostConfigDefaults)
	globalPath, globalErr := config.GlobalConfigPath()
	projectPath := config.ProjectConfigJSONPath(input.Ctx.ProjectDir)
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
		filePaths, fileStatus := configJSONPaths(globalPath, projectPath, globalExists, projectExists, globalErr)
		return writeConfigJSON(configJSONDocument{
			configJSONEnvelope: buildConfigEnvelope(input, opts, profile, profileErr, configJSONFiles(filePaths, fileStatus)),
			configJSONOutput:   legacyConfigJSON(rows, view, filePaths, fileStatus),
		})
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
		switch def.Apply {
		case config.ApplySliceMergeHost:
			row.Default = configCell{Value: config.FormatSlice(hostConfigDefaults), Set: true}
			row.Effective = config.FormatSlice(opts.HostConfigPaths)
		case config.ApplySliceMergeFeature:
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

// configSchemaVersion versions the typed `enclave config --json` envelope,
// an integration contract for external consumers.
const configSchemaVersion = "1"

const (
	configWarningValidation          = "validation"
	configWarningUnknownFeature      = "unknown_feature"
	configWarningFeaturesUnavailable = "features_unavailable"
	configWarningToolUnresolved      = "tool_unresolved"
	configWarningToolUnavailable     = "tool_unavailable"
	configWarningProjectUnavailable  = "project_unavailable"
)

type configJSONDocument struct {
	configJSONEnvelope
	configJSONOutput
}

type configJSONEnvelope struct {
	SchemaVersion string                      `json:"schemaVersion"`
	Kind          string                      `json:"kind"`
	View          string                      `json:"view"`
	Tool          configJSONTool              `json:"tool"`
	Project       *configJSONProject          `json:"project"`
	Files         map[string]configJSONFile   `json:"files"`
	Options       map[string]configJSONOption `json:"options"`
	Warnings      []configJSONWarning         `json:"warnings"`
	Valid         bool                        `json:"valid"`
	Error         string                      `json:"error"`
}

type configJSONTool struct {
	Name   *string `json:"name"`
	Source string  `json:"source"`
}

type configJSONProject struct {
	Dir     string `json:"dir"`
	RealDir string `json:"realDir"`
	Hash    string `json:"hash"`
	Name    string `json:"name"`
}

type configJSONFile struct {
	Path   string `json:"path,omitempty"`
	Status string `json:"status"`
}

type configJSONOption struct {
	Kind       string         `json:"kind"`
	Value      any            `json:"value"`
	Source     string         `json:"source"`
	Layers     map[string]any `json:"layers"`
	Directives *[]string      `json:"directives,omitempty"`
	ResolvedBy string         `json:"resolvedBy,omitempty"`
}

type configJSONWarning struct {
	Code    string `json:"code"`
	Option  string `json:"option,omitempty"`
	Layer   string `json:"layer,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// buildConfigEnvelope reports values a run with these options would use:
// validated options when validation passes, otherwise the normalized ones.
// Sources, layers, and directives always describe the configured input.
func buildConfigEnvelope(input *CommandInput, opts model.Options, profile *model.Profile, profileErr error, files map[string]configJSONFile) configJSONEnvelope {
	raw := input.Options
	envelope := configJSONEnvelope{
		SchemaVersion: configSchemaVersion,
		Kind:          "config",
		View:          input.ConfigView.Mode,
		Tool:          configJSONTool{Source: sourceLabel(raw.Sources.Tool)},
		Files:         files,
		Options:       map[string]configJSONOption{},
		Warnings:      []configJSONWarning{},
		Valid:         true,
	}
	addWarning := func(code string, option string, message string) {
		envelope.Warnings = append(envelope.Warnings, configJSONWarning{Code: code, Option: option, Message: message})
	}
	for _, w := range input.ConfigWarnings {
		envelope.Warnings = append(envelope.Warnings, configJSONWarning(w))
	}
	if project, err := input.Ctx.Project(); err == nil {
		envelope.Project = &configJSONProject{Dir: project.Dir, RealDir: project.RealDir, Hash: project.Hash, Name: project.Name}
	} else {
		addWarning(configWarningProjectUnavailable, "", fmt.Sprintf("Cannot resolve the project: %v", err))
	}

	values, validationWarnings, validationErr := optionsAsRun(input.Ctx.Paths, opts, raw.Sources)
	for _, message := range validationWarnings {
		addWarning(configWarningValidation, "", message)
	}
	if validationErr != nil {
		envelope.Valid = false
		envelope.Error = validationErr.Error()
	}

	loaded := profile
	if toolUnset(opts.Tool) {
		loaded = nil
		addWarning(configWarningToolUnresolved, "tool", "No tool selected; pass --tool or set \"tool\" in the global config")
	} else {
		name := opts.Tool
		envelope.Tool.Name = &name
		if loaded == nil {
			message := fmt.Sprintf("Cannot load tool %q: %v", name, profileErr)
			if errors.Is(profileErr, fs.ErrNotExist) {
				message = fmt.Sprintf("Unknown tool %q", name)
			}
			addWarning(configWarningToolUnavailable, "tool", message)
			if envelope.Valid {
				envelope.Valid = false
				envelope.Error = message
			}
		}
	}

	var enabledFeatures []string
	selection, err := resolveFeatureSelection(input.Ctx.Paths, values.BuildOptions)
	if err == nil {
		enabledFeatures = selection.enabledNames()
	}
	for _, warning := range featureSelectionWarnings(selection, err) {
		if warning.Code == configWarningUnknownFeature {
			warning.Layer = sourceLabel(raw.Sources.Features)
		}
		envelope.Warnings = append(envelope.Warnings, warning)
	}

	if loaded != nil {
		values.HostConfigPaths = config.ResolveHostConfigPaths(*loaded, values.HostConfigPaths)
	}
	for _, def := range config.OptionDefs() {
		option := configJSONOption{
			Kind:   string(def.Kind),
			Source: sourceLabel(config.OptionEffectiveSource(def, raw.Sources)),
			Layers: configOptionLayers(def, input),
		}
		option.Value, option.ResolvedBy = typedOptionValue(def, values, loaded, enabledFeatures)
		if def.Kind == config.OptionKindYolo {
			option.Kind = string(config.OptionKindBool)
		}
		if def.HasDirectives() {
			directives := config.OptionDirectives(def, raw)
			option.Directives = &directives
		}
		envelope.Options[def.Name] = option
	}
	return envelope
}

func configOptionLayers(def config.OptionDef, input *CommandInput) map[string]any {
	layers := map[string]any{}
	if value, ok := config.OptionTypedDefaultsValue(def, input.GlobalDefaults); ok {
		layers[sourceLabel(model.SourceGlobal)] = value
	}
	if value, ok := config.OptionTypedDefaultsValue(def, input.ProjectDefaults); ok {
		layers[sourceLabel(model.SourceProject)] = value
	}
	if input.HasToolDefaults {
		if value, ok := config.OptionTypedDefaultsValue(def, input.ToolDefaults); ok {
			layers[sourceLabel(model.SourceToolOverride)] = value
		}
	}
	raw := input.Options
	if config.OptionEffectiveSource(def, raw.Sources) == model.SourceCLI {
		if def.Kind == config.OptionKindYolo {
			if raw.YoloOverride != nil {
				layers[sourceLabel(model.SourceCLI)] = *raw.YoloOverride
			}
		} else {
			layers[sourceLabel(model.SourceCLI)] = config.OptionTypedEffectiveValue(def, raw)
		}
	}
	return layers
}

func configJSONFiles(paths map[string]string, status map[string]string) map[string]configJSONFile {
	files := map[string]configJSONFile{}
	for _, layer := range []string{"global", "project"} {
		file := configJSONFile{Path: paths[layer], Status: status[layer]}
		if file.Status == "" {
			file.Status = "unavailable"
		}
		files[layer] = file
	}
	return files
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

func legacyConfigJSON(rows []configRow, view model.ConfigView, paths map[string]string, status map[string]string) configJSONOutput {
	out := configJSONOutput{
		Effective: map[string]string{},
		Sources:   map[string]configJSONSources{},
		Paths:     paths,
		Status:    status,
	}
	for _, row := range rows {
		switch view.Mode {
		case "effective":
			out.Effective[row.Name] = row.Effective
		case "source":
			out.Sources[row.Name] = configJSONSources{Effective: row.Effective, Source: sourceLabel(row.Source)}
		default:
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
	}
	return out
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

func writeConfigJSON(out configJSONDocument) int {
	if err := encodeIndentedJSON(os.Stdout, out); err != nil {
		logx.Errorf("Failed to render config JSON: %v", err)
		return 1
	}
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
