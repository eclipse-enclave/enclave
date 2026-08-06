// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package legacyassets

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
)

func TestCleanRemovesOnlyKnownFilesAndEmptyDirectories(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "enclave")
	known := cleanupTestAssets()
	writeKnownAssets(t, root, known)

	unknownRoot := filepath.Join(root, "keep-me.txt")
	unknownNested := filepath.Join(root, "extensions", "tools", "custom", "user-file")
	writeCleanupFile(t, unknownRoot, "root sentinel")
	writeCleanupFile(t, unknownNested, "nested sentinel")
	writeCleanupFile(t, filepath.Join(root, "AGENTS.md"), "old staged file")

	result, err := Clean(root, home, known)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if result.SkipReason != "" {
		t.Fatalf("cleanup was skipped: %s", result.SkipReason)
	}
	if result.Removed == 0 {
		t.Fatal("cleanup removed no known assets")
	}
	for _, name := range []string{"Dockerfile", "entrypoint.sh", "docs/README.md", "AGENTS.md"} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Errorf("known asset %s remains", name)
		}
	}
	assertCleanupContent(t, unknownRoot, "root sentinel")
	assertCleanupContent(t, unknownNested, "nested sentinel")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("cleanup removed root: %v", err)
	}
}

func TestCleanDoesNotFollowSymlinkInsideAssetTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "enclave")
	known := cleanupTestAssets()
	writeKnownAssets(t, root, known)

	outside := t.TempDir()
	victim := filepath.Join(outside, "asset.txt")
	writeCleanupFile(t, victim, "outside")
	linkedDir := filepath.Join(root, "extensions", "tools", "linked")
	if err := os.RemoveAll(linkedDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, linkedDir); err != nil {
		t.Fatal(err)
	}

	result, err := Clean(root, home, known)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if result.SkipReason != "" {
		t.Fatalf("cleanup was skipped: %s", result.SkipReason)
	}
	assertCleanupContent(t, victim, "outside")
	info, err := os.Lstat(linkedDir)
	if err != nil {
		t.Fatalf("nested symlink was removed: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("nested path mode = %v, want symlink", info.Mode())
	}
}

func TestCleanSkipsSymlinkedRootPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	home := t.TempDir()
	outside := t.TempDir()
	known := cleanupTestAssets()
	writeKnownAssets(t, outside, known)
	linkParent := filepath.Join(home, ".local", "share")
	if err := os.MkdirAll(linkParent, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(linkParent, "enclave")
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}

	result, err := Clean(root, home, known)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if result.SkipReason == "" {
		t.Fatal("expected symlinked root to be skipped")
	}
	assertCleanupContent(t, filepath.Join(outside, "Dockerfile"), "Dockerfile")
}

func TestCleanSkipsSourceCheckoutAndIncompleteRoot(t *testing.T) {
	home := t.TempDir()
	known := cleanupTestAssets()

	checkout := filepath.Join(home, "checkout")
	writeKnownAssets(t, checkout, known)
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Clean(checkout, home, known)
	if err != nil {
		t.Fatal(err)
	}
	if result.SkipReason == "" {
		t.Fatal("expected source checkout to be skipped")
	}
	assertCleanupContent(t, filepath.Join(checkout, "Dockerfile"), "Dockerfile")

	incomplete := filepath.Join(home, "incomplete")
	writeCleanupFile(t, filepath.Join(incomplete, "Dockerfile"), "unrelated")
	result, err = Clean(incomplete, home, known)
	if err != nil {
		t.Fatal(err)
	}
	if result.SkipReason == "" {
		t.Fatal("expected incomplete root to be skipped")
	}
	assertCleanupContent(t, filepath.Join(incomplete, "Dockerfile"), "unrelated")
}

func TestCleanRejectsDangerousRoots(t *testing.T) {
	home := t.TempDir()
	known := cleanupTestAssets()
	for _, root := range []string{"", home, filepath.Dir(home), string(filepath.Separator)} {
		if _, err := Clean(root, home, known); err == nil {
			t.Errorf("Clean(%q) succeeded", root)
		}
	}
}

func cleanupTestAssets() fstest.MapFS {
	return fstest.MapFS{
		"Dockerfile":                             {Data: []byte("Dockerfile")},
		"Dockerfile.gateway":                     {Data: []byte("Dockerfile.gateway")},
		"entrypoint.sh":                          {Data: []byte("entrypoint.sh")},
		"gateway-entrypoint.sh":                  {Data: []byte("gateway-entrypoint.sh")},
		"docs/README.md":                         {Data: []byte("docs")},
		"extensions/tools/demo/spec.yaml":        {Data: []byte("demo")},
		"extensions/tools/linked/asset.txt":      {Data: []byte("linked")},
		"runtime-assets/gateway-allowlists/base": {Data: []byte("base")},
	}
}

func writeKnownAssets(t *testing.T, root string, known fs.FS) {
	t.Helper()
	err := fs.WalkDir(known, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || name == "." {
			return walkErr
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := fs.ReadFile(known, name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		t.Fatalf("write known assets: %v", err)
	}
}

func writeCleanupFile(t *testing.T, name string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertCleanupContent(t *testing.T, name string, want string) {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read preserved file %s: %v", name, err)
	}
	if string(content) != want {
		t.Fatalf("preserved file %s = %q, want %q", name, content, want)
	}
}
