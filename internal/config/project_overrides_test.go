// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRepoLocalProjectConfigNotHonored drives the real LoadDefaults path to
// prove that a config.json inside the repo-local .enclave directory is never
// read: project defaults come only from the hash-keyed config-root path (which
// is not present here).
func TestRepoLocalProjectConfigNotHonored(t *testing.T) {
	unsetXDGEnv(t)

	// Point home at a writable tempdir so ResolveHostHome/GlobalConfigPath and
	// the config-root override path resolve under a location we control.
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectDir := t.TempDir()
	repoLocalConfig := filepath.Join(projectDir, enclaveDirName, configFilename)
	if err := os.MkdirAll(filepath.Dir(repoLocalConfig), 0o755); err != nil {
		t.Fatalf("mkdir repo-local dir: %v", err)
	}
	// A value the repo-local config would set if it were (incorrectly) honored.
	if err := os.WriteFile(repoLocalConfig, []byte("{\"slim\": true}"), 0o600); err != nil {
		t.Fatalf("write repo-local config: %v", err)
	}

	// Sanity-check the fixture: read directly it must be valid, honorable JSON
	// (slim=true). This guards against the test silently degrading into an
	// invalid-JSON fixture that would pass for the wrong reason.
	if repoLocalDefaults, _, rerr := readDefaults(repoLocalConfig); rerr != nil {
		t.Fatalf("repo-local fixture must be valid JSON: %v", rerr)
	} else if repoLocalDefaults.Slim == nil || !*repoLocalDefaults.Slim {
		t.Fatalf("repo-local fixture must set slim=true when read directly, got %+v", repoLocalDefaults)
	}

	_, project, _, err := LoadDefaults(projectDir)
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	if project.Slim != nil {
		t.Fatalf("repo-local config must not be honored by LoadDefaults, got slim=%v", *project.Slim)
	}
}

func TestWriteProjectMarkers(t *testing.T) {
	unsetXDGEnv(t)

	home := t.TempDir()
	const hash = "projecthash"
	realDir := "/work/some/project"

	WriteProjectMarkers(home, hash, realDir)

	// The state-root marker is always written; the container state dir is
	// created for the session regardless.
	assertProjectMarker(t, HostProjectDir(home, hash), realDir)

	// Without a pre-existing overrides dir the config-root marker is not
	// written, so no empty override dir is left in the user's config tree.
	overridesDir := HostProjectOverridesDir(home, hash)
	if _, err := os.Stat(overridesDir); !os.IsNotExist(err) {
		t.Fatalf("expected no config-root override dir, stat err = %v", err)
	}

	// Once the user has created overrides, the marker is recorded there too.
	if err := os.MkdirAll(overridesDir, 0o700); err != nil {
		t.Fatalf("mkdir overrides: %v", err)
	}
	WriteProjectMarkers(home, hash, realDir)
	assertProjectMarker(t, overridesDir, realDir)
}

func assertProjectMarker(t *testing.T, dir string, realDir string) {
	t.Helper()
	markerPath := filepath.Join(dir, projectMarkerFilename)
	data, err := os.ReadFile(markerPath) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatalf("read marker %q: %v", markerPath, err)
	}
	var marker projectMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatalf("unmarshal marker %q: %v", markerPath, err)
	}
	if marker.Path != realDir {
		t.Fatalf("marker path = %q, want %q", marker.Path, realDir)
	}
}
