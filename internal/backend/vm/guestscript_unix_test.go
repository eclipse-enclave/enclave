// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The boot script's ensure_dir runs here as this user instead of the guest's
// root: what it creates, what it leaves alone and what it refuses are the
// same.
func TestGuestBootCreatesMissingDirectoriesForTheToolUser(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "runtime-assets", "vm", "session", "boot"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, found := strings.Cut(string(script), "\nensure_dir() {\n")
	body, _, closed := strings.Cut(rest, "\n}\n")
	if !found || !closed {
		t.Fatal("the boot script has no ensure_dir function")
	}
	// The function refuses links anywhere in the path, and a temp directory
	// can be behind one.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "chown.log")
	run := func(target string, env ...string) error {
		program := `set -u
agent_uid=4242 agent_gid=4343
chown() { printf '%s\n' "$*" >> "$LOG"; [ -z "${CHOWN_FAILS:-}" ]; }
as_agent() { "$@"; }
ensure_dir() {
` + body + `
}
ensure_dir "$1"`
		cmd := exec.Command(bash, "-c", program, "bash", target) // #nosec G204 -- the repository's own script and a fixed interpreter.
		cmd.Env = append(append(os.Environ(), "LOG="+log), env...)
		// A relative path must not reach the source tree if it is accepted.
		cmd.Dir = root
		return cmd.Run()
	}

	target := filepath.Join(existing, "a", "b")
	if err := run(target); err != nil {
		t.Fatalf("ensure_dir %s: %v", target, err)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("%s was not created: %v", target, err)
	}
	owned, _ := os.ReadFile(log)
	want := "-h 4242:4343 " + filepath.Join(existing, "a") + "\n-h 4242:4343 " + target + "\n"
	if string(owned) != want {
		t.Errorf("directories given to the tool user:\n%swant:\n%s", owned, want)
	}
	if info, _ := os.Stat(existing); info.Mode().Perm() != 0o711 {
		t.Errorf("an existing directory was changed: %v", info.Mode())
	}
	if err := run(target); err != nil {
		t.Errorf("ensure_dir on an existing directory: %v", err)
	}
	if again, _ := os.ReadFile(log); string(again) != want {
		t.Errorf("an existing directory changed owner:\n%s", again)
	}
	if err := run(filepath.Join(root, "file", "below")); err == nil {
		t.Error("a mount point below a file was accepted")
	}
	// A directory that could not be given to the tool user ends the walk: a
	// later one that could must not make the path look ready.
	unowned := filepath.Join(existing, "unowned")
	if err := run(filepath.Join(unowned, "below"), "CHOWN_FAILS=1"); err == nil {
		t.Error("a mount point below a directory with the wrong owner was accepted")
	}
	if _, err := os.Stat(filepath.Join(unowned, "below")); !os.IsNotExist(err) {
		t.Error("the walk went on after a failed change of owner")
	}
	want += "-h 4242:4343 " + unowned + "\n"
	// The directory with the wrong owner is not left for the next boot,
	// which would take it for one the guest had before. An existing
	// directory above it stays.
	if _, err := os.Stat(unowned); !os.IsNotExist(err) {
		t.Error("a directory that could not be given to the tool user was left behind")
	}
	if _, err := os.Stat(existing); err != nil {
		t.Errorf("an existing directory was removed: %v", err)
	}
	if err := run(filepath.Join(unowned, "below")); err != nil {
		t.Errorf("the next boot: %v", err)
	}
	want += "-h 4242:4343 " + unowned + "\n-h 4242:4343 " + filepath.Join(unowned, "below") + "\n"
	if err := run("relative/path"); err == nil {
		t.Error("a relative mount point was accepted")
	}
	if err := run(existing + "/../existing/c"); err == nil {
		t.Error("a mount point with .. in it was accepted")
	}

	// A link on the guest's disk must not take the mount point, or the
	// directories made for it, somewhere else.
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(filepath.Join(elsewhere, "there"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(existing, "link")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{link, filepath.Join(link, "there"), filepath.Join(link, "new", "deeper")} {
		if err := run(target); err == nil {
			t.Errorf("mount point %s through a symbolic link was accepted", target)
		}
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "new")); !os.IsNotExist(err) {
		t.Error("a directory was created through a symbolic link")
	}
	if after, _ := os.ReadFile(log); string(after) != want {
		t.Errorf("a directory behind a symbolic link changed owner:\n%s", after)
	}
}
