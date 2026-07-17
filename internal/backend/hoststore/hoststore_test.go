// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
)

func TestDirMapsEachKindToItsHostDirectory(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()

	cases := []struct {
		name string
		key  backend.StoreKey
		kind backend.StoreKind
		want string
	}{
		{"config default", backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}, backend.StoreKindConfig, config.HostStoreConfigDir(home, "codex", "abc123abc123", "default")},
		{"config suffix", backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123", Suffix: "wt2"}, backend.StoreKindConfig, config.HostStoreConfigDir(home, "codex", "abc123abc123", "wt2")},
		{"env", backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}, backend.StoreKindEnv, config.HostStoreEnvDir(home, "codex", "abc123abc123")},
		{"auth default identity", backend.StoreKey{Owner: "codex"}, backend.StoreKindAuth, config.HostStoreAuthDir(home, "codex", "")},
		{"auth named identity (--auth-name)", backend.StoreKey{Owner: "codex", Suffix: "personal"}, backend.StoreKindAuth, config.HostStoreAuthDir(home, "codex", "personal")},
		{"feature auth", backend.StoreKey{Owner: "playwright"}, backend.StoreKindFeatureAuth, config.HostStoreFeatureAuthDir(home, "playwright")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Dir(home, tc.key, tc.kind); got != tc.want {
				t.Fatalf("Dir() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDirIncompleteKeyIsEmpty(t *testing.T) {
	home := t.TempDir()
	if got := Dir(home, backend.StoreKey{Owner: "codex"}, backend.StoreKindConfig); got != "" {
		t.Fatalf("Dir() with missing project hash = %q, want empty", got)
	}
	if got := Dir(home, backend.StoreKey{ProjectHash: "abc123abc123"}, backend.StoreKindConfig); got != "" {
		t.Fatalf("Dir() with missing owner = %q, want empty", got)
	}
}

func TestDirRejectsMaliciousKeySegments(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		key  backend.StoreKey
		kind backend.StoreKind
	}{
		{"owner traversal", backend.StoreKey{Owner: "../evil", ProjectHash: "abc123abc123"}, backend.StoreKindConfig},
		{"owner separator", backend.StoreKey{Owner: "a/b", ProjectHash: "abc123abc123"}, backend.StoreKindConfig},
		{"hash traversal", backend.StoreKey{Owner: "codex", ProjectHash: ".."}, backend.StoreKindConfig},
		{"suffix traversal", backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123", Suffix: "../evil"}, backend.StoreKindConfig},
		{"auth owner traversal", backend.StoreKey{Owner: "../evil"}, backend.StoreKindAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Dir(home, tc.key, tc.kind); got != "" {
				t.Fatalf("Dir() = %q, want empty for malicious key", got)
			}
		})
	}
}

func TestWithLockCreatesStoreLockFile(t *testing.T) {
	home := t.TempDir()
	dir := Dir(home, backend.StoreKey{Owner: "codex"}, backend.StoreKindAuth)

	called := false
	if err := WithLock(home, dir, func() error { called = true; return nil }); err != nil {
		t.Fatalf("WithLock() error = %v", err)
	}
	if !called {
		t.Fatalf("WithLock() did not invoke fn")
	}
	entries, err := os.ReadDir(config.HostLocksDir(home))
	if err != nil {
		t.Fatalf("read locks dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("locks dir entries = %d, want 1", len(entries))
	}
}

func TestEnsureNoSymlinkChainRejectsSymlinkedComponent(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "tools")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "tools", "codex", "auth")
	if err := EnsureNoSymlinkChain(root, target, true); err == nil {
		t.Fatalf("EnsureNoSymlinkChain() accepted a symlinked component")
	}
	if err := EnsureNoSymlinkChain(root, filepath.Join(root, "missing", "auth"), true); err != nil {
		t.Fatalf("EnsureNoSymlinkChain() rejected nonexistent components: %v", err)
	}
}
