// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"strings"

	"enclave/internal/model"
)

const managedNamePrefix = model.AppName + "-"

// ParseManagedName decomposes a managed container/resource name into its tool,
// project-hash, and trailing segments.
func ParseManagedName(name string) (tool string, hash string, tail string, ok bool) {
	if !strings.HasPrefix(name, managedNamePrefix) {
		return "", "", "", false
	}
	trimmed := strings.TrimPrefix(name, managedNamePrefix)
	parts := strings.Split(trimmed, "-")
	hashIndex := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if model.IsHashSegment(parts[i]) {
			hashIndex = i
			break
		}
	}
	if hashIndex <= 0 {
		return "", "", "", false
	}
	tool = strings.Join(parts[:hashIndex], "-")
	if tool == "" {
		return "", "", "", false
	}
	hash = parts[hashIndex]
	tail = strings.Join(parts[hashIndex+1:], "-")
	ok = true
	return tool, hash, tail, ok
}

func PrimaryContainerName(summary Summary) string {
	if len(summary.Names) > 0 {
		return strings.TrimPrefix(summary.Names[0], "/")
	}
	if len(summary.ID) >= 12 {
		return summary.ID[:12]
	}
	return strings.TrimSpace(summary.ID)
}
