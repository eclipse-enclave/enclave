// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"fmt"
	"strconv"
	"strings"
)

// Binary size units. KB, MB and GB are multiples of 1024, matching what
// `docker --memory` accepts, and the same base the renderer formats bytes in.
const (
	unitKB = 1024
	unitMB = 1024 * unitKB
	unitGB = 1024 * unitMB
)

// ParseSize parses a byte size such as "32MB", "512KB", "1GB" or a bare byte
// count. "0", "off" and the empty string mean "no limit" and return 0.
// Suffixes are case insensitive.
func ParseSize(value string) (int64, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return 0, nil
	}
	if strings.EqualFold(raw, "off") || strings.EqualFold(raw, "none") {
		return 0, nil
	}

	normalized := strings.ToUpper(raw)
	multiplier := int64(1)
	for _, unit := range []struct {
		suffixes []string
		factor   int64
	}{
		{[]string{"GB", "G"}, unitGB},
		{[]string{"MB", "M"}, unitMB},
		{[]string{"KB", "K"}, unitKB},
		{[]string{"B"}, 1},
	} {
		matched := false
		for _, suffix := range unit.suffixes {
			if strings.HasSuffix(normalized, suffix) {
				normalized = strings.TrimSuffix(normalized, suffix)
				multiplier = unit.factor
				matched = true
				break
			}
		}
		if matched {
			break
		}
	}
	digits := strings.TrimSpace(normalized)
	if digits == "" {
		return 0, fmt.Errorf("invalid size %q: missing number", value)
	}

	number, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: expected a number with an optional KB, MB or GB suffix", value)
	}
	if number < 0 {
		return 0, fmt.Errorf("invalid size %q: must not be negative", value)
	}
	if number != 0 && number > (1<<62)/multiplier {
		return 0, fmt.Errorf("invalid size %q: too large", value)
	}
	return number * multiplier, nil
}

// FormatBytes renders a byte count for human output using the same binary base
// ParseSize accepts, so "4.2 KB" means 4300 bytes rather than 4200.
func FormatBytes(bytes int64) string {
	switch {
	case bytes >= unitGB:
		return formatUnit(bytes, unitGB, "GB")
	case bytes >= unitMB:
		return formatUnit(bytes, unitMB, "MB")
	case bytes >= unitKB:
		return formatUnit(bytes, unitKB, "KB")
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// formatUnit keeps one decimal below ten units and drops it above, so columns
// stay narrow without losing resolution on small values.
func formatUnit(bytes int64, unit int64, suffix string) string {
	value := float64(bytes) / float64(unit)
	if value < 10 {
		return fmt.Sprintf("%.1f %s", value, suffix)
	}
	return fmt.Sprintf("%.0f %s", value, suffix)
}
