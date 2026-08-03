// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vncviewer

import (
	"slices"
	"strings"
	"testing"
)

func testTarget() Target {
	return Target{Container: "enclave-claude-abc", Host: "127.0.0.1", Port: "43521", Password: "s3cret"}
}

func TestBuildArgvSubstitutesPlaceholders(t *testing.T) {
	argv, err := BuildArgv([]string{"remmina", "-c", "vnc://:{password}@{host}:{port}", "--name={container}"}, testTarget())
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	want := []string{"remmina", "-c", "vnc://:s3cret@127.0.0.1:43521", "--name=enclave-claude-abc"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
}

func TestBuildArgvAppendsAddressWhenUnreferenced(t *testing.T) {
	argv, err := BuildArgv([]string{"xtigervncviewer", "-FullScreen"}, testTarget())
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	want := []string{"xtigervncviewer", "-FullScreen", "127.0.0.1:43521"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
}

// A command that names only {port} is already addressed, so nothing is
// appended: the viewer is presumed to reach the display its own way (e.g. via
// a tunnel or a fixed host).
func TestBuildArgvDoesNotAppendWhenOnlyPortReferenced(t *testing.T) {
	argv, err := BuildArgv([]string{"myviewer", "--port", "{port}"}, testTarget())
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	want := []string{"myviewer", "--port", "43521"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
}

func TestBuildArgvRejectsEmptyViewer(t *testing.T) {
	if _, err := BuildArgv(nil, testTarget()); err == nil {
		t.Fatal("BuildArgv(nil) = nil error, want error")
	}
}

func TestBuildArgvRejectsIncompleteTarget(t *testing.T) {
	for name, target := range map[string]Target{
		"no host": {Port: "43521"},
		"no port": {Host: "127.0.0.1"},
	} {
		if _, err := BuildArgv([]string{"xtigervncviewer"}, target); err == nil {
			t.Errorf("%s: BuildArgv = nil error, want error", name)
		}
	}
}

// The default viewers must be usable with no configuration: on Linux the
// password reaches TigerVNC through the environment, on macOS through the URL
// that Screen Sharing accepts.
func TestDefaultViewerPerPlatform(t *testing.T) {
	linux, err := BuildArgv(defaultViewerFor("linux"), testTarget())
	if err != nil {
		t.Fatalf("BuildArgv(linux): %v", err)
	}
	if !slices.Equal(linux, []string{"xtigervncviewer", "127.0.0.1:43521"}) {
		t.Errorf("linux argv = %v", linux)
	}
	for _, arg := range linux {
		if strings.Contains(arg, "s3cret") {
			t.Errorf("linux argv leaks the password in argv: %v", linux)
		}
	}

	darwin, err := BuildArgv(defaultViewerFor("darwin"), testTarget())
	if err != nil {
		t.Fatalf("BuildArgv(darwin): %v", err)
	}
	if !slices.Equal(darwin, []string{"open", "vnc://:s3cret@127.0.0.1:43521"}) {
		t.Errorf("darwin argv = %v", darwin)
	}
}
