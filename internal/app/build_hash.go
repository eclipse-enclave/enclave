// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"enclave/internal/model"
	"enclave/internal/util"
)

func hashRuntimeImageStaticFiles(paths model.Paths) (string, error) {
	files, err := runtimeImageStaticFiles(paths)
	if err != nil {
		return "", err
	}
	return hashRuntimeInputFiles(files)
}

func hashRuntimeInputFiles(files []mergedExtensionFile) (string, error) {
	var combined strings.Builder
	for _, file := range files {
		fileHash, err := util.HashFile(file.SourcePath)
		if err != nil {
			return "", err
		}
		combined.WriteString(file.RelativePath)
		combined.WriteString("\n")
		combined.WriteString(fileHash)
		combined.WriteString("\n")
		_, _ = fmt.Fprintf(&combined, "%#o", file.Mode.Perm())
		combined.WriteString("\n")
	}
	return util.HashString(combined.String()), nil
}

// qemuBundleAssetHash hashes the microVM-specific runtime assets (the bundle
// builder and guest init) that shape a qemu bundle but are not part of the
// shared runtime-image hash. Without folding this into the bundle freshness
// key, editing the builder or init would leave stamped bundles marked current
// and never rebuilt.
func qemuBundleAssetHash(appRoot string) (string, error) {
	if strings.TrimSpace(appRoot) == "" {
		return "", fmt.Errorf("qemu bundle asset hash: empty app root")
	}
	root := filepath.Join(appRoot, "runtime-assets", "microvm")
	var files []mergedExtensionFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil // no microVM assets: hash the empty set deterministically
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(appRoot, path)
		if err != nil {
			return err
		}
		files = append(files, mergedExtensionFile{
			RelativePath: filepath.ToSlash(rel),
			SourcePath:   path,
			Mode:         info.Mode(),
		})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash qemu bundle assets: %w", err)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	return hashRuntimeInputFiles(files)
}

func runtimeImageStaticFiles(paths model.Paths) ([]mergedExtensionFile, error) {
	matcher, err := dockerignoreMatcher(paths.AppRoot)
	if err != nil {
		return nil, err
	}

	relPaths := []string{
		".dockerignore",
		filepath.ToSlash(filepath.Join("runtime-assets", "build-scripts")),
		filepath.ToSlash(filepath.Join("runtime-assets", "auth-reconcile.sh")),
		filepath.ToSlash(filepath.Join("runtime-assets", "net.sh")),
		filepath.ToSlash(filepath.Join("runtime-assets", "kit-init.sh")),
		"docs",
	}
	files := make([]mergedExtensionFile, 0)
	for _, rel := range relPaths {
		if err := collectRuntimeImageStaticFiles(paths.AppRoot, rel, matcher, &files); err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	return files, nil
}

func dockerignoreMatcher(appRoot string) (*patternmatcher.PatternMatcher, error) {
	if strings.TrimSpace(appRoot) == "" {
		return nil, nil
	}
	dockerignorePath := filepath.Join(appRoot, ".dockerignore")
	// #nosec G304 -- dockerignorePath is rooted in the resolved application root.
	file, err := os.Open(dockerignorePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	patterns, err := ignorefile.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return nil, nil
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, err
	}
	return matcher, nil
}

func collectRuntimeImageStaticFiles(appRoot string, rel string, matcher *patternmatcher.PatternMatcher, files *[]mergedExtensionFile) error {
	if strings.TrimSpace(appRoot) == "" || strings.TrimSpace(rel) == "" {
		return nil
	}
	cleanRel := filepath.ToSlash(filepath.Clean(rel))
	source := filepath.Join(appRoot, filepath.FromSlash(cleanRel))
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return appendRuntimeImageStaticFile(source, cleanRel, matcher, files)
	}

	return filepath.WalkDir(source, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !util.PathWithin(appRoot, path) {
			return fmt.Errorf("runtime image static file %s is outside app root %s", path, appRoot)
		}
		relPath, err := filepath.Rel(appRoot, path)
		if err != nil {
			return err
		}
		return appendRuntimeImageStaticFile(path, filepath.ToSlash(relPath), matcher, files)
	})
}

func appendRuntimeImageStaticFile(path string, rel string, matcher *patternmatcher.PatternMatcher, files *[]mergedExtensionFile) error {
	cleanRel := filepath.ToSlash(filepath.Clean(rel))
	if cleanRel != ".dockerignore" && matcher != nil {
		excluded, err := matcher.MatchesOrParentMatches(cleanRel)
		if err != nil {
			return err
		}
		if excluded {
			return nil
		}
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return nil
	}
	*files = append(*files, mergedExtensionFile{
		RelativePath: cleanRel,
		SourcePath:   path,
		Mode:         stat.Mode(),
	})
	return nil
}
