// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/config"
)

// overlayConfigDir replaces targetDir's contents with sourceDir's contents,
// preserving the paths matched by preservePaths across the swap. It ports the
// former in-container overlay shell to host filesystem operations: the
// preserve/wipe/repopulate/restore ordering and the path-matching semantics
// are unchanged.
func overlayConfigDir(targetDir string, sourceDir string, preservePaths []string) error {
	if strings.TrimSpace(targetDir) == "" {
		return errors.New("overlay target dir is empty")
	}
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return err
	}

	preserveDir, err := os.MkdirTemp("", "enclave-config-preserve-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(preserveDir) }()

	if err := stagePreservedPaths(targetDir, preserveDir, preservePaths); err != nil {
		return err
	}
	if err := clearDirContents(targetDir); err != nil {
		return err
	}
	if err := copyDirContents(sourceDir, targetDir); err != nil {
		return err
	}
	// Restore preserved state on top of the freshly repopulated source so
	// session state (auth files, resume state) survives the overlay.
	return copyDirContents(preserveDir, targetDir)
}

// stagePreservedPaths copies every top-level preserved node (a path that
// matches a preserve pattern and whose ancestors do not) from targetDir into
// preserveDir. A matched directory is staged wholesale, so its children are not
// enumerated separately: checking ancestor matches instead of preserve staging
// existence avoids treating an unrelated sibling as already handled.
func stagePreservedPaths(targetDir string, preserveDir string, preservePaths []string) error {
	return filepath.WalkDir(targetDir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == targetDir {
			return nil
		}
		rel, err := filepath.Rel(targetDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !preserveMatch(rel, preservePaths) || preserveHasMatchingParent(rel, preservePaths) {
			return nil
		}
		return copyPath(p, filepath.Join(preserveDir, filepath.FromSlash(rel)))
	})
}

// preserveMatch reports whether rel matches any preserve pattern, using the
// same directory-prefix / glob / exact matching rules as the host-config
// passthrough deny-list.
func preserveMatch(rel string, preservePaths []string) bool {
	for _, pattern := range preservePaths {
		if config.HostConfigPathMatches(rel, pattern) {
			return true
		}
	}
	return false
}

// preserveHasMatchingParent reports whether a strict ancestor of rel matches a
// preserve pattern (meaning rel is copied wholesale with that ancestor).
func preserveHasMatchingParent(rel string, preservePaths []string) bool {
	for parent := parentPath(rel); parent != ""; parent = parentPath(parent) {
		if preserveMatch(parent, preservePaths) {
			return true
		}
	}
	return false
}

func parentPath(rel string) string {
	idx := strings.LastIndex(rel, "/")
	if idx <= 0 {
		return ""
	}
	return rel[:idx]
}

// copyPath copies src (file, directory tree, or symlink) to dst, replicating
// `cp -a`: symlinks are recreated as symlinks (not dereferenced), directories
// are merged into dst, and file permissions are carried over.
func copyPath(src string, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return copySymlink(src, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	default:
		return copyRegularFile(src, dst, info.Mode().Perm())
	}
}

func copySymlink(src string, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(target, dst)
}

func copyRegularFile(src string, dst string, mode fs.FileMode) error {
	data, err := os.ReadFile(src) // #nosec G304 -- src is within the enclave config store being overlaid.
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode) // #nosec G703 -- dst is within the runtime-managed config store overlay.
}

// clearDirContents removes every entry inside dir while keeping dir itself.
func clearDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyDirContents copies each top-level entry of src into dst (equivalent to
// `cp -a src/. dst/`). A missing src is a no-op.
func copyDirContents(src string, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyPath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
