// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	dockerbackend "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	qemubackend "enclave/internal/backend/qemu"
	"enclave/internal/config"
	"enclave/internal/docker"
	"enclave/internal/model"
)

func TestProtectedIdentityRejectsLegacyStoreOperations(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NameQEMU} {
		t.Run(engine, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			home := t.TempDir()
			key := backend.StoreKey{Owner: "claude", Suffix: "protected"}
			dir := config.HostStoreAuthDir(home, key.Owner, key.Suffix)
			if err := hoststore.ProtectIdentity(home, dir, func() (bool, error) { return true, nil }); err != nil {
				t.Fatal(err)
			}
			grantPath := filepath.Join(dir, "oauth-grant.json")
			if err := os.WriteFile(grantPath, []byte("trusted-grant"), 0o600); err != nil {
				t.Fatal(err)
			}
			var be backend.Backend
			host := model.Host{Home: home}
			if engine == backend.NameDocker {
				be = dockerbackend.New(dockerbackend.Options{Host: host})
			} else {
				be = qemubackend.New(qemubackend.Options{Host: host})
			}
			storage := be.Storage()
			ctx := context.Background()
			configKey := backend.StoreKey{Owner: "claude", ProjectHash: "project"}
			configDir := config.HostStoreConfigDir(home, "claude", "project", "default")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(configDir, "keep")
			if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := be.PrepareStores(ctx, backend.StorePrep{
				Config:         &backend.ConfigStorePrep{Key: configKey, Recreate: true},
				Auth:           &backend.StorePrepEntry{Key: key},
				ResetAuthFiles: []string{"oauth-grant.json"},
			}); err == nil {
				t.Error("direct backend preparation bypassed protected identity")
			}
			if _, err := os.Stat(sentinel); err != nil {
				t.Fatalf("direct backend changed config before rejecting protected identity: %v", err)
			}
			operations := map[string]func() error{
				"read": func() error {
					_, err := storage.ReadFile(ctx, key, backend.StoreKindAuth, "oauth-grant.json")
					return err
				},
				"write": func() error {
					return storage.WriteFile(ctx, key, backend.StoreKindAuth, "oauth-grant.json", []byte("replacement"), 0o600)
				},
				"remove grant":    func() error { return storage.RemovePath(ctx, key, backend.StoreKindAuth, "oauth-grant.json") },
				"remove identity": func() error { return storage.Remove(ctx, key, backend.StoreKindAuth) },
			}
			for name, operation := range operations {
				if err := operation(); err == nil {
					t.Errorf("%s bypassed protection", name)
				}
			}
			// Host import/export use the same guarded storage interface, even if a
			// tool profile declares the broker's filename as a native auth file.
			hostAuthDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(hostAuthDir, "oauth-grant.json"), []byte("untrusted-import"), 0o600); err != nil {
				t.Fatal(err)
			}
			authCtx := authCommandContext{host: host, storage: storage, hostAuthDir: hostAuthDir, authFiles: []string{"oauth-grant.json"}}
			store := backend.StoreRef{Kind: backend.StoreKindAuth, Key: key}
			if err := importAuthFiles(authCtx, store); err == nil {
				t.Error("host import bypassed protection")
			}
			if err := exportAuthFiles(authCtx, store); err == nil {
				t.Error("host export bypassed protection")
			}
			data, err := os.ReadFile(grantPath)
			if err != nil || string(data) != "trusted-grant" {
				t.Fatalf("grant modified by legacy lifecycle operation: %q, %v", data, err)
			}
			data, err = os.ReadFile(filepath.Join(hostAuthDir, "oauth-grant.json"))
			if err != nil || string(data) != "untrusted-import" {
				t.Fatalf("grant exported by legacy lifecycle operation: %q, %v", data, err)
			}
			if err := hoststore.RequireUnprotectedAuth(home, dir); err == nil {
				t.Fatal("legacy lifecycle removed protection metadata")
			}
		})
	}
}

func TestCleanupRetainsProtectedIdentityAndRemovesOtherIdentity(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	dir := config.HostStoreAuthDir(home, "claude", "protected")
	if err := hoststore.ProtectIdentity(home, dir, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	grantPath := filepath.Join(dir, "oauth-grant.json")
	if err := os.WriteFile(grantPath, []byte("trusted-grant"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := config.HostStoreAuthDir(home, "claude", "legacy")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, ".enclave-oauth-identity.json"), []byte(`{"schemaVersion":1,"protected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupDirs(home, authStoreCleanupDirs(home)); err == nil {
		t.Fatal("cleanup did not report retaining protected auth")
	}
	data, err := os.ReadFile(grantPath)
	if err != nil || string(data) != "trusted-grant" {
		t.Fatalf("cleanup deleted protected grant through parent: %q, %v", data, err)
	}
	if _, err := os.Stat(hoststore.IdentityStatePath(home, dir)); err != nil {
		t.Fatalf("cleanup removed protection: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("ordinary legacy auth was not cleaned: %v", err)
	}
}

func TestCleanupRefusesCorruptProtectedIdentityMetadata(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	dir := config.HostStoreAuthDir(home, "claude", "corrupt")
	if err := hoststore.ProtectIdentity(home, dir, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	marker := hoststore.IdentityStatePath(home, dir)
	if err := os.WriteFile(marker, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupDirs(home, []cleanupDir{{Kind: authKind, Path: dir}}); err == nil {
		t.Fatal("cleanup treated corrupt protection as legacy")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cleanup removed corrupt marker: %v", err)
	}
}

func TestCleanupDryRunReportsRetainedIdentities(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	protected := config.HostStoreAuthDir(home, "claude", "protected")
	corrupt := config.HostStoreAuthDir(home, "claude", "corrupt")
	legacy := config.HostStoreAuthDir(home, "claude", "legacy")
	for _, dir := range []string{protected, corrupt} {
		if err := hoststore.ProtectIdentity(home, dir, func() (bool, error) { return true, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(hoststore.IdentityStatePath(home, corrupt), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := printCleanupPlan(home, authStoreCleanupDirs(home)); err == nil {
			t.Error("dry-run did not report incomplete cleanup")
		}
	})
	for _, dir := range []string{protected, corrupt} {
		if !strings.Contains(out, "Would retain auth: "+dir) || strings.Contains(out, "Would remove auth: "+dir) {
			t.Errorf("dry-run does not report retained identity %s: %s", dir, out)
		}
	}
	if !strings.Contains(out, "gateway-protected") || !strings.Contains(out, "invalid protected auth identity metadata") {
		t.Errorf("dry-run omitted retention reasons: %s", out)
	}
	if !strings.Contains(out, "Would remove auth: "+legacy) {
		t.Errorf("dry-run omitted legacy identity removal: %s", out)
	}
	tree := config.HostStoreAuthTreeDir(home, "claude")
	if !strings.Contains(out, "Would retain auth tree: "+tree) || strings.Contains(out, "Would remove auth tree if empty: "+tree) {
		t.Errorf("dry-run promised to remove retained identity's parent: %s", out)
	}
	for _, dir := range []string{protected, corrupt, legacy} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("dry-run modified %s: %v", dir, err)
		}
	}
}

func TestRunCleanupPrunesBuildCacheAfterRetainingIdentity(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("test isolates host data using XDG roots")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home, err := config.ResolveHostHome()
	if err != nil {
		t.Fatal(err)
	}
	protected := config.HostStoreAuthDir(home, "claude", "protected")
	if err := hoststore.ProtectIdentity(home, protected, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	calls := filepath.Join(binDir, "calls")
	t.Setenv("ENCLAVE_TEST_CLEANUP_CALLS", calls)
	cli := filepath.Join(binDir, "docker")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$ENCLAVE_TEST_CLEANUP_CALLS"
case "$1 $2" in
  "system df") printf '%s\n' '{"Type":"Build Cache","Size":"2MB","Reclaimable":"1MB"}' ;;
  "builder prune") printf '%s\n' 'Total reclaimed space: 1MB' ;;
  *) exit 98 ;;
esac
`
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	previousCLI := docker.Binary()
	docker.SetBinary(cli)
	t.Cleanup(func() { docker.SetBinary(previousCLI) })
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close() })
	if _, err := input.WriteString("yes\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	previousStdin := os.Stdin
	os.Stdin = input
	t.Cleanup(func() { os.Stdin = previousStdin })
	out := captureStdout(t, func() {
		if code := runCleanup(model.RunOptions{Tool: "claude"}, model.CleanupOptions{CleanupAll: true, CleanupBuildCache: true}); code != 1 {
			t.Errorf("cleanup status = %d, want incomplete status 1", code)
		}
	})
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "system df") || !strings.Contains(string(data), "builder prune --force --all") {
		t.Errorf("retained auth cancelled independent build-cache work: %s", data)
	}
	if strings.Contains(out, "Cleanup complete") {
		t.Errorf("cleanup falsely reported success: %s", out)
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("cleanup removed protected identity: %v", err)
	}
}

func TestCleanupDoesNotFollowAuthTreeSymlink(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	outside := t.TempDir()
	child := filepath.Join(outside, "identity")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	tree := config.HostStoreAuthTreeDir(home, "claude")
	if err := os.MkdirAll(filepath.Dir(tree), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, tree); err != nil {
		t.Fatal(err)
	}
	if err := cleanupDirs(home, authStoreCleanupDirs(home)); err != nil {
		t.Fatalf("cleanup failed to unlink auth tree symlink: %v", err)
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatalf("cleanup followed auth tree symlink: %v", err)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatalf("cleanup retained auth tree symlink: %v", err)
	}
}

func TestCleanupRemovesStrayAuthEntriesAndEmptyParent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	tree := config.HostStoreAuthTreeDir(home, "claude")
	identity := config.HostStoreAuthDir(home, "claude", "legacy")
	if err := os.MkdirAll(identity, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(identity, ".credentials.json"), []byte("legacy-auth"), 0o600); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(tree, ".DS_Store")
	if err := os.WriteFile(stray, []byte("stray"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"dangling": filepath.Join(outside, "missing"), "linked": outside} {
		if err := os.Symlink(target, filepath.Join(tree, name)); err != nil {
			t.Fatal(err)
		}
	}
	dirs := authStoreCleanupDirs(home)
	out := captureStdout(t, func() {
		if err := printCleanupPlan(home, dirs); err != nil {
			t.Errorf("dry-run retained stray auth entries: %v", err)
		}
	})
	if !strings.Contains(out, "Would remove auth: "+stray) || !strings.Contains(out, "Would remove auth tree if empty: "+tree) {
		t.Errorf("dry-run omitted stray entry or parent cleanup: %s", out)
	}
	if err := cleanupDirs(home, dirs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatalf("cleanup left auth tree behind: %v", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "untouched" {
		t.Fatalf("cleanup followed auth entry symlink: %q, %v", data, err)
	}
}

func TestCleanupKeepsNewProtectedIdentityAfterPlanning(t *testing.T) {
	for _, replaceStray := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace-stray=%t", replaceStray), func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			home := t.TempDir()
			legacy := config.HostStoreAuthDir(home, "claude", "legacy")
			if err := os.MkdirAll(legacy, 0o700); err != nil {
				t.Fatal(err)
			}
			protected := config.HostStoreAuthDir(home, "claude", "protected")
			if replaceStray {
				if err := os.WriteFile(protected, []byte("stray"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			dirs := authStoreCleanupDirs(home)
			if replaceStray {
				if err := os.Remove(protected); err != nil {
					t.Fatal(err)
				}
			}
			if err := hoststore.ProtectIdentity(home, protected, func() (bool, error) { return true, nil }); err != nil {
				t.Fatal(err)
			}
			err := cleanupDirs(home, dirs)
			if (err != nil) != replaceStray {
				t.Errorf("cleanup error = %v, expected retained planned identity = %t", err, replaceStray)
			}
			if _, err := os.Stat(protected); err != nil {
				t.Fatalf("cleanup deleted newly protected identity: %v", err)
			}
			if _, err := os.Stat(legacy); !os.IsNotExist(err) {
				t.Fatalf("cleanup retained ordinary legacy identity: %v", err)
			}
		})
	}
}

func TestCleanupKeepAuthPreservesStrayEntriesAndParent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	home := t.TempDir()
	tree := config.HostStoreAuthTreeDir(home, "claude")
	if err := os.MkdirAll(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(tree, ".DS_Store")
	if err := os.WriteFile(stray, []byte("stray"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tree, "dangling")
	if err := os.Symlink("missing", link); err != nil {
		t.Fatal(err)
	}
	dirs := cleanupDirsForRemoval(model.RunOptions{Tool: "claude"}, model.CleanupOptions{CleanupAll: true, CleanupKeepAuth: true}, home, model.Project{}, model.MemoryScopeProject)
	if err := cleanupDirs(home, dirs); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{tree, stray, link} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("--keep auth removed %s: %v", path, err)
		}
	}
}

func TestCleanupSerializesWithProtectedGrantMutation(t *testing.T) {
	for _, entryOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("entry-only=%t", entryOnly), func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			home := t.TempDir()
			identity := config.HostStoreAuthDir(home, "claude", "fresh")
			if err := os.MkdirAll(filepath.Dir(identity), 0o700); err != nil {
				t.Fatal(err)
			}
			if entryOnly {
				if err := os.WriteFile(identity, []byte("stray"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(identity, 0o700); err != nil {
				t.Fatal(err)
			}
			dirs := authStoreCleanupDirs(home)
			if entryOnly {
				if err := os.Remove(identity); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(identity, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			if err := hoststore.ProtectIdentity(home, identity, func() (bool, error) { return true, nil }); err != nil {
				t.Fatal(err)
			}
			writerLocked := make(chan struct{})
			commitProtection := make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-commitProtection:
				default:
					close(commitProtection)
				}
			})
			protectionDone := make(chan error, 1)
			go func() {
				protectionDone <- hoststore.WithIdleProtectedIdentity(home, identity, func() (bool, error) { return true, nil }, func() error {
					close(writerLocked)
					<-commitProtection
					return nil
				})
			}()
			select {
			case <-writerLocked:
			case err := <-protectionDone:
				t.Fatalf("protection failed before taking identity lock: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("protection did not take identity lock")
			}

			cleanupStarted := make(chan struct{})
			cleanupDone := make(chan error, 1)
			go func() {
				close(cleanupStarted)
				cleanupDone <- cleanupDirs(home, dirs)
			}()
			<-cleanupStarted
			var cleanupErr error
			cleanupFinished := false
			select {
			case cleanupErr = <-cleanupDone:
				cleanupFinished = true
				t.Errorf("cleanup bypassed identity lock during grant mutation: %v", cleanupErr)
			case <-time.After(50 * time.Millisecond):
			}
			if _, err := os.Stat(identity); err != nil {
				t.Errorf("cleanup removed identity while protection held its lock: %v", err)
			}
			close(commitProtection)
			select {
			case err := <-protectionDone:
				if err != nil {
					t.Errorf("protection commit failed: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("protection commit did not finish")
			}
			if !cleanupFinished {
				select {
				case cleanupErr = <-cleanupDone:
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not finish after protection released its lock")
				}
			}
			if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "gateway-protected") {
				t.Errorf("cleanup did not recheck committed protection under lock: %v", cleanupErr)
			}
			if _, err := os.Stat(identity); err != nil {
				t.Fatalf("cleanup removed newly protected identity: %v", err)
			}
		})
	}
}

func TestCleanupLockFailureRetainsAuthIdentity(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	identity := config.HostStoreAuthDir(home, "claude", "legacy")
	if err := os.MkdirAll(identity, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hoststore.LockPath(home, identity), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cleanupDirs(home, authStoreCleanupDirs(home)); err == nil {
		t.Fatal("cleanup ignored failure to acquire the identity lock")
	}
	if _, err := os.Stat(identity); err != nil {
		t.Fatalf("cleanup fell back to unlocked identity removal: %v", err)
	}
}

func TestProtectedAuthOptionValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*model.Options)
		fail   bool
	}{
		{"docker", func(o *model.Options) { o.Backend = "docker" }, false},
		{"podman unrestricted", func(o *model.Options) { o.Backend = "podman"; o.AllowAllNetwork = true }, false},
		{"unknown tool", func(o *model.Options) { o.Tool = "unknown" }, true},
		{"codex", func(o *model.Options) { o.Tool = "codex" }, false},
		{"unnamed theia", func(o *model.Options) { o.Tool, o.AuthName = "theia", "" }, false},
		{"qemu", func(o *model.Options) { o.Backend = "qemu" }, true},
		{"project", func(o *model.Options) { o.AuthScope = model.AuthScopeProject }, false},
		{"unnamed project", func(o *model.Options) { o.AuthScope, o.AuthName = model.AuthScopeProject, "" }, false},
		{"ephemeral", func(o *model.Options) { o.Ephemeral = true }, false},
		{"unnamed ephemeral", func(o *model.Options) { o.Ephemeral, o.AuthName = true, "" }, false},
		{"unnamed shared", func(o *model.Options) { o.AuthName = "" }, true},
		{"admin", func(o *model.Options) { o.Admin = true }, true},
		{"remap", func(o *model.Options) { o.RuntimeUIDRemap = true }, true},
		{"devcontainer", func(o *model.Options) { o.Devcontainer = true }, true},
		{"host config", func(o *model.Options) { o.HostConfig = model.HostConfigPassthrough }, true},
		{"inactive host paths", func(o *model.Options) {
			o.HostConfig = model.HostConfigNone
			o.HostConfigPaths = []string{"default", "+commands/"}
		}, false},
		{"unset host mode with paths", func(o *model.Options) {
			o.HostConfig = ""
			o.HostConfigPaths = []string{"settings.json"}
		}, false},
		{"provider key placeholder", func(o *model.Options) { o.PassAPIKey = true }, false},
		{"custom base image", func(o *model.Options) { o.BaseImage = "custom" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := config.DefaultOptions()
			opts.Backend = "docker"
			opts.Tool = "claude"
			opts.ProtectAuth = true
			opts.AuthName = "fresh"
			tc.change(&opts)
			_, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run"})
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v, want failure=%v", err, tc.fail)
			}
		})
	}
}

func TestCleanupReturnsRemovalFailureAndContinues(t *testing.T) {
	if os.Getuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("requires unprivileged Unix directory permissions")
	}
	for _, mode := range []cleanupRemovalMode{cleanupRecursive, cleanupEntryOnly} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			parent := t.TempDir()
			victim := filepath.Join(parent, "victim")
			if err := os.Mkdir(victim, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o500); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chmod(parent, 0o700) }()
			removable := t.TempDir()
			err := cleanupDirs(t.TempDir(), []cleanupDir{
				{Path: victim, Kind: cacheKind, Mode: mode},
				{Path: removable, Kind: cacheKind, Mode: cleanupRecursive},
			})
			if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), victim) {
				t.Fatalf("cleanup did not report retained data: %v", err)
			}
			if _, err := os.Stat(victim); err != nil {
				t.Fatal("failed removal lost the fixture")
			}
			if _, err := os.Stat(removable); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("cleanup stopped before processing remaining directories")
			}
		})
	}
}
