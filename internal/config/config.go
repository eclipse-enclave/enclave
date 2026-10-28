// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

type Defaults struct {
	Tool             string              `json:"tool"`
	ToolOverrides    map[string]Defaults `json:"tool_overrides"`
	Backend          string              `json:"backend"`
	AutoUpdate       *bool               `json:"auto_update"`
	UpdateChannel    string              `json:"update_channel"`
	HostConfig       string              `json:"host_config"`
	SkillsValidation string              `json:"skills_validation"`
	HostConfigPaths  []string            `json:"host_config_paths"`
	Yolo             *bool               `json:"yolo"`
	Ephemeral        *bool               `json:"ephemeral"`
	AuthScope        string              `json:"auth_scope"`
	AuthName         string              `json:"auth_name"`
	SecretsScope     string              `json:"secrets_scope"`
	ResetAuth        *bool               `json:"reset_auth"`
	NoAPIKey         *bool               `json:"no_api_key"`
	PassAPIKey       *bool               `json:"pass_api_key"`
	PassEnv          []string            `json:"pass_env"`
	AllowAllNetwork  *bool               `json:"allow_all_network"`
	NoCache          *bool               `json:"no_cache"`
	NoHistory        *bool               `json:"no_history"`
	NoMemory         *bool               `json:"no_memory"`
	SessionMonitor   *bool               `json:"session_monitor"`
	SessionTint      string              `json:"session_tint"`
	Timezone         string              `json:"timezone"`
	BaseImage        string              `json:"base_image"`
	Devcontainer     *bool               `json:"devcontainer"`
	Slim             *bool               `json:"slim"`
	ImageName        string              `json:"image_name"`
	Features         []string            `json:"features"`
	UseRemoteUser    *bool               `json:"use_remote_user"`
	CacheFrom        []string            `json:"cache_from"`
	Progress         string              `json:"progress"`
	NetworkLog       string              `json:"network_log"`
	Verbose          *bool               `json:"verbose"`
	Ports            []string            `json:"ports"`
	AddDirs          []string            `json:"add_dirs"`
	AddReadonlyDirs  []string            `json:"add_readonly_dirs"`
	ProjectMount     string              `json:"project_mount"`
	WorktreeMetadata string              `json:"worktree_metadata"`
	AllowDomains     []string            `json:"allow_domains"`
	BridgePorts      []string            `json:"bridge_ports"`
	PlaywrightMCP    *bool               `json:"playwright_mcp"`
}

// LoadGlobalDefaults reads only the global config, for commands that must not
// depend on the current project.
func LoadGlobalDefaults() (Defaults, []Warning, error) {
	globalPath, err := GlobalConfigPath()
	if err != nil {
		return Defaults{}, nil, err
	}
	return readDefaults(globalPath)
}

func LoadDefaults(projectDir string) (global Defaults, project Defaults, warnings []Warning, err error) {
	globalDefaults, globalWarns, err := LoadGlobalDefaults()
	if err != nil {
		return Defaults{}, Defaults{}, nil, err
	}
	warnings = append(warnings, withFileLayer(globalWarns, warningLayerGlobal)...)

	home, err := ResolveHostHome()
	if err != nil {
		return Defaults{}, Defaults{}, nil, err
	}
	proj, err := ResolveProjectFromDir(projectDir)
	if err != nil {
		return Defaults{}, Defaults{}, nil, err
	}

	projectPath := HostProjectConfigJSONPath(home, proj.Hash)
	projectDefaults, projectWarns, err := readDefaults(projectPath)
	if err != nil {
		return Defaults{}, Defaults{}, nil, err
	}
	warnings = append(warnings, withFileLayer(projectWarns, warningLayerProject)...)
	warnings = append(warnings, withFileLayer(stripGlobalOnlyKeys(projectPath, &projectDefaults), warningLayerProject)...)

	return globalDefaults, projectDefaults, warnings, nil
}

func ApplyDefaultsWithSources(opts model.Options, defaults Defaults, source model.OptionSource, sources *model.OptionSources) model.Options {
	if sources == nil {
		return opts
	}
	for _, spec := range OptionSpecs() {
		if spec.ApplyDefaultsWithSource == nil {
			continue
		}
		spec.ApplyDefaultsWithSource(&opts, defaults, source, sources)
	}
	return opts
}

func ResolveToolOverrideDefaults(global Defaults, project Defaults, tool string) (Defaults, bool) {
	name := strings.TrimSpace(tool)
	if name == "" {
		return Defaults{}, false
	}
	merged := mergeToolOverrides(global.ToolOverrides, project.ToolOverrides)
	if len(merged) == 0 {
		return Defaults{}, false
	}
	override, ok := merged[name]
	if ok {
		// Per-tool overrides must not carry nested overrides.
		override.ToolOverrides = nil
	}
	return override, ok
}

// ResolveOptionsForTool layers global, project, and the resolved tool's override
// defaults on top of the parsed CLI options, mirroring the resolution used
// before a run. When targetTool is non-empty it overrides the configured
// default tool: the `update` command uses this to resolve each explicit target
// with that tool's own overrides instead of the default tool's. It returns the
// resolved options (with Sources populated) plus the applied tool-override
// defaults and whether any applied.
func ResolveOptionsForTool(cliOpts model.Options, cliSources model.OptionSources, global Defaults, project Defaults, targetTool string) (model.Options, Defaults, bool) {
	sources := model.MergeOptionSources(model.DefaultOptionSources(), cliSources)
	opts := cliOpts
	opts = ApplyDefaultsWithSources(opts, global, model.SourceGlobal, &sources)
	opts = ApplyDefaultsWithSources(opts, project, model.SourceProject, &sources)
	tool := opts.Tool
	if t := strings.TrimSpace(targetTool); t != "" {
		tool = t
		opts.Tool = t
	}
	toolDefaults, hasToolDefaults := ResolveToolOverrideDefaults(global, project, tool)
	if hasToolDefaults {
		opts = ApplyDefaultsWithSources(opts, toolDefaults, model.SourceToolOverride, &sources)
	}
	opts.Sources = sources
	return opts, toolDefaults, hasToolDefaults
}

func canOverride(current model.OptionSource, incoming model.OptionSource) bool {
	switch incoming {
	case model.SourceGlobal:
		return current == model.SourceDefault
	case model.SourceProject:
		return current == model.SourceDefault || current == model.SourceGlobal
	case model.SourceToolOverride:
		return current == model.SourceDefault || current == model.SourceGlobal || current == model.SourceProject
	default:
		return false
	}
}

func readDefaults(path string) (Defaults, []Warning, error) {
	if path == "" {
		return Defaults{}, nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Defaults{}, nil, nil
		}
		return Defaults{}, nil, err
	}
	if info.IsDir() {
		return Defaults{}, []Warning{newWarning(WarningConfigPathIsDirectory, "", "", path, "Config path is a directory: %s", path)}, nil
	}
	// #nosec G304 -- path is an explicit config file path resolved by application config logic.
	data, err := os.ReadFile(path)
	if err != nil {
		return Defaults{}, nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return Defaults{}, nil, nil
	}
	var defaults Defaults
	if err := json.Unmarshal(data, &defaults); err != nil {
		return Defaults{}, []Warning{newWarning(WarningInvalidConfigJSON, "", "", path, "Invalid config JSON at %s: %v", path, err)}, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return defaults, nil, nil
	}

	if err := rejectRemovedConfigFields(path, doc); err != nil {
		return Defaults{}, nil, err
	}

	warnings := validateTopLevelDefaults(path, doc, &defaults)
	warnings = append(warnings, validateToolOverrides(path, doc, &defaults)...)
	warnings = append(warnings, validatePassEnvNames(path, &defaults)...)
	warnings = append(warnings, validateSessionTint(path, &defaults)...)
	warnings = append(warnings, validateUpdateChannel(path, &defaults)...)
	return defaults, warnings, nil
}

// removedConfigFields lists config options dropped with the per-tool-only image
// model. They are hard errors rather than warnings so stale configs surface
// loudly instead of silently building something unexpected.
var removedConfigFields = []struct{ key, hint string }{
	{"image_mode", "images are always per-tool; remove it"},
	{"agent_tools", "images are per-tool; select the agent with --tool"},
	{"no_agents", "the no-agent image was removed; use a tool image with the shell command instead"},
}

func rejectRemovedConfigFields(path string, doc map[string]json.RawMessage) error {
	for _, field := range removedConfigFields {
		if _, ok := doc[field.key]; ok {
			return fmt.Errorf("%s sets %q, which is no longer supported: %s", path, field.key, field.hint)
		}
	}

	rawOverrides, ok := doc["tool_overrides"]
	if !ok {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(rawOverrides, &entries); err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(entries[name], &entry); err != nil {
			continue
		}
		for _, field := range removedConfigFields {
			if _, ok := entry[field.key]; ok {
				return fmt.Errorf("%s sets tool_overrides.%s.%s, which is no longer supported: %s", path, name, field.key, field.hint)
			}
		}
	}
	return nil
}

// validatePassEnvNames drops entries from defaults.PassEnv (and each
// tool_overrides[*].PassEnv) that aren't valid environment variable names.
// Config-loaded values bypass the CLI's isValidEnvKey check, so this runs for
// both global and project configs.
func validatePassEnvNames(path string, defaults *Defaults) []Warning {
	if defaults == nil {
		return nil
	}

	warnings := make([]Warning, 0)
	defaults.PassEnv, warnings = filterPassEnv(path, "", defaults.PassEnv, warnings)

	if len(defaults.ToolOverrides) == 0 {
		return warnings
	}

	toolNames := make([]string, 0, len(defaults.ToolOverrides))
	for name := range defaults.ToolOverrides {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)

	for _, name := range toolNames {
		override := defaults.ToolOverrides[name]
		if len(override.PassEnv) == 0 {
			continue
		}
		var filtered []string
		filtered, warnings = filterPassEnv(path, name, override.PassEnv, warnings)
		override.PassEnv = filtered
		defaults.ToolOverrides[name] = override
	}

	return warnings
}

func filterPassEnv(path string, toolName string, values []string, warnings []Warning) ([]string, []Warning) {
	if len(values) == 0 {
		return values, warnings
	}
	kept := make([]string, 0, len(values))
	for _, raw := range values {
		if isValidEnvKey(raw) {
			kept = append(kept, raw)
			continue
		}
		entry := raw
		if name, _, hasValue := strings.Cut(raw, "="); hasValue {
			entry = name + "=…"
		}
		if toolName == "" {
			warnings = append(warnings, newWarning(WarningInvalidValue, "pass_env", "", path, "Ignoring pass_env entry %q in %s: not a valid environment variable name", entry, path))
		} else {
			warnings = append(warnings, newWarning(WarningInvalidValue, "pass_env", toolName, path, "Ignoring tool_overrides.%s.pass_env entry %q in %s: not a valid environment variable name", toolName, entry, path))
		}
	}
	if len(kept) == 0 {
		return nil, warnings
	}
	return kept, warnings
}

// validateSessionTint clears session_tint values (top level and per tool
// override) that aren't #rrggbb colors, so the warning names the config file
// that sets them and appears before a session takes over the screen.
func validateSessionTint(path string, defaults *Defaults) []Warning {
	if defaults == nil {
		return nil
	}

	warnings := make([]Warning, 0)
	if tint := strings.TrimSpace(defaults.SessionTint); tint != "" && !isValidTintColor(tint) {
		warnings = append(warnings, newWarning(WarningInvalidValue, "session_tint", "", path, "Ignoring session_tint %q in %s: expected an #rrggbb color", tint, path))
		defaults.SessionTint = ""
	}

	if len(defaults.ToolOverrides) == 0 {
		return warnings
	}

	toolNames := make([]string, 0, len(defaults.ToolOverrides))
	for name := range defaults.ToolOverrides {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)

	for _, name := range toolNames {
		override := defaults.ToolOverrides[name]
		tint := strings.TrimSpace(override.SessionTint)
		if tint == "" || isValidTintColor(tint) {
			continue
		}
		warnings = append(warnings, newWarning(WarningInvalidValue, "session_tint", name, path, "Ignoring tool_overrides.%s.session_tint %q in %s: expected an #rrggbb color", name, tint, path))
		override.SessionTint = ""
		defaults.ToolOverrides[name] = override
	}

	return warnings
}

// validateUpdateChannel clears an update_channel that names no release
// channel, so the binary falls back to the channel it was published on.
func validateUpdateChannel(path string, defaults *Defaults) []Warning {
	if defaults == nil {
		return nil
	}
	channel := strings.TrimSpace(defaults.UpdateChannel)
	defaults.UpdateChannel = channel
	if channel == "" || slices.Contains(model.UpdateChannels, channel) {
		return nil
	}
	defaults.UpdateChannel = ""
	return []Warning{newWarning(WarningInvalidValue, "update_channel", "", path, "Ignoring update_channel %q in %s: expected one of %s", channel, path, strings.Join(model.UpdateChannels, ", "))}
}

func validateTopLevelDefaults(path string, doc map[string]json.RawMessage, defaults *Defaults) []Warning {
	if defaults == nil {
		return nil
	}
	var warnings []Warning
	if _, ok := doc["host_config_paths"]; ok {
		defaults.HostConfigPaths = nil
		warnings = append(warnings, newWarning(WarningUnsupportedScope, "host_config_paths", "", path, "Ignoring %q in %s: this option is only supported under tool_overrides.<tool>", "host_config_paths", path))
	}
	return warnings
}

func validateToolOverrides(path string, doc map[string]json.RawMessage, defaults *Defaults) []Warning {
	if defaults == nil || len(defaults.ToolOverrides) == 0 {
		return nil
	}

	rawOverrides, ok := doc["tool_overrides"]
	if !ok {
		return nil
	}

	var entries map[string]json.RawMessage
	if err := json.Unmarshal(rawOverrides, &entries); err != nil {
		defaults.ToolOverrides = nil
		return []Warning{newWarning(WarningInvalidToolOverride, "", "", path, "Ignoring tool_overrides in %s: expected object: %v", path, err)}
	}

	valid := make(map[string]Defaults, len(defaults.ToolOverrides))
	toolNames := make([]string, 0, len(entries))
	for toolName := range entries {
		toolNames = append(toolNames, toolName)
	}
	sort.Strings(toolNames)

	warnings := make([]Warning, 0)
	for _, toolName := range toolNames {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(entries[toolName], &entry); err != nil {
			warnings = append(warnings, newWarning(WarningInvalidToolOverride, "", toolName, path, "Ignoring tool_overrides.%s in %s: override must be an object: %v", toolName, path, err))
			continue
		}
		if _, hasTool := entry["tool"]; hasTool {
			warnings = append(warnings, newWarning(WarningInvalidToolOverride, "", toolName, path, "Ignoring tool_overrides.%s in %s: field %q is not allowed in per-tool overrides", toolName, path, "tool"))
			continue
		}
		if _, hasNested := entry["tool_overrides"]; hasNested {
			warnings = append(warnings, newWarning(WarningInvalidToolOverride, "", toolName, path, "Ignoring tool_overrides.%s in %s: field %q is not allowed in per-tool overrides", toolName, path, "tool_overrides"))
			continue
		}
		if parsed, ok := defaults.ToolOverrides[toolName]; ok {
			if _, hasAutoUpdate := entry["auto_update"]; hasAutoUpdate {
				parsed.AutoUpdate = nil
				warnings = append(warnings, newWarning(WarningInvalidToolOverride, "auto_update", toolName, path, "Ignoring tool_overrides.%s.auto_update in %s: binary updates are configured globally", toolName, path))
			}
			if _, hasChannel := entry["update_channel"]; hasChannel {
				parsed.UpdateChannel = ""
				warnings = append(warnings, newWarning(WarningInvalidToolOverride, "update_channel", toolName, path, "Ignoring tool_overrides.%s.update_channel in %s: binary updates are configured globally", toolName, path))
			}
			warnings = append(warnings, validateToolOverrideHostConfigPaths(path, toolName, parsed.HostConfigPaths)...)
			valid[toolName] = parsed
		}
	}

	if len(valid) == 0 {
		defaults.ToolOverrides = nil
	} else {
		defaults.ToolOverrides = valid
	}
	return warnings
}

func validateToolOverrideHostConfigPaths(path string, toolName string, values []string) []Warning {
	if len(values) == 0 {
		return nil
	}

	warnings := make([]Warning, 0)
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
			value = strings.TrimSpace(value[1:])
		}
		if value == "" || strings.EqualFold(value, model.SelectionDefault) {
			continue
		}
		if filepath.IsAbs(value) || strings.HasPrefix(filepathToSlash(value), "/") {
			warnings = append(warnings, newWarning(WarningSuspiciousValue, "host_config_paths", toolName, path, "Suspicious tool_overrides.%s.host_config_paths entry %q in %s: passthrough paths are relative to the tool config dir, not absolute host paths", toolName, raw, path))
		}
	}
	return warnings
}

// stripGlobalOnlyKeys drops the binary update options, which only make sense
// for the whole host, from project config; readDefaults already drops them from
// tool_overrides in either file. Project config is host-owned, out of reach of
// sessions, so it may otherwise set anything global config can.
func stripGlobalOnlyKeys(path string, defaults *Defaults) []Warning {
	if defaults == nil {
		return nil
	}
	var warnings []Warning
	if defaults.AutoUpdate != nil {
		warnings = append(warnings, newWarning(WarningProjectGuardrail, "auto_update", "", path, "Ignoring auto_update=%v in %s: binary updates are configured globally", *defaults.AutoUpdate, path))
		defaults.AutoUpdate = nil
	}
	if defaults.UpdateChannel != "" {
		warnings = append(warnings, newWarning(WarningProjectGuardrail, "update_channel", "", path, "Ignoring update_channel=%q in %s: binary updates are configured globally", defaults.UpdateChannel, path))
		defaults.UpdateChannel = ""
	}
	return warnings
}

// isWithinProjectDir reports whether entry resolves to a path inside projectRoot.
// projectRoot must already be absolute and (if possible) symlink-evaluated.
// projectDir anchors relative entries (so that "./data" resolves relative to
// the project, not the process working directory). Symlink evaluation falls
// back to the absolute path when the target does not exist on disk.
func isWithinProjectDir(entry string, projectRoot string, projectDir string, home string) bool {
	value := strings.TrimSpace(entry)
	if value == "" {
		return false
	}
	if home != "" {
		value = util.ExpandTilde(value, home)
	}

	if !filepath.IsAbs(value) {
		if strings.TrimSpace(projectDir) == "" {
			return false
		}
		value = filepath.Join(projectDir, value)
	}

	within, err := util.RealPathWithin(projectRoot, value)
	return err == nil && within
}

// IsPathWithinProjectDir reports whether entry resolves to a path inside
// projectDir. Relative entries are anchored to projectDir, "~" is expanded
// against the user's home directory, and symlinks are evaluated when the
// target exists. Returns false for empty entries or unresolvable projectDir.
//
// This is the same containment check applied to project-config add_dirs
// entries, exported so other packages (notably internal/devcontainer) can
// clamp externally-supplied paths to the project subtree.
func IsPathWithinProjectDir(entry string, projectDir string) bool {
	if strings.TrimSpace(projectDir) == "" {
		return false
	}
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return false
	}
	home, _ := os.UserHomeDir()
	return isWithinProjectDir(entry, root, root, home)
}

func mergeDefaults(base Defaults, override Defaults) Defaults {
	merged := base
	mergedValue := reflect.ValueOf(&merged).Elem()
	overrideValue := reflect.ValueOf(override)

	for _, def := range OptionDefs() {
		fieldName := strings.TrimSpace(def.DefaultsField)
		if fieldName == "" || fieldName == "ToolOverrides" {
			continue
		}

		targetField := mergedValue.FieldByName(fieldName)
		incomingField := overrideValue.FieldByName(fieldName)
		if !targetField.IsValid() || !incomingField.IsValid() || !targetField.CanSet() {
			continue
		}
		switch def.Kind {
		case OptionKindYolo, OptionKindBool:
			if !incomingField.IsNil() {
				targetField.Set(incomingField)
			}
		case OptionKindString:
			if incomingField.String() != "" {
				targetField.SetString(incomingField.String())
			}
		case OptionKindStringSlice:
			if incomingField.IsNil() {
				continue
			}
			incomingSlice := copyStringSlice(incomingField.Interface().([]string))
			current := targetField.Interface().([]string)
			switch def.Apply {
			case ApplySliceMergeHost:
				setDefaultsStringSlice(targetField, mergeHostConfigSlice(current, incomingSlice))
			default:
				setDefaultsStringSlice(targetField, incomingSlice)
			}
		}
	}

	merged.ToolOverrides = mergeToolOverrides(merged.ToolOverrides, override.ToolOverrides)

	return merged
}

func setDefaultsStringSlice(field reflect.Value, values []string) {
	if values == nil {
		field.Set(reflect.Zero(field.Type()))
		return
	}
	field.Set(reflect.ValueOf(values))
}

func mergeToolOverrides(base map[string]Defaults, override map[string]Defaults) map[string]Defaults {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}

	merged := make(map[string]Defaults, len(base)+len(override))
	for toolName, defaults := range base {
		merged[toolName] = defaults
	}
	for toolName, defaults := range override {
		if existing, ok := merged[toolName]; ok {
			merged[toolName] = mergeDefaults(existing, defaults)
		} else {
			merged[toolName] = defaults
		}
	}
	return merged
}

// hasAdditiveDirective reports whether any value carries a '+' or '-' prefix,
// which switches mergeStringSlice from replace mode into additive mode.
func hasAdditiveDirective(values []string) bool {
	for _, v := range values {
		if strings.HasPrefix(v, "+") || strings.HasPrefix(v, "-") {
			return true
		}
	}
	return false
}

// mergeStringSlice merges two string slices with support for additive syntax.
// If any value in override starts with '+' or '-', additive mode is used:
//   - '+value' adds 'value' to the base set
//   - '-value' removes 'value' from the base set
//
// If no values have prefixes, override replaces base entirely (default behavior).
func mergeStringSlice(base, override []string) []string {
	if !hasAdditiveDirective(override) {
		// Replace mode (original behavior)
		return override
	}

	if base == nil {
		// Preserve additive directives so downstream resolution can apply them
		// against the implicit default set (e.g. features).
		return override
	}

	// Additive mode: start with base, apply modifications
	result := make(map[string]bool)
	for _, v := range base {
		result[v] = true
	}

	for _, v := range override {
		if strings.HasPrefix(v, "+") {
			result[strings.TrimPrefix(v, "+")] = true
		} else if strings.HasPrefix(v, "-") {
			delete(result, strings.TrimPrefix(v, "-"))
		} else {
			// No prefix in additive mode - treat as add
			result[v] = true
		}
	}

	// Convert back to slice
	merged := make([]string, 0, len(result))
	for v := range result {
		merged = append(merged, v)
	}
	// Sort for deterministic output
	sort.Strings(merged)
	return merged
}

// mergeFeatureSlice merges feature defaults with additive syntax.
// When base is nil, additive directives are preserved so downstream resolution
// can apply them against the implicit default-enabled feature set.
func mergeFeatureSlice(base, override []string) []string {
	if !hasAdditiveDirective(override) {
		return override
	}
	if base == nil {
		return copyStringSlice(override)
	}
	return mergeStringSlice(base, override)
}

// mergeHostConfigSlice merges raw host_config_paths directives across config
// scopes without resolving them against tool defaults yet.
//
// Rules:
// - nil override => no change
// - explicit empty slice => explicit empty override
// - additive-only override (+/- entries only) modifies the inherited raw list
// - any bare entry (including "default") replaces the inherited raw list
// - additive removals against an explicit empty base are no-ops
func mergeHostConfigSlice(base, override []string) []string {
	if override == nil {
		return copyStringSlice(base)
	}
	if len(override) == 0 {
		return []string{}
	}

	additiveOnly := true
	for _, raw := range override {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if !strings.HasPrefix(value, "+") && !strings.HasPrefix(value, "-") {
			additiveOnly = false
			break
		}
	}
	if !additiveOnly {
		return copyStringSlice(override)
	}

	if base == nil {
		return copyStringSlice(override)
	}
	if len(base) == 0 {
		resolved := make([]string, 0, len(override))
		for _, raw := range override {
			value := strings.TrimSpace(raw)
			if !strings.HasPrefix(value, "+") {
				continue
			}
			bare := strings.TrimSpace(strings.TrimPrefix(value, "+"))
			if bare != "" {
				resolved = append(resolved, bare)
			}
		}
		if resolved == nil {
			return []string{}
		}
		return resolved
	}

	merged := append(copyStringSlice(base), override...)
	return merged
}
