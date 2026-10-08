// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mounts

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realTempDir returns a symlink-resolved temp dir, as validated mount sources
// are resolved (macOS temp dirs live behind /var -> /private/var).
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

type reasonCase struct {
	dir  string
	want string
}

func assertReasons(t *testing.T, s SensitivePaths, cases []reasonCase) {
	t.Helper()
	for _, tc := range cases {
		if got := s.Reason(tc.dir); got != tc.want {
			t.Errorf("Reason(%s) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

func TestSensitivePathsHomeAndHiddenEntries(t *testing.T) {
	home := realTempDir(t)
	mkdirs(t,
		filepath.Join(home, ".ssh", "keys"),
		filepath.Join(home, "code", "x"),
		filepath.Join(home, "dotfiles-other"),
	)
	writeFile(t, filepath.Join(home, "dotfiles", "bash", ".bashrc"))
	symlink(t, filepath.Join(home, "dotfiles", "bash", ".bashrc"), filepath.Join(home, ".bashrc"))
	// Links to home or above must not make everything under home sensitive.
	symlink(t, "/", filepath.Join(home, ".root-link"))
	symlink(t, home, filepath.Join(home, ".home-link"))

	assertReasons(t, NewSensitivePaths(home, nil), []reasonCase{
		{home, "is your home directory"},
		{filepath.Dir(home), "contains your home directory"},
		{"/", "contains your home directory"},
		{filepath.Join(home, "code", "x"), ""},
		{filepath.Join(home, ".ssh"), "is ~/.ssh"},
		{filepath.Join(home, ".ssh", "keys"), "is inside ~/.ssh"},
		{filepath.Join(home, "dotfiles"), "contains ~/dotfiles/bash/.bashrc (target of ~/.bashrc)"},
		{filepath.Join(home, "dotfiles-other"), ""},
	})
}

func TestSensitivePathsResolveHome(t *testing.T) {
	home := realTempDir(t)
	link := filepath.Join(realTempDir(t), "home-link")
	symlink(t, home, link)

	if got, want := NewSensitivePaths(link, nil).Reason(home), "is your home directory"; got != want {
		t.Fatalf("Reason(%s) = %q, want %q", home, got, want)
	}
}

func TestSensitivePathsSSHAgentSocket(t *testing.T) {
	home := realTempDir(t)
	tmp := realTempDir(t)
	sock := filepath.Join(tmp, "ssh-abc", "agent.1")
	writeFile(t, sock)
	mkdirs(t, filepath.Join(tmp, "proj"))
	t.Setenv("SSH_AUTH_SOCK", sock)

	want := "contains the SSH agent socket $SSH_AUTH_SOCK (" + sock + ")"
	assertReasons(t, NewSensitivePaths(home, nil), []reasonCase{
		{tmp, want},
		{filepath.Dir(sock), want},
		{filepath.Join(tmp, "proj"), ""},
	})

	t.Setenv("SSH_AUTH_SOCK", "relative/agent.sock")
	if got := NewSensitivePaths(home, nil).Reason(tmp); got != "" {
		t.Fatalf("a relative SSH_AUTH_SOCK must be ignored, got %q", got)
	}
}

func TestSensitivePathsAppDataDirs(t *testing.T) {
	home := realTempDir(t)
	outside := realTempDir(t)
	mkdirs(t,
		filepath.Join(outside, "cfg", "gh"),
		filepath.Join(outside, "runtime", "bus"),
		filepath.Join(outside, "real"),
		filepath.Join(outside, "proj"),
	)
	// A data dir that does not exist yet still resolves through its parent.
	symlink(t, filepath.Join(outside, "real"), filepath.Join(outside, "link"))
	appDataDirs := []string{
		filepath.Join(outside, "cfg"),
		filepath.Join(outside, "link", "state"),
		filepath.Join(outside, "runtime"),
	}

	assertReasons(t, NewSensitivePaths(home, appDataDirs), []reasonCase{
		{filepath.Join(outside, "cfg"), "is " + filepath.Join(outside, "cfg")},
		{filepath.Join(outside, "cfg", "gh"), "is inside " + filepath.Join(outside, "cfg")},
		{outside, "contains " + filepath.Join(outside, "cfg")},
		{filepath.Join(outside, "real"), "contains " + filepath.Join(outside, "real", "state")},
		{filepath.Join(outside, "runtime", "bus"), "is inside " + filepath.Join(outside, "runtime")},
		{filepath.Join(outside, "proj"), ""},
	})
}

// An XDG override such as XDG_CONFIG_HOME=$HOME makes an application data
// directory home itself. Protecting it would make all of home sensitive, but
// the data dirs listed under it stay protected.
func TestSensitivePathsSkipAppDataDirAtHome(t *testing.T) {
	home := realTempDir(t)
	mkdirs(t, filepath.Join(home, "enclave"), filepath.Join(home, "code"))

	assertReasons(t, NewSensitivePaths(home, []string{home, filepath.Join(home, "enclave")}), []reasonCase{
		{filepath.Join(home, "enclave"), "is ~/enclave"},
		{filepath.Join(home, "code"), ""},
	})
}

// Devcontainer binds rely on this: they must resolve inside the project, so
// they cannot be sensitive unless the project is.
func TestSensitivePathsSubdirsOfSafeDirsAreSafe(t *testing.T) {
	home := realTempDir(t)
	outside := realTempDir(t)
	mkdirs(t,
		filepath.Join(home, ".ssh", "keys"),
		filepath.Join(home, ".config", "gh"),
		filepath.Join(home, "code", "x", "src"),
		filepath.Join(home, "dotfiles-other", "a"),
		filepath.Join(outside, "proj", "sub"),
		filepath.Join(outside, "runtime", "bus"),
	)
	writeFile(t, filepath.Join(home, "dotfiles", "bash", ".bashrc"))
	symlink(t, filepath.Join(home, "dotfiles", "bash", ".bashrc"), filepath.Join(home, ".bashrc"))
	writeFile(t, filepath.Join(outside, "ssh-abc", "agent.1"))
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(outside, "ssh-abc", "agent.1"))
	s := NewSensitivePaths(home, []string{filepath.Join(outside, "runtime")})

	for _, root := range []string{home, outside} {
		err := filepath.WalkDir(root, func(dir string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.IsDir() || s.Reason(dir) != "" {
				return err
			}
			return filepath.WalkDir(dir, func(sub string, entry fs.DirEntry, err error) error {
				if err == nil && entry.IsDir() {
					if reason := s.Reason(sub); reason != "" {
						t.Errorf("%s is safe but its subdirectory %s %s", dir, sub, reason)
					}
				}
				return err
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.Reason(filepath.Join(home, "code", "x", "src")) != "" || !strings.HasPrefix(s.Reason(filepath.Join(home, "dotfiles")), "contains ") {
		t.Fatal("fixture does not cover both safe and sensitive directories")
	}
}
