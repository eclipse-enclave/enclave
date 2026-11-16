// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"io/fs"
	"os"
)

// LoadWorktreeInheritanceSelector reads only the source-selection setting from
// a project config file. Its other options may be invalid or obsolete and must
// not block bootstrap selection.
func LoadWorktreeInheritanceSelector(home, hash string) (*bool, []Warning, error) {
	path := HostProjectConfigJSONPath(home, hash)
	data, warnings, err := readConfigBytes(path)
	if err != nil || len(data) == 0 {
		return nil, withFileLayer(warnings, warningLayerProject), err
	}
	var selection struct {
		WorktreeInheritance *bool `json:"worktree_inheritance"`
	}
	if err := json.Unmarshal(data, &selection); err != nil {
		return nil, withFileLayer([]Warning{newWarning(WarningInvalidConfigJSON, "worktree_inheritance", "", path, "Invalid config JSON at %s: %v", path, err)}, warningLayerProject), nil
	}
	return selection.WorktreeInheritance, nil, nil
}

// HasUserOverrides ignores Enclave's root marker and filesystem metadata.
func HasUserOverrides(root string) bool {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	found := false
	_ = fs.WalkDir(dir.FS(), ".", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if path == projectMarkerFilename || entry.Name() == ".DS_Store" {
			return nil
		}
		if path == configFilename {
			// A selection-only file was consumed during bootstrap, not ignored.
			data, readErr := fs.ReadFile(dir.FS(), configFilename)
			var doc map[string]json.RawMessage
			if readErr == nil && json.Unmarshal(data, &doc) == nil {
				if len(doc) == 0 {
					return nil
				}
				if _, selection := doc["worktree_inheritance"]; selection && len(doc) == 1 {
					return nil
				}
			}
		}
		found = true
		return fs.SkipAll
	})
	return found
}
