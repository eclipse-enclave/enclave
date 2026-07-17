// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "strings"

// WorktreeMetadataMode returns the concrete linked-worktree metadata mount
// mode used at runtime. Empty or unknown values resolve to follow, meaning the
// gitdir/commondir mounts follow the project mount mode.
func WorktreeMetadataMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case WorktreeMetadataReadonly:
		return WorktreeMetadataReadonly
	case WorktreeMetadataNone:
		return WorktreeMetadataNone
	default:
		return WorktreeMetadataFollow
	}
}
