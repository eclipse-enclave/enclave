// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package appassets

import (
	"testing"
	"testing/fstest"
)

func TestContentHashIncludesPathsContentAndModes(t *testing.T) {
	base := fstest.MapFS{"docs/file": {Data: []byte("one")}}
	baseHash, err := ContentHash(base)
	if err != nil {
		t.Fatal(err)
	}

	cases := []fstest.MapFS{
		{"docs/other": {Data: []byte("one")}},
		{"docs/file": {Data: []byte("two")}},
		{"extensions/test/install.sh": {Data: []byte("one")}},
	}
	for i, files := range cases {
		hash, err := ContentHash(files)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if hash == baseHash {
			t.Fatalf("case %d produced unchanged hash %s", i, hash)
		}
	}
	if len(baseHash) != 64 {
		t.Fatalf("hash length = %d, want 64", len(baseHash))
	}
}

func TestFileMode(t *testing.T) {
	tests := map[string]uint32{
		"entrypoint.sh":                                 0o755,
		"gateway-entrypoint.sh":                         0o755,
		"extensions/tools/demo/install.sh":              0o755,
		"extensions/features/demo/install.sh":           0o755,
		"extensions/tools/demo/check-update.sh":         0o644,
		"runtime-assets/build-scripts/install.sh":       0o755,
		"runtime-assets/build-scripts/bin/helper":       0o755,
		"runtime-assets/build-scripts/data/config":      0o644,
		"runtime-assets/microvm/alpine/build-bundle.sh": 0o755,
		"runtime-assets/microvm/alpine/init":            0o755,
		"docs/README.md":                                0o644,
	}
	for name, want := range tests {
		if got := uint32(FileMode(name)); got != want {
			t.Errorf("FileMode(%q) = %#o, want %#o", name, got, want)
		}
	}
}
