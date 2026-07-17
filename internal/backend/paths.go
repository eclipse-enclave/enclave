// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package backend

import (
	"fmt"
	"path/filepath"

	"enclave/internal/util"
)

// ValidateAuthFilePaths validates and cleans store-relative auth file paths.
// Paths must be relative and must not traverse outside the store.
func ValidateAuthFilePaths(authFiles []string) ([]string, error) {
	if len(authFiles) == 0 {
		return nil, nil
	}
	cleaned := make([]string, 0, len(authFiles))
	for _, authFile := range authFiles {
		if authFile == "" {
			return nil, fmt.Errorf("auth file path is empty")
		}
		if filepath.IsAbs(authFile) {
			return nil, fmt.Errorf("auth file path must be relative: %s", authFile)
		}
		if util.HasPathTraversal(authFile) {
			return nil, fmt.Errorf("auth file path contains traversal: %s", authFile)
		}
		cleanedPath := filepath.Clean(authFile)
		if cleanedPath == "." {
			return nil, fmt.Errorf("auth file path resolves to current directory: %s", authFile)
		}
		cleaned = append(cleaned, cleanedPath)
	}
	return cleaned, nil
}
