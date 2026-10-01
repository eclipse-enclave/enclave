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
		cleanedPath, err := ValidateStoreRelativePath(authFile)
		if err != nil {
			return nil, fmt.Errorf("auth file path %q: %w", authFile, err)
		}
		cleaned = append(cleaned, cleanedPath)
	}
	return cleaned, nil
}

// ValidateStoreRelativePath cleans a path that must stay within a store.
func ValidateStoreRelativePath(relative string) (string, error) {
	if relative == "" {
		return "", fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("path must be relative")
	}
	if util.HasPathTraversal(relative) {
		return "", fmt.Errorf("path contains traversal")
	}
	cleaned := filepath.Clean(relative)
	if cleaned == "." {
		return "", fmt.Errorf("path resolves to current directory")
	}
	return cleaned, nil
}
