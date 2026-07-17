// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"strings"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/theia"
	"enclave/internal/usercmd"
)

func Run(args []string) int {
	projectDir, err := resolveProjectDir()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	globalDefaults, projectDefaults, warnings, err := config.LoadDefaults(projectDir)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	for _, warning := range warnings {
		logx.Warnf(warning)
	}

	userCmds := discoverUserCommands()

	baseOpts := config.DefaultOptions()
	parsed, err := cli.Parse(args, baseOpts, userCmds...)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	for _, warning := range parsed.Warnings {
		logx.Warnf(warning)
	}
	if parsed.HelpShown {
		return 0
	}
	if parsed.Action == "completion" {
		return 0
	}

	var userCommandMount *model.UserCommandMount
	if parsed.Action == "user-command" {
		home, err := config.ResolveHostHome()
		if err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		switch parsed.UserCommand.Target {
		case usercmd.TargetHost:
			if parsed.Options.Verbose {
				logx.SetLevel("debug")
			}
			return runUserHostCommand(*parsed.UserCommand, parsed.UserCommandArgs, projectDir, home)
		case usercmd.TargetSession:
			// Session commands run through the standard run pipeline as a
			// shell-style execution; fall through with a rewritten action.
			userCommandMount = prepareUserSessionCommand(&parsed, home)
		default:
			logx.Errorf("unknown user command target %q", parsed.UserCommand.Target)
			return 1
		}
	}

	cliOpts := parsed.Options
	cliSources := parsed.Sources
	opts, toolDefaults, hasToolDefaults := config.ResolveOptionsForTool(cliOpts, cliSources, globalDefaults, projectDefaults, "")
	sources := opts.Sources
	parsed.Options = opts

	// Apply verbose here, before the per-action early returns below, so that
	// --verbose is honored uniformly for every command (cleanup/ps/stop/attach
	// bypass dispatchCommand via early return).
	if opts.Verbose {
		logx.SetLevel("debug")
	}

	if parsed.Action == "cleanup" {
		return runCleanup(parsed.Options.RunOptions, parsed.Options.CleanupOptions)
	}

	if parsed.Action == "ps" {
		return runPS(parsed.Options)
	}

	if parsed.Action == "status" {
		return runStatus(parsed.Options)
	}

	if parsed.Action == "stop" {
		return runStop(parsed.Options.RunOptions)
	}

	if parsed.Action == "attach" {
		return runAttach(parsed.Options.RunOptions)
	}

	if parsed.Action == "review-target" {
		return runReviewTarget(projectDir, parsed.ReviewTarget)
	}

	if parsed.Action == "theia" || parsed.Action == "theia-next" {
		return runTheia(theia.Variant(parsed.Action), projectDir, parsed.Options)
	}

	paths, err := config.ResolvePaths()
	if err != nil {
		logx.Errorf("%v", err)
		logx.Infof("Set %s to the directory containing Dockerfile and profiles.", model.EnvHome)
		return 1
	}
	ctx := NewAppContext(paths, projectDir)

	command := CommandInput{
		Ctx:              ctx,
		Action:           parsed.Action,
		Options:          opts,
		Sources:          sources,
		CLIOptions:       cliOpts,
		CLISources:       cliSources,
		BaseOptions:      baseOpts,
		GlobalDefaults:   globalDefaults,
		ProjectDefaults:  projectDefaults,
		ToolDefaults:     toolDefaults,
		HasToolDefaults:  hasToolDefaults,
		ConfigView:       parsed.ConfigView,
		UserCommandMount: userCommandMount,
	}
	return dispatchCommand(&command)
}

// discoverUserCommands scans the host command directories best-effort. If home
// cannot be resolved, discovery is skipped (commands that never needed home
// keep working) and warnings from discovery are logged immediately.
func discoverUserCommands() []usercmd.Command {
	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Debugf("skipping user command discovery: %v", err)
		return nil
	}
	cmds, warnings := usercmd.Discover(home)
	for _, warning := range warnings {
		logx.Warnf(warning)
	}
	return cmds
}

func resolveProjectDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return cwd, nil
}

// normalizeToolNames lowercases, validates against the available set, and
// de-duplicates tool names while preserving order. It rejects unknown names and
// an effectively empty list (all blank entries).
func normalizeToolNames(
	tools []string,
	available []string,
	unknownErr func(string) error,
	emptyErr func() error,
) ([]string, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	allowed := make(map[string]struct{}, len(available))
	for _, name := range available {
		trimmed := strings.TrimSpace(strings.ToLower(name))
		if trimmed == "" {
			continue
		}
		allowed[trimmed] = struct{}{}
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(tools))
	for _, raw := range tools {
		name := strings.TrimSpace(strings.ToLower(raw))
		if name == "" {
			continue
		}
		if _, ok := allowed[name]; !ok {
			return nil, unknownErr(raw)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	if len(normalized) == 0 {
		return nil, emptyErr()
	}
	return normalized, nil
}
