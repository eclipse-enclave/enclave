// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Mount modes in GuardEntry.MountMode and the membership file.
const (
	MountReadWrite = "rw"
	MountReadOnly  = "ro"
)

// GuardEntry is one member's host-canonical project path and mount mode.
type GuardEntry struct {
	Member      string
	ProjectPath string
	MountMode   string
}

// GuardResult lists overlaps that only warn; a refusal is ErrWorktreeClash.
type GuardResult struct {
	Warnings []string
}

// WorktreeGuard runs at join (proposal 6): two writable members on one path
// are refused, the orchestrator included; any other overlap warns.
type WorktreeGuard interface {
	Check(existing []GuardEntry, candidate GuardEntry) (GuardResult, error)
}

// NewWorktreeGuard returns the path-comparison guard.
func NewWorktreeGuard() WorktreeGuard { return pathGuard{} }

type pathGuard struct{}

// Check compares cleaned paths, resolving symlinks where the path exists;
// bind-mount aliases stay undetected (a documented v1 limit).
func (pathGuard) Check(existing []GuardEntry, candidate GuardEntry) (GuardResult, error) {
	cand, err := guardPath(candidate)
	if err != nil {
		return GuardResult{}, err
	}
	var res GuardResult
	for _, e := range existing {
		other, err := guardPath(e)
		if err != nil {
			return GuardResult{}, err
		}
		switch {
		case cand == other && candidate.MountMode == MountReadWrite && e.MountMode == MountReadWrite:
			return GuardResult{}, fmt.Errorf("%w: %s and %s both mount %s read-write", ErrWorktreeClash, e.Member, candidate.Member, cand)
		case cand == other:
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s (%s) and %s (%s) share project path %s", e.Member, e.MountMode, candidate.Member, candidate.MountMode, cand))
		case pathWithin(other, cand) || pathWithin(cand, other):
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s (%s, %s) and %s (%s, %s) have nested project paths", e.Member, e.MountMode, other, candidate.Member, candidate.MountMode, cand))
		}
	}
	return res, nil
}

func guardPath(e GuardEntry) (string, error) {
	if e.MountMode != MountReadWrite && e.MountMode != MountReadOnly {
		return "", fmt.Errorf("member %s: mount mode %q", e.Member, e.MountMode)
	}
	if !filepath.IsAbs(e.ProjectPath) {
		return "", fmt.Errorf("member %s: project path %q is not absolute", e.Member, e.ProjectPath)
	}
	p := filepath.Clean(e.ProjectPath)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return p, nil
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
