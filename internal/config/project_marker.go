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
	"strings"
)

const projectMarkerFilename = "project.json"

// projectMarker records the real project directory a hash-keyed directory was
// derived from, so users browsing the config/state roots can trace a hash back
// to its source project.
type projectMarker struct {
	Path string `json:"path"`
}

// WriteProjectMarkers writes a project.json marker into the config-root and
// state-root directories for projectHash if it is not already present. It is
// best-effort: write failures are intentionally ignored so a failure to record
// the discoverability marker never blocks a session from starting.
func WriteProjectMarkers(home string, projectHash string, realDir string) {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(projectHash) == "" {
		return
	}
	// The config-root overrides dir holds user-edited overrides. Writing the
	// marker eagerly would leave an empty override dir for every project ever
	// run, so only record it once the user has created overrides.
	if overridesDir := HostProjectOverridesDir(home, projectHash); dirExists(overridesDir) {
		_ = writeProjectMarker(overridesDir, realDir)
	}
	_ = writeProjectMarker(HostProjectDir(home, projectHash), realDir)
}

func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

func writeProjectMarker(dir string, realDir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	markerPath := filepath.Join(dir, projectMarkerFilename)
	if _, err := os.Stat(markerPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(projectMarker{Path: realDir}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(markerPath, data, 0o600)
}
