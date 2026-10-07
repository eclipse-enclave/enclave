// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package envflag parses on/off switches read from environment variables. The
// Windows launcher imports it, so it must depend on the standard library only.
package envflag

import "strings"

// Truthy reports whether a switch is on: 1, true, yes, or on, in any case and
// with surrounding whitespace ignored. It takes the results of os.LookupEnv;
// an unset variable is always off.
func Truthy(value string, set bool) bool {
	if !set {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
