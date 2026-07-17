// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "strings"

// ProjectMountIsReadonly reports whether a project mount mode requests a
// read-only project/worktree bind.
func ProjectMountIsReadonly(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), ProjectMountReadonly)
}

// ProjectMountMode returns the concrete project mount mode used at runtime.
func ProjectMountMode(value string) string {
	if ProjectMountIsReadonly(value) {
		return ProjectMountReadonly
	}
	return ProjectMountWritable
}
