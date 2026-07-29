// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"reflect"
	"strings"
	"testing"
)

func TestWSLArgsWithCD(t *testing.T) {
	t.Parallel()

	got := wslArgs(
		target{Distro: "Ubuntu", LinuxPath: "/home/p/proj"},
		"/home/p/.local/bin/enclave",
		true,
		[]string{"--tool", "codex", "--", "-p", "fix the bug"},
	)
	want := []string{
		"-d", "Ubuntu",
		"--cd", "/home/p/proj",
		"-e", "/home/p/.local/bin/enclave",
		"--tool", "codex", "--", "-p", "fix the bug",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wslArgs = %q, want %q", got, want)
	}
}

func TestWSLArgsWithoutDistroUsesWSLDefault(t *testing.T) {
	t.Parallel()

	got := wslArgs(target{LinuxPath: "/mnt/c/Users/p"}, "/usr/bin/enclave", true, nil)
	want := []string{"--cd", "/mnt/c/Users/p", "-e", "/usr/bin/enclave"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wslArgs = %q, want %q", got, want)
	}
}

// Without --cd the launcher needs a shell to change directory, but the user's
// arguments still arrive as positional parameters rather than as shell text.
func TestWSLArgsFallsBackToShellCD(t *testing.T) {
	t.Parallel()

	got := wslArgs(
		target{Distro: "Ubuntu", LinuxPath: "/home/p/proj"},
		"/usr/bin/enclave",
		false,
		[]string{"--tool", "claude"},
	)
	want := []string{
		"-d", "Ubuntu",
		"-e", "/bin/sh", "-c", cdScript, "enclave-wsl-launcher",
		"/home/p/proj", "/usr/bin/enclave",
		"--tool", "claude",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wslArgs = %q, want %q", got, want)
	}
}

func TestWSLArgsForwardsArgumentsVerbatim(t *testing.T) {
	t.Parallel()

	// The launcher parses no argv of its own, so even its own flag names are
	// just arguments to pass on.
	args := []string{"--help", "-d", "Debian", "--cd", "/elsewhere", "", "--"}
	got := wslArgs(target{Distro: "Ubuntu", LinuxPath: "/p"}, "/usr/bin/enclave", true, args)

	tail := got[len(got)-len(args):]
	if !reflect.DeepEqual(tail, args) {
		t.Errorf("arguments were altered: %q, want %q", tail, args)
	}
}

func TestProbeArgs(t *testing.T) {
	t.Parallel()

	got := probeArgs("Ubuntu", true)
	want := []string{"-d", "Ubuntu", "--cd", "/", "-e", "/bin/sh", "-lc", probeScript}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("probeArgs = %q, want %q", got, want)
	}

	got = probeArgs("", false)
	want = []string{"-e", "/bin/sh", "-lc", probeScript}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("probeArgs = %q, want %q", got, want)
	}
}

// The probe is the one place a shell sees a string the launcher assembled, so it
// must stay a fixed literal with no interpolation.
func TestProbeScriptIsSelfContained(t *testing.T) {
	t.Parallel()

	if strings.Contains(probeScript, "'") {
		t.Error("the probe script must not contain single quotes")
	}
	for _, want := range []string{"command -v enclave", "$HOME/.local/bin/enclave", "/usr/local/bin/enclave", "/usr/bin/enclave"} {
		if !strings.Contains(probeScript, want) {
			t.Errorf("the probe script does not look for %q", want)
		}
	}
}
