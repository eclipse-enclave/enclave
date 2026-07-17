// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package usercmd discovers user-defined subcommands dropped into
// ~/.config/enclave/commands/{host,session}/. Discovery is intentionally
// side-effect free: it returns warnings as plain strings so callers can log
// them via their own logging facility, keeping this package dependency-light.
package usercmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/config"
	"enclave/internal/model"
)

// Target identifies where a user command executes.
type Target string

const (
	// TargetHost commands exec directly on the host.
	TargetHost Target = "host"
	// TargetSession commands run inside a sandboxed session container.
	TargetSession Target = "session"
)

// Command is a discovered user-defined subcommand.
type Command struct {
	Name   string
	Path   string
	Target Target
}

// Discover scans ~/.config/enclave/commands/{host,session}/ and returns the
// discovered commands together with human-readable warnings. Missing
// directories are not an error. Regular files with any executable bit become
// commands; non-executable regular files produce a warning; subdirectories and
// other non-regular files are skipped silently. Symlinks are followed to their
// target: a link to an executable regular file becomes a command, a link to a
// non-executable file warns, a link to a directory is skipped silently, and a
// broken link warns. Session symlinks are additionally required to be relative
// and to stay inside the session command directory across every hop of the
// link chain. The session directory is bind-mounted at a neutral container path
// (model.UserCommandsContainerDir), so absolute link text does not survive the
// mount and only relative, in-tree chains resolve inside the container; a
// session symlink with an absolute hop, a hop that escapes the directory, or a
// loop warns and is skipped. Host symlinks may point anywhere. When the same
// name exists in both host/ and session/, the host command wins and a warning
// is emitted. Results are returned in deterministic (name-sorted) order.
func Discover(home string) ([]Command, []string) {
	hostCmds, warnings := scanDir(config.HostCommandsHostDir(home), TargetHost)
	sessionCmds, sessionWarnings := scanDir(config.HostCommandsSessionDir(home), TargetSession)
	warnings = append(warnings, sessionWarnings...)

	byName := make(map[string]Command, len(hostCmds)+len(sessionCmds))
	for _, c := range hostCmds {
		byName[c.Name] = c
	}
	for _, c := range sessionCmds {
		if existing, ok := byName[c.Name]; ok {
			warnings = append(warnings, fmt.Sprintf(
				"user command %q defined in both host (%s) and session (%s); using host, ignoring session",
				c.Name, existing.Path, c.Path))
			continue
		}
		byName[c.Name] = c
	}

	cmds := make([]Command, 0, len(byName))
	for _, c := range byName {
		cmds = append(cmds, c)
	}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	return cmds, warnings
}

// scanDir reads a single command directory. Entries are returned by os.ReadDir
// in sorted order, so warnings and commands are already deterministic.
func scanDir(dir string, target Target) ([]Command, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf(
			"cannot read user command directory %q: %v", dir, err)}
	}

	var (
		cmds     []Command
		warnings []string
	)
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skipping user command %q: %v", path, err))
			continue
		}
		isSymlink := info.Mode()&fs.ModeSymlink != 0
		if isSymlink {
			// entry.Info() is lstat-based, so a symlink's own mode (and perm
			// bits) are meaningless. Follow the link and evaluate the target.
			info, err = os.Stat(path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf(
					"skipping user command %q: broken symlink: %v", path, err))
				continue
			}
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"user command %q is not executable; chmod +x or remove it", path))
			continue
		}
		// Session commands run inside a container where only the session
		// command directory is bind-mounted, at a neutral path
		// (model.UserCommandsContainerDir). Host-side containment is not enough:
		// the bind mount carries link text verbatim, so only relative, in-tree
		// link chains resolve inside the container. Validate the chain with
		// container semantics and skip anything that would break at exec time.
		// Host symlinks may point anywhere the host can execute.
		if isSymlink && target == TargetSession {
			if warning := validateSessionSymlink(dir, entry.Name()); warning != "" {
				warnings = append(warnings, warning)
				continue
			}
		}
		cmds = append(cmds, Command{Name: entry.Name(), Path: path, Target: target})
	}
	return cmds, warnings
}

// maxSymlinkHops caps symlink chain traversal, mirroring the Linux ELOOP
// behaviour so loops and pathologically long chains terminate.
const maxSymlinkHops = 40

// validateSessionSymlink resolves the entry dir/name one path component at a
// time using the same semantics the container sees: the session directory is
// bind-mounted at a neutral path, so link text is carried verbatim and only
// relative, in-tree chains resolve inside the container. Resolution is rooted
// at dir, so a symlink in any component (final or intermediate) is validated:
// absolute link text is rejected (it does not exist under the container mount),
// and ".." that would climb above the root is rejected as an escape. It returns
// an empty string when the entry is acceptable, or a human-readable warning
// explaining why it was skipped. Broken/unreadable hops return "" here because
// the os.Stat branch in scanDir already reports final-target breakage.
func validateSessionSymlink(dir, name string) string {
	full := filepath.Join(dir, name)
	// pending holds components still to resolve; resolved holds the in-tree
	// components already resolved, relative to dir.
	pending := splitPathComponents(name)
	var resolved []string
	hops := 0
	for len(pending) > 0 {
		comp := pending[0]
		pending = pending[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return fmt.Sprintf(
					"user session command %q is a symlink whose target escapes the mounted session command directory; use a relative symlink that stays within %q",
					full, dir)
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		candidate := append(append([]string{}, resolved...), comp)
		candidatePath := filepath.Join(dir, filepath.Join(candidate...))
		info, err := os.Lstat(candidatePath)
		if err != nil {
			return ""
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			resolved = candidate
			continue
		}
		hops++
		if hops >= maxSymlinkHops {
			return fmt.Sprintf(
				"user session command %q is a symlink with too many levels of indirection (possible loop); use a direct relative symlink within %q",
				full, dir)
		}
		linkValue, err := os.Readlink(candidatePath)
		if err != nil {
			return ""
		}
		if filepath.IsAbs(linkValue) {
			return fmt.Sprintf(
				"user session command %q is a symlink with an absolute target %q; absolute link targets do not resolve inside the container mount at %q — use a relative symlink within %q",
				full, linkValue, model.UserCommandsContainerDir, dir)
		}
		// The symlink lives at candidate; its relative target resolves from the
		// symlink's parent (resolved), so splice the link components ahead of the
		// remaining pending components without adding comp to resolved.
		pending = append(splitPathComponents(linkValue), pending...)
	}
	return ""
}

// splitPathComponents splits a relative path into its components on the OS path
// separator, preserving ".." and "." so the caller can resolve them against the
// virtual root.
func splitPathComponents(p string) []string {
	return strings.Split(p, string(os.PathSeparator))
}
