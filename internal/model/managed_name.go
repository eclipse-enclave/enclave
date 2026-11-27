// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "strings"

const managedNamePrefix = AppName + "-"

// ParseManagedName decomposes a managed container/resource name of the form
// enclave-<tool>-<hash>-<session> into its tool, project-hash, and trailing
// segments. The tool name may itself contain hyphens, so the last hash-shaped
// segment separates it from the session.
func ParseManagedName(name string) (tool string, hash string, tail string, ok bool) {
	trimmed, found := strings.CutPrefix(name, managedNamePrefix)
	if !found {
		return "", "", "", false
	}
	parts := strings.Split(trimmed, "-")
	hashIndex := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if IsHashSegment(parts[i]) {
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
	return tool, parts[hashIndex], strings.Join(parts[hashIndex+1:], "-"), true
}
