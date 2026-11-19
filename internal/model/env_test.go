// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"reflect"
	"testing"
)

func TestIsReservedEnvKey(t *testing.T) {
	for key, want := range map[string]bool{
		"PROJECT_DIR":        true,
		"HOST_PROJECT_DIR":   true,
		"TOOL":               true,
		EnvAllowRoot:         true,
		EnvPrefix:            true,
		"PROJECT_DIR_X":      false,
		"HOST_PROJECT_DIR_X": false,
		"TOOLS":              false,
		"enclave_yolo":       false,
		"VISIBLE":            false,
	} {
		if got := IsReservedEnvKey(key); got != want {
			t.Errorf("IsReservedEnvKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestFilterReservedEnv(t *testing.T) {
	kept, dropped := FilterReservedEnv([]string{"HOST_PROJECT_DIR=/evil", "VISIBLE=kept", EnvAllowRoot, "TOOL=x=y"})
	if want := []string{"VISIBLE=kept"}; !reflect.DeepEqual(kept, want) {
		t.Errorf("kept = %v, want %v", kept, want)
	}
	if want := []string{"HOST_PROJECT_DIR", EnvAllowRoot, "TOOL"}; !reflect.DeepEqual(dropped, want) {
		t.Errorf("dropped = %v, want %v", dropped, want)
	}
}
