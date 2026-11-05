// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package codex

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// TranscriptPaths inventories only this thread's rollout files and shell
// snapshot. Shared SQLite indexes and caches must never be deleted per context.
func TranscriptPaths(configDir, sessionID string) ([]string, error) {
	if !uuidPattern.MatchString(sessionID) {
		return nil, nil
	}
	var paths []string
	for _, dir := range []string{"sessions", "archived_sessions", "shell_snapshots"} {
		root := filepath.Join(configDir, dir)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if path == root && errors.Is(walkErr, fs.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			name := entry.Name()
			rollout := dir != "shell_snapshots" && strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, "-"+sessionID+".jsonl")
			snapshot := dir == "shell_snapshots" && (name == sessionID+".sh" || strings.HasPrefix(name, sessionID+".") && strings.HasSuffix(name, ".sh"))
			if entry.Type().IsRegular() && (rollout || snapshot) {
				rel, err := filepath.Rel(configDir, path)
				if err != nil {
					return err
				}
				paths = append(paths, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return paths, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}
