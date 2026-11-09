//go:build !windows

// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestNativeReadAndDurableUpdatePreserveCanonicalDataAndOwnership(t *testing.T) {
	dir := t.TempDir()
	file := claudeProfile().Providers[0].AuthSecrets[0]
	source := `{"claudeAiOauth":{"accessToken":"synthetic-old-access","refreshToken":"synthetic-refresh","futureMetadata":{"kept":true}}}`
	nativeFile(t, dir, file.File, source)
	target := filepath.Join(dir, file.File)
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadNative(dir, file)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Values["/claudeAiOauth/accessToken"] != "synthetic-old-access" || snapshot.Fingerprint == "" {
		t.Fatal("missing canonical snapshot")
	}
	oauth := snapshot.Document["claudeAiOauth"].(map[string]any)
	if oauth["futureMetadata"] == nil {
		t.Fatal("canonical read stripped unknown fields")
	}
	oauth["accessToken"] = "synthetic-new-access"
	data, err := json.Marshal(snapshot.Document)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteNative(dir, file.File, data); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	oldOwner, newOwner := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if os.SameFile(before, after) || before.Mode().Perm() != after.Mode().Perm() || oldOwner.Uid != newOwner.Uid || oldOwner.Gid != newOwner.Gid {
		t.Fatal("durable rename did not preserve canonical mode/owner")
	}
	updated, err := ReadNative(dir, file)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Values["/claudeAiOauth/accessToken"] != "synthetic-new-access" || updated.Fingerprint == snapshot.Fingerprint || updated.Document["claudeAiOauth"].(map[string]any)["futureMetadata"] == nil {
		t.Fatal("canonical update lost data or its fingerprint")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("durable write left a temporary file")
	}
}

func TestNativeWriteRefusesLinksAndLeavesTheirTargetsUnchanged(t *testing.T) {
	for _, outside := range []bool{false, true} {
		dir := t.TempDir()
		destination := filepath.Join(dir, "inside")
		if outside {
			destination = filepath.Join(t.TempDir(), "outside")
		}
		if err := os.WriteFile(destination, []byte("untouched"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(destination, filepath.Join(dir, ".credentials.json")); err != nil {
			t.Fatal(err)
		}
		if err := WriteNative(dir, ".credentials.json", []byte("replacement")); err == nil {
			t.Fatal("native write followed a link")
		}
		got, err := os.ReadFile(destination)
		if err != nil || string(got) != "untouched" {
			t.Fatal("native write changed a symlink target")
		}
	}
}
