// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

func ShortHash(hash string) string {
	if len(hash) > HashLength {
		return hash[:HashLength]
	}
	return hash
}

// IsHashSegment reports whether value matches the project-hash shape
// (HashLength lowercase-hex chars) that docker.ParseManagedName treats as the
// hash segment of a managed container or volume name.
func IsHashSegment(value string) bool {
	if len(value) != HashLength {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
