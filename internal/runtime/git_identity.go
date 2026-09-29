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

	"enclave/internal/model"
	"enclave/internal/mounts"
	"enclave/internal/util"
)

type hostGitIdentity struct {
	forwardedName  string
	forwardedEmail string
	effectiveName  string
	effectiveEmail string
}

type hostGitValue struct {
	forwarded string
	effective string
}

func resolveHostGitIdentity(home, projectDir string) (hostGitIdentity, error) {
	name, err := resolveHostGitValue(home, projectDir, "user.name")
	if err != nil {
		return hostGitIdentity{}, err
	}
	email, err := resolveHostGitValue(home, projectDir, "user.email")
	if err != nil {
		return hostGitIdentity{}, err
	}
	return hostGitIdentity{
		forwardedName:  name.forwarded,
		forwardedEmail: email.forwarded,
		effectiveName:  name.effective,
		effectiveEmail: email.effective,
	}, nil
}

func resolveHostGitValue(home, projectDir, key string) (hostGitValue, error) {
	cmd := exec.Command("git", "config", "--includes", "--show-scope", "-z", "--get-all", key) // #nosec G204 G702 -- executable and key are fixed; no shell is used.
	cmd.Env = append(os.Environ(), "HOME="+home)
	if projectDir != "" {
		cmd.Dir = projectDir
	}
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if exitErr.ExitCode() == 1 && stderr == "" {
				return hostGitValue{}, nil
			}
			if stderr != "" {
				return hostGitValue{}, fmt.Errorf("read host Git %s: %w: %s", key, err, stderr)
			}
		}
		return hostGitValue{}, fmt.Errorf("read host Git %s: %w", key, err)
	}
	parts := strings.Split(string(output), "\x00")
	if len(parts) < 3 || parts[len(parts)-1] != "" || (len(parts)-1)%2 != 0 {
		return hostGitValue{}, fmt.Errorf("read host Git %s: invalid scoped output", key)
	}
	var result hostGitValue
	for i := 0; i < len(parts)-1; i += 2 {
		scope, value := parts[i], parts[i+1]
		switch scope {
		case "system", "global":
			result.forwarded = value
			result.effective = value
		case "local", "worktree":
			result.effective = value
		case "command":
			// Host command-scope config is not forwarded into the session.
		default:
			return hostGitValue{}, fmt.Errorf("read host Git %s: unsupported scope %q", key, scope)
		}
	}
	return result, nil
}

func validateGitIdentity(identity hostGitIdentity, sessionEnv []string) error {
	env := make(map[string]string)
	for _, entry := range sessionEnv {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	authorName, authorEmail := identity.effectiveName, identity.effectiveEmail
	committerName, committerEmail := identity.effectiveName, identity.effectiveEmail
	if value, ok := env["GIT_AUTHOR_NAME"]; ok {
		authorName = value
	}
	if value, ok := env["GIT_AUTHOR_EMAIL"]; ok {
		authorEmail = value
	}
	if value, ok := env["GIT_COMMITTER_NAME"]; ok {
		committerName = value
	}
	if value, ok := env["GIT_COMMITTER_EMAIL"]; ok {
		committerEmail = value
	}
	var missing []string
	if authorName == "" {
		missing = append(missing, "author name")
	}
	if authorEmail == "" {
		missing = append(missing, "author email")
	}
	if committerName == "" {
		missing = append(missing, "committer name")
	}
	if committerEmail == "" {
		missing = append(missing, "committer email")
	}
	if len(missing) > 0 {
		return fmt.Errorf("git identity is incomplete (missing %s); configure user.name and user.email globally or in this repository, or set the missing GIT_AUTHOR_* and GIT_COMMITTER_* variables in the project's .env file or devcontainer containerEnv", strings.Join(missing, ", "))
	}
	return nil
}

func addHostGitIdentityEnv(mounts *mountAccumulator, identity hostGitIdentity) {
	if identity.forwardedName != "" {
		mounts.AddEnv(model.EnvGitName, identity.forwardedName)
	}
	if identity.forwardedEmail != "" {
		mounts.AddEnv(model.EnvGitEmail, identity.forwardedEmail)
	}
}

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
	paths := []string{filepath.Join(projectDir, ".gitconfig")}
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
	output, err := gitConfigPathOutput(home, projectDir, "config", "--includes", "--show-origin", "--null", "--list")
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
		output, err := gitConfigPathOutput(home, projectDir, "rev-parse", "--git-path", name)
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
	output, err = gitConfigPathOutput(home, projectDir, "rev-parse", "--git-common-dir")
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

func gitConfigPathOutput(home, projectDir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...) // #nosec G204 G702 -- fixed executable and internal config-query arguments; no shell.
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "HOME="+home)
	return cmd.Output()
}
