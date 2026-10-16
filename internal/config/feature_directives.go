// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"strings"

	"enclave/internal/model"
)

// IsFeatureSelector reports whether name is the "default" or "all" selector.
func IsFeatureSelector(name string) bool {
	name = strings.TrimSpace(name)
	return strings.EqualFold(name, model.SelectionDefault) || strings.EqualFold(name, model.FeatureSelectionAll)
}

// HasFeatureSelectors reports whether any entry selects "default" or "all",
// with or without a '+' prefix.
func HasFeatureSelectors(values []string) bool {
	additions, _ := SplitFeatureDirectives(values)
	for _, name := range additions {
		if IsFeatureSelector(name) {
			return true
		}
	}
	return false
}

// HasFeatureDirectives reports whether any entry carries a '+' or '-' prefix.
func HasFeatureDirectives(values []string) bool {
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
			return true
		}
	}
	return false
}

// SplitFeatureDirectives separates feature entries into added names (bare or
// '+'-prefixed) and removed names ('-'-prefixed), dropping empty entries.
// Callers apply all additions before any removal so removals win within one
// list regardless of order.
func SplitFeatureDirectives(values []string) (additions, removals []string) {
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		removal := strings.HasPrefix(value, "-")
		if removal || strings.HasPrefix(value, "+") {
			value = strings.TrimSpace(value[1:])
		}
		if value == "" {
			continue
		}
		if removal {
			removals = append(removals, value)
		} else {
			additions = append(additions, value)
		}
	}
	return additions, removals
}
