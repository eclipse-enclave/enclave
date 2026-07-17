// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func runInfo(ctx *AppContext, opts model.Options) int {
	project, err := ctx.Project()
	if err != nil {
		logx.Errorf("Failed to resolve project: %v", err)
		return 1
	}

	globalConfig, globalErr := config.GlobalConfigPath()
	projectConfig := config.ProjectConfigJSONPath(ctx.ProjectDir)

	// The info action bypasses ValidateOptions, so an auth_name set via
	// config.json arrives unnormalized here; normalize it the same way a run
	// would, or the preview shows a store path a run never mounts.
	authName := strings.TrimSpace(opts.AuthName)
	if authName != "" {
		if normalized, err := config.ValidateAuthName(authName); err == nil {
			authName = normalized
		} else {
			logx.Warnf("Configured auth name is invalid and a run would reject it: %v", err)
		}
	}

	var projectOverridesDir, projectStateDir string
	var configStoreDir, envStoreDir, authStoreDir string
	if home, homeErr := config.ResolveHostHome(); homeErr == nil {
		projectOverridesDir = config.HostProjectOverridesDir(home, project.Hash)
		projectStateDir = config.HostProjectDir(home, project.Hash)
		configStoreDir = hoststore.Dir(home, backend.StoreKey{Owner: opts.Tool, ProjectHash: project.Hash}, backend.StoreKindConfig)
		envStoreDir = hoststore.Dir(home, backend.StoreKey{Owner: opts.Tool, ProjectHash: project.Hash}, backend.StoreKindEnv)
		authStoreDir = hoststore.Dir(home, backend.StoreKey{Owner: opts.Tool, Suffix: authName}, backend.StoreKindAuth)
	}

	imageName := opts.ImageName
	buildOptions, normErr := normalizeConfiguredBuildOptions(ctx.Paths, opts.BuildOptions)
	if normErr != nil {
		logx.Warnf("Unable to normalize feature directives for image selection: %v", normErr)
	} else if build, err := resolveBuildConfig(buildOptions, opts.Tool, project, ctx.Paths.AppRoot); err == nil {
		imageName = build.ImageName
	} else {
		logx.Warnf("Unable to resolve build image name: %v", err)
	}

	info := imageInfo{Labels: map[string]string{}}
	if err := checkDocker(); err != nil {
		logx.Warnf("Docker not available: %v", err)
	} else {
		info = inspectImageInfo(imageName)
	}

	fmt.Printf("%s info\n", model.AppName)
	fmt.Printf("App Root: %s\n", ctx.Paths.AppRoot)
	fmt.Printf("Project Directory: %s\n", project.Dir)
	fmt.Printf("Project Hash: %s\n", project.Hash)
	fmt.Printf("Tool: %s\n", opts.Tool)
	fmt.Printf("Image Name: %s\n", imageName)
	fmt.Printf("Persistent Store Directories (static):\n")
	fmt.Printf("  Config: %s\n", storeDirDisplay(configStoreDir))
	fmt.Printf("  Env: %s\n", storeDirDisplay(envStoreDir))
	fmt.Printf("  Shared Auth: %s\n", storeDirDisplay(authStoreDir))
	fmt.Printf("  Note: Actual runtime store usage depends on flags/profile (for example --ephemeral, --auth-scope).\n")
	if info.Exists {
		fmt.Printf("Image Present: yes\n")
	} else {
		fmt.Printf("Image Present: no\n")
	}
	if len(info.Labels) > 0 {
		fmt.Printf("Image Labels:\n")
		if value := info.Labels[model.LabelVersion]; value != "" {
			fmt.Printf("  %s: %s\n", model.LabelVersion, value)
		}
		if value := info.Labels[model.LabelBuilt]; value != "" {
			fmt.Printf("  %s: %s\n", model.LabelBuilt, value)
		}
		if value := info.Labels[model.LabelHash]; value != "" {
			fmt.Printf("  %s: %s\n", model.LabelHash, value)
		}
	}
	if globalErr == nil {
		if _, err := os.Stat(globalConfig); err == nil {
			fmt.Printf("Global Config: %s\n", globalConfig)
		} else {
			fmt.Printf("Global Config: none\n")
		}
	} else {
		fmt.Printf("Global Config: unavailable (%v)\n", globalErr)
	}
	if projectConfig != "" {
		if _, err := os.Stat(projectConfig); err == nil {
			fmt.Printf("Project Config: %s\n", projectConfig)
		} else {
			fmt.Printf("Project Config: %s (missing)\n", projectConfig)
		}
	} else {
		fmt.Printf("Project Config: none\n")
	}
	if projectOverridesDir != "" {
		fmt.Printf("Project Overrides Dir: %s\n", projectOverridesDir)
	}
	if projectStateDir != "" {
		fmt.Printf("Project State Dir: %s\n", projectStateDir)
	}

	return 0
}

// storeDirDisplay renders a resolved store directory, falling back to a marker
// when the home directory could not be resolved.
func storeDirDisplay(dir string) string {
	if dir == "" {
		return "unavailable"
	}
	return dir
}
