// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"strings"
	"testing"
)

// fixedDriveTypes answers as a host with a local C: and a mapped Z:.
func fixedDriveTypes(root string) driveKind {
	if strings.EqualFold(root, `Z:\`) {
		return driveRemote
	}
	return driveOther
}

func env(pairs ...string) lookupFunc {
	return lookupIn(pairs)
}

func TestResolveTargetWSLUNCPaths(t *testing.T) {
	cases := []struct {
		name      string
		cwd       string
		wantName  string
		wantPath  string
		wantWarns int
	}{
		{"wsl.localhost", `\\wsl.localhost\Ubuntu\home\p\proj`, "Ubuntu", "/home/p/proj", 0},
		{"legacy wsl$", `\\wsl$\Ubuntu\home\p\proj`, "Ubuntu", "/home/p/proj", 0},
		{"case-insensitive host", `\\WSL.LOCALHOST\Ubuntu\home\p`, "Ubuntu", "/home/p", 0},
		{"versioned distro name", `\\wsl.localhost\Ubuntu-24.04\home\p`, "Ubuntu-24.04", "/home/p", 0},
		{"distro root", `\\wsl.localhost\Ubuntu`, "Ubuntu", "/", 0},
		{"distro root with separator", `\\wsl.localhost\Ubuntu\`, "Ubuntu", "/", 0},
		{"forward slashes", `//wsl.localhost/Ubuntu/home/p`, "Ubuntu", "/home/p", 0},
		{"repeated separators", `\\wsl.localhost\Ubuntu\home\\p\`, "Ubuntu", "/home/p", 0},
		{"extended UNC form", `\\?\UNC\wsl.localhost\Ubuntu\home\p`, "Ubuntu", "/home/p", 0},
		{"spaces in path", `\\wsl.localhost\Ubuntu\home\p\my proj`, "Ubuntu", "/home/p/my proj", 0},
		{"non-ascii path", `\\wsl.localhost\Ubuntu\home\p\プロジェクト`, "Ubuntu", "/home/p/プロジェクト", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTarget(tc.cwd, env(), fixedDriveTypes)
			if err != nil {
				t.Fatalf("resolveTarget(%q): %v", tc.cwd, err)
			}
			if got.Distro != tc.wantName {
				t.Errorf("distro = %q, want %q", got.Distro, tc.wantName)
			}
			if got.LinuxPath != tc.wantPath {
				t.Errorf("linux path = %q, want %q", got.LinuxPath, tc.wantPath)
			}
			if len(got.Warnings) != tc.wantWarns {
				t.Errorf("warnings = %q, want %d", got.Warnings, tc.wantWarns)
			}
		})
	}
}

// The working directory has to win: a different distribution cannot see the
// files the session is supposed to work on.
func TestResolveTargetWorkingDirectoryBeatsConfiguredDistro(t *testing.T) {
	got, err := resolveTarget(`\\wsl.localhost\Ubuntu\home\p\proj`, env(envDistro+"=Debian"), fixedDriveTypes)
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if got.Distro != "Ubuntu" {
		t.Errorf("distro = %q, want Ubuntu", got.Distro)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one", got.Warnings)
	}
	for _, want := range []string{envDistro, "Debian", "Ubuntu"} {
		if !strings.Contains(got.Warnings[0], want) {
			t.Errorf("warning %q does not mention %q", got.Warnings[0], want)
		}
	}
}

func TestResolveTargetNoWarningWhenConfiguredDistroAgrees(t *testing.T) {
	// Distribution names are matched case-insensitively, as WSL treats them.
	got, err := resolveTarget(`\\wsl.localhost\Ubuntu\home\p`, env(envDistro+"=ubuntu"), fixedDriveTypes)
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings = %q, want none", got.Warnings)
	}
}

func TestResolveTargetRefusesWindowsDriveByDefault(t *testing.T) {
	_, err := resolveTarget(`C:\Users\p\proj`, env(), fixedDriveTypes)
	if err == nil {
		t.Fatal("expected a Windows drive path to be refused")
	}
	for _, want := range []string{"/mnt/c", envAllowWindowsPath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestResolveTargetAllowsWindowsDriveWhenOptedIn(t *testing.T) {
	cases := []struct {
		cwd      string
		wantPath string
	}{
		{`C:\Users\p\proj`, "/mnt/c/Users/p/proj"},
		{`c:\Users\p\proj`, "/mnt/c/Users/p/proj"},
		{`D:\work\proj`, "/mnt/d/work/proj"},
		{`C:\`, "/mnt/c"},
		{`C:\my dir\proj`, "/mnt/c/my dir/proj"},
		{`\\?\C:\Users\p`, "/mnt/c/Users/p"},
	}
	for _, tc := range cases {
		t.Run(tc.cwd, func(t *testing.T) {
			got, err := resolveTarget(tc.cwd, env(envAllowWindowsPath+"=1"), fixedDriveTypes)
			if err != nil {
				t.Fatalf("resolveTarget(%q): %v", tc.cwd, err)
			}
			if got.LinuxPath != tc.wantPath {
				t.Errorf("linux path = %q, want %q", got.LinuxPath, tc.wantPath)
			}
			if got.Distro != "" {
				t.Errorf("distro = %q, want the WSL default", got.Distro)
			}
		})
	}
}

func TestResolveTargetWindowsDriveUsesConfiguredDistro(t *testing.T) {
	got, err := resolveTarget(`C:\Users\p`, env(envAllowWindowsPath+"=1", envDistro+"=Debian"), fixedDriveTypes)
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if got.Distro != "Debian" {
		t.Errorf("distro = %q, want Debian", got.Distro)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings = %q, want none: there is no conflict to report", got.Warnings)
	}
}

func TestResolveTargetRefusesMappedNetworkDriveEvenWhenOptedIn(t *testing.T) {
	// A mapped drive has no meaningful path inside a distribution, so the
	// Windows-path opt-in does not apply to it.
	_, err := resolveTarget(`Z:\proj`, env(envAllowWindowsPath+"=1"), fixedDriveTypes)
	if err == nil {
		t.Fatal("expected a mapped network drive to be refused")
	}
	if !strings.Contains(err.Error(), "mapped network drive") {
		t.Errorf("error %q does not explain the cause", err)
	}
}

func TestResolveTargetRefusesNetworkShare(t *testing.T) {
	for _, cwd := range []string{`\\server\share\proj`, `\\?\UNC\server\share\proj`} {
		_, err := resolveTarget(cwd, env(envAllowWindowsPath+"=1"), fixedDriveTypes)
		if err == nil {
			t.Fatalf("expected %q to be refused", cwd)
		}
		if !strings.Contains(err.Error(), "network share") {
			t.Errorf("error %q does not explain the cause", err)
		}
	}
}

func TestResolveTargetRefusesUNCWithoutDistro(t *testing.T) {
	for _, cwd := range []string{`\\wsl.localhost`, `\\wsl.localhost\`, `\\wsl$\`} {
		if _, err := resolveTarget(cwd, env(), fixedDriveTypes); err == nil {
			t.Errorf("expected %q to be refused: it names no distribution", cwd)
		}
	}
}

func TestResolveTargetRefusesUnrecognizedPaths(t *testing.T) {
	for _, cwd := range []string{"", "relative\\path", "/home/p/proj", "1:\\proj"} {
		if _, err := resolveTarget(cwd, env(), fixedDriveTypes); err == nil {
			t.Errorf("expected %q to be refused", cwd)
		}
	}
}

func TestAllowWindowsPathAcceptsCommonTruthyValues(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "on", " 1 "} {
		if _, err := resolveTarget(`C:\p`, env(envAllowWindowsPath+"="+value), fixedDriveTypes); err != nil {
			t.Errorf("%s=%q should opt in: %v", envAllowWindowsPath, value, err)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "maybe"} {
		if _, err := resolveTarget(`C:\p`, env(envAllowWindowsPath+"="+value), fixedDriveTypes); err == nil {
			t.Errorf("%s=%q should not opt in", envAllowWindowsPath, value)
		}
	}
}

func TestTargetDescribe(t *testing.T) {
	if got := (target{}).describe(); got != "the default WSL distribution" {
		t.Errorf("describe() = %q", got)
	}
	if got := (target{Distro: "Ubuntu"}).describe(); got != "WSL distribution Ubuntu" {
		t.Errorf("describe() = %q", got)
	}
}

func TestLookupInIsCaseInsensitiveAndDistinguishesEmpty(t *testing.T) {
	lookup := lookupIn([]string{"Path=C:\\Windows", "ENCLAVE_WSL_DISTRO="})

	if value, ok := lookup("PATH"); !ok || value != "C:\\Windows" {
		t.Errorf("lookup(PATH) = %q, %v", value, ok)
	}
	if value, ok := lookup(envDistro); !ok || value != "" {
		t.Errorf("an empty value must stay distinguishable from an unset one: %q, %v", value, ok)
	}
	if _, ok := lookup("NOT_SET"); ok {
		t.Error("lookup reported an unset variable as set")
	}
}
