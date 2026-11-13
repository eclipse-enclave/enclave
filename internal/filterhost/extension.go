// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package filterhost is the host side of request filters: loading installed
// filters, OAuth grants and their login flow, and resolving the filters a
// session selects into the bundles the gateway enforces.
//
// Nothing in this package writes credential material anywhere but the host
// state directory and the backend request bundles meant for the gateway.
package filterhost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/config"
	"enclave/internal/filter/spec"
	"enclave/internal/model"
)

// Load loads and validates the installed filter named name, including its
// shipped presets.
func Load(paths model.Paths, name string) (*spec.Filter, error) {
	dir, err := config.FilterExtensionDir(paths, name)
	if err != nil {
		return nil, err
	}
	return spec.Load(dir, name)
}

// List returns the installed filters as extensions, sorted by name. One whose
// spec does not load is skipped with a warning.
func List(paths model.Paths, warn func(string)) ([]model.Extension, error) {
	names, err := config.ListFilterNames(paths)
	if err != nil {
		return nil, err
	}
	var exts []model.Extension
	for _, name := range names {
		f, err := Load(paths, name)
		if err != nil {
			if warn != nil {
				warn(fmt.Sprintf("Skipping filter %s: %v", name, err))
			}
			continue
		}
		exts = append(exts, model.Extension{
			Name:        f.Name,
			DisplayName: f.DisplayName,
			Description: f.Description,
			Type:        model.ExtensionKindFilter,
			Priority:    model.DefaultExtensionPriority,
		})
	}
	return exts, nil
}

// ValidateDir validates the content of the filter directory dir named name:
// its spec, every shipped preset, and the files it carries that nothing
// reads. Messages name files relative to dir, so a staged copy reads the
// same as the installed one.
func ValidateDir(name string, dir string) config.ExtensionValidation {
	result := config.ExtensionValidation{}
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	f, err := spec.Load(dir, name)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("filter %q: invalid %s: %s",
			name, config.SpecFilename, strings.ReplaceAll(err.Error(), prefix, "")))
		return result
	}
	for _, warning := range f.Warnings {
		result.Warnings = append(result.Warnings, fmt.Sprintf("filter %q: %s", name, strings.ReplaceAll(warning, prefix, "")))
	}
	return result
}

// ValidateInstalled validates every installed filter's content.
func ValidateInstalled(paths model.Paths) (config.ExtensionValidation, error) {
	result := config.ExtensionValidation{}
	_, root := config.ExtensionRoots(paths, model.KindFilter)
	if strings.TrimSpace(root) == "" {
		return result, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}
	for _, entry := range entries {
		if !config.IsExtensionDir(entry) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, config.SpecFilename)); err != nil {
			if _, jsonErr := os.Stat(filepath.Join(dir, config.SpecFilenameJSON)); jsonErr != nil {
				continue // config.ValidateExtensions reports the missing spec.
			}
		}
		one := ValidateDir(entry.Name(), dir)
		result.Warnings = append(result.Warnings, one.Warnings...)
		result.Errors = append(result.Errors, one.Errors...)
	}
	return result, nil
}
