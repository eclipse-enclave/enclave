// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package gitworktree verifies linked-worktree metadata without executing Git.
// Verification is not atomic across independently writable directories.
package gitworktree

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxMetadataBytes = 4096

// Repository config commonly grows with per-branch tracking sections.
const maxRepositoryConfigBytes = 1024 * 1024

var errSymlink = errors.New("symlink file")

type Status int

const (
	NotLinked Status = iota
	Invalid
	Verified
	// VerifiedUnanchored has a verified relationship but no supported config anchor.
	VerifiedUnanchored
)

// Result retains pointer paths for callers enforcing their own exposure policy.
// Anchor is set only for a supported, verified config layout.
type Result struct {
	Status    Status
	GitDir    string
	CommonDir string
	Anchor    string
	Reason    string
}

func Resolve(checkout string) Result {
	checkout, err := canonical(checkout)
	if err != nil {
		return Result{}
	}
	data, err := readRegularFile(checkout, ".git", maxMetadataBytes)
	if err != nil {
		if errors.Is(err, errSymlink) {
			return Result{Status: Invalid, Reason: "symlinked .git file"}
		}
		if info, statErr := os.Lstat(filepath.Join(checkout, ".git")); !os.IsNotExist(statErr) && (statErr != nil || !info.IsDir()) {
			return Result{Status: Invalid, Reason: "unreadable or invalid .git file"}
		}
		return Result{}
	}
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	if !strings.HasPrefix(line, "gitdir:") {
		return Result{Status: Invalid, Reason: "malformed .git pointer"}
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if target == "" {
		return Result{Status: Invalid, Reason: "empty .git pointer"}
	}
	gitdir, err := relativeTarget(checkout, target)
	if err != nil {
		return Result{Status: Invalid, Reason: "missing or invalid Git metadata directory"}
	}
	r := Result{GitDir: gitdir}
	data, err = readRegularFile(gitdir, "commondir", maxMetadataBytes)
	// Submodules and separate git directories do not have commondir.
	if err != nil {
		if _, statErr := os.Lstat(filepath.Join(gitdir, "commondir")); statErr == nil || filepath.Base(filepath.Dir(gitdir)) == "worktrees" {
			r.Status, r.Reason = Invalid, "missing or invalid commondir"
		}
		return r
	}
	r.Status = Invalid
	r.Reason = "invalid commondir"
	common, err := relativeTarget(gitdir, strings.TrimSpace(string(data)))
	if err != nil {
		return r
	}
	r.CommonDir = common
	entries, err := canonical(filepath.Join(common, "worktrees"))
	if err != nil || filepath.Dir(gitdir) != entries || entries != filepath.Join(common, "worktrees") {
		r.Reason = "worktree entry is not a direct child of common/worktrees"
		return r
	}
	data, err = readRegularFile(gitdir, "gitdir", maxMetadataBytes)
	if err != nil {
		r.Reason = "missing or invalid gitdir back-pointer"
		return r
	}
	back, err := relativeTarget(gitdir, strings.TrimSpace(string(data)))
	if err != nil || back != filepath.Join(checkout, ".git") {
		r.Reason = "gitdir back-pointer does not identify this checkout"
		return r
	}
	layout := repositoryKind(common)
	switch {
	case !layout.supported:
		r.Status, r.Reason = VerifiedUnanchored, "repository configuration does not establish a supported main-checkout anchor"
	case layout.bare:
		r.Status, r.Reason, r.Anchor = Verified, "", common
	case layout.customWorktree || filepath.Base(common) != ".git":
		r.Status, r.Reason = VerifiedUnanchored, "separate Git metadata does not identify a supported main-checkout anchor"
	default:
		r.Status, r.Reason, r.Anchor = Verified, "", filepath.Dir(common)
	}
	return r
}

// Relationship reports a verified link, with or without a supported anchor.
func (r Result) Relationship() bool {
	return r.Status == Verified || r.Status == VerifiedUnanchored
}

type repositoryLayout struct {
	bare           bool
	supported      bool
	customWorktree bool
}

// repositoryKind recognizes an unambiguous bare marker and custom layouts.
// It never follows includes or uses core.worktree as an inheritance anchor.
// It reads only config, which sessions mount read-only: writable paths such as
// HEAD or refs must not be able to change the selected anchor.
func repositoryKind(common string) repositoryLayout {
	data, err := readRegularFile(common, "config", maxRepositoryConfigBytes)
	if err != nil {
		return repositoryLayout{}
	}
	section := ""
	layout := repositoryLayout{}
	for _, raw := range strings.Split(string(data), "\n") {
		line, ok := configLine(raw)
		if !ok {
			return repositoryLayout{}
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return repositoryLayout{}
			}
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if strings.HasPrefix(section, "include") {
				return repositoryLayout{}
			}
			continue
		}
		if section != "core" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "worktree" {
			if !ok || strings.TrimSpace(value) == "" || layout.customWorktree {
				return repositoryLayout{}
			}
			// Recognize the custom layout, but never follow its proposed path.
			layout.customWorktree = true
			continue
		}
		if key != "bare" {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if !ok || layout.supported || (value != "true" && value != "false") {
			return repositoryLayout{}
		}
		layout.bare, layout.supported = value == "true", true
	}
	if layout.bare && layout.customWorktree {
		return repositoryLayout{}
	}
	return layout
}

// configLine strips comments outside double quotes without interpreting Git
// values, includes, or continuations. Unsupported syntax cannot select an anchor.
func configLine(raw string) (string, bool) {
	quoted, escaped := false, false
	for i, c := range raw {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && (c == '#' || c == ';') {
			return strings.TrimSpace(raw[:i]), true
		}
	}
	return strings.TrimSpace(raw), !quoted && !escaped
}

func relativeTarget(base, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("empty metadata pointer")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	return canonical(target)
}

func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// readRegularFile refuses symlinks and bounds metadata reads.
func readRegularFile(dir, name string, limit int) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errSymlink
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("file changed while opening: %s", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("metadata file too large: %s", name)
	}
	return data, nil
}
