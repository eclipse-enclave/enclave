// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package usercmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"enclave/internal/config"
)

func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write exec %s: %v", path, err)
	}
}

func writeNonExec(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write non-exec %s: %v", path, err)
	}
}

func mkdirs(t *testing.T, home string) (string, string) {
	t.Helper()
	hostDir := config.HostCommandsHostDir(home)
	sessionDir := config.HostCommandsSessionDir(home)
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatalf("mkdir host: %v", err)
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	return hostDir, sessionDir
}

func TestDiscoverExecBitFiltering(t *testing.T) {
	home := t.TempDir()
	hostDir, _ := mkdirs(t, home)
	writeExec(t, filepath.Join(hostDir, "deploy"))
	writeNonExec(t, filepath.Join(hostDir, "notexec"))

	cmds, warnings := Discover(home)

	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d: %v", len(cmds), cmds)
	}
	if cmds[0].Name != "deploy" || cmds[0].Target != TargetHost {
		t.Fatalf("unexpected command: %+v", cmds[0])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "notexec") || !strings.Contains(warnings[0], "not executable") {
		t.Fatalf("expected non-exec warning, got %v", warnings)
	}
}

func TestDiscoverExtensionKeptInName(t *testing.T) {
	home := t.TempDir()
	hostDir, _ := mkdirs(t, home)
	writeExec(t, filepath.Join(hostDir, "deploy.sh"))

	cmds, _ := Discover(home)
	if len(cmds) != 1 || cmds[0].Name != "deploy.sh" {
		t.Fatalf("expected command name deploy.sh, got %v", cmds)
	}
}

func TestDiscoverHostSessionDuplicateHostWins(t *testing.T) {
	home := t.TempDir()
	hostDir, sessionDir := mkdirs(t, home)
	writeExec(t, filepath.Join(hostDir, "triage"))
	writeExec(t, filepath.Join(sessionDir, "triage"))

	cmds, warnings := Discover(home)

	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d: %v", len(cmds), cmds)
	}
	if cmds[0].Target != TargetHost {
		t.Fatalf("expected host to win, got %+v", cmds[0])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "triage") || !strings.Contains(warnings[0], "using host") {
		t.Fatalf("expected duplicate warning, got %v", warnings)
	}
}

func TestDiscoverSessionCommand(t *testing.T) {
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	writeExec(t, filepath.Join(sessionDir, "review"))

	cmds, warnings := Discover(home)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if len(cmds) != 1 || cmds[0].Name != "review" || cmds[0].Target != TargetSession {
		t.Fatalf("unexpected session command: %v", cmds)
	}
}

func TestDiscoverMissingDirs(t *testing.T) {
	home := t.TempDir()

	cmds, warnings := Discover(home)
	if len(cmds) != 0 {
		t.Fatalf("expected no commands, got %v", cmds)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
}

func TestDiscoverSkipsSubdirectories(t *testing.T) {
	home := t.TempDir()
	hostDir, _ := mkdirs(t, home)
	if err := os.MkdirAll(filepath.Join(hostDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	writeExec(t, filepath.Join(hostDir, "deploy"))

	cmds, warnings := Discover(home)
	if len(cmds) != 1 || cmds[0].Name != "deploy" {
		t.Fatalf("expected only deploy, got %v", cmds)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for subdirectory, got %v", warnings)
	}
}

func TestDiscoverUnreadableDirWarns(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permission bits")
	}
	home := t.TempDir()
	hostDir, _ := mkdirs(t, home)
	writeExec(t, filepath.Join(hostDir, "deploy"))
	if err := os.Chmod(hostDir, 0o000); err != nil {
		t.Fatalf("chmod host dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(hostDir, 0o755) })

	cmds, warnings := Discover(home)
	if len(cmds) != 0 {
		t.Fatalf("expected no commands from unreadable dir, got %v", cmds)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "cannot read user command directory") {
		t.Fatalf("expected unreadable-dir warning, got %v", warnings)
	}
}

func TestDiscoverSymlinkToExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	hostDir, _ := mkdirs(t, home)
	target := filepath.Join(t.TempDir(), "real-deploy")
	writeExec(t, target)
	if err := os.Symlink(target, filepath.Join(hostDir, "deploy")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if len(cmds) != 1 || cmds[0].Name != "deploy" || cmds[0].Target != TargetHost {
		t.Fatalf("expected deploy command from symlink, got %v", cmds)
	}
}

func TestDiscoverSessionSymlinkOutsideWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	target := filepath.Join(t.TempDir(), "real-deploy")
	writeExec(t, target)
	if err := os.Symlink(target, filepath.Join(sessionDir, "deploy")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(cmds) != 0 {
		t.Fatalf("expected no commands from external session symlink, got %v", cmds)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %v", warnings)
	}
	w := warnings[0]
	if !strings.Contains(w, "deploy") || !strings.Contains(w, "symlink") ||
		!strings.Contains(w, "absolute target") {
		t.Fatalf("expected absolute session symlink warning, got %q", w)
	}
}

func TestDiscoverSessionSymlinkInsideRegisters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	// Place the real script in a subdirectory (subdirectories are not
	// registered directly) so only the symlink becomes a command.
	sub := filepath.Join(sessionDir, "tools")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	target := filepath.Join(sub, "real-deploy")
	writeExec(t, target)
	// Relative link is the only shape that resolves inside the container mount.
	if err := os.Symlink("tools/real-deploy", filepath.Join(sessionDir, "deploy")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if len(cmds) != 1 || cmds[0].Name != "deploy" || cmds[0].Target != TargetSession {
		t.Fatalf("expected deploy session command from inside symlink, got %v", cmds)
	}
}

func TestDiscoverSessionSymlinkAbsoluteInsideWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	sub := filepath.Join(sessionDir, "tools")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	target := filepath.Join(sub, "real-deploy")
	writeExec(t, target)
	// Absolute target is in-tree on the host but does not resolve under the
	// neutral container mount path, so it must be skipped.
	if err := os.Symlink(target, filepath.Join(sessionDir, "deploy")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(cmds) != 0 {
		t.Fatalf("expected no commands from absolute in-tree symlink, got %v", cmds)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "deploy") ||
		!strings.Contains(warnings[0], "absolute target") {
		t.Fatalf("expected absolute-target warning, got %v", warnings)
	}
}

func TestDiscoverSessionSymlinkEscapeReenterWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	sub := filepath.Join(sessionDir, "tools")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	writeExec(t, filepath.Join(sub, "real-deploy"))
	// Relative link that escapes the directory (..) before re-entering; the
	// leading ".." does not exist above the container mount, so reject it.
	if err := os.Symlink("../session/tools/real-deploy", filepath.Join(sessionDir, "deploy")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(cmds) != 0 {
		t.Fatalf("expected no commands from escape-and-reenter symlink, got %v", cmds)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "deploy") ||
		!strings.Contains(warnings[0], "escapes") {
		t.Fatalf("expected escape warning, got %v", warnings)
	}
}

func TestDiscoverSessionSymlinkMultiHopRelativeRegisters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	sub := filepath.Join(sessionDir, "tools")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	writeExec(t, filepath.Join(sub, "real-deploy"))
	// deploy -> mid -> tools/real-deploy, all relative and in-tree.
	if err := os.Symlink("tools/real-deploy", filepath.Join(sessionDir, "mid")); err != nil {
		t.Fatalf("symlink mid: %v", err)
	}
	if err := os.Symlink("mid", filepath.Join(sessionDir, "deploy")); err != nil {
		t.Fatalf("symlink deploy: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	names := map[string]bool{}
	for _, c := range cmds {
		if c.Target != TargetSession {
			t.Fatalf("expected session target, got %+v", c)
		}
		names[c.Name] = true
	}
	if !names["deploy"] || !names["mid"] {
		t.Fatalf("expected deploy and mid to register, got %v", cmds)
	}
}

func TestDiscoverSessionSymlinkMultiHopAbsoluteHopWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	sub := filepath.Join(sessionDir, "tools")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	target := filepath.Join(sub, "real-deploy")
	writeExec(t, target)
	// deploy (relative) -> mid (absolute); the absolute later hop is rejected.
	if err := os.Symlink(target, filepath.Join(sessionDir, "mid")); err != nil {
		t.Fatalf("symlink mid: %v", err)
	}
	if err := os.Symlink("mid", filepath.Join(sessionDir, "deploy")); err != nil {
		t.Fatalf("symlink deploy: %v", err)
	}

	cmds, warnings := Discover(home)
	// mid itself is an absolute link and is also warned + skipped.
	for _, c := range cmds {
		if c.Name == "deploy" {
			t.Fatalf("expected deploy to be skipped, got %v", cmds)
		}
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, "deploy") && strings.Contains(w, "absolute target") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected absolute-hop warning for deploy, got %v", warnings)
	}
}

func TestDiscoverSessionSymlinkLoopWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	// a -> b -> a: a relative loop that never reaches a regular file.
	if err := os.Symlink("b", filepath.Join(sessionDir, "a")); err != nil {
		t.Fatalf("symlink a: %v", err)
	}
	if err := os.Symlink("a", filepath.Join(sessionDir, "b")); err != nil {
		t.Fatalf("symlink b: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(cmds) != 0 {
		t.Fatalf("expected no commands from symlink loop, got %v", cmds)
	}
	// A self-referential loop is rejected: os.Stat follows the chain and fails
	// with ELOOP ("broken symlink"); a chain that resolves but exceeds the hop
	// cap trips validateSessionSymlink ("too many levels"/"loop"). Either way
	// the entry must be warned about and skipped.
	if len(warnings) == 0 {
		t.Fatalf("expected a warning for symlink loop, got none")
	}
	for _, w := range warnings {
		if !strings.Contains(w, "symlink") {
			t.Fatalf("expected symlink-related warning, got %q", w)
		}
	}
}

func TestDiscoverSessionSymlinkAbsoluteIntermediateDirWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	realtools := filepath.Join(sessionDir, "realtools")
	if err := os.MkdirAll(realtools, 0o755); err != nil {
		t.Fatalf("mkdir realtools: %v", err)
	}
	writeExec(t, filepath.Join(realtools, "real-deploy"))
	// dirlink is an absolute symlink to an in-tree directory; it resolves on
	// the host but the absolute link text is invalid under the container mount.
	if err := os.Symlink(realtools, filepath.Join(sessionDir, "dirlink")); err != nil {
		t.Fatalf("symlink dirlink: %v", err)
	}
	// deploy is a relative link that traverses the absolute intermediate dir.
	if err := os.Symlink("dirlink/real-deploy", filepath.Join(sessionDir, "deploy")); err != nil {
		t.Fatalf("symlink deploy: %v", err)
	}

	cmds, warnings := Discover(home)
	for _, c := range cmds {
		if c.Name == "deploy" {
			t.Fatalf("expected deploy to be skipped (absolute intermediate dir), got %v", cmds)
		}
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, "deploy") && strings.Contains(w, "absolute target") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected absolute-target warning for deploy via intermediate dir, got %v", warnings)
	}
}

func TestDiscoverSessionSymlinkEscapingIntermediateDirWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	// outsidedir lives beside the session dir (commands/outsidedir), outside
	// the mounted session command directory.
	outside := filepath.Join(filepath.Dir(sessionDir), "outsidedir")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	writeExec(t, filepath.Join(outside, "real-deploy"))
	// dirlink is a relative link that escapes the session dir.
	if err := os.Symlink("../outsidedir", filepath.Join(sessionDir, "dirlink")); err != nil {
		t.Fatalf("symlink dirlink: %v", err)
	}
	if err := os.Symlink("dirlink/real-deploy", filepath.Join(sessionDir, "deploy")); err != nil {
		t.Fatalf("symlink deploy: %v", err)
	}

	cmds, warnings := Discover(home)
	for _, c := range cmds {
		if c.Name == "deploy" {
			t.Fatalf("expected deploy to be skipped (escaping intermediate dir), got %v", cmds)
		}
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, "deploy") && strings.Contains(w, "escapes") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected escape warning for deploy via intermediate dir, got %v", warnings)
	}
}

func TestDiscoverSessionSymlinkRelativeIntermediateDirRegisters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	_, sessionDir := mkdirs(t, home)
	realtools := filepath.Join(sessionDir, "realtools")
	if err := os.MkdirAll(realtools, 0o755); err != nil {
		t.Fatalf("mkdir realtools: %v", err)
	}
	writeExec(t, filepath.Join(realtools, "real-deploy"))
	// dirlink is a relative, in-tree symlinked directory; traversing it is fine.
	if err := os.Symlink("realtools", filepath.Join(sessionDir, "dirlink")); err != nil {
		t.Fatalf("symlink dirlink: %v", err)
	}
	if err := os.Symlink("dirlink/real-deploy", filepath.Join(sessionDir, "deploy")); err != nil {
		t.Fatalf("symlink deploy: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	var found bool
	for _, c := range cmds {
		if c.Name == "deploy" {
			if c.Target != TargetSession {
				t.Fatalf("expected session target, got %+v", c)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("expected deploy to register via relative intermediate dir, got %v", cmds)
	}
}

func TestDiscoverBrokenSymlinkWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	hostDir, _ := mkdirs(t, home)
	missing := filepath.Join(t.TempDir(), "missing")
	if err := os.Symlink(missing, filepath.Join(hostDir, "deploy")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(cmds) != 0 {
		t.Fatalf("expected no commands from broken symlink, got %v", cmds)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "deploy") || !strings.Contains(warnings[0], "broken symlink") {
		t.Fatalf("expected broken-symlink warning, got %v", warnings)
	}
}

func TestDiscoverSymlinkToDirectorySkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	hostDir, _ := mkdirs(t, home)
	targetDir := t.TempDir()
	if err := os.Symlink(targetDir, filepath.Join(hostDir, "linkdir")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeExec(t, filepath.Join(hostDir, "deploy"))

	cmds, warnings := Discover(home)
	if len(cmds) != 1 || cmds[0].Name != "deploy" {
		t.Fatalf("expected only deploy, got %v", cmds)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for symlinked directory, got %v", warnings)
	}
}

func TestDiscoverSymlinkToNonExecutableWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home := t.TempDir()
	hostDir, _ := mkdirs(t, home)
	target := filepath.Join(t.TempDir(), "real-plain")
	writeNonExec(t, target)
	if err := os.Symlink(target, filepath.Join(hostDir, "deploy")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmds, warnings := Discover(home)
	if len(cmds) != 0 {
		t.Fatalf("expected no commands, got %v", cmds)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "deploy") || !strings.Contains(warnings[0], "not executable") {
		t.Fatalf("expected not-executable warning, got %v", warnings)
	}
}

func TestDiscoverDeterministicOrder(t *testing.T) {
	home := t.TempDir()
	hostDir, sessionDir := mkdirs(t, home)
	writeExec(t, filepath.Join(hostDir, "charlie"))
	writeExec(t, filepath.Join(hostDir, "alpha"))
	writeExec(t, filepath.Join(sessionDir, "bravo"))

	cmds, _ := Discover(home)
	got := []string{cmds[0].Name, cmds[1].Name, cmds[2].Name}
	want := []string{"alpha", "bravo", "charlie"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, got)
		}
	}
}
