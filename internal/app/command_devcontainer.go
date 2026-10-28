// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"strings"

	"enclave/internal/config"
	"enclave/internal/devcontainer"
	"enclave/internal/logx"
)

func runDevcontainerGenerate(input *CommandInput) int {
	project, err := input.Ctx.Project()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	buildOptions, err := normalizeConfiguredBuildOptions(input.Ctx.Paths, input.Options.BuildOptions)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	buildCfg, err := resolveBuildConfig(buildOptions, input.Options.Tool, project, input.Ctx.Paths.AppRoot)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	profile, err := config.LoadProfile(input.Ctx.Paths, input.Options.Tool)
	if err != nil {
		logx.Errorf("%v", err)
		if available, listErr := config.ListProfiles(input.Ctx.Paths); listErr == nil {
			logx.Infof("Available tools: %s", strings.Join(available, " "))
		}
		return 1
	}

	force := input.Options.Force

	outPath, err := devcontainer.Generate(devcontainer.GenerateConfig{
		Image:         buildCfg.ImageName,
		Tool:          input.Options.Tool,
		ProjectDir:    project.Dir,
		SecretEnvVars: profile.DeclaredSecretEnvVars(),
		Force:         force,
	})
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	logx.Successf("Generated %s", outPath)
	return 0
}
