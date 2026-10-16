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

	assertReasons(t, NewSensitivePaths(home, nil, nil), []reasonCase{
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

	if got, want := NewSensitivePaths(link, nil, nil).Reason(home), "is your home directory"; got != want {
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
	assertReasons(t, NewSensitivePaths(home, nil, nil), []reasonCase{
		{tmp, want},
		{filepath.Dir(sock), want},
		{filepath.Join(tmp, "proj"), ""},
	})

	t.Setenv("SSH_AUTH_SOCK", "relative/agent.sock")
	if got := NewSensitivePaths(home, nil, nil).Reason(tmp); got != "" {
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

	assertReasons(t, NewSensitivePaths(home, appDataDirs, nil), []reasonCase{
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

	assertReasons(t, NewSensitivePaths(home, []string{home, filepath.Join(home, "enclave")}, nil), []reasonCase{
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
	s := NewSensitivePaths(home, []string{filepath.Join(outside, "runtime")}, nil)

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

type assessCase struct {
	dir    string
	reason string
	hard   string
	origin string
}

func assertAssessments(t *testing.T, s SensitivePaths, exceptions []SensitiveDirException, cases []assessCase) {
	t.Helper()
	for _, tc := range cases {
		got := s.Assess(tc.dir, exceptions)
		origin := ""
		if got.Exception != nil {
			origin = got.Exception.Origin
		}
		if got.Reason != tc.reason || got.HardReason != tc.hard || origin != tc.origin {
			t.Errorf("Assess(%s) = reason %q, hard %q, exception %q; want %q, %q, %q",
				tc.dir, got.Reason, got.HardReason, origin, tc.reason, tc.hard, tc.origin)
		}
	}
}

func TestSensitiveDirExceptionCoversItsSubtreeOnly(t *testing.T) {
	home := realTempDir(t)
	m2 := filepath.Join(home, ".m2")
	repo := filepath.Join(m2, "repository")
	mkdirs(t, filepath.Join(repo, "org"), filepath.Join(m2, "wrapper"), filepath.Join(home, "code"))
	exceptions := []SensitiveDirException{NewSensitiveDirException(repo, "entry")}

	assertAssessments(t, NewSensitivePaths(home, nil, nil), exceptions, []assessCase{
		{dir: repo, reason: "is inside ~/.m2", origin: "entry"},
		{dir: filepath.Join(repo, "org"), reason: "is inside ~/.m2", origin: "entry"},
		{dir: m2, reason: "is ~/.m2"},
		{dir: filepath.Join(m2, "wrapper"), reason: "is inside ~/.m2"},
		{dir: filepath.Join(home, "code")},
	})
}

func TestSensitiveDirExceptionResolvesSymlinks(t *testing.T) {
	home := realTempDir(t)
	outside := realTempDir(t)
	m2 := filepath.Join(outside, "m2")
	mkdirs(t, filepath.Join(m2, "repository"), filepath.Join(home, "links"), filepath.Join(home, ".gradle", "caches"))
	// The mount source of ~/.m2 is where it really lives.
	symlink(t, m2, filepath.Join(home, ".m2"))
	// An exception set through a symlink covers where the link points.
	symlink(t, filepath.Join(home, ".gradle"), filepath.Join(home, "links", "gradle"))
	exceptions := []SensitiveDirException{
		NewSensitiveDirException(filepath.Join(home, ".m2"), "m2 entry"),
		NewSensitiveDirException(filepath.Join(home, "links", "gradle"), "gradle entry"),
	}

	assertAssessments(t, NewSensitivePaths(home, nil, nil), exceptions, []assessCase{
		{dir: m2, reason: "is " + m2 + " (target of ~/.m2)", origin: "m2 entry"},
		{dir: filepath.Join(m2, "repository"), reason: "is inside " + m2 + " (target of ~/.m2)", origin: "m2 entry"},
		{dir: filepath.Join(home, ".gradle", "caches"), reason: "is inside ~/.gradle", origin: "gradle entry"},
	})
}

// A Homeshell workstream root that does not exist yet still works once it is
// created, also under a symlinked parent.
func TestSensitiveDirExceptionCoversDirectoryCreatedLater(t *testing.T) {
	home := realTempDir(t)
	outside := realTempDir(t)
	symlink(t, outside, filepath.Join(home, ".local"))
	exceptions := []SensitiveDirException{
		NewSensitiveDirException(filepath.Join(home, ".local", "share", "homeshell", "workstreams"), "entry"),
	}
	workstream := filepath.Join(outside, "share", "homeshell", "workstreams", "p-x")
	mkdirs(t, workstream)

	assertAssessments(t, NewSensitivePaths(home, nil, nil), exceptions, []assessCase{
		{dir: workstream, reason: "is inside " + outside + " (target of ~/.local)", origin: "entry"},
		{dir: filepath.Join(outside, "share", "homeshell"), reason: "is inside " + outside + " (target of ~/.local)"},
	})
}

// enclaveRoots creates Enclave's config, state, and cache roots under home at
// their default Linux locations.
func enclaveRoots(t *testing.T, home string) []string {
	t.Helper()
	roots := []string{
		filepath.Join(home, ".config", "enclave"),
		filepath.Join(home, ".local", "state", "enclave"),
		filepath.Join(home, ".cache", "enclave"),
	}
	mkdirs(t, roots...)
	return roots
}

func TestSensitiveDirExceptionsCannotCoverHardProtectedLocations(t *testing.T) {
	home := realTempDir(t)
	roots := enclaveRoots(t, home)
	mkdirs(t,
		filepath.Join(home, ".cache", "enclave", "assets"),
		filepath.Join(home, ".cache", "pip"),
		filepath.Join(home, ".local", "state", "host-connector"),
	)
	sock := filepath.Join(home, ".local", "state", "ssh", "agent.sock")
	writeFile(t, sock)
	t.Setenv("SSH_AUTH_SOCK", sock)
	exceptions := []SensitiveDirException{
		NewSensitiveDirException(filepath.Join(home, ".cache"), "cache"),
		NewSensitiveDirException(filepath.Join(home, ".config"), "config"),
		NewSensitiveDirException(filepath.Join(home, ".local", "state"), "state"),
	}
	agent := "the SSH agent socket $SSH_AUTH_SOCK (~/.local/state/ssh/agent.sock)"

	assertAssessments(t, NewSensitivePaths(home, nil, roots), exceptions, []assessCase{
		{dir: filepath.Join(home, ".cache"), reason: "is ~/.cache", hard: "contains ~/.cache/enclave", origin: "cache"},
		{dir: filepath.Join(home, ".cache", "enclave"), reason: "is inside ~/.cache", hard: "is ~/.cache/enclave", origin: "cache"},
		{dir: filepath.Join(home, ".cache", "enclave", "assets"), reason: "is inside ~/.cache", hard: "is inside ~/.cache/enclave", origin: "cache"},
		{dir: filepath.Join(home, ".cache", "pip"), reason: "is inside ~/.cache", origin: "cache"},
		{dir: filepath.Join(home, ".config"), reason: "is ~/.config", hard: "contains ~/.config/enclave", origin: "config"},
		{dir: filepath.Join(home, ".local", "state", "ssh"), reason: "is inside ~/.local", hard: "contains " + agent, origin: "state"},
		{dir: filepath.Join(home, ".local", "state", "host-connector"), reason: "is inside ~/.local", origin: "state"},
	})
}

func TestSensitiveDirExceptionsCannotCoverHome(t *testing.T) {
	home := realTempDir(t)
	exceptions := []SensitiveDirException{NewSensitiveDirException(home, "home")}

	assertAssessments(t, NewSensitivePaths(home, nil, nil), exceptions, []assessCase{
		{dir: home, reason: "is your home directory", hard: "is your home directory", origin: "home"},
		{dir: filepath.Dir(home), reason: "contains your home directory", hard: "contains your home directory"},
	})
}

// The first reason a mount matches need not be a hard one: with ~/.config
// linked into ~/.dotfiles, ~/.dotfiles first matches the link target, yet it
// also contains Enclave's config root.
func TestSensitiveDirExceptionsScanAllHardProtectedLocations(t *testing.T) {
	for _, tc := range []struct {
		name         string
		linkedConfig bool
		reason, hard string
	}{
		{
			name:         "config linked into dotfiles",
			linkedConfig: true,
			reason:       "contains ~/.dotfiles/config (target of ~/.config)",
			hard:         "contains ~/.dotfiles/config/enclave (target of ~/.config/enclave)",
		},
		{name: "config is a real directory", reason: "is ~/.dotfiles"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := realTempDir(t)
			dotfiles := filepath.Join(home, ".dotfiles")
			if tc.linkedConfig {
				mkdirs(t, filepath.Join(dotfiles, "config", "enclave"))
				symlink(t, filepath.Join(dotfiles, "config"), filepath.Join(home, ".config"))
			} else {
				mkdirs(t, dotfiles)
			}
			roots := enclaveRoots(t, home)
			exceptions := []SensitiveDirException{NewSensitiveDirException(dotfiles, "dotfiles")}

			assertAssessments(t, NewSensitivePaths(home, nil, roots), exceptions, []assessCase{
				{dir: dotfiles, reason: tc.reason, hard: tc.hard, origin: "dotfiles"},
			})
		})
	}
}
