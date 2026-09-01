// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import "fmt"

// Binary size units, so "4.2 KB" means 4300 bytes rather than 4200.
const (
	unitKB = 1024
	unitMB = 1024 * unitKB
	unitGB = 1024 * unitMB
)

// FormatBytes renders a byte count for human output, generic so that unsigned
// callers need no lossy conversion.
func FormatBytes[T int64 | uint64](bytes T) string {
	switch {
	case bytes >= unitGB:
		return formatUnit(float64(bytes), unitGB, "GB")
	case bytes >= unitMB:
		return formatUnit(float64(bytes), unitMB, "MB")
	case bytes >= unitKB:
		return formatUnit(float64(bytes), unitKB, "KB")
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// formatUnit keeps one decimal below ten units and drops it above, so columns
// stay narrow without losing resolution on small values.
func formatUnit(bytes float64, unit float64, suffix string) string {
	value := bytes / unit
	if value < 10 {
		return fmt.Sprintf("%.1f %s", value, suffix)
	}
	return fmt.Sprintf("%.0f %s", value, suffix)
}
