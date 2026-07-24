// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package legacyassets safely removes files staged by the former source
// installation layout.
package legacyassets

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Result summarizes a cleanup attempt. A skipped cleanup never changes root.
type Result struct {
	Removed    int
	SkipReason string
}

// Clean removes only files whose exact paths are present in knownAssets. It
// never follows symlinks, recursively removes a directory, or removes root.
// Unknown files and non-empty directories are preserved.
func Clean(root string, home string, knownAssets fs.FS) (Result, error) {
	var result Result
	if strings.TrimSpace(root) == "" {
		return result, fmt.Errorf("legacy asset root is empty")
	}
	if strings.TrimSpace(home) == "" {
		return result, fmt.Errorf("home directory is empty")
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(home) {
		return result, fmt.Errorf("legacy asset root and home directory must be absolute")
	}
	root = filepath.Clean(root)
	home = filepath.Clean(home)
	if isFilesystemRoot(root) {
		return result, fmt.Errorf("refusing to clean filesystem root %s", root)
	}
	if root == home {
		return result, fmt.Errorf("refusing to clean home directory %s", root)
	}
	insideHome, err := pathWithin(home, root)
	if err != nil {
		return result, err
	}
	if !insideHome {
		return result, fmt.Errorf("refusing to clean %s because it is outside home directory %s", root, home)
	}
	if knownAssets == nil {
		return result, fmt.Errorf("known asset filesystem is nil")
	}

	if reason, err := unsafeRootReason(home, root); err != nil {
		return result, err
	} else if reason != "" {
		result.SkipReason = reason
		return result, nil
	}
	if !looksLikeLegacyAppRoot(root) {
		result.SkipReason = "directory is not a complete legacy Enclave asset root"
		return result, nil
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		result.SkipReason = "directory contains .git and may be a source checkout"
		return result, nil
	} else if !os.IsNotExist(err) {
		return result, fmt.Errorf("inspect source-control marker: %w", err)
	}

	files, directories, err := knownAssetPaths(knownAssets)
	if err != nil {
		return result, err
	}
	files = append(files, "AGENTS.md", "CLAUDE.md")
	for _, name := range files {
		removed, err := removeKnownFile(root, name)
		if err != nil {
			return result, err
		}
		if removed {
			result.Removed++
		}
	}
	for _, name := range directories {
		removed, err := removeKnownDirectoryIfEmpty(root, name)
		if err != nil {
			return result, err
		}
		if removed {
			result.Removed++
		}
	}
	return result, nil
}

func isFilesystemRoot(name string) bool {
	volume := filepath.VolumeName(name)
	return name == volume+string(filepath.Separator)
}

func pathWithin(parent string, child string) (bool, error) {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false, fmt.Errorf("compare cleanup paths: %w", err)
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

func unsafeRootReason(home string, root string) (string, error) {
	homeInfo, err := os.Lstat(home)
	if os.IsNotExist(err) {
		return "home directory does not exist", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect home directory %s: %w", home, err)
	}
	if homeInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Sprintf("home directory %s is a symlink", home), nil
	}
	if !homeInfo.IsDir() {
		return fmt.Sprintf("home path %s is not a directory", home), nil
	}

	rel, err := filepath.Rel(home, root)
	if err != nil {
		return "", fmt.Errorf("resolve legacy asset root relative to home: %w", err)
	}
	current := home
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return "legacy asset root does not exist", nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect legacy asset path %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Sprintf("legacy asset path %s is a symlink", current), nil
		}
		if !info.IsDir() {
			return fmt.Sprintf("legacy asset path %s is not a directory", current), nil
		}
	}
	return "", nil
}

func looksLikeLegacyAppRoot(root string) bool {
	for _, name := range []string{"Dockerfile", "Dockerfile.gateway", "entrypoint.sh", "gateway-entrypoint.sh"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	for _, name := range []string{"extensions", "runtime-assets"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func knownAssetPaths(knownAssets fs.FS) ([]string, []string, error) {
	var files []string
	var directories []string
	err := fs.WalkDir(knownAssets, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if !fs.ValidPath(name) {
			return fmt.Errorf("invalid known asset path %q", name)
		}
		if entry.IsDir() {
			directories = append(directories, name)
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("known asset %s is not a regular file", name)
		}
		files = append(files, name)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate known legacy assets: %w", err)
	}
	sort.Strings(files)
	sort.Slice(directories, func(i int, j int) bool {
		leftDepth := strings.Count(directories[i], "/")
		rightDepth := strings.Count(directories[j], "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return directories[i] > directories[j]
	})
	return files, directories, nil
}

func removeKnownFile(root string, name string) (bool, error) {
	target, safe, err := safeTarget(root, name)
	if err != nil || !safe {
		return false, err
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect legacy asset %s: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if err := os.Remove(target); err != nil {
		return false, fmt.Errorf("remove legacy asset %s: %w", target, err)
	}
	return true, nil
}

func removeKnownDirectoryIfEmpty(root string, name string) (bool, error) {
	target, safe, err := safeTarget(root, name)
	if err != nil || !safe {
		return false, err
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect legacy asset directory %s: %w", target, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	if err := os.Remove(target); err != nil {
		// A non-empty directory is expected when it contains an unknown file. It
		// is safer to preserve any failed directory removal than to classify
		// platform-specific ENOTEMPTY errors and retry recursively.
		if current, statErr := os.Lstat(target); statErr == nil && current.IsDir() {
			return false, nil
		}
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("remove empty legacy asset directory %s: %w", target, err)
	}
	return true, nil
}

func safeTarget(root string, name string) (string, bool, error) {
	if !fs.ValidPath(name) || name == "." {
		return "", false, fmt.Errorf("invalid known asset path %q", name)
	}
	parts := strings.Split(name, "/")
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("inspect legacy asset parent %s: %w", current, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", false, nil
		}
	}
	return filepath.Join(root, filepath.FromSlash(name)), true, nil
}
