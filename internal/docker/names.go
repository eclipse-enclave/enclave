// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"strings"
)

func PrimaryContainerName(summary Summary) string {
	if len(summary.Names) > 0 {
		return strings.TrimPrefix(summary.Names[0], "/")
	}
	if len(summary.ID) >= 12 {
		return summary.ID[:12]
	}
	return strings.TrimSpace(summary.ID)
}
