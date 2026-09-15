// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"regexp"
	"strings"
)

const sessionNameMaxLength = 32

var sessionNameSeparators = regexp.MustCompile(`[^a-z0-9]+`)

// SanitizeSessionName normalises a user-provided session name to the canonical
// form used as the trailing segment of a managed container name, as the value
// of LabelSession, and as the lookup key when a session is addressed by name.
// Callers must compare sanitized values on both sides so that `--name "My
// Task"` is addressable as `my-task` (and vice versa).
func SanitizeSessionName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = sessionNameSeparators.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if len(name) > sessionNameMaxLength {
		name = name[:sessionNameMaxLength]
		name = strings.TrimRight(name, "-")
	}
	return name
}
