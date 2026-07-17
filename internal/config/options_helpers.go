// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"strings"
)

func boolValue(value bool) (string, bool) {
	return fmt.Sprintf("%t", value), true
}

func boolPtrValue(value *bool) (string, bool) {
	if value == nil {
		return "", false
	}
	return fmt.Sprintf("%t", *value), true
}

func stringValue(value string, require bool) (string, bool) {
	if strings.TrimSpace(value) == "" {
		if require {
			return value, true
		}
		return "", false
	}
	return value, true
}

func sliceValue(values []string) (string, bool) {
	if values == nil {
		return "", false
	}
	return formatSlice(values), true
}

func formatSlice(values []string) string {
	if values == nil {
		return ""
	}
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		trimmed = append(trimmed, value)
	}
	return "[" + strings.Join(trimmed, ",") + "]"
}

func FormatSlice(values []string) string {
	return formatSlice(values)
}

func copyStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
