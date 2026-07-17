// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPathWithin(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "project")
	cases := []struct {
		path string
		want bool
	}{
		{root, true},
		{filepath.Join(root, "sub"), true},
		{filepath.Join(root, "sub", ".."), true},
		{filepath.Join(root, "..", "other"), false},
		{root + "-suffix", false},
	}
	for _, tc := range cases {
		if got := PathWithin(root, tc.path); got != tc.want {
			t.Fatalf("PathWithin(%q, %q) = %v, want %v", root, tc.path, got, tc.want)
		}
	}
}

func TestRealPathWithinResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	within, err := RealPathWithin(root, inside)
	if err != nil || !within {
		t.Fatalf("RealPathWithin inside = %v, %v; want true, nil", within, err)
	}
	within, err = RealPathWithin(root, filepath.Join(link, "file"))
	if err != nil {
		t.Fatalf("RealPathWithin symlink error: %v", err)
	}
	if within {
		t.Fatal("symlink to outside should not be within root")
	}
}

func TestParseEnvPairs(t *testing.T) {
	pairs, err := ParseEnvPairs(strings.NewReader("# comment\nexport A='one'\nB= two \ninvalid\nA=override\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvPair{{Key: "A", Value: "one"}, {Key: "B", Value: "two"}, {Key: "A", Value: "override"}}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("ParseEnvPairs() = %#v, want %#v", pairs, want)
	}
}
