// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"enclave/internal/config"
)

func TestMigrateLegacySSHDir(t *testing.T) {
	t.Run("old only", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		writeTestSSHDirectory(t, legacy, "old-key")

		state, err := migrateLegacySSHDir(home)
		if err != nil {
			t.Fatalf("migrateLegacySSHDir() error = %v", err)
		}
		if state != sshMigrationCompleted {
			t.Fatalf("migration state = %v, want completed", state)
		}
		assertFileContent(t, filepath.Join(current, "id_ed25519"), "old-key")
		assertSSHPathMissing(t, legacy)
		assertMode(t, current, 0o700)
		assertMode(t, filepath.Join(current, "id_ed25519"), 0o600)
		assertMode(t, filepath.Join(current, "id_ed25519.pub"), 0o644)

		state, err = migrateLegacySSHDir(home)
		if err != nil {
			t.Fatalf("second migrateLegacySSHDir() error = %v", err)
		}
		if state != sshMigrationNone {
			t.Fatalf("second migration state = %v, want none", state)
		}
	})

	t.Run("new only", func(t *testing.T) {
		home := t.TempDir()
		current := config.HostSSHDir(home)
		writeTestSSHDirectory(t, current, "new-key")

		state, err := migrateLegacySSHDir(home)
		if err != nil {
			t.Fatalf("migrateLegacySSHDir() error = %v", err)
		}
		if state != sshMigrationNone {
			t.Fatalf("migration state = %v, want none", state)
		}
		assertFileContent(t, filepath.Join(current, "id_ed25519"), "new-key")
	})

	t.Run("both", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		writeTestSSHDirectory(t, legacy, "old-key")
		writeTestSSHDirectory(t, current, "new-key")

		state, err := migrateLegacySSHDir(home)
		if err != nil {
			t.Fatalf("migrateLegacySSHDir() error = %v", err)
		}
		if state != sshMigrationConflict {
			t.Fatalf("migration state = %v, want conflict", state)
		}
		assertFileContent(t, filepath.Join(legacy, "id_ed25519"), "old-key")
		assertFileContent(t, filepath.Join(current, "id_ed25519"), "new-key")
	})

	t.Run("legacy symlink with new directory", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		legacyTarget := filepath.Join(home, "legacy-target")
		writeTestSSHDirectory(t, legacyTarget, "linked-old-key")
		if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
			t.Fatalf("create legacy parent: %v", err)
		}
		if err := os.Symlink(legacyTarget, legacy); err != nil {
			t.Fatalf("create legacy symlink: %v", err)
		}
		writeTestSSHDirectory(t, current, "new-key")

		state, err := migrateLegacySSHDir(home)
		if err != nil {
			t.Fatalf("migrateLegacySSHDir() error = %v", err)
		}
		if state != sshMigrationConflict {
			t.Fatalf("migration state = %v, want conflict", state)
		}
		assertSymlink(t, legacy)
		assertFileContent(t, filepath.Join(current, "id_ed25519"), "new-key")
	})

	t.Run("legacy file with new directory", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
			t.Fatalf("create legacy parent: %v", err)
		}
		if err := os.WriteFile(legacy, []byte("legacy-file"), 0o600); err != nil {
			t.Fatalf("create legacy file: %v", err)
		}
		writeTestSSHDirectory(t, current, "new-key")

		state, err := migrateLegacySSHDir(home)
		if err != nil {
			t.Fatalf("migrateLegacySSHDir() error = %v", err)
		}
		if state != sshMigrationConflict {
			t.Fatalf("migration state = %v, want conflict", state)
		}
		assertFileContent(t, legacy, "legacy-file")
		assertFileContent(t, filepath.Join(current, "id_ed25519"), "new-key")
	})

	t.Run("legacy symlink without new directory", func(t *testing.T) {
		for _, relative := range []bool{false, true} {
			t.Run(fmt.Sprintf("relative=%v", relative), func(t *testing.T) {
				home := t.TempDir()
				legacy := config.HostLegacySSHDir(home)
				current := config.HostSSHDir(home)
				legacyTarget := filepath.Join(home, "legacy-target")
				writeTestSSHDirectory(t, legacyTarget, "linked-old-key")
				if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
					t.Fatalf("create legacy parent: %v", err)
				}
				linkTarget := legacyTarget
				if relative {
					var err error
					if linkTarget, err = filepath.Rel(filepath.Dir(legacy), legacyTarget); err != nil {
						t.Fatalf("relative legacy target: %v", err)
					}
				}
				if err := os.Symlink(linkTarget, legacy); err != nil {
					t.Fatalf("create legacy symlink: %v", err)
				}

				state, err := migrateLegacySSHDir(home)
				if err != nil {
					t.Fatalf("migrateLegacySSHDir() error = %v", err)
				}
				if state != sshMigrationCompleted {
					t.Fatalf("migration state = %v, want completed", state)
				}
				assertSSHPathMissing(t, legacy)
				got, err := os.Readlink(current)
				if err != nil {
					t.Fatalf("read moved symlink: %v", err)
				}
				want, err := filepath.EvalSymlinks(legacyTarget)
				if err != nil {
					t.Fatalf("resolve legacy target: %v", err)
				}
				if got != want {
					t.Fatalf("moved symlink target = %q, want %q", got, want)
				}
				// The link is moved, not followed: its target keeps its modes.
				assertMode(t, legacyTarget, 0o755)
				assertFileContent(t, filepath.Join(current, "id_ed25519"), "linked-old-key")
			})
		}
	})

	t.Run("broken legacy symlink is not moved", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
			t.Fatalf("create legacy parent: %v", err)
		}
		if err := os.Symlink(filepath.Join(home, "missing-target"), legacy); err != nil {
			t.Fatalf("create broken legacy symlink: %v", err)
		}

		if _, err := migrateLegacySSHDir(home); !errors.Is(err, errLegacySSHNotDirectory) {
			t.Fatalf("migrateLegacySSHDir() error = %v, want %v", err, errLegacySSHNotDirectory)
		}
		assertSymlink(t, legacy)
		assertSSHPathMissing(t, config.HostSSHDir(home))
	})

	t.Run("legacy symlink to the config root is not moved", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		for _, dir := range []string{filepath.Dir(legacy), filepath.Dir(current)} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("create %s: %v", dir, err)
			}
		}
		if err := os.Symlink(filepath.Dir(current), legacy); err != nil {
			t.Fatalf("create legacy symlink: %v", err)
		}

		if _, err := migrateLegacySSHDir(home); !errors.Is(err, errLegacySSHNotDirectory) || !strings.Contains(err.Error(), "overlaps current location") {
			t.Fatalf("migrateLegacySSHDir() error = %v, want an overlap error wrapping %v", err, errLegacySSHNotDirectory)
		}
		assertSymlink(t, legacy)
		assertSSHPathMissing(t, current)
	})

	t.Run("relative legacy symlink under a symlinked cache root", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		// The cache root is a link to real/a/enclave, so ../../keys from the
		// legacy path is real/keys and not what joining the paths would give.
		realCacheRoot := filepath.Join(home, "real", "a", "enclave")
		keys := filepath.Join(home, "real", "keys")
		writeTestSSHDirectory(t, keys, "linked-old-key")
		for _, dir := range []string{realCacheRoot, filepath.Dir(filepath.Dir(legacy))} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("create %s: %v", dir, err)
			}
		}
		if err := os.Symlink(realCacheRoot, filepath.Dir(legacy)); err != nil {
			t.Fatalf("create cache root symlink: %v", err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "keys"), legacy); err != nil {
			t.Fatalf("create legacy symlink: %v", err)
		}

		if state, err := migrateLegacySSHDir(home); err != nil || state != sshMigrationCompleted {
			t.Fatalf("migrateLegacySSHDir() = %v, %v, want completed", state, err)
		}
		assertSSHPathMissing(t, legacy)
		assertFileContent(t, filepath.Join(current, "id_ed25519"), "linked-old-key")
	})

	t.Run("legacy file without new directory", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
			t.Fatalf("create legacy parent: %v", err)
		}
		if err := os.WriteFile(legacy, []byte("legacy-file"), 0o600); err != nil {
			t.Fatalf("create legacy file: %v", err)
		}

		state, err := migrateLegacySSHDir(home)
		if !errors.Is(err, errLegacySSHNotDirectory) {
			t.Fatalf("migrateLegacySSHDir() error = %v, want %v", err, errLegacySSHNotDirectory)
		}
		if state != sshMigrationNone {
			t.Fatalf("migration state = %v, want none", state)
		}
		assertFileContent(t, legacy, "legacy-file")
		assertSSHPathMissing(t, config.HostSSHDir(home))
	})

	t.Run("empty legacy directory with new directory", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		if err := os.MkdirAll(legacy, 0o700); err != nil {
			t.Fatalf("create empty legacy directory: %v", err)
		}
		writeTestSSHDirectory(t, current, "new-key")

		if state, err := inspectSSHMigration(home); err != nil || state != sshMigrationEmptyLegacy {
			t.Fatalf("inspectSSHMigration() = %v, %v, want empty legacy", state, err)
		}
		state, err := migrateLegacySSHDir(home)
		if err != nil {
			t.Fatalf("migrateLegacySSHDir() error = %v", err)
		}
		if state != sshMigrationNone {
			t.Fatalf("migration state = %v, want none", state)
		}
		assertSSHPathMissing(t, legacy)
		assertFileContent(t, filepath.Join(current, "id_ed25519"), "new-key")
	})

	t.Run("current symlink with legacy directory", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		currentTarget := filepath.Join(home, "current-target")
		writeTestSSHDirectory(t, legacy, "old-key")
		writeTestSSHDirectory(t, currentTarget, "linked-new-key")
		if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
			t.Fatalf("create current parent: %v", err)
		}
		if err := os.Symlink(currentTarget, current); err != nil {
			t.Fatalf("create current symlink: %v", err)
		}

		state, err := migrateLegacySSHDir(home)
		if err != nil {
			t.Fatalf("migrateLegacySSHDir() error = %v", err)
		}
		if state != sshMigrationConflict {
			t.Fatalf("migration state = %v, want conflict", state)
		}
		assertSymlink(t, current)
		assertFileContent(t, filepath.Join(current, "id_ed25519"), "linked-new-key")
	})

	t.Run("broken current symlink reports current path", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		writeTestSSHDirectory(t, legacy, "old-key")
		if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
			t.Fatalf("create current parent: %v", err)
		}
		if err := os.Symlink(filepath.Join(home, "missing-target"), current); err != nil {
			t.Fatalf("create broken current symlink: %v", err)
		}

		state, err := migrateLegacySSHDir(home)
		if err == nil || !strings.Contains(err.Error(), "current SSH path") {
			t.Fatalf("migrateLegacySSHDir() error = %v, want current-path error", err)
		}
		if state != sshMigrationNone {
			t.Fatalf("migration state = %v, want none", state)
		}
		assertSymlink(t, current)
	})

	t.Run("current file reports current path", func(t *testing.T) {
		home := t.TempDir()
		legacy := config.HostLegacySSHDir(home)
		current := config.HostSSHDir(home)
		writeTestSSHDirectory(t, legacy, "old-key")
		if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
			t.Fatalf("create current parent: %v", err)
		}
		if err := os.WriteFile(current, []byte("current-file"), 0o600); err != nil {
			t.Fatalf("create current file: %v", err)
		}

		state, err := migrateLegacySSHDir(home)
		if err == nil || !strings.Contains(err.Error(), "current SSH path "+current+" is not a directory") {
			t.Fatalf("migrateLegacySSHDir() error = %v, want current-path error", err)
		}
		if state != sshMigrationNone {
			t.Fatalf("migration state = %v, want none", state)
		}
		assertFileContent(t, current, "current-file")
		assertFileContent(t, filepath.Join(legacy, "id_ed25519"), "old-key")
	})
}

func TestPrepareSSHInitDirectoryReplacesUnmigratableLegacyPath(t *testing.T) {
	home := t.TempDir()
	legacy := config.HostLegacySSHDir(home)
	current := config.HostSSHDir(home)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatalf("create legacy parent: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("legacy-file"), 0o600); err != nil {
		t.Fatalf("create legacy file: %v", err)
	}

	got, err := prepareSSHInitDirectory(home)
	if err != nil {
		t.Fatalf("prepareSSHInitDirectory() error = %v", err)
	}
	if got != current {
		t.Fatalf("prepareSSHInitDirectory() = %q, want %q", got, current)
	}
	assertMode(t, current, 0o700)
	assertFileContent(t, legacy, "legacy-file")

	if err := prepareSSHDirectory(home); err != nil {
		t.Fatalf("prepareSSHDirectory() after ssh-init error = %v", err)
	}
}

func TestPrepareSSHInitDirectoryMigratesLegacyDirectory(t *testing.T) {
	home := t.TempDir()
	legacy := config.HostLegacySSHDir(home)
	writeTestSSHDirectory(t, legacy, "old-key")

	current, err := prepareSSHInitDirectory(home)
	if err != nil {
		t.Fatalf("prepareSSHInitDirectory() error = %v", err)
	}
	assertSSHPathMissing(t, legacy)
	assertFileContent(t, filepath.Join(current, "id_ed25519"), "old-key")
	assertMode(t, filepath.Join(current, "id_ed25519"), 0o600)
}

func TestMigrateLegacySSHDirAcrossFilesystemsFailsAndKeepsSource(t *testing.T) {
	home := t.TempDir()
	legacy := config.HostLegacySSHDir(home)
	current := config.HostSSHDir(home)
	writeTestSSHDirectory(t, legacy, "cross-device-key")

	crossDeviceRename := func(oldPath string, newPath string) error {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EXDEV}
	}
	_, err := migrateLegacySSHDirWithRename(home, crossDeviceRename)
	if !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("migrateLegacySSHDirWithRename() error = %v, want EXDEV", err)
	}
	for _, want := range []string{legacy, current, "move it manually"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("migrateLegacySSHDirWithRename() error = %q, want it to contain %q", err, want)
		}
	}
	assertFileContent(t, filepath.Join(legacy, "id_ed25519"), "cross-device-key")
	assertSSHPathMissing(t, current)

	// An empty current directory is still there after the failed move.
	if err := os.Mkdir(current, 0o750); err != nil {
		t.Fatalf("create empty current SSH directory: %v", err)
	}
	if _, err := migrateLegacySSHDirWithRename(home, crossDeviceRename); !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("migrateLegacySSHDirWithRename() error = %v, want EXDEV", err)
	}
	assertFileContent(t, filepath.Join(legacy, "id_ed25519"), "cross-device-key")
	assertMode(t, current, 0o750)
}

func TestMigrateLegacySSHDirReplacesEmptyCurrentDirectory(t *testing.T) {
	home := t.TempDir()
	legacy := config.HostLegacySSHDir(home)
	current := config.HostSSHDir(home)
	writeTestSSHDirectory(t, legacy, "old-key")
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatalf("create empty current SSH directory: %v", err)
	}

	state, err := migrateLegacySSHDir(home)
	if err != nil {
		t.Fatalf("migrateLegacySSHDir() error = %v", err)
	}
	if state != sshMigrationCompleted {
		t.Fatalf("migration state = %v, want completed", state)
	}
	assertSSHPathMissing(t, legacy)
	assertFileContent(t, filepath.Join(current, "id_ed25519"), "old-key")
	assertMode(t, current, 0o700)
}

func TestSetupSSHRestrictsExistingKnownHostsCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	current := config.HostSSHDir(home)
	// An existing key keeps setupSSH from running ssh-keygen.
	writeTestSSHDirectory(t, current, "existing-key")
	knownHostsCopy := filepath.Join(current, "known_hosts")
	if err := os.WriteFile(knownHostsCopy, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write existing known_hosts copy: %v", err)
	}
	if err := os.Chmod(knownHostsCopy, 0o644); err != nil {
		t.Fatalf("chmod existing known_hosts copy: %v", err)
	}
	userSSH := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(userSSH, 0o700); err != nil {
		t.Fatalf("create user SSH directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userSSH, "known_hosts"), []byte("host-keys"), 0o644); err != nil {
		t.Fatalf("write user known_hosts: %v", err)
	}

	if err := setupSSH(); err != nil {
		t.Fatalf("setupSSH() error = %v", err)
	}
	assertFileContent(t, knownHostsCopy, "host-keys")
	assertMode(t, knownHostsCopy, 0o600)
}

func TestPrepareSSHInitDirectoryHardensSymlinkedCurrentDirectory(t *testing.T) {
	home := t.TempDir()
	current := config.HostSSHDir(home)
	target := filepath.Join(home, "keys")
	writeTestSSHDirectory(t, target, "linked-key")
	if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
		t.Fatalf("create current parent: %v", err)
	}
	if err := os.Symlink(target, current); err != nil {
		t.Fatalf("create current symlink: %v", err)
	}

	if _, err := prepareSSHInitDirectory(home); err != nil {
		t.Fatalf("prepareSSHInitDirectory() error = %v", err)
	}
	assertSymlink(t, current)
	assertMode(t, target, 0o700)
	assertMode(t, filepath.Join(target, "id_ed25519"), 0o600)
}

func TestMigrateLegacySSHDirRenameFailureLeavesSource(t *testing.T) {
	home := t.TempDir()
	legacy := config.HostLegacySSHDir(home)
	current := config.HostSSHDir(home)
	writeTestSSHDirectory(t, legacy, "old-key")
	wantErr := errors.New("rename denied")

	state, err := migrateLegacySSHDirWithRename(home, func(string, string) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("migrateLegacySSHDirWithRename() error = %v, want %v", err, wantErr)
	}
	if state != sshMigrationPending {
		t.Fatalf("migration state = %v, want pending", state)
	}
	assertFileContent(t, filepath.Join(legacy, "id_ed25519"), "old-key")
	assertSSHPathMissing(t, current)
}

func TestPrepareSSHForCleanupDryRunDoesNotMigrate(t *testing.T) {
	home := t.TempDir()
	legacy := config.HostLegacySSHDir(home)
	current := config.HostSSHDir(home)
	writeTestSSHDirectory(t, legacy, "old-key")

	if err := prepareSSHForCleanup(home, true); err != nil {
		t.Fatalf("prepareSSHForCleanup() error = %v", err)
	}
	assertFileContent(t, filepath.Join(legacy, "id_ed25519"), "old-key")
	assertMode(t, legacy, 0o755)
	assertMode(t, filepath.Join(legacy, "id_ed25519"), 0o644)
	assertSSHPathMissing(t, current)
}

func writeTestSSHDirectory(t *testing.T, dir string, key string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create SSH directory: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod SSH directory: %v", err)
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte(key), 0o644); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod private key: %v", err)
	}
	pubPath := keyPath + ".pub"
	if err := os.WriteFile(pubPath, []byte("public-key"), 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	if err := os.Chmod(pubPath, 0o644); err != nil {
		t.Fatalf("chmod public key: %v", err)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s content = %q, want %q", path, string(data), want)
	}
}

func assertSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat symlink %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s mode = %v, want symlink", path, info.Mode())
	}
}

func assertSSHPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be absent, stat error = %v", path, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
