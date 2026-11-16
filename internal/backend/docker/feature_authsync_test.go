// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/model"
)

// copyFeatureAuthFileIfMissing opens the parents of src and dst as store roots
// and runs the rooted copy, so tests can address both files by host path.
func copyFeatureAuthFileIfMissing(src, dst string, placeholders ...string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	source, err := hoststore.OpenFiles(filepath.Dir(src))
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	target, err := hoststore.OpenFiles(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer func() { _ = target.Close() }()
	return copyRootedFeatureAuthFileIfMissing(source, filepath.Base(src), target, filepath.Base(dst), placeholders...)
}

func setBeforeFeatureAuthSourceRead(t *testing.T, fn func()) {
	t.Helper()
	previous := beforeFeatureAuthSourceRead
	beforeFeatureAuthSourceRead = fn
	t.Cleanup(func() { beforeFeatureAuthSourceRead = previous })
}

type featureAuthStores struct {
	b         *Backend
	configDir string
	authDir   string
	outside   string
	sync      backend.FeatureAuthSync
}

func newFeatureAuthStores(t *testing.T, configSubdir string, authFile string) featureAuthStores {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	stores := featureAuthStores{
		b:         New(Options{Host: model.Host{Home: home, UID: "1000", GID: "1000"}}),
		configDir: t.TempDir(),
		authDir:   config.HostStoreFeatureAuthDir(home, "github-cli"),
		outside:   t.TempDir(),
		sync:      backend.FeatureAuthSync{Feature: "github-cli", ConfigDir: configSubdir, AuthFiles: []string{authFile}},
	}
	writeFile(t, filepath.Join(stores.configDir, configSubdir, authFile), "oauth_token: inside\n")
	return stores
}

func replaceWithSymlink(t *testing.T, target string, link string) {
	t.Helper()
	if err := os.Rename(link, link+".saved"); err != nil {
		t.Fatalf("move %s aside: %v", link, err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s: %v", link, err)
	}
}

// The destination passes the early path checks as an empty regular file (or
// below a real directory) and is replaced by a link to another host directory
// while the copy is between its source Lstat and its source read.
func TestSyncFeatureAuthStoreKeepsSwappedDestinationInStore(t *testing.T) {
	for _, tc := range []struct {
		name     string
		authFile string
		existing bool
		swap     func(t *testing.T, s featureAuthStores)
		outside  string
	}{
		{
			name:     "dangling file link",
			authFile: "config.yml",
			swap: func(t *testing.T, s featureAuthStores) {
				replaceWithSymlink(t, filepath.Join(s.outside, "canary"), filepath.Join(s.authDir, "config.yml"))
			},
			outside: "canary",
		},
		{
			name:     "file link to existing empty file",
			authFile: "config.yml",
			existing: true,
			swap: func(t *testing.T, s featureAuthStores) {
				replaceWithSymlink(t, filepath.Join(s.outside, "canary"), filepath.Join(s.authDir, "config.yml"))
			},
			outside: "canary",
		},
		{
			name:     "parent directory link",
			authFile: "gh/hosts.yml",
			swap: func(t *testing.T, s featureAuthStores) {
				replaceWithSymlink(t, s.outside, filepath.Join(s.authDir, "gh"))
			},
			outside: "hosts.yml",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stores := newFeatureAuthStores(t, "", tc.authFile)
			writeFile(t, filepath.Join(stores.authDir, tc.authFile), "")
			canary := filepath.Join(stores.outside, tc.outside)
			if tc.existing {
				writeFile(t, canary, "")
			}
			swapped := false
			setBeforeFeatureAuthSourceRead(t, func() {
				tc.swap(t, stores)
				swapped = true
			})

			_ = stores.b.syncFeatureAuthStore(context.Background(), stores.configDir, stores.sync)

			if !swapped {
				t.Fatal("copy did not reach the source read")
			}
			data, err := os.ReadFile(canary)
			switch {
			case tc.existing && (err != nil || len(data) != 0):
				t.Fatalf("outside file changed: %q, %v", data, err)
			case !tc.existing && !os.IsNotExist(err):
				t.Fatalf("outside file created: %q, %v", data, err)
			}
		})
	}
}

// The source passes the early path checks and is replaced by a link to a host
// file outside the config store before it is read.
func TestSyncFeatureAuthStoreDoesNotCopySwappedSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap func(t *testing.T, s featureAuthStores)
	}{
		{
			name: "file link",
			swap: func(t *testing.T, s featureAuthStores) {
				replaceWithSymlink(t, filepath.Join(s.outside, "hosts.yml"), filepath.Join(s.configDir, "gh", "hosts.yml"))
			},
		},
		{
			name: "parent directory link",
			swap: func(t *testing.T, s featureAuthStores) {
				replaceWithSymlink(t, s.outside, filepath.Join(s.configDir, "gh"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stores := newFeatureAuthStores(t, "gh", "hosts.yml")
			writeFile(t, filepath.Join(stores.outside, "hosts.yml"), "host-only")
			swapped := false
			setBeforeFeatureAuthSourceRead(t, func() {
				tc.swap(t, stores)
				swapped = true
			})

			_ = stores.b.syncFeatureAuthStore(context.Background(), stores.configDir, stores.sync)

			if !swapped {
				t.Fatal("copy did not reach the source read")
			}
			if data, err := os.ReadFile(filepath.Join(stores.authDir, "hosts.yml")); !os.IsNotExist(err) {
				t.Fatalf("auth store received %q, %v", data, err)
			}
		})
	}
}

func TestSyncFeatureAuthStoreRejectsSymlinkedDestination(t *testing.T) {
	stores := newFeatureAuthStores(t, "", "config.yml")
	if err := os.MkdirAll(stores.authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(stores.outside, "canary")
	if err := os.Symlink(canary, filepath.Join(stores.authDir, "config.yml")); err != nil {
		t.Fatal(err)
	}

	if err := stores.b.syncFeatureAuthStore(context.Background(), stores.configDir, stores.sync); err == nil {
		t.Fatal("expected symlinked destination to be rejected")
	}
	if _, err := os.Lstat(canary); !os.IsNotExist(err) {
		t.Fatalf("outside file created: %v", err)
	}
}

func TestCopyFeatureAuthFileIfMissingFillsEmptyDestination(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src", "auth.json"), filepath.Join(root, "dst", "auth.json")
	writeFile(t, src, `{"token":"new"}`)
	writeFile(t, dst, "")

	if err := copyFeatureAuthFileIfMissing(src, dst); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, dst, `{"token":"new"}`)
}

func TestCopyFeatureAuthFileIfMissingSkipsSymlinkedSource(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	writeFile(t, outside, "host-only")
	src, dst := filepath.Join(root, "src", "auth.json"), filepath.Join(root, "dst", "auth.json")
	if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, src); err != nil {
		t.Fatal(err)
	}

	if err := copyFeatureAuthFileIfMissing(src, dst); err != nil {
		t.Fatalf("symlinked source should be skipped, got %v", err)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("symlinked source copied: %v", err)
	}
}

func TestCopyFeatureAuthFileIfMissingRefusesSymlinkedDestination(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "canary")
	src, dst := filepath.Join(root, "src", "auth.json"), filepath.Join(root, "dst", "auth.json")
	writeFile(t, src, `{"token":"new"}`)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dst); err != nil {
		t.Fatal(err)
	}

	if err := copyFeatureAuthFileIfMissing(src, dst); err == nil {
		t.Fatal("expected symlinked destination to be refused")
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside file created: %v", err)
	}
	if target, err := os.Readlink(dst); err != nil || target != outside {
		t.Fatalf("destination link changed: %q, %v", target, err)
	}
}
