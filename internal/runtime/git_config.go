// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"enclave/internal/mounts"
	"enclave/internal/util"
)

func (r *Runtime) protectGitConfigFiles(acc *mountAccumulator) error {
	paths, err := hostGitConfigPaths(r.host.Home, r.project.Dir)
	if err != nil {
		return err
	}
	acc.protectedFiles = paths
	acc.mounts, err = mounts.ProtectFiles(acc.mounts, paths)
	return err
}

// Discover config files without granting access to any new host paths. The
// mount layer only protects files already exposed by an existing bind mount.
func hostGitConfigPaths(home, projectDir string) ([]string, error) {
	var paths []string
	// Empty config files have no --show-origin records.
	if global, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		if global != "" {
			paths = append(paths, global)
		}
	} else {
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		paths = append(paths, filepath.Join(home, ".gitconfig"), filepath.Join(xdg, "git", "config"))
	}
	switch strings.ToLower(os.Getenv("GIT_CONFIG_NOSYSTEM")) {
	case "", "0", "false", "no", "off":
		if system := os.Getenv("GIT_CONFIG_SYSTEM"); system != "" {
			paths = append(paths, system)
		}
	}
	// Environment overrides can be relative to Git's working directory.
	for i, path := range paths {
		if !filepath.IsAbs(path) {
			paths[i] = filepath.Join(projectDir, path)
		}
	}
	gitPointer := filepath.Join(projectDir, ".git")
	if info, err := os.Lstat(gitPointer); err == nil && info.Mode().IsRegular() {
		paths = append(paths, gitPointer)
	}
	output, err := hostGitOutput(home, projectDir, "config", "--includes", "--show-origin", "--null", "--list")
	if err != nil {
		return nil, fmt.Errorf("discover Git config files: %w", err)
	}
	parts := strings.Split(string(output), "\x00")
	if len(parts)%2 != 1 || parts[len(parts)-1] != "" {
		return nil, fmt.Errorf("discover Git config files: invalid origin output")
	}
	for i := 0; i < len(parts)-1; i += 2 {
		origin, ok := strings.CutPrefix(parts[i], "file:")
		if !ok {
			continue
		}
		if !filepath.IsAbs(origin) {
			origin = filepath.Join(projectDir, origin)
		}
		paths = append(paths, origin)
		key, value, _ := strings.Cut(parts[i+1], "\n")
		// Empty included files produce no origin records of their own.
		if value != "" && (key == "include.path" || (strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path"))) {
			include := util.ExpandTilde(value, home)
			if !filepath.IsAbs(include) {
				include = filepath.Join(filepath.Dir(origin), include)
			}
			paths = append(paths, include)
		}
	}
	// These may be empty, and config.worktree may exist before worktreeConfig
	// is enabled, so neither is necessarily present in --show-origin output.
	// Pointer files must also stay fixed to prevent redirecting Git elsewhere.
	// Query separately: rev-parse emits literal newlines in paths, so batched
	// output cannot be split unambiguously into individual paths.
	for _, name := range []string{"config", "config.worktree", "commondir", "gitdir"} {
		output, err := hostGitOutput(home, projectDir, "rev-parse", "--git-path", name)
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
				// Sessions outside a repository still protect exposed global config.
				return paths, nil
			}
			return nil, fmt.Errorf("locate Git %s: %w", name, err)
		}
		path := strings.TrimSuffix(string(output), "\n")
		if !filepath.IsAbs(path) {
			path = filepath.Join(projectDir, path)
		}
		paths = append(paths, path)
	}
	output, err = hostGitOutput(home, projectDir, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("locate Git common directory: %w", err)
	}
	common := strings.TrimSuffix(string(output), "\n")
	if !filepath.IsAbs(common) {
		common = filepath.Join(projectDir, common)
	}
	paths = append(paths, filepath.Join(common, "config.worktree"))
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("discover sibling worktree config: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			for _, name := range []string{"config.worktree", "commondir", "gitdir"} {
				paths = append(paths, filepath.Join(common, "worktrees", entry.Name(), name))
			}
		}
	}
	return paths, nil
}
