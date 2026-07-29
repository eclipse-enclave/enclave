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

// fixedDriveTypes answers as a host with local drives plus two mappings: Z: to a
// WSL share, as `pushd \\wsl.localhost\...` produces in cmd.exe, and W: to an
// ordinary file server.
func fixedDriveTypes(root string) driveKind {
	if strings.EqualFold(root, `Z:\`) || strings.EqualFold(root, `W:\`) || strings.EqualFold(root, `V:\`) {
		return driveRemote
	}
	return driveOther
}

func fixedDriveTargets(letter string) string {
	switch strings.ToUpper(letter) {
	case "Z":
		return `\\wsl.localhost\Ubuntu\home\p\proj`
	case "W":
		return `\\fileserver\share`
	default:
		// V: is remote but unresolvable, as a disconnected mapping would be.
		return ""
	}
}

func env(pairs ...string) lookupFunc {
	return lookupIn(pairs)
}

func resolve(t *testing.T, cwd string, environ ...string) (target, error) {
	t.Helper()
	return resolveTarget(cwd, env(environ...), fixedDriveTypes, fixedDriveTargets)
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
			got, err := resolve(t, tc.cwd)
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
	got, err := resolve(t, `\\wsl.localhost\Ubuntu\home\p\proj`, envDistro+"=Debian")
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
	got, err := resolve(t, `\\wsl.localhost\Ubuntu\home\p`, envDistro+"=ubuntu")
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings = %q, want none", got.Warnings)
	}
}

func TestResolveTargetRefusesWindowsDriveByDefault(t *testing.T) {
	_, err := resolve(t, `C:\Users\p\proj`)
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
			got, err := resolve(t, tc.cwd, envAllowWindowsPath+"=1")
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
	got, err := resolve(t, `C:\Users\p`, envAllowWindowsPath+"=1", envDistro+"=Debian")
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

// A drive letter mapping a WSL share is an alias for a path the launcher already
// accepts, which is what cmd.exe produces for `pushd \\wsl.localhost\...`.
func TestResolveTargetResolvesDriveLetterMappingAWSLShare(t *testing.T) {
	cases := []struct {
		name     string
		cwd      string
		wantPath string
	}{
		{"drive root is the mapped directory", `Z:\`, "/home/p/proj"},
		{"drive root without separator", `Z:`, "/home/p/proj"},
		{"lowercase letter", `z:\`, "/home/p/proj"},
		{"below the mapped directory", `Z:\sub\dir`, "/home/p/proj/sub/dir"},
		{"spaces below the mapped directory", `Z:\my dir`, "/home/p/proj/my dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(t, tc.cwd)
			if err != nil {
				t.Fatalf("resolveTarget(%q): %v", tc.cwd, err)
			}
			if got.Distro != "Ubuntu" {
				t.Errorf("distro = %q, want Ubuntu", got.Distro)
			}
			if got.LinuxPath != tc.wantPath {
				t.Errorf("linux path = %q, want %q", got.LinuxPath, tc.wantPath)
			}
			if len(got.Warnings) != 0 {
				t.Errorf("warnings = %q, want none", got.Warnings)
			}
		})
	}
}

// The opt-in is about accepting /mnt/<letter> slowness, which does not apply
// here: the resolved path is inside the distribution's own filesystem.
func TestResolveTargetResolvesWSLDriveWithoutTheWindowsPathOptIn(t *testing.T) {
	got, err := resolve(t, `Z:\sub`)
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if got.LinuxPath != "/home/p/proj/sub" {
		t.Errorf("linux path = %q", got.LinuxPath)
	}
}

func TestResolveTargetWarnsOnDistroConflictThroughAMappedDrive(t *testing.T) {
	got, err := resolve(t, `Z:\`, envDistro+"=Debian")
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if got.Distro != "Ubuntu" {
		t.Errorf("distro = %q, want Ubuntu", got.Distro)
	}
	if len(got.Warnings) != 1 {
		t.Errorf("warnings = %q, want the same conflict warning a UNC path gets", got.Warnings)
	}
}

func TestResolveTargetResolvesLegacyWSLShareMapping(t *testing.T) {
	legacy := func(string) string { return `\\wsl$\Debian\srv\app` }

	got, err := resolveTarget(`Z:\x`, env(), fixedDriveTypes, legacy)
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if got.Distro != "Debian" || got.LinuxPath != "/srv/app/x" {
		t.Errorf("target = %q at %q", got.Distro, got.LinuxPath)
	}
}

// Everything that is not a WSL share stays refused, including under the
// Windows-path opt-in: guessing a path would bind-mount the wrong directory into
// a container an agent can write to.
func TestResolveTargetRefusesMappedNetworkDrivesThatAreNotWSLShares(t *testing.T) {
	cases := []struct {
		name       string
		cwd        string
		wantDetail string
	}{
		{"ordinary file server", `W:\proj`, `\\fileserver\share`},
		{"unresolvable mapping", `V:\proj`, "could not resolve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolve(t, tc.cwd, envAllowWindowsPath+"=1")
			if err == nil {
				t.Fatalf("expected %q to be refused", tc.cwd)
			}
			if !strings.Contains(err.Error(), "mapped network drive") {
				t.Errorf("error %q does not explain the cause", err)
			}
			if !strings.Contains(err.Error(), tc.wantDetail) {
				t.Errorf("error %q does not mention %q", err, tc.wantDetail)
			}
		})
	}
}

// A resolver that answers with something unusable must not be trusted into a
// half-formed target.
func TestResolveTargetRefusesMalformedDriveMappings(t *testing.T) {
	for _, mapped := range []string{`C:\local\path`, `\\`, `\\wsl.localhost`, `\\wsl.localhost\`, "relative"} {
		t.Run(mapped, func(t *testing.T) {
			resolver := func(string) string { return mapped }
			if _, err := resolveTarget(`Z:\proj`, env(), fixedDriveTypes, resolver); err == nil {
				t.Errorf("expected a mapping to %q to be refused", mapped)
			}
		})
	}
}

func TestResolveTargetRefusesNetworkShare(t *testing.T) {
	for _, cwd := range []string{`\\server\share\proj`, `\\?\UNC\server\share\proj`} {
		_, err := resolve(t, cwd, envAllowWindowsPath+"=1")
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
		if _, err := resolve(t, cwd); err == nil {
			t.Errorf("expected %q to be refused: it names no distribution", cwd)
		}
	}
}

func TestResolveTargetRefusesUnrecognizedPaths(t *testing.T) {
	for _, cwd := range []string{"", "relative\\path", "/home/p/proj", "1:\\proj"} {
		if _, err := resolve(t, cwd); err == nil {
			t.Errorf("expected %q to be refused", cwd)
		}
	}
}

func TestAllowWindowsPathAcceptsCommonTruthyValues(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "on", " 1 "} {
		if _, err := resolve(t, `C:\p`, envAllowWindowsPath+"="+value); err != nil {
			t.Errorf("%s=%q should opt in: %v", envAllowWindowsPath, value, err)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "maybe"} {
		if _, err := resolve(t, `C:\p`, envAllowWindowsPath+"="+value); err == nil {
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
