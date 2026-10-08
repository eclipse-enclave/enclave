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

	"enclave/internal/config"
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
type SensitivePaths struct {
	home      string
	protected []protectedPath
}

type protectedPath struct {
	label string
	path  string
}

// NewSensitivePaths collects the protected locations for home. Locations that
// cannot be resolved stay protected under their cleaned path.
func NewSensitivePaths(home string) SensitivePaths {
	var s SensitivePaths
	if filepath.IsAbs(home) {
		s.home = resolveOrClean(home)
		s.addHiddenEntries()
		for _, dir := range config.HostAppDataDirs(s.home) {
			s.add(dir, nil)
		}
		// Listed on their own: an XDG override such as XDG_CONFIG_HOME=$HOME
		// would otherwise leave them unprotected.
		for _, dir := range []string{config.HostConfigRootDir(s.home), config.HostStateRootDir(s.home), config.HostCacheDir(s.home)} {
			s.add(dir, nil)
		}
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		s.add(sock, func(shown string) string { return "the SSH agent socket $SSH_AUTH_SOCK (" + shown + ")" })
	}
	return s
}

// Reason returns why bind-mounting dir would expose sensitive data, or "" if
// it would not. dir must be absolute with its symlinks resolved, as validated
// mount sources are.
func (s SensitivePaths) Reason(dir string) string {
	dir = filepath.Clean(dir)
	if s.home != "" && util.PathWithin(dir, s.home) {
		if dir == s.home {
			return "is your home directory"
		}
		return "contains your home directory"
	}
	for _, p := range s.protected {
		switch {
		case dir == p.path:
			return "is " + p.label
		case util.PathWithin(p.path, dir):
			return "is inside " + p.label
		case util.PathWithin(dir, p.path):
			return "contains " + p.label
		}
	}
	return ""
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
			s.protect(path, nil)
			continue
		}
		link := s.display(path)
		s.add(path, func(shown string) string { return shown + " (target of " + link + ")" })
	}
}

// add protects path. label turns the resolved path, as shown in messages, into
// the location's description; nil uses the path alone.
func (s *SensitivePaths) add(path string, label func(shown string) string) {
	if !filepath.IsAbs(path) {
		return
	}
	s.protect(resolveOrClean(path), label)
}

func (s *SensitivePaths) protect(real string, label func(shown string) string) {
	// Home and its ancestors are covered by the home rule. Protecting them as
	// locations would make everything under home sensitive.
	if s.home != "" && util.PathWithin(real, s.home) {
		return
	}
	shown := s.display(real)
	if label != nil {
		shown = label(shown)
	}
	s.protected = append(s.protected, protectedPath{label: shown, path: real})
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
