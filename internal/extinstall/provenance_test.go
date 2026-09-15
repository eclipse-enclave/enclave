// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

func writeFixture(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// fixtureExtension builds a fixture directory named "foo": readOrigin checks
// that a sidecar's recorded Name matches its directory's basename, and every
// caller here writes a sidecar with Name: "foo".
func fixtureExtension(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "foo")
	writeFixture(t, filepath.Join(dir, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: foo\n", 0o644)
	writeFixture(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\necho hi\n", 0o755)
	return dir
}

func TestOriginRoundTrip(t *testing.T) {
	dir := fixtureExtension(t)
	want := Origin{
		SchemaVersion: OriginSchemaVersion,
		Kind:          string(model.KindFeature),
		Name:          "foo",
		Remote:        "https://github.com/acme/kits",
		Source:        "acme/kits/extensions/features/foo",
		Subpath:       "extensions/features/foo",
		Ref:           "main",
		RefType:       RefTypeBranch,
		Commit:        "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		InstalledAt:   "2026-08-26T10:15:00Z",
		InstalledBy:   "enclave 1.0.0",
		TreeHash:      "sha256:deadbeef",
	}
	if err := WriteOrigin(dir, want); err != nil {
		t.Fatalf("WriteOrigin: %v", err)
	}
	got, err := readOrigin(dir)
	if err != nil {
		t.Fatalf("readOrigin: %v", err)
	}
	if got == nil {
		t.Fatal("readOrigin returned nil for a written sidecar")
	}
	if *got != want {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", *got, want)
	}
}

// TestReadOriginRejectsUnsafeSidecars: readOrigin must reject a sidecar whose
// remote, ref, or name is unsafe, because updateOne feeds those straight to
// the fetcher, which hands them to git as bare operands.
func TestReadOriginRejectsUnsafeSidecars(t *testing.T) {
	baseOrigin := func() Origin {
		return Origin{
			SchemaVersion: OriginSchemaVersion,
			Kind:          string(model.KindFeature),
			Name:          "foo",
			Remote:        "https://github.com/acme/kits",
			Source:        "acme/kits",
			Ref:           "main",
			RefType:       RefTypeBranch,
			Commit:        "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		}
	}

	cases := []struct {
		name   string
		mutate func(*Origin)
	}{
		{
			name: "ext:: remote transport",
			mutate: func(o *Origin) {
				o.Remote = "ext::sh -c touch% /tmp/pwned"
			},
		},
		{
			name: "leading-dash remote",
			mutate: func(o *Origin) {
				o.Remote = "--upload-pack=touch /tmp/pwned;"
			},
		},
		{
			name: "mismatched name",
			mutate: func(o *Origin) {
				o.Name = "not-foo"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixtureExtension(t)
			origin := baseOrigin()
			tc.mutate(&origin)
			if err := WriteOrigin(dir, origin); err != nil {
				t.Fatalf("WriteOrigin: %v", err)
			}
			if _, err := readOrigin(dir); err == nil {
				t.Fatalf("readOrigin accepted an unsafe sidecar (%s)", tc.name)
			}
		})
	}
}

// TestReadOriginRedactsCredentials covers a hand-authored or copied sidecar:
// the installer never records credentials, so redacting at the read boundary is
// what keeps them out of every downstream printer, --json field, and git
// invocation.
func TestReadOriginRedactsCredentials(t *testing.T) {
	dir := fixtureExtension(t)
	if err := WriteOrigin(dir, Origin{
		SchemaVersion: OriginSchemaVersion,
		Kind:          string(model.KindFeature),
		Name:          filepath.Base(dir),
		Remote:        "https://user:s3cr3t@github.com/acme/kits",
		Source:        "https://user:s3cr3t@github.com/acme/kits",
		Ref:           "main",
		RefType:       RefTypeBranch,
		Commit:        "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
	}); err != nil {
		t.Fatalf("WriteOrigin: %v", err)
	}

	origin, err := readOrigin(dir)
	if err != nil {
		t.Fatalf("readOrigin: %v", err)
	}
	for field, got := range map[string]string{"Remote": origin.Remote, "Source": origin.Source} {
		if strings.Contains(got, "s3cr3t") {
			t.Errorf("origin.%s = %q, still carries the credential", field, got)
		}
		if !strings.Contains(got, "github.com/acme/kits") {
			t.Errorf("origin.%s = %q, lost the remote itself", field, got)
		}
	}
}

func TestReadOriginAbsent(t *testing.T) {
	origin, err := readOrigin(fixtureExtension(t))
	if err != nil {
		t.Fatalf("readOrigin: %v", err)
	}
	if origin != nil {
		t.Fatalf("origin = %+v, want nil for an unmanaged directory", origin)
	}
}

// TestTreeHashPinnedValue pins the digest TreeHash produces for a fixed tree.
// The value is persisted in every installed extension's sidecar and compared
// against on the next update, so changing how the digest is built would make
// every already-installed extension report as locally modified.
func TestTreeHashPinnedValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "foo")
	writeFixture(t, filepath.Join(dir, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: foo\n", 0o644)
	writeFixture(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\necho hi\n", 0o755)
	writeFixture(t, filepath.Join(dir, "entrypoint.d", "10-setup.sh"), "#!/bin/sh\n", 0o755)
	writeFixture(t, filepath.Join(dir, "files", "home", ".config", "foo.toml"), "a = 1\n", 0o644)
	writeFixture(t, filepath.Join(dir, "empty"), "", 0o644)
	writeFixture(t, filepath.Join(dir, model.ExtensionSourceFilename), `{"commit":"whatever"}`, 0o644)
	for path, mode := range map[string]os.FileMode{
		"spec.yaml":                   0o644,
		"install.sh":                  0o755,
		"entrypoint.d/10-setup.sh":    0o755,
		"files/home/.config/foo.toml": 0o644,
		"empty":                       0o644,
		model.ExtensionSourceFilename: 0o644,
	} {
		if err := os.Chmod(filepath.Join(dir, filepath.FromSlash(path)), mode); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
	}

	const want = "sha256:02aecf8bd96ebe0a9e1b58b923d44b947d7d86317cbf132599ce3a830d7ad878"
	got, err := TreeHash(dir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	if got != want {
		t.Fatalf("TreeHash = %q, want %q", got, want)
	}
}

func TestTreeHashIgnoresSidecar(t *testing.T) {
	dir := fixtureExtension(t)
	before, err := TreeHash(dir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	writeFixture(t, filepath.Join(dir, model.ExtensionSourceFilename), `{"commit":"whatever"}`, 0o644)
	after, err := TreeHash(dir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	if before != after {
		t.Fatalf("tree hash changed when only the sidecar was added: %s -> %s", before, after)
	}
}

func TestTreeHashTracksContentAndMode(t *testing.T) {
	dir := fixtureExtension(t)
	base, err := TreeHash(dir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}

	writeFixture(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\necho changed\n", 0o755)
	changedContent, err := TreeHash(dir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	if changedContent == base {
		t.Fatal("tree hash unchanged after a content edit")
	}

	if err := os.Chmod(filepath.Join(dir, "install.sh"), 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	changedMode, err := TreeHash(dir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	if changedMode == changedContent {
		t.Fatal("tree hash unchanged after dropping the execute bit")
	}
}

func TestIsLocallyModified(t *testing.T) {
	dir := fixtureExtension(t)
	hash, err := TreeHash(dir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	origin := Origin{TreeHash: hash}

	modified, err := isLocallyModified(dir, origin)
	if err != nil {
		t.Fatalf("isLocallyModified: %v", err)
	}
	if modified {
		t.Fatal("clean directory reported as modified")
	}

	writeFixture(t, filepath.Join(dir, "extra.txt"), "local edit\n", 0o644)
	modified, err = isLocallyModified(dir, origin)
	if err != nil {
		t.Fatalf("isLocallyModified: %v", err)
	}
	if !modified {
		t.Fatal("edited directory reported as clean")
	}
}

func TestShortCommit(t *testing.T) {
	if got := ShortCommit("a1b2c3d4e5f6a7b8"); got != "a1b2c3d" {
		t.Fatalf("ShortCommit = %q", got)
	}
	if got := ShortCommit("abc"); got != "abc" {
		t.Fatalf("ShortCommit of a short value = %q", got)
	}
}
