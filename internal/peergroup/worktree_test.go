// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorktreeGuard(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	sub := filepath.Join(app, "sub")
	other := filepath.Join(root, "other")
	link := filepath.Join(root, "link")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(app, link); err != nil {
		t.Fatal(err)
	}
	orch := GuardEntry{Member: "orch", ProjectPath: app, MountMode: MountReadWrite}
	cases := []struct {
		name      string
		candidate GuardEntry
		clash     bool
		warnings  int
	}{
		{"same path both rw", GuardEntry{"w1", app, MountReadWrite}, true, 0},
		{"same path via symlink", GuardEntry{"w1", link, MountReadWrite}, true, 0},
		{"same path with trailing slash", GuardEntry{"w1", app + "/", MountReadWrite}, true, 0},
		{"same path read-only", GuardEntry{"w1", app, MountReadOnly}, false, 1},
		{"nested rw", GuardEntry{"w1", sub, MountReadWrite}, false, 1},
		{"disjoint", GuardEntry{"w1", other, MountReadWrite}, false, 0},
		{"sibling prefix", GuardEntry{"w1", app + "x", MountReadWrite}, false, 0},
	}
	g := NewWorktreeGuard()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := g.Check([]GuardEntry{orch}, tc.candidate)
			if tc.clash != errors.Is(err, ErrWorktreeClash) {
				t.Fatalf("err = %v, want clash %v", err, tc.clash)
			}
			if len(res.Warnings) != tc.warnings {
				t.Fatalf("warnings = %v", res.Warnings)
			}
		})
	}
	if _, err := g.Check(nil, GuardEntry{"w1", "relative", MountReadWrite}); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := g.Check(nil, GuardEntry{"w1", app, "rx"}); err == nil {
		t.Fatal("unknown mount mode accepted")
	}
	// The parent containing a member's path is nested too.
	res, err := g.Check([]GuardEntry{{"w1", sub, MountReadOnly}}, GuardEntry{"w2", app, MountReadWrite})
	if err != nil || len(res.Warnings) != 1 {
		t.Fatalf("parent overlap = %v, %v", res, err)
	}
}

func TestWriteOverlay(t *testing.T) {
	path := OverlayPath(filepath.Join(t.TempDir(), "claude"), "enclave-claude-ab12-worker-1")
	o := Overlay{Generation: 3, Active: true, Group: "review", Member: "worker-1", HubAddress: "172.18.0.14", HubPort: HubPort, AdapterPort: AdapterPort, DNSName: "a2a.enclave"}
	if err := WriteOverlay(path, o); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Overlay
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	o.SchemaVersion = SchemaVersion
	if got != o {
		t.Fatalf("overlay = %+v", got)
	}
	if err := WriteOverlay(path, Overlay{}); err == nil {
		t.Fatal("generation 0 accepted")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}
