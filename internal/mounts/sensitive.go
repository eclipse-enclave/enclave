// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mounts

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/util"
)

// SensitivePaths decides whether bind-mounting a host directory would expose
// sensitive user data. A directory is sensitive if it is the home directory or
// one of its ancestors, or if it is, lies inside, or contains a protected
// location: a hidden top-level entry of home (where it really lives, when it
// is a symlink), a per-user application data directory, one of Enclave's own
// roots, or the SSH agent socket.
//
// Because paths form a tree, every subdirectory of a directory that is not
// sensitive is not sensitive either. The zero value protects nothing.
//
// Home and its ancestors, Enclave's own roots, and the SSH agent socket are
// hard-protected: no sensitive-dir exception covers a directory that is,
// lies inside, or contains one. A mount of Enclave's roots would let the
// agent widen its own exceptions or add host commands, and the agent socket
// signs with the user's keys.
type SensitivePaths struct {
	home      string
	protected []protectedPath
}

type protectedPath struct {
	label string
	path  string
	hard  bool
}

// NewSensitivePaths collects the protected locations for home. appDataDirs are
// the per-user application data directories, as config.HostAppDataDirs lists
// them, and enclaveRoots are Enclave's own config, state, and cache roots, as
// config.HostEnclaveRoots lists them. Locations that cannot be resolved stay
// protected under their cleaned path.
func NewSensitivePaths(home string, appDataDirs []string, enclaveRoots []string) SensitivePaths {
	var s SensitivePaths
	if filepath.IsAbs(home) {
		s.home = resolveOrClean(home)
		s.addHiddenEntries()
		for _, dir := range appDataDirs {
			s.add(dir, nil, false)
		}
		for _, dir := range enclaveRoots {
			s.add(dir, s.targetOf(dir), true)
		}
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		s.add(sock, func(shown string) string { return "the SSH agent socket $SSH_AUTH_SOCK (" + shown + ")" }, true)
	}
	return s
}

// Reason returns why bind-mounting dir would expose sensitive data, or "" if
// it would not. dir must be absolute with its symlinks resolved, as validated
// mount sources are.
func (s SensitivePaths) Reason(dir string) string {
	return s.reason(filepath.Clean(dir), false)
}

// reason is Reason, restricted to hard-protected locations if hardOnly. Both
// scan in order and return the first match, so the first location Reason
// matches need not be hard.
func (s SensitivePaths) reason(dir string, hardOnly bool) string {
	if reason := s.homeReason(dir); reason != "" {
		return reason
	}
	for _, p := range s.protected {
		if p.hard || !hardOnly {
			if reason := p.reason(dir); reason != "" {
				return reason
			}
		}
	}
	return ""
}

func (s SensitivePaths) homeReason(dir string) string {
	switch {
	case s.home == "" || !util.PathWithin(dir, s.home):
		return ""
	case dir == s.home:
		return "is your home directory"
	default:
		return "contains your home directory"
	}
}

func (p protectedPath) reason(dir string) string {
	switch {
	case dir == p.path:
		return "is " + p.label
	case util.PathWithin(p.path, dir):
		return "is inside " + p.label
	case util.PathWithin(dir, p.path):
		return "contains " + p.label
	}
	return ""
}

// SensitiveDirException lets a sensitive directory and everything below it be
// mounted. It never covers a parent of Path.
type SensitiveDirException struct {
	// Path is absolute, with its symlinks resolved where it exists.
	Path string
	// Origin names where the exception was set, for messages.
	Origin string
}

// NewSensitiveDirException exempts the subtree at path, which must be
// absolute. A path that does not exist yet keeps its missing tail under its
// resolved parent, so it covers the directory once it is created there.
func NewSensitiveDirException(path, origin string) SensitiveDirException {
	return SensitiveDirException{Path: resolveOrClean(path), Origin: origin}
}

// Assessment is how the rule and the sensitive-dir exceptions apply to one
// mount source.
type Assessment struct {
	// Reason is why mounting the directory would expose sensitive data, as
	// Reason returns it; "" if it would not.
	Reason string
	// HardReason names a hard-protected location the directory involves,
	// which no exception can cover; "" if there is none.
	HardReason string
	// Exception is the first exception covering the directory, or nil.
	Exception *SensitiveDirException
}

// Assess checks dir against the rule and the exceptions. dir must be absolute
// with its symlinks resolved, as validated mount sources are.
func (s SensitivePaths) Assess(dir string, exceptions []SensitiveDirException) Assessment {
	dir = filepath.Clean(dir)
	a := Assessment{Reason: s.reason(dir, false)}
	if a.Reason == "" {
		return a
	}
	a.HardReason = s.reason(dir, true)
	for i := range exceptions {
		if util.PathWithin(exceptions[i].Path, dir) {
			a.Exception = &exceptions[i]
			break
		}
	}
	return a
}

func (s *SensitivePaths) addHiddenEntries() {
	entries, err := os.ReadDir(s.home)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(s.home, entry.Name())
		if entry.Type()&fs.ModeSymlink == 0 {
			// Already resolved: home is. Not stat'ing it also keeps a hung
			// FUSE mount point from blocking the check.
			s.protect(path, nil, false)
			continue
		}
		s.add(path, s.targetOf(path), false)
	}
}

// targetOf labels a location that resolves elsewhere with the path it was
// reached through, as in "~/.dotfiles/config (target of ~/.config)".
func (s SensitivePaths) targetOf(path string) func(shown string) string {
	link := filepath.Clean(path)
	return func(shown string) string {
		if shown == s.display(link) {
			return shown
		}
		return shown + " (target of " + s.display(link) + ")"
	}
}

// add protects path, hard-protected if hard. label turns the resolved path,
// as shown in messages, into the location's description; nil uses the path
// alone.
func (s *SensitivePaths) add(path string, label func(shown string) string, hard bool) {
	if !filepath.IsAbs(path) {
		return
	}
	s.protect(resolveOrClean(path), label, hard)
}

func (s *SensitivePaths) protect(real string, label func(shown string) string, hard bool) {
	// Home and its ancestors are covered by the home rule. Protecting them as
	// locations would make everything under home sensitive.
	if s.home != "" && util.PathWithin(real, s.home) {
		return
	}
	shown := s.display(real)
	if label != nil {
		shown = label(shown)
	}
	s.protected = append(s.protected, protectedPath{label: shown, path: real, hard: hard})
}

// display shortens paths under home to ~/... for messages.
func (s SensitivePaths) display(path string) string {
	if s.home == "" || !util.PathStrictlyWithin(s.home, path) {
		return path
	}
	rel, err := filepath.Rel(s.home, path)
	if err != nil {
		return path
	}
	return filepath.Join("~", rel)
}

func resolveOrClean(path string) string {
	if real, err := util.RealPath(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}
