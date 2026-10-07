// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package envflag

import "testing"

func TestTruthy(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "True", "yes", "on", " 1 ", "\tyes\n"} {
		if !Truthy(value, true) {
			t.Errorf("Truthy(%q, true) = false, want true", value)
		}
	}
	for _, value := range []string{"", " ", "0", "false", "no", "off", "maybe", "11"} {
		if Truthy(value, true) {
			t.Errorf("Truthy(%q, true) = true, want false", value)
		}
	}
	if Truthy("1", false) {
		t.Error("Truthy(\"1\", false) = true, want false: an unset variable is always off")
	}
}
