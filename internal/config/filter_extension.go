// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"os"

	"enclave/internal/model"
)

// FilterExtensionDir returns the installed directory of the filter named
// name. Filters only exist in the user extension root.
//
// Package filterhost loads and validates filters. config cannot: the gateway
// proxy links config, and must not link the filter spec loader.
func FilterExtensionDir(paths model.Paths, name string) (string, error) {
	if err := ValidateExtensionName(name); err != nil {
		return "", err
	}
	_, userDir := ResolveExtensionDirs(paths, model.KindFilter, name)
	if userDir == "" {
		return "", fmt.Errorf("filter %q is not installed: %w", name, os.ErrNotExist)
	}
	return userDir, nil
}

// ListFilterNames returns the names of the installed filters that carry a
// spec document, sorted.
func ListFilterNames(paths model.Paths) ([]string, error) {
	return listSpecNames(paths, KindFilter)
}
