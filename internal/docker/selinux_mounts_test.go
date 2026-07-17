// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"testing"
)

func TestFormatBind(t *testing.T) {
	tests := []struct {
		name     string
		mount    Mount
		expected string
	}{
		{
			name: "read-write",
			mount: Mount{
				Type:   MountTypeBind,
				Source: "/host/data",
				Target: "/container/data",
			},
			expected: "/host/data:/container/data:z",
		},
		{
			name: "read-only",
			mount: Mount{
				Type:     MountTypeBind,
				Source:   "/host/config",
				Target:   "/container/config",
				ReadOnly: true,
			},
			expected: "/host/config:/container/config:ro,z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBind(tt.mount); got != tt.expected {
				t.Errorf("formatBind() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSplitBindMounts(t *testing.T) {
	mounts := []Mount{
		{Type: MountTypeBind, Source: "/host/a", Target: "/container/a"},
		{Type: MountTypeVolume, Source: "vol", Target: "/container/b"},
		{Type: MountTypeBind, Source: "/host/c", Target: "/container/c", ReadOnly: true},
		{Type: MountTypeTmpfs, Target: "/tmp"},
	}

	binds, remaining := splitBindMounts(mounts)

	if len(binds) != 2 {
		t.Fatalf("expected 2 binds, got %d", len(binds))
	}
	if binds[0] != "/host/a:/container/a:z" {
		t.Errorf("binds[0] = %q, want %q", binds[0], "/host/a:/container/a:z")
	}
	if binds[1] != "/host/c:/container/c:ro,z" {
		t.Errorf("binds[1] = %q, want %q", binds[1], "/host/c:/container/c:ro,z")
	}
	if len(remaining) != 2 {
		t.Fatalf("expected 2 remaining, got %d", len(remaining))
	}
	if remaining[0].Type != MountTypeVolume {
		t.Errorf("remaining[0].Type = %q, want %q", remaining[0].Type, MountTypeVolume)
	}
	if remaining[1].Type != MountTypeTmpfs {
		t.Errorf("remaining[1].Type = %q, want %q", remaining[1].Type, MountTypeTmpfs)
	}
}

func TestSplitBindMountsCounts(t *testing.T) {
	cases := []struct {
		name          string
		mounts        []Mount
		wantBinds     int
		wantRemaining int
	}{
		{
			name:          "all binds",
			mounts:        []Mount{{Type: MountTypeBind, Source: "/a", Target: "/b"}},
			wantBinds:     1,
			wantRemaining: 0,
		},
		{
			name:          "no binds",
			mounts:        []Mount{{Type: MountTypeVolume, Source: "vol", Target: "/data"}},
			wantBinds:     0,
			wantRemaining: 1,
		},
		{name: "empty"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binds, remaining := splitBindMounts(tc.mounts)
			if len(binds) != tc.wantBinds {
				t.Errorf("expected %d binds, got %d", tc.wantBinds, len(binds))
			}
			if len(remaining) != tc.wantRemaining {
				t.Errorf("expected %d remaining, got %d", tc.wantRemaining, len(remaining))
			}
		})
	}
}

func TestSplitMountsForSELinuxWith(t *testing.T) {
	mounts := []Mount{
		{Type: MountTypeBind, Source: "/host/a", Target: "/container/a"},
		{Type: MountTypeTmpfs, Target: "/tmp"},
	}

	binds, remaining := SplitMountsForSELinuxWith(mounts, false)
	if binds != nil {
		t.Errorf("binds = %v, want nil when not enforcing", binds)
	}
	if len(remaining) != len(mounts) {
		t.Fatalf("remaining = %v, want all mounts passed through", remaining)
	}

	binds, remaining = SplitMountsForSELinuxWith(mounts, true)
	if len(binds) != 1 || binds[0] != "/host/a:/container/a:z" {
		t.Errorf("binds = %v, want [/host/a:/container/a:z]", binds)
	}
	if len(remaining) != 1 || remaining[0].Type != MountTypeTmpfs {
		t.Errorf("remaining = %v, want only the tmpfs mount", remaining)
	}
}
