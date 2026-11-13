// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// GWSFilterFixtureDir is the filter engine's gws test fixture.
func GWSFilterFixtureDir(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate the testutil package")
	}
	return filepath.Join(filepath.Dir(file), "..", "filter", "testdata", "gws")
}

// InstallGWSFilter copies the gws fixture to dir/gws and returns that path.
func InstallGWSFilter(t testing.TB, dir string) string {
	t.Helper()
	src := GWSFilterFixtureDir(t)
	dst := filepath.Join(dir, "gws")
	err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := os.ReadFile(path) // #nosec G304 G122 -- copies a repository test fixture.
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600) // #nosec G703 -- target is inside the caller's temp dir.
	})
	if err != nil {
		t.Fatalf("install gws filter fixture: %v", err)
	}
	return dst
}

// StaticFilterFiles are the files of the test filter "tok", keyed by their
// path in the filter directory. Its static credential is read from the host
// env var TOK_HOST_TOKEN or the file ~/.config/tok/token.json, and the
// container gets the placeholder in TOK_TOKEN.
func StaticFilterFiles() map[string]string {
	return map[string]string{
		"spec.yaml": `schemaVersion: "1"
kind: filter
name: tok
defaultPreset: default
hosts: [api.example.com, "*.files.example.com"]
credential:
  static:
    env: [TOK_HOST_TOKEN]
    file: { path: ~/.config/tok/token.json, parser: "json:token" }
  inject: { header: authorization, format: "Bearer %s" }
  env: [TOK_TOKEN]
capabilities:
  read: { description: Read things }
routes:
  - op: things.get
    capability: read
    method: GET
    host: api.example.com
    path: /v1/things/{id}
`,
		"README.md":            "# tok\n",
		"presets/default.yaml": "allow:\n  read: {}\n",
	}
}

// InstallStaticFilter writes the "tok" filter to dir/tok and returns that
// path.
func InstallStaticFilter(t testing.TB, dir string) string {
	t.Helper()
	dst := filepath.Join(dir, "tok")
	for rel, content := range StaticFilterFiles() {
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatalf("install tok filter fixture: %v", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil { // #nosec G703 -- target is inside the caller's temp dir.
			t.Fatalf("install tok filter fixture: %v", err)
		}
	}
	return dst
}
