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
	"sort"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

type Defaults struct {
	Tool             string              `json:"tool"`
	ToolOverrides    map[string]Defaults `json:"tool_overrides"`
	Backend          string              `json:"backend"`
	HostConfig       string              `json:"host_config"`
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
	ImageInbox       *bool               `json:"image_inbox"`
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

func LoadDefaults(projectDir string) (global Defaults, project Defaults, warnings []string, err error) {
	globalPath, err := GlobalConfigPath()
	if err != nil {
		return Defaults{}, Defaults{}, nil, err
	}

	globalDefaults, globalWarns, err := readDefaults(globalPath)
	if err != nil {
		return Defaults{}, Defaults{}, nil, err
	}
	warnings = append(warnings, globalWarns...)

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
	warnings = append(warnings, projectWarns...)
	warnings = append(warnings, applyProjectOverrideGuardrailsAgainst(projectPath, proj.RealDir, globalDefaults, &projectDefaults)...)

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

func readDefaults(path string) (Defaults, []string, error) {
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
		return Defaults{}, []string{fmt.Sprintf("Config path is a directory: %s", path)}, nil
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
		return Defaults{}, []string{fmt.Sprintf("Invalid config JSON at %s: %v", path, err)}, nil
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
func validatePassEnvNames(path string, defaults *Defaults) []string {
	if defaults == nil {
		return nil
	}

	warnings := make([]string, 0)
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

func filterPassEnv(path string, toolName string, values []string, warnings []string) ([]string, []string) {
	if len(values) == 0 {
		return values, warnings
	}
	kept := make([]string, 0, len(values))
	for _, raw := range values {
		if isValidEnvKey(raw) {
			kept = append(kept, raw)
			continue
		}
		if toolName == "" {
			warnings = append(warnings, fmt.Sprintf("Ignoring pass_env entry %q in %s: not a valid environment variable name", raw, path))
		} else {
			warnings = append(warnings, fmt.Sprintf("Ignoring tool_overrides.%s.pass_env entry %q in %s: not a valid environment variable name", toolName, raw, path))
		}
	}
	if len(kept) == 0 {
		return nil, warnings
	}
	return kept, warnings
}

func validateTopLevelDefaults(path string, doc map[string]json.RawMessage, defaults *Defaults) []string {
	if defaults == nil {
		return nil
	}
	var warnings []string
	if _, ok := doc["host_config_paths"]; ok {
		defaults.HostConfigPaths = nil
		warnings = append(warnings, fmt.Sprintf("Ignoring %q in %s: this option is only supported under tool_overrides.<tool>", "host_config_paths", path))
	}
	return warnings
}

func validateToolOverrides(path string, doc map[string]json.RawMessage, defaults *Defaults) []string {
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
		return []string{fmt.Sprintf("Ignoring tool_overrides in %s: expected object: %v", path, err)}
	}

	valid := make(map[string]Defaults, len(defaults.ToolOverrides))
	toolNames := make([]string, 0, len(entries))
	for toolName := range entries {
		toolNames = append(toolNames, toolName)
	}
	sort.Strings(toolNames)

	warnings := make([]string, 0)
	for _, toolName := range toolNames {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(entries[toolName], &entry); err != nil {
			warnings = append(warnings, fmt.Sprintf("Ignoring tool_overrides.%s in %s: override must be an object: %v", toolName, path, err))
			continue
		}
		if _, hasTool := entry["tool"]; hasTool {
			warnings = append(warnings, fmt.Sprintf("Ignoring tool_overrides.%s in %s: field %q is not allowed in per-tool overrides", toolName, path, "tool"))
			continue
		}
		if _, hasNested := entry["tool_overrides"]; hasNested {
			warnings = append(warnings, fmt.Sprintf("Ignoring tool_overrides.%s in %s: field %q is not allowed in per-tool overrides", toolName, path, "tool_overrides"))
			continue
		}
		if parsed, ok := defaults.ToolOverrides[toolName]; ok {
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

func validateToolOverrideHostConfigPaths(path string, toolName string, values []string) []string {
	if len(values) == 0 {
		return nil
	}

	warnings := make([]string, 0)
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
			warnings = append(warnings, fmt.Sprintf("Suspicious tool_overrides.%s.host_config_paths entry %q in %s: passthrough paths are relative to the tool config dir, not absolute host paths", toolName, raw, path))
		}
	}
	return warnings
}

func applyProjectOverrideGuardrails(path string, projectDir string, defaults *Defaults) []string {
	return applyProjectOverrideGuardrailsAgainst(path, projectDir, Defaults{}, defaults)
}

func applyProjectOverrideGuardrailsAgainst(path string, projectDir string, global Defaults, defaults *Defaults) []string {
	if defaults == nil {
		return nil
	}

	projectRoot := resolveProjectRoot(projectDir)
	warnings := applyProjectDefaultsGuardrails(path, "", projectRoot, projectDir, global.WorktreeMetadata, defaults, nil)

	if defaults.Tool != "" {
		warnings = append(warnings, fmt.Sprintf("Ignoring tool=%q in %s: project configs cannot change the active tool; pass --tool or set it in global config instead", defaults.Tool, path))
		defaults.Tool = ""
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
		prefix := "tool_overrides." + name
		inheritedWorktreeMetadata := global.WorktreeMetadata
		if defaults.WorktreeMetadata != "" {
			inheritedWorktreeMetadata = defaults.WorktreeMetadata
		}
		if globalOverride, ok := global.ToolOverrides[name]; ok && globalOverride.WorktreeMetadata != "" {
			inheritedWorktreeMetadata = globalOverride.WorktreeMetadata
		}
		warnings = applyProjectDefaultsGuardrails(path, prefix, projectRoot, projectDir, inheritedWorktreeMetadata, &override, warnings)
		defaults.ToolOverrides[name] = override
	}

	return warnings
}

func applyProjectDefaultsGuardrails(path string, prefix string, projectRoot string, projectDir string, inheritedWorktreeMetadata string, defaults *Defaults, warnings []string) []string {
	if defaults.AllowAllNetwork != nil && *defaults.AllowAllNetwork {
		field := projectGuardrailField(prefix, "allow_all_network")
		valueLabel := field + "=true"
		if prefix == "" {
			valueLabel = fmt.Sprintf("%q=true", field)
		}
		defaults.AllowAllNetwork = nil
		warnings = append(warnings, fmt.Sprintf("Ignoring %s in %s: project configs cannot enable unrestricted network; set it in global config or CLI instead", valueLabel, path))
	}

	if len(defaults.AllowDomains) > 0 {
		field := projectGuardrailField(prefix, "allow_domains")
		warnings = append(warnings, fmt.Sprintf("Ignoring %s=%v in %s: project configs cannot widen the network allowlist; set it in global config or pass --allow-domain instead", field, defaults.AllowDomains, path))
		defaults.AllowDomains = nil
	}

	defaults.AddDirs, warnings = filterProjectAddDirs(defaults.AddDirs, projectGuardrailField(prefix, "add_dirs"), projectRoot, projectDir, path, warnings)
	defaults.AddReadonlyDirs, warnings = filterProjectAddDirs(defaults.AddReadonlyDirs, projectGuardrailField(prefix, "add_readonly_dirs"), projectRoot, projectDir, path, warnings)

	if len(defaults.PassEnv) > 0 {
		field := projectGuardrailField(prefix, "pass_env")
		defaults.PassEnv = nil
		warnings = append(warnings, fmt.Sprintf("Ignoring %s in %s: project configs cannot forward host environment variables; use global config or CLI instead", field, path))
	}

	if defaults.BaseImage != "" {
		field := projectGuardrailField(prefix, "base_image")
		warnings = append(warnings, fmt.Sprintf("Ignoring %s=%q in %s: project configs cannot override the base image; set it in global config or CLI instead", field, defaults.BaseImage, path))
		defaults.BaseImage = ""
	}

	if len(defaults.BridgePorts) > 0 {
		field := projectGuardrailField(prefix, "bridge_ports")
		warnings = append(warnings, fmt.Sprintf("Ignoring %s=%v in %s: project configs cannot bridge container ports to the host; set it in global config or CLI instead", field, defaults.BridgePorts, path))
		defaults.BridgePorts = nil
	}

	if defaults.Yolo != nil {
		field := projectGuardrailField(prefix, "yolo")
		warnings = append(warnings, fmt.Sprintf("Ignoring %s=%v in %s: project configs cannot toggle yolo mode; pass --yolo/--no-yolo or set it in global config instead", field, *defaults.Yolo, path))
		defaults.Yolo = nil
	}

	if defaults.HostConfig != "" {
		field := projectGuardrailField(prefix, "host_config")
		warnings = append(warnings, fmt.Sprintf("Ignoring %s=%q in %s: project configs cannot widen host config passthrough; set it in global config or CLI instead", field, defaults.HostConfig, path))
		defaults.HostConfig = ""
	}

	if len(defaults.HostConfigPaths) > 0 {
		field := projectGuardrailField(prefix, "host_config_paths")
		warnings = append(warnings, fmt.Sprintf("Ignoring %s=%v in %s: project configs cannot extend host config passthrough; set it in global config or CLI instead", field, defaults.HostConfigPaths, path))
		defaults.HostConfigPaths = nil
	}

	if strings.EqualFold(strings.TrimSpace(defaults.ProjectMount), model.ProjectMountWritable) {
		field := projectGuardrailField(prefix, "project_mount")
		warnings = append(warnings, fmt.Sprintf("Ignoring %s=%q in %s: project configs cannot make the project mount writable; set it in global config or CLI instead", field, defaults.ProjectMount, path))
		defaults.ProjectMount = ""
	}

	worktreeMetadata := strings.ToLower(strings.TrimSpace(defaults.WorktreeMetadata))
	if worktreeMetadata != "" && (worktreeMetadata == model.WorktreeMetadataFollow || worktreeMetadataStrength(worktreeMetadata) < worktreeMetadataStrength(inheritedWorktreeMetadata)) {
		field := projectGuardrailField(prefix, "worktree_metadata")
		warnings = append(warnings, fmt.Sprintf("Ignoring %s=%q in %s: project configs cannot relax inherited worktree_metadata=%q; set it in global config or CLI instead", field, defaults.WorktreeMetadata, path, model.WorktreeMetadataMode(inheritedWorktreeMetadata)))
		defaults.WorktreeMetadata = ""
	}

	return warnings
}

func worktreeMetadataStrength(value string) int {
	switch model.WorktreeMetadataMode(value) {
	case model.WorktreeMetadataNone:
		return 2
	case model.WorktreeMetadataReadonly:
		return 1
	default:
		return 0
	}
}

func projectGuardrailField(prefix string, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// resolveProjectRoot returns the absolute, symlink-evaluated project directory
// used to anchor project-config containment checks. Now that project overrides
// are hash-keyed under the config root, the project root is passed in
// explicitly rather than derived from the config path shape. Returns an empty
// string if projectDir is empty or cannot be resolved.
func resolveProjectRoot(projectDir string) string {
	if strings.TrimSpace(projectDir) == "" {
		return ""
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// filterProjectAddDirs drops entries that resolve outside the project root and
// appends a warning per dropped entry. fieldLabel is the JSON field name used
// in the warning (e.g. "add_dirs" or "tool_overrides.<tool>.add_dirs").
// projectDir anchors relative entries; configPath is used only for warnings.
func filterProjectAddDirs(entries []string, fieldLabel string, projectRoot string, projectDir string, configPath string, warnings []string) ([]string, []string) {
	if len(entries) == 0 {
		return entries, warnings
	}

	// If we cannot determine the project root, drop everything to fail closed:
	// a project config that we cannot anchor must not mount arbitrary host
	// paths into the container.
	if projectRoot == "" {
		for _, raw := range entries {
			warnings = append(warnings, projectAddDirsWarning(raw, fieldLabel, configPath))
		}
		return nil, warnings
	}

	home, _ := os.UserHomeDir()
	kept := make([]string, 0, len(entries))
	for _, raw := range entries {
		if isWithinProjectDir(raw, projectRoot, projectDir, home) {
			kept = append(kept, raw)
			continue
		}
		warnings = append(warnings, projectAddDirsWarning(raw, fieldLabel, configPath))
	}
	if len(kept) == 0 {
		return nil, warnings
	}
	return kept, warnings
}

func projectAddDirsWarning(entry string, fieldLabel string, configPath string) string {
	return fmt.Sprintf("Ignoring %s entry %q in %s: project configs can only mount subdirectories of the project; use global config or CLI for other paths", fieldLabel, entry, configPath)
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
