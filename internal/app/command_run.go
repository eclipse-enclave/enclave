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
	"slices"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/mounts"
	"enclave/internal/runtime"
	"enclave/internal/theia"
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

	if autoBackgroundForIDE(input.Action, opts, profile) {
		logx.Infof("%s auto-launches the %s IDE; starting detached (reattach later with `%s %s <container>`)",
			profile.Name, profile.PostStart.OpenIDE, model.AppName, profile.PostStart.OpenIDE)
		opts.Background = true
		input.Options.Background = true
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

	// exec enters an existing container, so it mounts nothing.
	validatedDirs, validatedReadonlyDirs, err := resolveAdditionalMounts(opts, project, host.Home, isRunAction(input.Action))
	if err != nil {
		logx.Errorf("%v", err)
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
	features, provenance := sessionFeaturesAndProvenance(input, opts, profile)
	runner := runtime.New(model.RuntimeConfig{
		Paths:                 input.Ctx.Paths,
		Host:                  host,
		Project:               project,
		Profile:               profile,
		Run:                   opts.RunOptions,
		Auth:                  opts.AuthOptions,
		Build:                 opts.BuildOptions,
		RunSources:            opts.Sources.RunSources(),
		Handler:               tools.Resolve(profile.Name),
		Devcontainer:          devcontainerConfig,
		ValidatedDirs:         validatedDirs,
		ValidatedReadonlyDirs: validatedReadonlyDirs,
		YoloEnabled:           yoloEnabled,
		Features:              features.enabled,
		Provenance:            provenance,
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
	var execTarget runtime.ExecTarget
	if input.Action == "exec" {
		execTarget, err = execSessionTarget(context.Background(), be, opts, project)
		if err != nil {
			logx.Errorf("%v", err)
			return 1
		}
	}
	if err := checkToolInstalled(opts.ImageName, opts.Tool, opts.Shell, opts.Admin); err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	return dispatchRunner(input, runner, execTarget)
}

// resolveAdditionalMounts validates the directories to mount besides the
// project and returns them read-write and read-only. With guard set, it
// refuses sensitive ones (see checkSensitiveMounts), taking the
// sensitive-dir exceptions from config and from
// --add-sensitive-dir/--add-readonly-sensitive-dir. Unlike --add-dir, those
// flags add to the configured directories instead of replacing them.
func resolveAdditionalMounts(opts model.Options, project model.Project, home string, guard bool) ([]string, []string, error) {
	sensitiveDirs, exceptions, err := resolveSensitiveDirFlag(opts.AddSensitiveDirs, false, project, home)
	if err != nil {
		return nil, nil, err
	}
	readonlySensitiveDirs, readonlyExceptions, err := resolveSensitiveDirFlag(opts.AddReadonlySensitiveDirs, true, project, home)
	if err != nil {
		return nil, nil, err
	}

	writable, readonly, err := validateAdditionalDirs(opts, sensitiveDirs, readonlySensitiveDirs, project.RealDir, home)
	if err != nil {
		return nil, nil, err
	}

	if guard {
		configExceptions, warnings := configSensitiveDirExceptions(opts, home)
		for _, warning := range warnings {
			logx.Warnf("%s", warning)
		}
		exceptions = slices.Concat(configExceptions, exceptions, readonlyExceptions)
		if err := checkSensitiveMounts(project, slices.Concat(writable, readonly), exceptions, home, opts.AllowSensitiveMounts); err != nil {
			return nil, nil, err
		}
	}
	return additionalDirPaths(writable), additionalDirPaths(readonly), nil
}

const (
	addSensitiveDirFlag         = "--add-sensitive-dir"
	addReadonlySensitiveDirFlag = "--add-readonly-sensitive-dir"
)

// resolveSensitiveDirFlag resolves the directories given with
// --add-sensitive-dir, or with --add-readonly-sensitive-dir if readonly. Each
// is a sensitive-dir exception for this run, and each but the project
// directory, which is already mounted, is a directory to mount. The project
// mount mode alone decides whether the project is read-only.
func resolveSensitiveDirFlag(dirs []string, readonly bool, project model.Project, home string) ([]string, []mounts.SensitiveDirException, error) {
	flag := addSensitiveDirFlag
	if readonly {
		flag = addReadonlySensitiveDirFlag
	}
	var mountDirs []string
	var exceptions []mounts.SensitiveDirException
	var errs []error
	for _, dir := range dirs {
		realPath, err := mounts.ResolveExtraDir(dir, home)
		switch {
		case err != nil:
			errs = append(errs, err)
		case realPath == project.RealDir && readonly:
			errs = append(errs, fmt.Errorf("%s %s is the project directory; use %s %s to exempt it and --project-mount readonly to mount it read-only",
				flag, dir, addSensitiveDirFlag, dir))
		default:
			exceptions = append(exceptions, mounts.NewSensitiveDirException(realPath, flag))
			if realPath != project.RealDir {
				mountDirs = append(mountDirs, realPath)
			}
		}
	}
	return mountDirs, exceptions, errors.Join(errs...)
}

// autoBackgroundForIDE reports whether a run should be forced detached because
// the profile auto-launches a host IDE on start. Such profiles
// (post_start.open_ide) run `sleep infinity` and only fire the IDE hook on the
// detached path, so a foreground run would hang with no IDE ever opening.
// Running detached lets a bare `enclave --tool theia` start the container and
// open the IDE in one command. An explicit `shell` session is exempt: there the
// user wants the container shell, not the IDE. A session the user already
// requested as background needs no adjustment.
func autoBackgroundForIDE(action string, opts model.Options, profile model.Profile) bool {
	return action == "run" && !opts.Shell && !opts.Background &&
		profile.PostStart != nil && theia.Variant(profile.PostStart.OpenIDE).Valid()
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

// dispatchRunner starts the session, or for exec enters execTarget (resolved
// from `--name`; the zero value auto-selects).
func dispatchRunner(input *CommandInput, runner *runtime.Runtime, execTarget runtime.ExecTarget) int {
	if input.Options.Background {
		containerName, err := runner.ExecuteBackground()
		if err != nil {
			return runErrorExitCode(err)
		}
		fmt.Println(containerName)
		return 0
	}
	var err error
	if input.Action == "exec" {
		err = runner.Exec(execTarget)
	} else {
		err = runner.Execute()
	}
	if err != nil {
		return runErrorExitCode(err)
	}
	return 0
}

// execSessionTarget resolves the `--name` of `exec` like the argument of
// `attach`: a container name or ID from `enclave ps` matches verbatim from any
// directory, anything else as a session name, current project first. The tint
// follows the resolved session, as for attach. Without `--name` it returns the
// zero target, leaving auto-selection to the runtime.
func execSessionTarget(ctx context.Context, be backend.Backend, opts model.Options, project model.Project) (runtime.ExecTarget, error) {
	name, given := sessionNameFilter(opts)
	if !given {
		return runtime.ExecTarget{}, nil
	}
	session, err := resolveSessionTarget(ctx, be, sessionTargetQuery{
		Args:    []string{name},
		Tool:    sessionTargetTool(opts),
		Project: project,
	})
	if err != nil {
		return runtime.ExecTarget{}, err
	}
	return runtime.ExecTarget{Name: session.Ref.Name, SessionTint: attachSessionTint(session, opts.SessionTint)}, nil
}

// runErrorExitCode maps a session error to the process exit code. Tool exits
// and interrupted starts were already reported, so only other failures log.
func runErrorExitCode(err error) int {
	var exitErr *backend.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	if errors.Is(err, backend.ErrInterrupted) {
		return backend.ExitCodeInterrupted
	}
	logx.Errorf("%v", err)
	return 1
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
		logx.Warnf("Skipping runtime image build (--no-rebuild or %s=1).", model.EnvNoRebuild)
		if err := ensureExistingRuntimeImage(opts.ImageName); err != nil {
			logx.Errorf("%v", err)
			return buildConfig{}, 1
		}
		return resolved, 0
	}
	// A forced image rebuild must also invalidate the selected tool install
	// layers; otherwise BuildKit can reproduce a known-bad image entirely from
	// cache. Automatic interval-based refresh still applies to normal runs.
	// One memoized probe serves both this resolve and the in-lock recheck.
	probe := memoizeToolFingerprintProbe(probeToolUpdateFingerprint)
	buildPlan, err := resolveRuntimeImageBuildPlan(input.Ctx.Paths, resolved, opts.BuildOptions, opts.Tool, host.Home, opts.ForceRebuild, time.Now().UTC(), probe)
	if err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	if opts.ForceRebuild || buildPlan.NeedsRebuild() {
		if code := buildOrReuseRuntimeImage(input, opts, host, resolved, probe); code != 0 {
			return buildConfig{}, code
		}
	}
	return resolved, 0
}

func buildOrReuseRuntimeImage(input *CommandInput, opts model.Options, host model.Host, resolved buildConfig, probe toolFingerprintProbe) int {
	resolveBuildPlan := func() (runtimeImageBuildPlan, error) {
		return resolveRuntimeImageBuildPlan(input.Ctx.Paths, resolved, opts.BuildOptions, opts.Tool, host.Home, opts.ForceRebuild, time.Now().UTC(), probe)
	}
	executeBuildPlan := func(buildPlan runtimeImageBuildPlan) error {
		reused := false
		if !opts.ForceRebuild && buildPlan.StructuralRebuild && !buildPlan.AgentUpdates.NeedsRebuild {
			ok, reuseErr := reuseRuntimeImageByContentHash(context.Background(), resolved.ImageName, buildPlan.CombinedHash)
			if reuseErr != nil {
				logx.Debugf("content-cache lookup failed: %v", reuseErr)
			}
			reused = ok
		}
		if reused {
			return nil
		}
		if !opts.ForceRebuild {
			if buildPlan.StructuralRebuild {
				logx.Infof("Build inputs changed, rebuilding automatically.")
			} else {
				logx.Infof("Agent update interval elapsed, rebuilding automatically.")
			}
		}
		return buildImage(context.Background(), input.Ctx.Paths, host, buildPlan.CombinedHash, resolved, opts.BuildOptions, opts.Tool, buildPlan.AgentUpdates)
	}
	if err := coordinateRuntimeImageBuild(host.Home, resolved.ImageName, opts.ForceRebuild, resolveBuildPlan, executeBuildPlan); err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	return 0
}

func resolveYoloEnabled(profile model.Profile, opts model.Options) bool {
	enabled, resolvedBy := resolveYolo(profile, opts)
	if !enabled {
		switch resolvedBy {
		case yoloResolvedByCLI:
			warnDisabledYoloIgnored("--no-yolo ignored", profile.Name, profile.YoloFlag != "", profile.YoloEnabledValue())
		case yoloResolvedByConfig:
			warnDisabledYoloIgnored("Config yolo=false ignored", profile.Name, profile.YoloFlag != "", profile.YoloEnabledValue())
		}
	}
	return enabled
}

const (
	yoloResolvedByCLI    = "cli"
	yoloResolvedByConfig = "config"
	yoloResolvedByTool   = "tool"
)

// resolveYolo returns whether yolo mode is on and which input decided it.
func resolveYolo(profile model.Profile, opts model.Options) (bool, string) {
	if opts.YoloOverride != nil {
		return *opts.YoloOverride, yoloResolvedByCLI
	}
	if profile.YoloEnabled == nil && opts.ConfigDefaultYolo != nil {
		return *opts.ConfigDefaultYolo, yoloResolvedByConfig
	}
	return profile.YoloEnabledValue(), yoloResolvedByTool
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

	// Profile arg lists are trimmed and compacted at load.
	continueArgs := profile.ContinueArgs
	resumeArgs := profile.ResumeArgs

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
