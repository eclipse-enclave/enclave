// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package secretfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFileSecretRawContents(t *testing.T) {
	// Empty parser -> the file's raw, trimmed contents.
	got, found, err := parseFileSecret([]byte("  s3cr3t-token\n\n"), "")
	if err != nil {
		t.Fatalf("parseFileSecret() error = %v", err)
	}
	if !found || got != "s3cr3t-token" {
		t.Fatalf("parseFileSecret() = (%q, %v), want (%q, true)", got, found, "s3cr3t-token")
	}
}

func TestParseFileSecretJSONDotPath(t *testing.T) {
	doc := []byte(`{"auth": {"token": "abc123", "nested": {"deep": "xyz"}}, "n": 42}`)

	cases := []struct {
		parser string
		want   string
	}{
		{"json:auth.token", "abc123"},
		{"json:auth.nested.deep", "xyz"},
		{"json:n", "42"},
	}
	for _, tc := range cases {
		got, found, err := parseFileSecret(doc, tc.parser)
		if err != nil {
			t.Fatalf("parseFileSecret(%q) error = %v", tc.parser, err)
		}
		if !found || got != tc.want {
			t.Fatalf("parseFileSecret(%q) = (%q, %v), want (%q, true)", tc.parser, got, found, tc.want)
		}
	}
}

func TestParseFileSecretRejectsRFC6901Pointer(t *testing.T) {
	doc := []byte(`{"auth": {"token": "abc123"}}`)
	// A bare slash-prefixed pointer and a json:-prefixed slash pointer must both
	// fail loudly rather than silently returning nothing. This resolver is a
	// dot-path resolver, NOT the RFC 6901 pointer used by authSession.
	for _, parser := range []string{"/auth/token", "json:/auth/token"} {
		if _, _, err := parseFileSecret(doc, parser); err == nil {
			t.Fatalf("parseFileSecret(%q) error = nil, want a loud error", parser)
		}
	}
}

func TestParseFileSecretMissingKeyReportsNotFound(t *testing.T) {
	// A partially populated credentials file (key absent) is a normal state:
	// it must report found=false with no error so the caller can fall back to
	// another source instead of aborting session start.
	doc := []byte(`{"auth": {"token": "abc123"}}`)
	for _, parser := range []string{"json:auth.missing", "json:other.token"} {
		got, found, err := parseFileSecret(doc, parser)
		if err != nil {
			t.Fatalf("parseFileSecret(%q) error = %v, want nil", parser, err)
		}
		if found || got != "" {
			t.Fatalf("parseFileSecret(%q) = (%q, %v), want (\"\", false)", parser, got, found)
		}
	}
}

func TestParseFileSecretErrorsOnMalformedShape(t *testing.T) {
	doc := []byte(`{"auth": {"token": "abc123", "obj": {"k": "v"}}}`)
	// Descending into a scalar and resolving to a non-scalar are shape errors,
	// not missing values: they stay loud.
	if _, _, err := parseFileSecret(doc, "json:auth.token.deeper"); err == nil {
		t.Fatalf("parseFileSecret(descend into scalar) error = nil, want error")
	}
	if _, _, err := parseFileSecret(doc, "json:auth.obj"); err == nil {
		t.Fatalf("parseFileSecret(non-scalar value) error = nil, want error")
	}
}

func TestParseFileSecretErrorsOnNonJSON(t *testing.T) {
	if _, _, err := parseFileSecret([]byte("not json"), "json:a.b"); err == nil {
		t.Fatalf("parseFileSecret(non-JSON) error = nil, want error")
	}
}

func TestResolveFileSecretReadsFileAndExpandsTilde(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "svc", "token.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"auth":{"token":"file-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, found, err := ResolveFileSecret(home, "~/.config/svc/token.json", "json:auth.token")
	if err != nil {
		t.Fatalf("ResolveFileSecret() error = %v", err)
	}
	if !found || got != "file-token" {
		t.Fatalf("ResolveFileSecret() = (%q, %v), want (%q, true)", got, found, "file-token")
	}
}

func TestResolveFileSecretMissingFileNotFound(t *testing.T) {
	home := t.TempDir()
	got, found, err := ResolveFileSecret(home, "~/.config/svc/absent.json", "")
	if err != nil {
		t.Fatalf("ResolveFileSecret(missing) error = %v, want nil", err)
	}
	if found || got != "" {
		t.Fatalf("ResolveFileSecret(missing) = (%q, %v), want (\"\", false)", got, found)
	}
}

func TestResolveFileSecretMissingKeyFallsBack(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.json")
	if err := os.WriteFile(path, []byte(`{"other":"value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, found, err := ResolveFileSecret(home, "~/auth.json", "json:auth.token")
	if err != nil {
		t.Fatalf("ResolveFileSecret(missing key) error = %v, want nil", err)
	}
	if found || got != "" {
		t.Fatalf("ResolveFileSecret(missing key) = (%q, %v), want (\"\", false)", got, found)
	}
}

func TestResolveFileSecretMalformedParserIsLoudEvenWhenFileMissing(t *testing.T) {
	home := t.TempDir()
	if _, _, err := ResolveFileSecret(home, "~/.config/svc/absent.json", "/auth/token"); err == nil {
		t.Fatalf("ResolveFileSecret(malformed parser) error = nil, want loud error")
	}
}
