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

	var projectOverridesDir, projectStateDir, globalSecretsFile, globalSecretsToolFile, projectSecretsFile string
	var configStoreDir, envStoreDir, authStoreDir string
	if home, homeErr := config.ResolveHostHome(); homeErr == nil {
		projectOverridesDir = config.HostProjectOverridesDir(home, project.Hash)
		projectStateDir = config.HostProjectDir(home, project.Hash)
		globalSecretsFile = config.HostSecretsGlobalSharedFile(home)
		globalSecretsToolFile = config.HostSecretsGlobalFile(home, opts.Tool)
		projectSecretsFile = config.HostSecretsProjectFile(home, project.Hash, opts.Tool)
		configStoreDir = hoststore.Dir(home, backend.StoreKey{Owner: opts.Tool, ProjectHash: project.Hash}, backend.StoreKindConfig)
		envStoreDir = hoststore.Dir(home, backend.StoreKey{Owner: opts.Tool, ProjectHash: project.Hash}, backend.StoreKindEnv)
		authStoreDir = hoststore.Dir(home, backend.StoreKey{Owner: opts.Tool, Suffix: authName}, backend.StoreKindAuth)
	}

	imageName := opts.ImageName
	buildOptions, featureWarnings, normErr := normalizeConfiguredBuildOptions(ctx.Paths, opts, opts.Sources.Features)
	for _, warning := range featureWarnings {
		logx.Warnf("%s", warning)
	}
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
	fmt.Printf("Secrets Files:\n")
	fmt.Printf("  Global: %s\n", secretsFileDisplay(globalSecretsFile))
	fmt.Printf("  Global Tool: %s\n", secretsFileDisplay(globalSecretsToolFile))
	fmt.Printf("  Project: %s\n", secretsFileDisplay(projectSecretsFile))

	return 0
}

// secretsFileDisplay renders a secrets file path with its status, falling back
// to a marker when the home directory could not be resolved.
func secretsFileDisplay(path string) string {
	if path == "" {
		return "unavailable"
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return path + " (missing)"
	case err != nil:
		return fmt.Sprintf("%s (unreadable: %v)", path, err)
	case info.IsDir():
		return path + " (not a file)"
	default:
		return path
	}
}

// storeDirDisplay renders a resolved store directory, falling back to a marker
// when the home directory could not be resolved.
func storeDirDisplay(dir string) string {
	if dir == "" {
		return "unavailable"
	}
	return dir
}
