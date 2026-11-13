// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package hoststore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRootedReadsRejectFIFOWithoutOpeningItBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.ReadFile("fifo"); err == nil {
		t.Fatal("FIFO accepted as regular file")
	}
	if _, err := root.ReadDir("fifo"); err == nil {
		t.Fatal("FIFO accepted as directory")
	}
}

func TestWriteFileSetsExactModeUnderUmask(t *testing.T) {
	previous := unix.Umask(0o077)
	defer unix.Umask(previous)
	root, err := OpenFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile("settings.json", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := root.Lstat("settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", info.Mode().Perm())
	}
}

func makeSpecialNodes(t *testing.T, dir string) []string {
	t.Helper()
	names := []string{"ipc.fifo"}
	if err := unix.Mkfifo(filepath.Join(dir, "ipc.fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mknod(filepath.Join(dir, "ipc.sock"), unix.S_IFSOCK|0o600, 0); err == nil {
		names = append(names, "ipc.sock")
	}
	return names
}

func TestOverlayDropsSpecialNodesOutsidePreservedPaths(t *testing.T) {
	target, source := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "auth.json"), []byte("credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	special := makeSpecialNodes(t, target)

	if err := Overlay(target, source, []string{"auth.json"}); err != nil {
		t.Fatalf("special node outside preserved paths blocked the overlay: %v", err)
	}
	for _, name := range special {
		if _, err := os.Lstat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived the overlay: %v", name, err)
		}
	}
	for name, want := range map[string]string{"auth.json": "credentials", "settings.json": "generated"} {
		if data, err := os.ReadFile(filepath.Join(target, name)); err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
}

func TestOverlayRejectsSpecialNodeInPreservedPath(t *testing.T) {
	target, source := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "auth.json"), []byte("credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(target, "state", "ipc.fifo"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Overlay(target, source, []string{"state/"}); err == nil {
		t.Fatal("expected a FIFO in a preserved path to stop the overlay")
	}
	if data, err := os.ReadFile(filepath.Join(target, "auth.json")); err != nil || string(data) != "credentials" {
		t.Fatalf("original changed: %q, %v", data, err)
	}
}

func TestOverlayDropsUnreadableFileOutsidePreservedPaths(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	target, source := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "projects", "history"), []byte("resume"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "stale.json"), []byte("old"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Overlay(target, source, []string{"projects/"}); err != nil {
		t.Fatalf("unreadable file outside preserved paths blocked the overlay: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("stale.json survived the overlay: %v", err)
	}
	for name, want := range map[string]string{"projects/history": "resume", "settings.json": "generated"} {
		if data, err := os.ReadFile(filepath.Join(target, name)); err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
}

func TestOverlayRejectsUnreadableFileInPreservedPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	target, source := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "projects", "history"), []byte("resume"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "auth.json"), []byte("credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Overlay(target, source, []string{"projects/"})
	if err == nil {
		t.Fatal("expected an unreadable file in a preserved path to stop the overlay")
	}
	if strings.Contains(err.Error(), "recoverable") {
		t.Fatalf("overlay failed after it changed the store: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "auth.json")); err != nil || string(data) != "credentials" {
		t.Fatalf("original changed: %q, %v", data, err)
	}
	if info, err := os.Lstat(filepath.Join(target, "projects", "history")); err != nil || info.Mode().Perm() != 0 {
		t.Fatalf("preserved file changed: %v, %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("generated config published: %v", err)
	}
}

func TestOverlayDropsEmptyUnreadableDirectoryOutsidePreservedPaths(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	target, source := t.TempDir(), t.TempDir()
	for _, dir := range []string{"cache", "agent/tmp"} {
		if err := os.MkdirAll(filepath.Join(target, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "agent", "history"), []byte("resume"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"cache", "agent/tmp"} {
		if err := os.Chmod(filepath.Join(target, dir), 0o000); err != nil {
			t.Fatal(err)
		}
	}

	if err := Overlay(target, source, []string{"agent/history"}); err != nil {
		t.Fatalf("empty unreadable directory outside preserved paths blocked the overlay: %v", err)
	}
	for _, dir := range []string{"cache", "agent/tmp"} {
		if _, err := os.Lstat(filepath.Join(target, dir)); !os.IsNotExist(err) {
			t.Fatalf("%s survived the overlay: %v", dir, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(target, "agent", "history")); err != nil || string(data) != "resume" {
		t.Fatalf("preserved history = %q, %v", data, err)
	}
}

func TestOverlayRejectsUnreadableDirectoryItCannotDrop(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	for name, tc := range map[string]struct {
		dir, file, want string
		preserve        []string
	}{
		"not empty":           {dir: "cache", file: "cache/blob", want: "cannot clear unreadable", preserve: []string{"auth.json"}},
		"holds preserved":     {dir: "agent", file: "agent/history", want: "permission denied", preserve: []string{"agent/history"}},
		"inside preserved":    {dir: "projects/x", want: "permission denied", preserve: []string{"projects/"}},
		"may hold glob match": {dir: "cache", want: "permission denied", preserve: []string{"*.jsonl"}},
	} {
		t.Run(name, func(t *testing.T) {
			target, source := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(target, tc.dir), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(target, tc.file), []byte("state"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(target, "auth.json"), []byte("credentials"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("generated"), 0o600); err != nil {
				t.Fatal(err)
			}
			unreadable := filepath.Join(target, tc.dir)
			if err := os.Chmod(unreadable, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(unreadable, 0o700) })

			err := Overlay(target, source, tc.preserve)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Overlay() = %v; want an error containing %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "recoverable") {
				t.Fatalf("overlay failed after it changed the store: %v", err)
			}
			if info, err := os.Lstat(unreadable); err != nil || !info.IsDir() || info.Mode().Perm() != 0 {
				t.Fatalf("unreadable directory changed: %v, %v", info, err)
			}
			if data, err := os.ReadFile(filepath.Join(target, "auth.json")); err != nil || string(data) != "credentials" {
				t.Fatalf("original changed: %q, %v", data, err)
			}
			if _, err := os.Lstat(filepath.Join(target, "settings.json")); !os.IsNotExist(err) {
				t.Fatalf("generated config published: %v", err)
			}
		})
	}
}

func TestOverlayRestoresRemovedUnreadableDirectoriesWhenALaterOneBlocks(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	target, source := t.TempDir(), t.TempDir()
	// The empty directories are created first, so directory order on common
	// filesystems tries at least one of them before the full one.
	empty := []string{"a", "c", "e", "g"}
	all := append(append([]string{}, empty...), "full")
	for _, dir := range all {
		if err := os.Mkdir(filepath.Join(target, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "full", "blob"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, dir := range all {
		unreadable := filepath.Join(target, dir)
		if err := os.Chtimes(unreadable, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unreadable, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(unreadable, 0o700) })
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Overlay(target, source, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot clear unreadable") {
		t.Fatalf("Overlay() = %v; want the full unreadable directory to stop it", err)
	}
	if strings.Contains(err.Error(), "recoverable") || strings.Contains(err.Error(), "could not restore") {
		t.Fatalf("overlay did not leave the store as it was: %v", err)
	}
	for _, dir := range all {
		info, err := os.Lstat(filepath.Join(target, dir))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0 || !info.ModTime().Equal(stamp) {
			t.Fatalf("%s not as before the overlay: %v, %v", dir, info, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("generated config published: %v", err)
	}
}
