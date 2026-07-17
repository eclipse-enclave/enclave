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
	"strings"
	"testing"
)

func TestPathStrictlyWithin(t *testing.T) {
	if PathStrictlyWithin("/a/b", "/a/b") {
		t.Fatal("root itself must be excluded by PathStrictlyWithin")
	}
	if !PathStrictlyWithin("/a/b", "/a/b/c") {
		t.Fatal("child must be within")
	}
	if PathStrictlyWithin("/a/b", "/a/bc") {
		t.Fatal("sibling sharing a string prefix must not be within")
	}
	if PathStrictlyWithin("/a/b", "/a") {
		t.Fatal("parent must not be within")
	}
}

// TestRealPathWithinFailsClosed pins the security-relevant semantic: when paths
// cannot be resolved, RealPathWithin must return an error (so callers fail
// closed) rather than silently reporting "within".
func TestRealPathWithinFailsClosed(t *testing.T) {
	dir := t.TempDir()

	// Non-existent root must error, not pass.
	if within, err := RealPathWithin(filepath.Join(dir, "missing-root"), dir); err == nil || within {
		t.Fatalf("non-existent root: got (within=%v, err=%v), want (false, error)", within, err)
	}

	// A path descending through a regular file (ENOTDIR, not ENOENT) must error.
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if within, err := RealPathWithin(dir, filepath.Join(file, "sub")); err == nil || within {
		t.Fatalf("descend-into-file: got (within=%v, err=%v), want (false, error)", within, err)
	}

	// Sanity: a real child of a real root resolves and is within.
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if within, err := RealPathWithin(dir, child); err != nil || !within {
		t.Fatalf("real child: got (within=%v, err=%v), want (true, nil)", within, err)
	}
}

// TestParseEnvPairsHandlesLongLines is a regression test: the parser must not
// cap line length (a >64KB value previously made bufio.Scanner error and drop
// the whole file).
func TestParseEnvPairsHandlesLongLines(t *testing.T) {
	bigValue := strings.Repeat("x", 200_000)
	content := "FOO=" + bigValue + "\nBAR=baz\n"
	pairs, err := ParseEnvPairs(strings.NewReader(content))
	if err != nil {
		t.Fatalf("ParseEnvPairs returned error on long line: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d pairs, want 2", len(pairs))
	}
	if pairs[0].Key != "FOO" || pairs[0].Value != bigValue {
		t.Fatalf("long value not parsed correctly (key=%q len=%d)", pairs[0].Key, len(pairs[0].Value))
	}
	if pairs[1].Key != "BAR" || pairs[1].Value != "baz" {
		t.Fatalf("var after long line lost: %+v", pairs[1])
	}
}
