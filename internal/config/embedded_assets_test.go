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
	"time"

	"enclave/internal/appassets"
	"enclave/internal/model"
	"enclave/internal/util"
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

	for _, readOnly := range []bool{false, true} {
		root, err := discoverAppRoot(readOnly)
		if err == nil && root == legacyRoot {
			t.Fatalf("discoverAppRoot(%v) returned legacy root %q", readOnly, root)
		}
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

func TestFindAppAssetsNeverWrites(t *testing.T) {
	files := testAppAssets("content")
	key, err := appassets.ContentHash(files)
	if err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(t.TempDir(), "cache")

	if _, err := findAppAssets(files, key, cacheRoot); err == nil {
		t.Fatal("found assets in an empty cache")
	}
	if _, err := os.Lstat(cacheRoot); !os.IsNotExist(err) {
		t.Fatalf("lookup created the cache root: %v", err)
	}

	root, err := extractAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	found, err := findAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatalf("find extracted assets: %v", err)
	}
	if found != root {
		t.Fatalf("found root = %q, want %q", found, root)
	}

	pruned := filepath.Join(root, "docs", "README.md")
	if err := os.Remove(pruned); err != nil {
		t.Fatal(err)
	}
	if _, err := findAppAssets(files, key, cacheRoot); err == nil {
		t.Fatal("accepted a pruned cache entry")
	}
	if _, err := os.Lstat(pruned); !os.IsNotExist(err) {
		t.Fatalf("lookup repaired the pruned cache entry: %v", err)
	}
}

func TestExtractAppAssetsRecordsUseAtMostDaily(t *testing.T) {
	files := testAppAssets("content")
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	root := extractTestAppAssets(t, files, cacheRoot)

	recent := backdateAppAssets(t, root, embeddedAssetUseRefreshInterval-time.Hour)
	extractTestAppAssets(t, files, cacheRoot)
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(recent) {
		t.Fatalf("use within the refresh interval rewrote the entry mtime to %v, want %v", info.ModTime(), recent)
	}

	backdateAppAssets(t, root, embeddedAssetUseRefreshInterval+time.Hour)
	extractTestAppAssets(t, files, cacheRoot)
	info, err = os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if age := time.Since(info.ModTime()); age > time.Minute {
		t.Fatalf("use was not recorded: entry mtime is %v old", age)
	}
}

func TestExtractAppAssetsServesPublishedEntryWhenUseCannotBeRecorded(t *testing.T) {
	files := testAppAssets("content")
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	root := extractTestAppAssets(t, files, cacheRoot)
	key := filepath.Base(root)
	// A directory in place of the lock file cannot be opened even as root.
	lockPath := appAssetsLockPath(cacheRoot, key)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	backdateAppAssets(t, root, embeddedAssetUseRefreshInterval+time.Hour)

	got, err := extractAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatalf("published entry failed when its use could not be recorded: %v", err)
	}
	if got != root {
		t.Fatalf("root = %q, want %q", got, root)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := extractAppAssets(files, key, cacheRoot); err == nil {
		t.Fatal("extracted a missing entry without taking its lock")
	}
}

func TestExtractAppAssetsRemovesLongUnusedEntries(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	unusedRoot := extractTestAppAssets(t, testAppAssets("unused"), cacheRoot)
	recentRoot := extractTestAppAssets(t, testAppAssets("recent"), cacheRoot)
	lockedRoot := extractTestAppAssets(t, testAppAssets("locked"), cacheRoot)
	backdateAppAssets(t, unusedRoot, embeddedAssetUnusedRetention+time.Hour)
	backdateAppAssets(t, recentRoot, embeddedAssetUnusedRetention-time.Hour)
	backdateAppAssets(t, lockedRoot, embeddedAssetUnusedRetention+time.Hour)
	release, err := util.AcquireFileLock(appAssetsLockPath(cacheRoot, filepath.Base(lockedRoot)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	extractTestAppAssets(t, testAppAssets("current"), cacheRoot)

	if _, err := os.Lstat(unusedRoot); !os.IsNotExist(err) {
		t.Fatalf("entry unused beyond the retention remains: %v", err)
	}
	if err := validateAppRoot(recentRoot); err != nil {
		t.Fatalf("entry used within the retention was removed: %v", err)
	}
	if err := validateAppRoot(lockedRoot); err != nil {
		t.Fatalf("entry whose lock is held was removed: %v", err)
	}
}

func extractTestAppAssets(t *testing.T, files fstest.MapFS, cacheRoot string) string {
	t.Helper()
	key, err := appassets.ContentHash(files)
	if err != nil {
		t.Fatal(err)
	}
	root, err := extractAppAssets(files, key, cacheRoot)
	if err != nil {
		t.Fatalf("extract assets: %v", err)
	}
	return root
}

func backdateAppAssets(t *testing.T, root string, age time.Duration) time.Time {
	t.Helper()
	when := time.Now().Add(-age).Truncate(time.Second)
	if err := os.Chtimes(root, when, when); err != nil {
		t.Fatal(err)
	}
	return when
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

func TestExternalAppRootFollowsDiscoveryPrecedence(t *testing.T) {
	withTree := t.TempDir()
	if err := writeAppAssets(testAppAssets("adjacent"), withTree); err != nil {
		t.Fatal(err)
	}
	standalone := filepath.Join(t.TempDir(), "bin", "enclave")

	t.Setenv(model.EnvHome, "")
	if root, ok := ExternalAppRoot(filepath.Join(withTree, "bin", "enclave")); !ok || root != withTree {
		t.Fatalf("tree above the executable: got %q, %v; want %q", root, ok, withTree)
	}
	if root, ok := ExternalAppRoot(standalone); ok {
		t.Fatalf("standalone executable reported external assets at %q", root)
	}

	t.Setenv(model.EnvHome, "/opt/enclave-assets")
	if root, ok := ExternalAppRoot(standalone); !ok || root != "/opt/enclave-assets" {
		t.Fatalf("ENCLAVE_HOME: got %q, %v; want it to take precedence", root, ok)
	}
}
