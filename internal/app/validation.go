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
	"strconv"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

type ValidationContext struct {
	Paths  model.Paths
	Action string
	// WarnUnknownFeatures keeps unknown feature names from failing
	// validation, for previews that report them with featureSelectionWarnings.
	WarnUnknownFeatures bool
}

func ValidateOptions(opts model.Options, sources model.OptionSources, ctx ValidationContext) (model.Options, model.OptionSources, []string, error) {
	warnings := []string{}
	opts, normalizationWarnings := normalizeOptions(opts)
	warnings = append(warnings, normalizationWarnings...)
	if err := validateBuildControlConflicts(opts); err != nil {
		return opts, sources, warnings, err
	}
	var err error
	opts, sources, err = validateAuthOptions(opts, sources)
	if err != nil {
		return opts, sources, warnings, err
	}
	if opts.Backend == "" {
		// Standalone validation accepts zero-value options (including test
		// fixtures); Run rejects an explicitly empty --backend before this call.
		opts.Backend = backend.NameDocker
	}
	if err := validateBackendName(opts.Backend); err != nil {
		return opts, sources, warnings, err
	}
	if opts.Devcontainer && opts.Backend == backend.NameQEMU {
		return opts, sources, warnings, fmt.Errorf("devcontainer mode requires --backend %s or %s", backend.NameDocker, backend.NamePodman)
	}
	normalizedBuildOptions, featureWarnings, normErr := normalizeConfiguredBuildOptions(ctx.Paths, opts, sources.Features)
	warnings = append(warnings, featureWarnings...)
	var unknownFeatures *unknownFeaturesError
	if errors.As(normErr, &unknownFeatures) && ctx.WarnUnknownFeatures {
		// The caller reports unknown names with featureSelectionWarnings.
		normErr = nil
		normalizedBuildOptions = opts.BuildOptions
	}
	if errors.As(normErr, &unknownFeatures) {
		// A stale config selection must not block a session that discards it.
		ignoredBy := ""
		switch {
		case opts.Slim:
			ignoredBy = "--slim"
		case opts.Backend == backend.NameQEMU && isRunAction(ctx.Action):
			ignoredBy = "--backend qemu"
		}
		if ignoredBy != "" {
			var fromCLI unknownFeatureGroups
			for _, group := range unknownFeatures.groups {
				if group.cli {
					fromCLI = append(fromCLI, group)
					continue
				}
				warnings = append(warnings, fmt.Sprintf("%s; ignored because %s discards the feature selection", group.message(), ignoredBy))
			}
			normErr = nil
			normalizedBuildOptions = opts.BuildOptions
			if len(fromCLI) > 0 {
				normErr = &unknownFeaturesError{groups: fromCLI}
			}
		}
	}
	if normErr != nil {
		return opts, sources, warnings, normErr
	}
	opts.BuildOptions = normalizedBuildOptions
	if opts.Backend == backend.NameQEMU && isRunAction(ctx.Action) {
		var qemuWarnings []string
		opts, qemuWarnings, err = coerceQEMURunOptions(opts, sources, ctx.Paths)
		if err != nil {
			return opts, sources, warnings, err
		}
		warnings = append(warnings, qemuWarnings...)
	}
	// Validate --auth-name here (not only in the CLI apply path) so a value set
	// via config.json is checked too. Under project scope the flag has no
	// target shared store, so warn and drop it.
	if strings.TrimSpace(opts.AuthName) != "" {
		normalized, nameErr := config.ValidateAuthName(opts.AuthName)
		if nameErr != nil {
			return opts, sources, warnings, nameErr
		}
		opts.AuthName = normalized
		if opts.AuthScope == model.AuthScopeProject {
			warnings = append(warnings, "--auth-name is ignored under --auth-scope=project; auth lives in the per-project config store")
			opts.AuthName = ""
		}
	}
	if opts.Devcontainer && opts.BaseImage != "" {
		return opts, sources, warnings, fmt.Errorf("devcontainer mode is mutually exclusive with --base-image")
	}
	switch opts.SkillsValidation {
	case model.SkillsValidationStrict, model.SkillsValidationAgent:
	default:
		return opts, sources, warnings, fmt.Errorf("--skills-validation must be strict or agent")
	}
	switch opts.HostConfig {
	case "", model.HostConfigNone, model.HostConfigPassthrough:
	default:
		return opts, sources, warnings, fmt.Errorf("--host-config must be %s or %s", model.HostConfigNone, model.HostConfigPassthrough)
	}
	switch opts.NetworkLog {
	case model.NetworkLogCoarse, model.NetworkLogRequests:
	default:
		return opts, sources, warnings, fmt.Errorf("--network-log must be %s or %s", model.NetworkLogCoarse, model.NetworkLogRequests)
	}
	switch opts.ProjectMount {
	case model.ProjectMountWritable, model.ProjectMountReadonly:
	default:
		return opts, sources, warnings, fmt.Errorf("--project-mount must be %s or %s", model.ProjectMountWritable, model.ProjectMountReadonly)
	}
	switch opts.WorktreeMetadata {
	case model.WorktreeMetadataFollow, model.WorktreeMetadataReadonly, model.WorktreeMetadataNone:
	default:
		return opts, sources, warnings, fmt.Errorf("--worktree-metadata must be %s, %s, or %s", model.WorktreeMetadataFollow, model.WorktreeMetadataReadonly, model.WorktreeMetadataNone)
	}
	if opts.UseRemoteUser && !opts.Devcontainer {
		warnings = append(warnings, "--use-remote-user requires devcontainer mode; ignoring")
		opts.UseRemoteUser = false
	}
	if opts.RuntimeUIDRemap && opts.UseRemoteUser {
		return opts, sources, warnings, fmt.Errorf("--runtime-uid-remap is incompatible with --use-remote-user")
	}
	if err := validateBuildIdentityOptions(opts.BuildOptions); err != nil {
		return opts, sources, warnings, err
	}
	progress := normalizeBuildProgress(opts.Progress)
	if progress == "" {
		progress = model.BuildProgressCompact
	}
	switch progress {
	case model.BuildProgressQuiet, model.BuildProgressCompact, model.BuildProgressVerbose:
		opts.Progress = progress
	default:
		return opts, sources, warnings, fmt.Errorf("--progress must be %s, %s, or %s", model.BuildProgressQuiet, model.BuildProgressCompact, model.BuildProgressVerbose)
	}
	if strings.TrimSpace(opts.ImageName) == "" {
		return opts, sources, warnings, fmt.Errorf("--image-name requires a non-empty value")
	}
	if err := config.CheckListProblems(opts); err != nil {
		return opts, sources, warnings, err
	}

	return opts, sources, warnings, nil
}

// validateAsRun validates opts the way a run would. The auto backend is left
// unresolved by read-only commands, so the docker default is validated.
func validateAsRun(paths model.Paths, opts model.Options, sources model.OptionSources) (model.Options, []string, error) {
	return validateRunOptions(opts, sources, ValidationContext{Paths: paths, Action: "run"})
}

// previewAsRun is validateAsRun for commands that only show what a run would
// get, such as network print: unknown feature names do not fail, so the caller
// can report them with featureSelectionWarnings and still show the rest.
func previewAsRun(paths model.Paths, opts model.Options, sources model.OptionSources) (model.Options, []string, error) {
	return validateRunOptions(opts, sources, ValidationContext{Paths: paths, Action: "run", WarnUnknownFeatures: true})
}

func validateRunOptions(opts model.Options, sources model.OptionSources, ctx ValidationContext) (model.Options, []string, error) {
	if strings.TrimSpace(opts.Backend) == backend.NameAuto {
		opts.Backend = backend.NameDocker
	}
	validated, _, warnings, err := ValidateOptions(opts, sources, ctx)
	return validated, warnings, err
}

// optionsAsRun returns the options a run would use: the validated options with
// the configured backend when validation passes, otherwise the normalized ones.
func optionsAsRun(paths model.Paths, opts model.Options, sources model.OptionSources) (model.Options, []string, error) {
	validated, warnings, err := validateAsRun(paths, opts, sources)
	if err != nil {
		normalized, _ := normalizeOptions(opts)
		return normalized, warnings, err
	}
	validated.Backend = opts.Backend
	return validated, warnings, nil
}

// coerceQEMURunOptions aligns run options with what the experimental qemu
// backend can actually do, so the user doesn't have to pass flags whose only
// supported value is dictated by the backend. It has no network gateway
// (Capabilities.RestrictedNetwork is false) and builds tool-only bundles, so
// --allow-all-network and --slim are implied rather than required. Requests the
// backend genuinely cannot satisfy — an allowlist or real features — stay
// errors. A notice is emitted each run so the absent isolation is never silent.
func coerceQEMURunOptions(opts model.Options, sources model.OptionSources, paths model.Paths) (model.Options, []string, error) {
	// An allowlist means "restrict egress", which the qemu backend cannot do;
	// silently allowing everything would be a security downgrade, so error even
	// when the allowlist came from config.
	if len(opts.AllowDomains) > 0 {
		hint := "remove --allow-domain"
		if sources.AllowDomains != model.SourceCLI {
			hint = "remove allow_domains from config"
		}
		return opts, nil, fmt.Errorf("qemu backend has no network gateway and cannot restrict network access; %s", hint)
	}
	// Detached sessions are rejected by the backend only in Start(), which runs
	// after the (potentially minutes-long) bundle build; fail fast here instead.
	// --background is CLI-only, so there is no config-sourced value to soft-drop.
	if opts.Background {
		return opts, nil, fmt.Errorf("qemu backend runs foreground sessions only and cannot start detached; remove --background")
	}
	// Features requested on THIS command line cannot be honored by a tool-only
	// bundle, so fail loudly. Features coming from config are dropped with a
	// notice below, so `--backend qemu` still runs.
	if sources.Features == model.SourceCLI && len(opts.Features) > 0 {
		return opts, nil, fmt.Errorf("qemu backend builds tool-only bundles and cannot install features (%s); pass --features none to run without them", strings.Join(opts.Features, ", "))
	}

	// What a docker run would install for these options (config selections plus
	// defaults); a tool-only bundle drops all of it. Reported below so the drop
	// is never silent.
	dropped := qemuDroppedFeatures(opts.Features, paths)

	opts.AllowAllNetwork = true
	opts.Slim = true
	opts.Features = []string{}

	warnings := []string{"qemu backend: network isolation is unavailable (all outbound network allowed)"}
	if len(dropped) > 0 {
		warnings = append(warnings, fmt.Sprintf("qemu backend: tool-only bundle, not installing features: %s", strings.Join(dropped, ", ")))
	} else {
		warnings = append(warnings, "qemu backend: tool-only bundle (no features)")
	}
	return opts, warnings, nil
}

// qemuDroppedFeatures returns the feature names a tool-only qemu bundle skips:
// the requested selection, or the default-enabled features when none is
// requested. --slim and devcontainer mode are not considered. Best-effort: if
// features can't be enumerated it returns nil and the caller logs a generic
// notice rather than failing the run.
func qemuDroppedFeatures(requested []string, paths model.Paths) []string {
	selection, err := resolveFeatureSelection(paths, model.BuildOptions{Features: requested})
	if err != nil {
		return nil
	}
	return selection.enabledNames()
}

func validateBuildIdentityOptions(opts model.BuildOptions) error {
	if err := validateNonNegativeID("--build-uid", opts.BuildUID); err != nil {
		return err
	}
	if err := validateNonNegativeID("--build-gid", opts.BuildGID); err != nil {
		return err
	}
	return nil
}

func validateNonNegativeID(flag string, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fmt.Errorf("%s requires a non-negative numeric value", flag)
	}
	return nil
}

func validateAuthOptions(opts model.Options, sources model.OptionSources) (model.Options, model.OptionSources, error) {
	if opts.Ephemeral && opts.ResetAuth {
		ephemeralWins, err := resolveFlagConflict("--ephemeral", "--reset-auth", sources.Ephemeral, sources.ResetAuth)
		if err != nil {
			return opts, sources, err
		}
		if ephemeralWins {
			opts.ResetAuth = false
			sources.ResetAuth = model.SourceUnset
		} else {
			opts.Ephemeral = false
			opts.Persist = true
			sources.Ephemeral = model.SourceUnset
		}
	}
	if opts.NoAPIKey && opts.PassAPIKey {
		noAPIKeyWins, err := resolveFlagConflict("--no-api-key", "--pass-api-key", sources.NoAPIKey, sources.PassAPIKey)
		if err != nil {
			return opts, sources, err
		}
		if noAPIKeyWins {
			opts.PassAPIKey = false
			sources.PassAPIKey = model.SourceUnset
		} else {
			opts.NoAPIKey = false
			sources.NoAPIKey = model.SourceUnset
		}
	}
	return opts, sources, nil
}

func resolveFlagConflict(leftFlag string, rightFlag string, leftSource model.OptionSource, rightSource model.OptionSource) (bool, error) {
	if leftSource == rightSource {
		return false, fmt.Errorf("%s is incompatible with %s", leftFlag, rightFlag)
	}
	return leftSource > rightSource, nil
}

func validateBuildControlConflicts(opts model.Options) error {
	if !opts.NoRebuild {
		return nil
	}
	if opts.ForceRebuild {
		return fmt.Errorf("%s is incompatible with --rebuild", rebuildSuppressionCause(opts))
	}
	return nil
}

func ValidateRunOptions(opts model.Options, sources model.OptionSources, ctx ValidationContext, project model.Project) (model.Options, model.OptionSources, []string, *buildConfig, error) {
	opts, sources, warnings, err := ValidateOptions(opts, sources, ctx)
	if err != nil {
		return opts, sources, warnings, nil, err
	}
	if !isRunAction(ctx.Action) {
		return opts, sources, warnings, nil, nil
	}
	buildConfig, err := resolveBuildConfig(opts.BuildOptions, opts.Tool, project, ctx.Paths.AppRoot)
	if err != nil {
		return opts, sources, warnings, nil, err
	}
	if err := validateRuntimeUIDRemapDevcontainer(opts, buildConfig); err != nil {
		return opts, sources, warnings, nil, err
	}
	opts.ImageName = buildConfig.ImageName
	return opts, sources, warnings, &buildConfig, nil
}

func validateRuntimeUIDRemapDevcontainer(opts model.Options, buildCfg buildConfig) error {
	if !opts.RuntimeUIDRemap || buildCfg.Devcontainer == nil {
		return nil
	}
	if strings.TrimSpace(buildCfg.Devcontainer.RuntimeConfig.RemoteUser) == "" {
		return nil
	}
	if opts.Shell {
		return fmt.Errorf("--runtime-uid-remap is incompatible with devcontainer remoteUser in shell mode")
	}
	return nil
}

// normalizeOptions centralizes option normalization before validation or display.
func normalizeOptions(opts model.Options) (model.Options, []string) {
	warnings := []string{}
	opts.Persist = !opts.Ephemeral
	opts.SkillsValidation = strings.ToLower(strings.TrimSpace(opts.SkillsValidation))
	if opts.SkillsValidation == "" {
		opts.SkillsValidation = model.SkillsValidationStrict
	}
	opts.NetworkLog = strings.TrimSpace(strings.ToLower(opts.NetworkLog))
	if opts.NetworkLog == "" {
		opts.NetworkLog = model.NetworkLogCoarse
	}
	opts.ProjectMount = strings.TrimSpace(strings.ToLower(opts.ProjectMount))
	if opts.ProjectMount == "" {
		opts.ProjectMount = model.ProjectMountWritable
	}
	opts.WorktreeMetadata = strings.TrimSpace(strings.ToLower(opts.WorktreeMetadata))
	if opts.WorktreeMetadata == "" {
		opts.WorktreeMetadata = model.WorktreeMetadataFollow
	}
	opts.BuildUID = strings.TrimSpace(opts.BuildUID)
	opts.BuildGID = strings.TrimSpace(opts.BuildGID)
	opts.BuildxCacheDir = strings.TrimSpace(opts.BuildxCacheDir)
	return opts, warnings
}

func normalizeBuildProgress(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}
