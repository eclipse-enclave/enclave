// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os/exec"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

const maxBranchTagLen = 32

func branchImageName(appRoot string, tool string, target string) (string, bool) {
	branch, ok := resolveGitBranch(appRoot)
	if !ok {
		return "", false
	}
	defaultBranch := resolveGitDefaultBranch(appRoot)
	if isDefaultBranch(branch, defaultBranch) {
		return "", false
	}
	prefix := branchTagPrefix(branch)
	if prefix == "" {
		return "", false
	}
	return fmt.Sprintf("%s-%s:%s-%s", model.AppName, tool, prefix, targetToTagName(target)), true
}

func resolveGitBranch(appRoot string) (string, bool) {
	if strings.TrimSpace(appRoot) == "" {
		return "", false
	}
	cmd := exec.Command("git", "-C", appRoot, "rev-parse", "--abbrev-ref", "HEAD") // #nosec G204 -- command and args are fixed; appRoot is validated non-empty.
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	branch := strings.TrimSpace(string(output))
	if branch == "" || branch == "HEAD" {
		return "", false
	}
	return branch, true
}

func resolveGitDefaultBranch(appRoot string) string {
	if strings.TrimSpace(appRoot) == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", appRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD") // #nosec G204 -- command and args are fixed; appRoot is validated non-empty.
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(output))
	if ref == "" {
		return ""
	}
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

func isDefaultBranch(branch string, defaultBranch string) bool {
	if branch == "" {
		return false
	}
	if defaultBranch != "" {
		return branch == defaultBranch
	}
	return branch == "main" || branch == "master"
}

func branchTagPrefix(branch string) string {
	slug := sanitizeTagSegment(branch)
	hash := model.ShortHash(util.HashString(branch))
	if slug == "" {
		return "branch-" + hash
	}
	return "branch-" + slug + "-" + hash
}

func sanitizeTagSegment(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range input {
		if isDockerTagChar(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	cleaned := b.String()
	cleaned = strings.TrimLeftFunc(cleaned, func(r rune) bool {
		return !isDockerTagStartChar(r)
	})
	cleaned = strings.TrimRight(cleaned, "-.")
	if cleaned == "" {
		return ""
	}
	if len(cleaned) > maxBranchTagLen {
		cleaned = cleaned[:maxBranchTagLen]
	}
	return cleaned
}

func isDockerTagStartChar(r rune) bool {
	return r == '_' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func isDockerTagChar(r rune) bool {
	return isDockerTagStartChar(r) || r == '.' || r == '-'
}
