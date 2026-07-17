// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/mounts"
	"enclave/internal/runtime"
	"enclave/internal/tools"
)

func runExecutionCommand(input *CommandInput) int {
	opts := input.Options

	project, err := input.Ctx.Project()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	var validationWarnings []string
	var buildCfg *buildConfig
	opts, sources, validationWarnings, buildCfg, err := ValidateRunOptions(opts, input.Sources, ValidationContext{
		Paths:  input.Ctx.Paths,
		Action: input.Action,
	}, project)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	for _, warning := range validationWarnings {
		logx.Warnf(warning)
	}
	opts.Sources = sources

	profile, err := loadProfileOrReport(input.Ctx.Paths, opts.Tool)
	if err != nil {
		return 1
	}
	opts.HostConfigPaths = config.ResolveHostConfigPaths(profile, opts.HostConfigPaths)

	sessionArgs, sessionMode, fallback, err := resolveSessionActionArgs(input.Action, profile)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	if fallback {
		logx.Warnf("%s does not define %s behavior; falling back to %s behavior", profile.Name, input.Action, sessionMode)
	}
	if len(sessionArgs) > 0 {
		opts.CmdArgs = append(sessionArgs, opts.CmdArgs...)
	}

	yoloEnabled := resolveYoloEnabled(profile, opts)

	if executionRequiresDocker(input.Action, opts) {
		if code := requireDocker(); code != 0 {
			return code
		}
	}

	host, err := input.Ctx.Host()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	validatedDirs, err := mounts.ValidateExtraDirs(opts.AddDirs, project.RealDir, host.Home)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	validatedReadonlyDirs, err := mounts.ValidateExtraDirsWithExisting(opts.AddReadonlyDirs, validatedDirs, project.RealDir, host.Home)
	if err != nil {
		logx.Errorf(err.Error())
		return 1
	}

	resolvedBuildConfig, code := ensureRuntimeImage(input, opts, buildCfg, host, profile)
	if code != 0 {
		return code
	}
	if isRunAction(input.Action) {
		opts.ImageName = resolvedBuildConfig.ImageName
	}

	var devcontainerConfig *model.DevcontainerConfig
	if resolvedBuildConfig.Devcontainer != nil {
		devcontainerConfig = &resolvedBuildConfig.Devcontainer.RuntimeConfig
	}
	enabledFeatures := resolveEnabledFeatures(input.Ctx.Paths, opts.BuildOptions)

	handler := tools.Resolve(profile.Name)
	runner := runtime.New(model.RuntimeConfig{
		Paths:                 input.Ctx.Paths,
		Host:                  host,
		Project:               project,
		Profile:               profile,
		Run:                   opts.RunOptions,
		Auth:                  opts.AuthOptions,
		Build:                 opts.BuildOptions,
		RunSources:            opts.Sources.RunSources(),
		Handler:               handler,
		Devcontainer:          devcontainerConfig,
		ValidatedDirs:         validatedDirs,
		ValidatedReadonlyDirs: validatedReadonlyDirs,
		YoloEnabled:           yoloEnabled,
		Features:              enabledFeatures,
		UserCommandMount:      input.UserCommandMount,
	})
	dockerOpts := dockerBackendOptions(host, input.Ctx.Paths, opts.BuildOptions, opts.RunOptions)
	dockerOpts.ProjectDir = project.Dir
	if devcontainerConfig != nil {
		dockerOpts.DevcontainerRunArgs = append([]string(nil), devcontainerConfig.RunArgs...)
	}
	be, err := selectBackend(opts, dockerOpts)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	runner.SetBackend(be)
	if err := checkToolInstalled(opts.ImageName, opts.Tool, opts.Shell, opts.Admin); err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	return dispatchRunner(input, runner)
}

func executionRequiresDocker(action string, opts model.Options) bool {
	if strings.TrimSpace(opts.Backend) != backend.NameQEMU {
		return true
	}
	if isRunAction(action) {
		// Docker is only the bundle build helper: both --no-rebuild and
		// --image-name independently guarantee ensureQEMUBundle never builds.
		return !opts.NoRebuild && !opts.ImageNameSet
	}
	return false
}

func dispatchRunner(input *CommandInput, runner *runtime.Runtime) int {
	if input.Options.Background {
		containerName, err := runner.ExecuteBackground()
		if err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		fmt.Println(containerName)
		return 0
	}
	var err error
	if input.Action == "exec" {
		err = runner.Exec()
	} else {
		err = runner.Execute()
	}
	if err != nil {
		var exitErr *backend.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.Code
		}
		logx.Errorf("%v", err)
		return 1
	}
	return 0
}

func resolveEnabledFeatures(paths model.Paths, build model.BuildOptions) []model.Extension {
	allFeatures, err := config.ListFeatures(paths)
	if err != nil {
		logx.Debugf("Failed to list features: %v", err)
		return nil
	}
	if len(allFeatures) == 0 {
		return nil
	}
	if build.Slim {
		return nil
	}
	if build.Devcontainer && build.Features == nil {
		return nil
	}
	// If explicit features list is provided, filter to only those
	if build.Features != nil {
		selected := resolveConfiguredFeatures(build.Features, allFeatures)
		requested := map[string]bool{}
		for _, f := range selected {
			requested[f] = true
		}
		var result []model.Extension
		for _, feat := range allFeatures {
			if requested[feat.Name] {
				result = append(result, feat)
			}
		}
		return result
	}
	// Otherwise, return all default-enabled features
	var result []model.Extension
	for _, feat := range allFeatures {
		if feat.DefaultEnabled {
			result = append(result, feat)
		}
	}
	return result
}

func ensureRuntimeImage(input *CommandInput, opts model.Options, buildCfg *buildConfig, host model.Host, profile model.Profile) (buildConfig, int) {
	if !isRunAction(input.Action) {
		return buildConfig{}, 0
	}
	if buildCfg == nil {
		logx.Errorf("build config missing for run action")
		return buildConfig{}, 1
	}
	resolved := *buildCfg
	resolved.HashSuffix = appendEffectiveBuildIdentityHashSuffix(resolved.HashSuffix, host, opts.BuildOptions)
	if isQEMUBackend(opts) {
		return ensureQEMUBundle(input, opts, resolved, host, profile)
	}
	logx.Infof("Using image: %s", opts.ImageName)
	if opts.ForceRebuild {
		logx.Infof("Forcing image rebuild.")
	}
	if opts.NoRebuild {
		logx.Warnf("Skipping runtime image build due to --no-rebuild.")
		if err := ensureExistingRuntimeImage(opts.ImageName); err != nil {
			logx.Errorf("%v", err)
			return buildConfig{}, 1
		}
		return resolved, 0
	}
	// Normal runs never force an agent update; the `update` command is the
	// explicit path. Automatic interval-based refresh still applies below.
	buildPlan, err := resolveRuntimeImageBuildPlan(input.Ctx.Paths, resolved, opts.BuildOptions, opts.Tool, host.Home, false, time.Now().UTC())
	if err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	if opts.ForceRebuild || buildPlan.NeedsRebuild() {
		if code := buildOrReuseRuntimeImage(input, opts, host, resolved, buildPlan); code != 0 {
			return buildConfig{}, code
		}
	}
	return resolved, 0
}

func buildOrReuseRuntimeImage(input *CommandInput, opts model.Options, host model.Host, resolved buildConfig, buildPlan runtimeImageBuildPlan) int {
	reused := false
	if !opts.ForceRebuild && buildPlan.StructuralRebuild && !buildPlan.AgentUpdates.NeedsRebuild {
		ok, reuseErr := reuseRuntimeImageByContentHash(context.Background(), resolved.ImageName, buildPlan.CombinedHash)
		if reuseErr != nil {
			logx.Debugf("content-cache lookup failed: %v", reuseErr)
		}
		reused = ok
	}
	if reused {
		return 0
	}
	if !opts.ForceRebuild {
		if buildPlan.StructuralRebuild {
			logx.Infof("Build inputs changed, rebuilding automatically.")
		} else {
			logx.Infof("Agent update interval elapsed, rebuilding automatically.")
		}
	}
	if err := buildImage(context.Background(), input.Ctx.Paths, host, buildPlan.CombinedHash, resolved, opts.BuildOptions, opts.Tool, buildPlan.AgentUpdates); err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	return 0
}

func resolveYoloEnabled(profile model.Profile, opts model.Options) bool {
	yoloEnabled := profile.YoloEnabledValue()
	profileHasYoloFlag := profile.YoloFlag != ""
	if opts.YoloOverride != nil {
		if !*opts.YoloOverride {
			warnDisabledYoloIgnored("--no-yolo ignored", profile.Name, profileHasYoloFlag, yoloEnabled)
		}
		return *opts.YoloOverride
	}
	if profile.YoloEnabled == nil && opts.ConfigDefaultYolo != nil {
		if !*opts.ConfigDefaultYolo {
			warnDisabledYoloIgnored("Config yolo=false ignored", profile.Name, profileHasYoloFlag, yoloEnabled)
		}
		return *opts.ConfigDefaultYolo
	}
	return yoloEnabled
}

func warnDisabledYoloIgnored(prefix string, profileName string, hasYoloFlag bool, yoloEnabled bool) {
	if !hasYoloFlag {
		logx.Warnf("%s: %s does not have a YOLO flag", prefix, profileName)
	} else if !yoloEnabled {
		logx.Warnf("%s: YOLO is already disabled for %s", prefix, profileName)
	}
}

func resolveSessionActionArgs(action string, profile model.Profile) ([]string, string, bool, error) {
	if action != actionContinue && action != "resume" {
		return nil, "", false, nil
	}

	continueArgs := compactProfileArgs(profile.ContinueArgs)
	resumeArgs := compactProfileArgs(profile.ResumeArgs)

	switch action {
	case actionContinue:
		if len(continueArgs) > 0 {
			return continueArgs, actionContinue, false, nil
		}
		if len(resumeArgs) > 0 {
			return resumeArgs, "resume", true, nil
		}
	case "resume":
		if len(resumeArgs) > 0 {
			return resumeArgs, "resume", false, nil
		}
		if len(continueArgs) > 0 {
			return continueArgs, actionContinue, true, nil
		}
	}

	return nil, "", false, fmt.Errorf(
		"%s is not supported for %s: tool profile defines neither continue_args (latest session) nor resume_args (session picker)",
		action,
		profile.Name,
	)
}

func compactProfileArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	result := make([]string, 0, len(args))
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
