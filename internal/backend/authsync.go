// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package backend

import (
	"bytes"
	"strings"

	"enclave/internal/model"
)

// ContainsSecretPlaceholder checks both recognizable placeholders from earlier
// sessions and the current session's custom-shaped placeholders.
func ContainsSecretPlaceholder(data []byte, placeholders []string) bool {
	if bytes.Contains(data, []byte(model.SecretPlaceholderMarker)) {
		return true
	}
	for _, placeholder := range placeholders {
		if placeholder != "" && bytes.Contains(data, []byte(placeholder)) {
			return true
		}
	}
	return false
}

// Placeholder lists are newline-delimited fixed-string grep patterns. Empty
// entries must be omitted because an empty pattern matches every auth file.
func EncodeSecretPlaceholders(placeholders []string) string {
	return strings.Join(nonEmptySecretPlaceholders(placeholders), "\n")
}

func DecodeSecretPlaceholders(value string) []string {
	return nonEmptySecretPlaceholders(strings.Split(value, "\n"))
}

func nonEmptySecretPlaceholders(values []string) []string {
	var placeholders []string
	for _, value := range values {
		if value != "" {
			placeholders = append(placeholders, value)
		}
	}
	return placeholders
}

func SecretPlaceholders(secrets []SecretRelease) []string {
	var placeholders []string
	for _, secret := range secrets {
		if secret.Placeholder != "" {
			placeholders = append(placeholders, secret.Placeholder)
		}
	}
	return placeholders
}
