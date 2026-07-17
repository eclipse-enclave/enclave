// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package git

import (
	"os/exec"
	"strings"
)

// WorktreeInfo represents one entry from `git worktree list --porcelain`.
type WorktreeInfo struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	IsMain bool   `json:"isMain"`
}

// ListWorktrees returns git worktrees for dir.
func ListWorktrees(dir string) ([]WorktreeInfo, error) {
	// #nosec G204 -- command and flags are fixed; dir is passed as a single argument.
	cmd := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return ParseWorktreeList(out), nil
}

// ResolveMainWorktree returns the main worktree path for a git repository.
// If dir is already the main worktree, or is not a git repo, dir is returned unchanged.
func ResolveMainWorktree(dir string) string {
	wts, err := ListWorktrees(dir)
	if err != nil || len(wts) == 0 {
		return dir
	}
	return wts[0].Path
}

// ListLocalBranches returns the names of all local branches in dir.
func ListLocalBranches(dir string) ([]string, error) {
	// #nosec G204 -- command and flags are fixed; dir is passed as a single argument.
	cmd := exec.Command("git", "-C", dir, "branch", "--format=%(refname:short)")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// ParseWorktreeList parses `git worktree list --porcelain` output.
func ParseWorktreeList(data []byte) []WorktreeInfo {
	var worktrees []WorktreeInfo
	var current WorktreeInfo
	isFirst := true

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				worktrees = append(worktrees, current)
			}
			current = WorktreeInfo{
				Path:   strings.TrimPrefix(line, "worktree "),
				IsMain: isFirst,
			}
			isFirst = false
		case strings.HasPrefix(line, "branch refs/heads/"):
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "detached":
			current.Branch = "(detached)"
		}
	}
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}
	return worktrees
}
