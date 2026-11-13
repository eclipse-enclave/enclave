// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverlayMissingNestedStateDoesNotBlockStartup(t *testing.T) {
	target, source := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(target, "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Overlay(target, source, []string{"agent/history.json"}); err != nil {
		t.Fatalf("missing optional state prevented overlay: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "settings.json")); err != nil || string(data) != "generated" {
		t.Fatalf("generated config missing: %q, %v", data, err)
	}
}

func TestOverlayKeepsStoreDirectoryIdentityAndMode(t *testing.T) {
	target, source := t.TempDir(), t.TempDir()
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := Overlay(target, source, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("store directory replaced or permissions changed: %v -> %v", before.Mode(), after.Mode())
	}
}

func TestOverlayPreservedStateWinsTypeConflict(t *testing.T) {
	target, source := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "auth.json"), []byte("credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "auth.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Overlay(target, source, []string{"auth.json"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "auth.json"))
	if err != nil || string(data) != "credentials" {
		t.Fatalf("lost credentials: %q %v", data, err)
	}
}

func TestOverlayBadSourceLeavesOriginalUntouched(t *testing.T) {
	target, source := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "auth.json"), []byte("credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A generated symlink cannot redirect restoration through an outside tree.
	if err := os.Symlink(t.TempDir(), filepath.Join(source, "agent")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "agent", "history"), []byte("resume"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Overlay(target, source, []string{"agent/history"}); err == nil {
		t.Fatal("expected unsafe candidate to fail")
	}
	data, err := os.ReadFile(filepath.Join(target, "auth.json"))
	if err != nil || !strings.Contains(string(data), "credentials") {
		t.Fatalf("original changed: %q %v", data, err)
	}
}

func TestOverlayPublicationFailureRetainsRecoveryCopy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	target, source := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "auth"), []byte("credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(target, 0o700) }()
	err := Overlay(target, source, []string{"auth"})
	if err == nil {
		t.Fatal("expected publication to fail")
	}
	_, backup, ok := strings.Cut(err.Error(), "; original config recoverable at ")
	if !ok {
		t.Fatalf("missing recovery location: %v", err)
	}
	defer func() { _ = os.Chmod(backup, 0o700); _ = os.RemoveAll(filepath.Dir(backup)) }()
	data, readErr := os.ReadFile(filepath.Join(backup, "auth"))
	if readErr != nil || string(data) != "credentials" {
		t.Fatalf("backup lost: %q %v", data, readErr)
	}
}

func TestOverlayMergesPreservedDirectoryWithGeneratedFiles(t *testing.T) {
	target, source := t.TempDir(), t.TempDir()
	for dir, files := range map[string]map[string]string{
		target: {"session.md": "session", "both.md": "session edit"},
		source: {"shipped.md": "generated", "both.md": "generated"},
	} {
		if err := os.MkdirAll(filepath.Join(dir, "prompt-templates"), 0o700); err != nil {
			t.Fatal(err)
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, "prompt-templates", name), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Overlay(target, source, []string{"prompt-templates/"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"session.md": "session", "shipped.md": "generated", "both.md": "session edit"} {
		if data, err := os.ReadFile(filepath.Join(target, "prompt-templates", name)); err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
}
