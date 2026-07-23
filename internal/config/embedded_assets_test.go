// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"testing/fstest"

	"enclave/internal/appassets"
	"enclave/internal/model"
)

func TestDiscoverAppRootDoesNotUseLegacyDataRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("ENCLAVE_HOME", "")
	legacyRoot := legacyDataRootForTest(home)
	if err := os.MkdirAll(legacyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeAppAssets(testAppAssets("legacy"), legacyRoot); err != nil {
		t.Fatal(err)
	}
	if err := validateAppRoot(legacyRoot); err != nil {
		t.Fatalf("test legacy root is invalid: %v", err)
	}

	root, err := discoverAppRoot()
	if err == nil && root == legacyRoot {
		t.Fatalf("discoverAppRoot returned legacy root %q", root)
	}
}

func legacyDataRootForTest(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", model.AppID, "data")
	}
	return filepath.Join(xdgBaseDir("XDG_DATA_HOME", home, filepath.Join(".local", "share")), model.AppName)
}

func TestExtractAppAssets(t *testing.T) {
	files := testAppAssets("first")
	key, err := appassets.ContentHash(files)
	if err != nil {
		t.Fatalf("hash assets: %v", err)
	}
	cacheRoot := filepath.Join(t.TempDir(), "cache")

	root, err := extractAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatalf("extract assets: %v", err)
	}
	wantRoot := filepath.Join(cacheRoot, embeddedAssetStoreDir, key)
	if root != wantRoot {
		t.Fatalf("root = %q, want %q", root, wantRoot)
	}
	if err := validateAppRoot(root); err != nil {
		t.Fatalf("extracted root is invalid: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "docs", "README.md"))
	if err != nil {
		t.Fatalf("read extracted documentation: %v", err)
	}
	if string(content) != "first" {
		t.Fatalf("documentation content = %q, want first", content)
	}

	if runtime.GOOS != "windows" {
		assertExtractedMode(t, root, "entrypoint.sh", 0o755)
		assertExtractedMode(t, root, "gateway-entrypoint.sh", 0o755)
		assertExtractedMode(t, root, "extensions/tools/test/install.sh", 0o755)
		assertExtractedMode(t, root, "runtime-assets/build-scripts/run.sh", 0o755)
		assertExtractedMode(t, root, "runtime-assets/build-scripts/bin/helper", 0o755)
		assertExtractedMode(t, root, "runtime-assets/microvm/alpine/build-bundle.sh", 0o755)
		assertExtractedMode(t, root, "runtime-assets/microvm/alpine/init", 0o755)
		assertExtractedMode(t, root, "docs/README.md", 0o644)
	}
}

func TestExtractAppAssetsConcurrentFirstRun(t *testing.T) {
	files := testAppAssets("concurrent")
	key, err := appassets.ContentHash(files)
	if err != nil {
		t.Fatalf("hash assets: %v", err)
	}
	cacheRoot := filepath.Join(t.TempDir(), "cache")

	const workers = 12
	roots := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			roots[i], errs[i] = extractAppAssets(files, key, cacheRoot)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		if roots[i] != roots[0] {
			t.Fatalf("worker %d root = %q, want %q", i, roots[i], roots[0])
		}
	}
	entries, err := os.ReadDir(filepath.Join(cacheRoot, embeddedAssetStoreDir))
	if err != nil {
		t.Fatalf("read asset cache directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != key {
		t.Fatalf("asset cache entries = %v, want only %s", entryNames(entries), key)
	}
}

func TestExtractAppAssetsRemovesInterruptedExtraction(t *testing.T) {
	files := testAppAssets("content")
	key, err := appassets.ContentHash(files)
	if err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	assetsDir := filepath.Join(cacheRoot, embeddedAssetStoreDir)
	interruptedRoot := filepath.Join(assetsDir, embeddedAssetExtractionPrefix+key+"-abandoned")
	if err := os.MkdirAll(interruptedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interruptedRoot, "partial"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := extractAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatalf("extract assets: %v", err)
	}
	if _, err := os.Lstat(interruptedRoot); !os.IsNotExist(err) {
		t.Fatalf("interrupted extraction remains: %v", err)
	}
	if err := validatePublishedAppRoot(root, files); err != nil {
		t.Fatalf("extracted root is invalid: %v", err)
	}
}

func TestExtractAppAssetsUsesDistinctAppendOnlyCacheEntries(t *testing.T) {
	firstFiles := testAppAssets("first")
	secondFiles := testAppAssets("second")
	firstKey, err := appassets.ContentHash(firstFiles)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := appassets.ContentHash(secondFiles)
	if err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(t.TempDir(), "cache")

	firstRoot, err := extractAppAssets(firstFiles, firstKey, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot, err := extractAppAssets(secondFiles, secondKey, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if firstRoot == secondRoot {
		t.Fatalf("distinct content used the same root %q", firstRoot)
	}
	firstContent, err := os.ReadFile(filepath.Join(firstRoot, "docs", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstContent) != "first" {
		t.Fatalf("first cache entry was modified: %q", firstContent)
	}
}

func TestExtractAppAssetsRepairsCacheEntrySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	files := testAppAssets("content")
	key, err := appassets.ContentHash(files)
	if err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	assetsDir := filepath.Join(cacheRoot, embeddedAssetStoreDir)
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeAppAssets(files, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assetsDir, key)); err != nil {
		t.Fatal(err)
	}

	root, err := extractAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatalf("repair cache entry symlink: %v", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("repaired cache entry is not a real directory: %v", info.Mode())
	}
	content, err := os.ReadFile(filepath.Join(outside, "docs", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "content" {
		t.Fatalf("symlink target was modified: %q", content)
	}
}

func TestExtractAppAssetsRepairsInvalidCacheEntry(t *testing.T) {
	files := testAppAssets("content")
	key, err := appassets.ContentHash(files)
	if err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	root := filepath.Join(cacheRoot, embeddedAssetStoreDir, key)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "do-not-remove")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	repairedRoot, err := extractAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatalf("repair invalid cache entry: %v", err)
	}
	if repairedRoot != root {
		t.Fatalf("repaired root = %q, want %q", repairedRoot, root)
	}
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("invalid cache contents remain: %v", err)
	}
	if err := validatePublishedAppRoot(root, files); err != nil {
		t.Fatalf("repaired cache entry is invalid: %v", err)
	}
}

func TestExtractAppAssetsRepairsPrunedCacheEntry(t *testing.T) {
	files := testAppAssets("content")
	key, err := appassets.ContentHash(files)
	if err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	root, err := extractAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	pruned := filepath.Join(root, "docs", "README.md")
	if err := os.Remove(pruned); err != nil {
		t.Fatal(err)
	}

	repairedRoot, err := extractAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatalf("repair pruned cache entry: %v", err)
	}
	if repairedRoot != root {
		t.Fatalf("repaired root = %q, want %q", repairedRoot, root)
	}
	content, err := os.ReadFile(pruned)
	if err != nil {
		t.Fatalf("pruned file was not restored: %v", err)
	}
	if string(content) != "content" {
		t.Fatalf("restored content = %q, want %q", content, "content")
	}
}

func testAppAssets(docContent string) fstest.MapFS {
	return fstest.MapFS{
		".dockerignore":                                 {Data: []byte("bin\n")},
		"Dockerfile":                                    {Data: []byte("FROM scratch\n")},
		"Dockerfile.gateway":                            {Data: []byte("FROM scratch\n")},
		"entrypoint.sh":                                 {Data: []byte("#!/bin/sh\n")},
		"gateway-entrypoint.sh":                         {Data: []byte("#!/bin/sh\n")},
		"docs/README.md":                                {Data: []byte(docContent)},
		"extensions/tools/test/spec.yaml":               {Data: []byte("name: test\n")},
		"extensions/tools/test/install.sh":              {Data: []byte("#!/bin/sh\n")},
		"runtime-assets/gateway-allowlists/base.conf":   {Data: []byte("server=/test/1.1.1.1\n")},
		"runtime-assets/build-scripts/run.sh":           {Data: []byte("#!/bin/sh\n")},
		"runtime-assets/build-scripts/bin/helper":       {Data: []byte("#!/bin/sh\n")},
		"runtime-assets/microvm/alpine/build-bundle.sh": {Data: []byte("#!/bin/sh\n")},
		"runtime-assets/microvm/alpine/init":            {Data: []byte("#!/bin/sh\n")},
	}
}

func assertExtractedMode(t *testing.T, root string, rel string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %#o, want %#o", rel, got, want)
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
