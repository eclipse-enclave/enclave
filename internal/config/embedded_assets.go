// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/appassets"
	"enclave/internal/util"
)

const (
	embeddedAssetStoreDir         = "assets"
	embeddedAssetExtractionPrefix = ".extract-"
)

func extractRegisteredAppAssets() (string, error) {
	files, key, err := appassets.Embedded()
	if err != nil {
		return "", err
	}
	home, err := ResolveHostHome()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return extractAppAssets(files, key, hostCacheRoot(home))
}

func extractAppAssets(files fs.FS, key string, cacheRoot string) (string, error) {
	if files == nil {
		return "", fmt.Errorf("asset filesystem is nil")
	}
	if len(key) != 64 {
		return "", fmt.Errorf("invalid embedded asset key %q", key)
	}
	for _, char := range key {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", fmt.Errorf("invalid embedded asset key %q", key)
		}
	}
	if !filepath.IsAbs(cacheRoot) {
		return "", fmt.Errorf("asset cache root is not absolute: %s", cacheRoot)
	}

	assetsDir := filepath.Join(cacheRoot, embeddedAssetStoreDir)
	root := filepath.Join(assetsDir, key)
	if _, err := os.Lstat(root); err == nil {
		if validationErr := validatePublishedAppRoot(root, files); validationErr == nil {
			return root, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect embedded asset cache %s: %w", root, err)
	}

	if err := os.MkdirAll(assetsDir, 0o755); err != nil { // #nosec G301 -- extracted build contexts must be traversable.
		return "", fmt.Errorf("create embedded asset cache %s: %w", assetsDir, err)
	}
	lockPath := filepath.Join(cacheRoot, ".assets-"+key+".lock")
	if err := util.WithFileLock(lockPath, func() error {
		return materializeAppAssets(files, key, assetsDir, root)
	}); err != nil {
		return "", err
	}
	return root, nil
}

func materializeAppAssets(files fs.FS, key string, assetsDir string, root string) error {
	if err := removeInterruptedAppAssetExtractions(assetsDir, key); err != nil {
		return err
	}

	if _, err := os.Lstat(root); err == nil {
		if validationErr := validatePublishedAppRoot(root, files); validationErr == nil {
			return nil
		}
		if err := os.RemoveAll(root); err != nil { // #nosec G703 -- root is a validated hash entry under the cache directory.
			return fmt.Errorf("remove invalid embedded asset cache %s: %w", root, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect embedded asset cache %s: %w", root, err)
	}

	tempRoot, err := os.MkdirTemp(assetsDir, embeddedAssetExtractionPrefix+key+"-")
	if err != nil {
		return fmt.Errorf("create temporary embedded asset directory: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tempRoot) // #nosec G703 -- tempRoot is returned by os.MkdirTemp above.
	}()

	if err := os.Chmod(tempRoot, 0o755); err != nil { // #nosec G302 -- extracted build contexts must be traversable.
		return fmt.Errorf("set temporary embedded asset directory mode: %w", err)
	}
	if err := writeAppAssets(files, tempRoot); err != nil {
		return err
	}
	if err := validatePublishedAppRoot(tempRoot, files); err != nil {
		return fmt.Errorf("validate extracted assets: %w", err)
	}

	if err := os.Rename(tempRoot, root); err != nil {
		if validationErr := validatePublishedAppRoot(root, files); validationErr == nil {
			return nil
		}
		return fmt.Errorf("publish embedded assets to %s: %w", root, err)
	}
	return nil
}

func removeInterruptedAppAssetExtractions(assetsDir string, key string) error {
	prefix := embeddedAssetExtractionPrefix + key + "-"
	entries, err := os.ReadDir(assetsDir)
	if err != nil {
		return fmt.Errorf("read embedded asset cache %s: %w", assetsDir, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		path := filepath.Join(assetsDir, entry.Name())
		if err := os.RemoveAll(path); err != nil { // #nosec G703 -- path is a direct child returned by os.ReadDir above.
			return fmt.Errorf("remove interrupted embedded asset extraction %s: %w", path, err)
		}
	}
	return nil
}

func validatePublishedAppRoot(root string, files fs.FS) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("not a real directory")
	}
	if err := validateAppRoot(root); err != nil {
		return err
	}
	for _, entry := range []appRootRequiredEntry{
		{rel: ".dockerignore", label: ".dockerignore"},
		{rel: "docs", label: "documentation directory", isDir: true},
	} {
		path := filepath.Join(root, entry.rel)
		entryInfo, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s not found at %s: %w", entry.label, path, err)
		}
		if entryInfo.IsDir() != entry.isDir {
			return fmt.Errorf("%s has the wrong file type at %s", entry.label, path)
		}
	}
	return validateEmbeddedAssetsPresent(root, files)
}

// validateEmbeddedAssetsPresent verifies every embedded asset exists with the
// expected type, so a partially pruned cache entry is re-extracted instead of
// serving an incomplete build context.
func validateEmbeddedAssetsPresent(root string, files fs.FS) error {
	return fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if !fs.ValidPath(name) {
			return fmt.Errorf("invalid embedded asset path %q", name)
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("embedded asset %s is missing: %w", name, err)
		}
		if entry.IsDir() {
			if !info.IsDir() {
				return fmt.Errorf("embedded asset %s is not a directory", name)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("embedded asset %s is not a regular file", name)
		}
		return nil
	})
}

func writeAppAssets(files fs.FS, root string) error {
	return fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if !fs.ValidPath(name) {
			return fmt.Errorf("invalid embedded asset path %q", name)
		}

		target := filepath.Join(root, filepath.FromSlash(name))
		if entry.IsDir() {
			if err := os.Mkdir(target, 0o755); err != nil { // #nosec G301 -- build context directories must be traversable.
				return fmt.Errorf("create embedded asset directory %s: %w", name, err)
			}
			if err := os.Chmod(target, 0o755); err != nil { // #nosec G302 -- build context directories must be traversable.
				return fmt.Errorf("set embedded asset directory mode %s: %w", name, err)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("embedded asset %s is not a regular file", name)
		}

		source, err := files.Open(name)
		if err != nil {
			return fmt.Errorf("open embedded asset %s: %w", name, err)
		}
		mode := appassets.FileMode(name)
		// #nosec G304 -- target is under a fresh temp root and name passed fs.ValidPath above.
		destination, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			_ = source.Close()
			return fmt.Errorf("create embedded asset %s: %w", name, err)
		}
		_, copyErr := io.Copy(destination, source)
		closeDestinationErr := destination.Close()
		closeSourceErr := source.Close()
		if copyErr != nil {
			return fmt.Errorf("write embedded asset %s: %w", name, copyErr)
		}
		if closeDestinationErr != nil {
			return fmt.Errorf("close embedded asset %s: %w", name, closeDestinationErr)
		}
		if closeSourceErr != nil {
			return fmt.Errorf("close embedded source %s: %w", name, closeSourceErr)
		}
		if err := os.Chmod(target, mode); err != nil {
			return fmt.Errorf("set embedded asset mode %s: %w", name, err)
		}
		return nil
	})
}
