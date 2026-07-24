// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	appRootDockerfileRel        = "Dockerfile"
	appRootEntrypointRel        = "entrypoint.sh"
	appRootGatewayDockerfileRel = "Dockerfile.gateway"
	appRootGatewayEntrypointRel = "gateway-entrypoint.sh"
	appRootExtensionsRel        = "extensions"
	appRootRuntimeAssetsRel     = "runtime-assets"
	appRootBuildScriptsRel      = "build-scripts"
)

type appRootRequiredEntry struct {
	rel   string
	label string
	isDir bool
}

func ResolvePaths() (model.Paths, error) {
	root, err := discoverAppRoot()
	if err != nil {
		return model.Paths{}, fmt.Errorf("discover app root: %w", err)
	}

	extensionsDir := filepath.Join(root, appRootExtensionsRel)
	paths := model.Paths{
		AppRoot:           root,
		Dockerfile:        filepath.Join(root, appRootDockerfileRel),
		Entrypoint:        filepath.Join(root, appRootEntrypointRel),
		GatewayDockerfile: filepath.Join(root, appRootGatewayDockerfileRel),
		GatewayEntrypoint: filepath.Join(root, appRootGatewayEntrypointRel),
		ExtensionsDir:     extensionsDir,
		ToolsDir:          filepath.Join(extensionsDir, "tools"),
		FeaturesDir:       filepath.Join(extensionsDir, "features"),
		AllowlistsDir:     filepath.Join(root, appRootRuntimeAssetsRel, GatewayAllowlistsDirName),
		BuildScriptsDir:   filepath.Join(root, appRootRuntimeAssetsRel, appRootBuildScriptsRel),
	}
	resolveUserExtensionPaths(&paths)

	if err := validateAppRoot(root); err != nil {
		return model.Paths{}, err
	}

	return paths, nil
}

func resolveUserExtensionPaths(paths *model.Paths) {
	home, err := ResolveHostHome()
	if err != nil || home == "" {
		return
	}

	userExtensionsDir := HostExtensionsDir(home)
	info, err := os.Stat(userExtensionsDir)
	if err != nil || !info.IsDir() {
		return
	}

	paths.UserExtensionsDir = userExtensionsDir
	paths.UserToolsDir = filepath.Join(userExtensionsDir, "tools")
	paths.UserFeaturesDir = filepath.Join(userExtensionsDir, "features")
}

func ResolveProjectFromDir(dir string) (model.Project, error) {
	home, err := ResolveHostHome()
	if err != nil {
		return model.Project{}, fmt.Errorf("resolve host home: %w", err)
	}
	description, err := DescribeProjectFromDir(home, dir)
	if err != nil {
		return model.Project{}, err
	}
	return description.Project, nil
}

func resolveCanonicalProject(dir string) (model.Project, error) {
	if dir == "" {
		return model.Project{}, fmt.Errorf("project dir is empty")
	}

	absPath, err := filepath.Abs(dir)
	if err != nil {
		return model.Project{}, fmt.Errorf("resolve project dir %s: %w", dir, err)
	}

	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return model.Project{}, fmt.Errorf("resolve project dir %s: %w", dir, err)
	}

	info, err := os.Stat(realPath)
	if err != nil {
		return model.Project{}, fmt.Errorf("stat project dir %s: %w", realPath, err)
	}
	if !info.IsDir() {
		return model.Project{}, fmt.Errorf("project dir %s is not a directory", realPath)
	}

	return model.Project{
		Dir:     dir,
		RealDir: realPath,
		Hash:    ProjectHashForPath(realPath),
		Name:    filepath.Base(dir),
	}, nil
}

func ProjectHashForPath(path string) string {
	return model.ShortHash(util.HashString(path))
}

func discoverAppRoot() (string, error) {
	if root := os.Getenv(model.EnvHome); root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", err
		}
		if isAppRoot(abs) {
			return abs, nil
		}
		return "", fmt.Errorf("%s does not contain required files: %s", model.EnvHome, abs)
	}

	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	exeRoot := filepath.Dir(resolved)
	if root, ok := findAppRootFromDir(exeRoot); ok {
		return root, nil
	}

	root, err := extractRegisteredAppAssets()
	if err != nil {
		return "", fmt.Errorf("extract embedded %s assets: %w", model.AppName, err)
	}
	return root, nil
}

func findAppRootFromDir(start string) (string, bool) {
	dir := start
	for {
		if isAppRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func requiredAppRootEntries() []appRootRequiredEntry {
	return []appRootRequiredEntry{
		{rel: appRootDockerfileRel, label: "dockerfile"},
		{rel: appRootEntrypointRel, label: "entrypoint.sh"},
		{rel: appRootGatewayDockerfileRel, label: "gateway dockerfile"},
		{rel: appRootGatewayEntrypointRel, label: "gateway entrypoint"},
		{rel: appRootExtensionsRel, label: "extensions directory", isDir: true},
		{rel: filepath.Join(appRootRuntimeAssetsRel, GatewayAllowlistsDirName), label: "gateway allowlists directory", isDir: true},
		{rel: filepath.Join(appRootRuntimeAssetsRel, appRootBuildScriptsRel), label: "build scripts directory", isDir: true},
	}
}

func validateAppRoot(dir string) error {
	for _, entry := range requiredAppRootEntries() {
		path := filepath.Join(dir, entry.rel)
		info, err := os.Stat(path) // #nosec G703 -- rel comes from fixed app-root entries.
		if err != nil {
			return fmt.Errorf("%s not found at %s: %w", entry.label, path, err)
		}
		if entry.isDir && !info.IsDir() {
			return fmt.Errorf("%s not found at %s: not a directory", entry.label, path)
		}
		if !entry.isDir && info.IsDir() {
			return fmt.Errorf("%s not found at %s: is a directory", entry.label, path)
		}
	}
	return nil
}

func isAppRoot(dir string) bool {
	return validateAppRoot(dir) == nil
}
