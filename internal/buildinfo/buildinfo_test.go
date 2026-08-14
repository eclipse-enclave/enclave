// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolvePrefersLinkedValues(t *testing.T) {
	goInfo := &debug.BuildInfo{
		Main: debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "1111111111111111111111111111111111111111"},
			{Key: "vcs.time", Value: "2025-01-02T03:04:05Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	got := resolve(Info{
		Version: "0.1.0",
		Commit:  "6ab6e6f",
		Date:    "2026-08-11",
	}, goInfo)

	want := Info{Version: "0.1.0", Commit: "6ab6e6f", Date: "2026-08-11"}
	if got != want {
		t.Fatalf("resolve() = %#v, want %#v", got, want)
	}
}

func TestResolveFallsBackToGoBuildInfo(t *testing.T) {
	goInfo := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.2.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "6ab6e6fc4ca8c7381c021e8e683cef6e69010507"},
			{Key: "vcs.time", Value: "2026-08-11T01:22:33+02:00"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	got := resolve(Info{}, goInfo)
	want := Info{Version: "v0.2.0", Commit: "6ab6e6f-dirty", Date: "2026-08-11"}
	if got != want {
		t.Fatalf("resolve() = %#v, want %#v", got, want)
	}
}

func TestResolveUsesUnknownWithoutBuildInfo(t *testing.T) {
	got := resolve(Info{}, nil)
	want := Info{Version: unknown, Commit: unknown, Date: unknown}
	if got != want {
		t.Fatalf("resolve() = %#v, want %#v", got, want)
	}
	if got.String() != "unknown (unknown, unknown)" {
		t.Fatalf("String() = %q", got.String())
	}
}
