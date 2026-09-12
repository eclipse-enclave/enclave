// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

// loadFeatureWithCaches writes a mixin spec declaring cachesYAML (the caches:
// block body) and loads it as a feature extension.
func loadFeatureWithCaches(t *testing.T, cachesYAML string) (model.Extension, error) {
	t.Helper()
	root := t.TempDir()
	spec := "schemaVersion: \"1\"\nkind: mixin\nname: java-dev\ncaches:\n" + cachesYAML
	writeTestFile(t, filepath.Join(root, "features", "java-dev", SpecFilename), spec)
	return LoadFeatureExtension(model.Paths{FeaturesDir: filepath.Join(root, "features")}, "java-dev")
}

func TestSpecCachesLoadIntoExtension(t *testing.T) {
	ext, err := loadFeatureWithCaches(t, "  - name: m2\n    target: .m2//repository/\n")
	if err != nil {
		t.Fatalf("LoadFeatureExtension: %v", err)
	}
	want := []model.CacheConfig{{Name: "m2", Target: ".m2/repository"}}
	if len(ext.Caches) != 1 || ext.Caches[0] != want[0] {
		t.Fatalf("Caches = %v, want %v (target cleaned)", ext.Caches, want)
	}
}

func TestSpecCachesLoadIntoProfile(t *testing.T) {
	root := t.TempDir()
	spec := "schemaVersion: \"1\"\nkind: sandbox\nname: demo\ncaches:\n  - name: demo-cache\n    target: .cache/demo\n"
	writeTestFile(t, filepath.Join(root, "tools", "demo", SpecFilename), spec)

	profile, err := LoadProfile(model.Paths{ToolsDir: filepath.Join(root, "tools")}, "demo")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	want := model.CacheConfig{Name: "demo-cache", Target: ".cache/demo"}
	if len(profile.Caches) != 1 || profile.Caches[0] != want {
		t.Fatalf("Caches = %v, want %v", profile.Caches, want)
	}
}

func TestSpecCachesDedupeIdenticalEntries(t *testing.T) {
	ext, err := loadFeatureWithCaches(t,
		"  - name: m2\n    target: .m2/repository\n  - name: m2\n    target: .m2/repository\n")
	if err != nil {
		t.Fatalf("LoadFeatureExtension: %v", err)
	}
	if len(ext.Caches) != 1 {
		t.Fatalf("Caches = %v, want the identical entries deduped", ext.Caches)
	}
}

func TestSpecCachesRejectInvalidEntries(t *testing.T) {
	cases := []struct {
		name    string
		caches  string
		wantErr string
	}{
		{"empty name", "  - target: .m2\n", "name must not be empty"},
		{"invalid name charset", "  - name: ../m2\n    target: .m2\n", "invalid name"},
		{"absolute target", "  - name: m2\n    target: /opt/m2\n", "must be relative"},
		{"traversal target", "  - name: m2\n    target: ../outside\n", "traversal"},
		{"empty target", "  - name: m2\n    target: \"\"\n", "path is empty"},
		{"dot target", "  - name: m2\n    target: .\n", "current directory"},
		{"reserved built-in name", "  - name: npm\n    target: .m2\n", "reserved by a built-in cache"},
		{"reserved built-in target", "  - name: m2\n    target: .cache/pip\n", "reserved by the built-in"},
		{"target nested in built-in", "  - name: m2\n    target: .cache/pip/wheels\n", "reserved by the built-in"},
		{"target containing built-in", "  - name: m2\n    target: .nvm\n", "reserved by the built-in"},
		{"reserved auth target", "  - name: m2\n    target: " + model.ContainerAuthDir + "\n", "reserved by the auth store"},
		{"target nested in auth store", "  - name: m2\n    target: " + model.ContainerAuthDir + "/tokens\n", "reserved by the auth store"},
		{"reserved history target", "  - name: m2\n    target: " + model.ContainerHistoryDir + "\n", "reserved by the shell history store"},
		{"reserved ssh target", "  - name: m2\n    target: " + model.ContainerSSHDir + "/keys\n", "reserved by the SSH mount"},
		{"reserved config file target", "  - name: m2\n    target: .npmrc\n", "reserved by a persistent config file mount"},
		{"same name different target", "  - name: m2\n    target: .m2\n  - name: m2\n    target: .m2/repository\n", "declared twice with different targets"},
		{"same target different name", "  - name: m2\n    target: .m2\n  - name: maven\n    target: .m2\n", "overlaps target"},
		{"nested targets", "  - name: m2\n    target: .m2\n  - name: maven\n    target: .m2/repository\n", "overlaps target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFeatureWithCaches(t, tc.caches)
			if err == nil {
				t.Fatalf("caches %q loaded, want error containing %q", tc.caches, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
