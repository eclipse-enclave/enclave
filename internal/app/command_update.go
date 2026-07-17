// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// runUpdate rebuilds one or more tool images with a forced agent CLI update,
// without starting a container. With no tool arguments it targets the resolved
// default tool; otherwise it targets each named tool. Normal `run` invocations
// refresh agents automatically once the update interval elapses; this command
// is the explicit, build-only path to refresh now.
func runUpdate(input *CommandInput) int {
	// update always rebuilds, so --no-rebuild is a contradiction. It is
	// CLI-only and tool-agnostic, so one check covers every target.
	if input.Options.NoRebuild {
		logx.Errorf("--no-rebuild is incompatible with the update command")
		return 1
	}

	tools, err := resolveUpdateTools(input.Ctx.Paths, input.Options)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	if code := requireDocker(); code != 0 {
		return code
	}
	project, err := input.Ctx.Project()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	host, err := input.Ctx.Host()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	for _, tool := range tools {
		// Resolve each target with its own tool overrides so the rebuilt image
		// matches what `enclave --tool <tool>` would produce, not the default
		// tool's variant.
		toolOpts, _, _ := config.ResolveOptionsForTool(input.CLIOptions, input.CLISources, input.GlobalDefaults, input.ProjectDefaults, tool)
		toolOpts, _, warnings, validateErr := ValidateOptions(toolOpts, toolOpts.Sources, ValidationContext{
			Paths:  input.Ctx.Paths,
			Action: input.Action,
		})
		if validateErr != nil {
			logx.Errorf("%s: %v", tool, validateErr)
			return 1
		}
		for _, warning := range warnings {
			logx.Warnf(warning)
		}
		logx.Infof("Updating %s agent image.", tool)
		if err := updateToolImage(input.Ctx, toolOpts, host, project, tool); err != nil {
			logx.Errorf("%v", err)
			return 1
		}
	}
	logx.Successf("Updated %s.", strings.Join(tools, ", "))
	return 0
}

// resolveUpdateTools returns the validated, de-duplicated set of tools to
// update: the explicit positional arguments, or the resolved default tool when
// none were given.
func resolveUpdateTools(paths model.Paths, opts model.Options) ([]string, error) {
	requested := opts.UpdateTools
	if len(requested) == 0 {
		tool := strings.TrimSpace(opts.Tool)
		if tool == "" {
			return nil, fmt.Errorf("no tool to update")
		}
		requested = []string{tool}
	}
	available, err := config.ListProfiles(paths)
	if err != nil {
		return nil, err
	}
	return normalizeToolNames(
		requested,
		available,
		func(raw string) error { return fmt.Errorf("unknown tool: %s", raw) },
		func() error { return fmt.Errorf("update requires at least one tool") },
	)
}

// updateToolImage rebuilds a single tool's runtime image with a forced agent
// CLI update. It never starts a container. buildImage commits the agent-update
// stamps on success, so the automatic update interval resets too.
func updateToolImage(ctx *AppContext, opts model.Options, host model.Host, project model.Project, tool string) error {
	buildCfg, err := resolveBuildConfig(opts.BuildOptions, tool, project, ctx.Paths.AppRoot)
	if err != nil {
		return err
	}
	buildCfg.HashSuffix = appendEffectiveBuildIdentityHashSuffix(buildCfg.HashSuffix, host, opts.BuildOptions)
	buildPlan, err := resolveRuntimeImageBuildPlan(ctx.Paths, buildCfg, opts.BuildOptions, tool, host.Home, true, time.Now().UTC())
	if err != nil {
		return err
	}
	return buildImage(context.Background(), ctx.Paths, host, buildPlan.CombinedHash, buildCfg, opts.BuildOptions, tool, buildPlan.AgentUpdates)
}
