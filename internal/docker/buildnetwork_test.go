// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"strings"
	"testing"
)

func TestBuildNetworkModeFromEnv(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  string
		fails bool
	}{
		{value: "", want: ""},
		{value: "default", want: ""},
		{value: "host", want: "host"},
		{value: " HOST ", want: "host"},
		{value: "bridge", fails: true},
		{value: "none", fails: true},
	} {
		t.Setenv(BuildNetworkEnv, tc.value)
		got, err := BuildNetworkModeFromEnv()
		if tc.fails {
			if err == nil {
				t.Errorf("%q: expected an error, got mode %q", tc.value, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got (%q, %v), want (%q, nil)", tc.value, got, err, tc.want)
		}
	}
}

func TestBuildNetworkDNSHintNamesTheOverride(t *testing.T) {
	if hint := BuildNetworkDNSHint(); !strings.Contains(hint, BuildNetworkEnv+"=host") {
		t.Fatalf("hint must tell the user how to opt in, got %q", hint)
	}
}

func TestBuildProgressIsQuiet(t *testing.T) {
	if !BuildProgressIsQuiet(" Quiet ") {
		t.Fatal("quiet must be recognised case-insensitively")
	}
	for _, value := range []string{"", "compact", "verbose"} {
		if BuildProgressIsQuiet(value) {
			t.Fatalf("%q must not count as quiet", value)
		}
	}
}
