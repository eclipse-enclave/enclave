// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestMergeBridgePorts(t *testing.T) {
	cases := []struct {
		name       string
		discovered []string
		explicit   []string
		want       []string
	}{
		{name: "merge and dedupe", discovered: []string{"9800", "9801"}, explicit: []string{"9802", "9801"}, want: []string{"9800", "9801", "9802"}},
		{name: "empty", want: nil},
		{name: "explicit only", explicit: []string{"9800"}, want: []string{"9800"}},
		{name: "discovered only", discovered: []string{"9800"}, want: []string{"9800"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := mergeBridgePorts(tc.discovered, tc.explicit)
			if !reflect.DeepEqual(result, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, result)
			}
		})
	}
}

func TestDiscoverIdeBridgePorts(t *testing.T) {
	tmpHome := t.TempDir()
	ideDir := filepath.Join(tmpHome, ".claude", "ide")
	if err := os.MkdirAll(ideDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"9800.lock", "9801.lock", "notaport.lock", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(ideDir, name), []byte(""), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Create a subdirectory that should be skipped
	if err := os.Mkdir(filepath.Join(ideDir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	ports := discoverIdeBridgePorts(tmpHome)
	sort.Strings(ports)
	expected := []string{"9800", "9801"}
	if !reflect.DeepEqual(ports, expected) {
		t.Fatalf("expected %v, got %v", expected, ports)
	}
}

func TestDiscoverIdeBridgePortsMissing(t *testing.T) {
	tmpHome := t.TempDir()
	ports := discoverIdeBridgePorts(tmpHome)
	if ports != nil {
		t.Fatalf("expected nil, got %v", ports)
	}
}
