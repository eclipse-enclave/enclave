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
	"strings"

	"enclave/internal/model"
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
