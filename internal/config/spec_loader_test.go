// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

// testPathsWithExtensions builds a model.Paths whose ToolsDir/FeaturesDir
// point at internal/config/<fixtureDir>/{tools,features}, with no user
// override dirs configured.
func testPathsWithExtensions(t *testing.T, fixtureDir string) model.Paths {
	t.Helper()
	return model.Paths{
		ToolsDir:    filepath.Join(fixtureDir, "tools"),
		FeaturesDir: filepath.Join(fixtureDir, "features"),
	}
}

func TestLoadProfileFromSpec(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	p, err := LoadProfile(paths, "demo")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Name != "demo" || p.Command == "" {
		t.Fatalf("profile from spec.yaml wrong: %+v", p)
	}
	if p.Command != "demo" {
		t.Fatalf("expected entrypoint.run to map to command, got %+v", p)
	}
	if p.ConfigDir != ".demo" {
		t.Fatalf("expected configDir to be mapped, got %+v", p)
	}
}

func TestLoadProfileFromSpecJSON(t *testing.T) {
	root := t.TempDir()
	toolDir := filepath.Join(root, "tools", "json-tool")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatalf("create tool directory: %v", err)
	}
	spec := `{"schemaVersion":"1","kind":"sandbox","name":"json-tool","sandbox":{"entrypoint":{"run":["json-tool"]},"configDir":".json-tool"}}`
	if err := os.WriteFile(filepath.Join(toolDir, SpecFilenameJSON), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec.json: %v", err)
	}

	profile, err := LoadProfile(model.Paths{ToolsDir: filepath.Join(root, "tools")}, "json-tool")
	if err != nil {
		t.Fatalf("LoadProfile from spec.json: %v", err)
	}
	if profile.Name != "json-tool" || profile.Command != "json-tool" || profile.ConfigDir != ".json-tool" {
		t.Fatalf("profile from spec.json wrong: %+v", profile)
	}
}

func TestLoadToolExtensionFromSpec(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	ext, err := LoadToolExtension(paths, "demo")
	if err != nil {
		t.Fatalf("LoadToolExtension: %v", err)
	}
	if ext.Type != model.ExtensionKindSandbox || ext.Name != "demo" {
		t.Fatalf("tool extension from spec.yaml wrong: %+v", ext)
	}
	if !ext.DefaultIncluded {
		t.Fatalf("expected default DefaultIncluded=true for tool, got %+v", ext)
	}
}

func TestLoadFeatureExtensionFromSpec(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	ext, err := LoadFeatureExtension(paths, "demo-mixin")
	if err != nil {
		t.Fatalf("LoadFeatureExtension: %v", err)
	}
	if ext.Type != model.ExtensionKindMixin {
		t.Fatalf("expected feature, got %q", ext.Type)
	}
	if !ext.DefaultEnabled {
		t.Fatalf("expected default DefaultEnabled=true for mixin, got %+v", ext)
	}
}

func TestListToolsIncludesSpecOnlyTools(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	tools, err := ListTools(paths)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{"demo": true}
	got := map[string]bool{}
	for _, name := range tools {
		got[name] = true
	}
	for name := range want {
		if !got[name] {
			t.Fatalf("ListTools=%v missing %q", tools, name)
		}
	}
}

func TestListProfilesIncludesSpecOnlyTools(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	names, err := ListProfiles(paths)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	got := map[string]bool{}
	for _, name := range names {
		got[name] = true
	}
	if !got["demo"] {
		t.Fatalf("ListProfiles=%v missing demo", names)
	}
}

// TestLoadSpecRejectsKindInDocument loads a spec file that resolves under
// tools/ (KindSandbox resolution) but whose document declares kind: mixin,
// so resolution succeeds and the doc.Kind != kind check actually fires.
func TestLoadSpecRejectsKindInDocument(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	_, err := LoadSpec(paths, "badkind", KindSandbox)
	if err == nil {
		t.Fatalf("expected kind mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "kind must be") {
		t.Fatalf("expected kind-mismatch error, got %v", err)
	}
}

// TestLoadSpecRejectsNameInDocument loads a spec whose document name differs
// from its directory name, exercising the doc.Name != name check.
func TestLoadSpecRejectsNameInDocument(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	_, err := LoadSpec(paths, "badname", KindSandbox)
	if err == nil {
		t.Fatalf("expected name mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "name must be") {
		t.Fatalf("expected name-mismatch error, got %v", err)
	}
}

// TestLoadSpecMissingFileRoutesElsewhere confirms that resolving a spec of a
// kind whose root does not contain the extension returns not-found (the
// previous "kind mismatch" test only ever hit this path).
func TestLoadSpecRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "tools", "demo", SpecFilename),
		"schemaVersion: \"1\"\nkind: sandbox\nname: demo\nnetwrok:\n  deniedDomains: [evil.com]\n")
	paths := model.Paths{ToolsDir: filepath.Join(dir, "tools"), FeaturesDir: filepath.Join(dir, "features")}
	if _, err := LoadSpec(paths, "demo", KindSandbox); err == nil || !strings.Contains(err.Error(), "netwrok") {
		t.Fatalf("expected unknown-key parse error mentioning the typo, got %v", err)
	}
}

func TestLoadSpecRejectsDuplicateKeys(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "tools", "demo", SpecFilename),
		"schemaVersion: \"1\"\nkind: sandbox\nname: demo\nnetwork:\n  deniedDomains: [a.com]\nnetwork:\n  deniedDomains: [b.com]\n")
	paths := model.Paths{ToolsDir: filepath.Join(dir, "tools"), FeaturesDir: filepath.Join(dir, "features")}
	if _, err := LoadSpec(paths, "demo", KindSandbox); err == nil {
		t.Fatal("expected duplicate-key parse error, got nil")
	}
}

func TestLoadSpecRejectsWrongSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	for _, version := range []string{"", "v1", "2"} {
		line := ""
		if version != "" {
			line = "schemaVersion: \"" + version + "\"\n"
		}
		writeTestFile(t, filepath.Join(dir, "tools", "demo", SpecFilename),
			line+"kind: sandbox\nname: demo\n")
		if _, err := LoadSpec(paths(dir), "demo", KindSandbox); err == nil || !strings.Contains(err.Error(), "schemaVersion") {
			t.Fatalf("schemaVersion %q: expected validation error, got %v", version, err)
		}
	}
}

func paths(dir string) model.Paths {
	return model.Paths{ToolsDir: filepath.Join(dir, "tools"), FeaturesDir: filepath.Join(dir, "features")}
}

func TestLoadSpecRejectsUnknownProxyManagedEntry(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "tools", "demo", SpecFilename), `
schemaVersion: "1"
kind: sandbox
name: demo
credentials:
  sources:
    svc: { env: [SVC_TOKEN] }
environment:
  proxyManaged: [SVC_TOKEN, SVC_TOKEN_TYPO]
`)
	if _, err := LoadSpec(paths(dir), "demo", KindSandbox); err == nil || !strings.Contains(err.Error(), "SVC_TOKEN_TYPO") {
		t.Fatalf("expected proxyManaged validation error naming the typo, got %v", err)
	}
}

func TestLoadSpecAcceptsMatchingProxyManagedEntry(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "tools", "demo", SpecFilename), `
schemaVersion: "1"
kind: sandbox
name: demo
credentials:
  sources:
    svc: { env: [SVC_TOKEN, SVC_ALIAS] }
environment:
  proxyManaged: [SVC_ALIAS]
`)
	if _, err := LoadSpec(paths(dir), "demo", KindSandbox); err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
}

func TestLoadSpecRejectsWhitespaceEntrypointArgv(t *testing.T) {
	dir := t.TempDir()
	for _, spec := range []string{
		"schemaVersion: \"1\"\nkind: sandbox\nname: demo\nsandbox:\n  entrypoint:\n    run: [\"my tool\"]\n",
		"schemaVersion: \"1\"\nkind: sandbox\nname: demo\nsandbox:\n  entrypoint:\n    run: [demo]\n    args: [\"--title\", \"My App\"]\n",
	} {
		writeTestFile(t, filepath.Join(dir, "tools", "demo", SpecFilename), spec)
		if _, err := LoadSpec(paths(dir), "demo", KindSandbox); err == nil || !strings.Contains(err.Error(), "whitespace") {
			t.Fatalf("expected whitespace argv rejection, got %v (spec %q)", err, spec)
		}
	}
}

func TestLoadSpecMissingFileRoutesElsewhere(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	// "demo" only exists under tools/; KindMixin resolves under features/.
	if _, err := LoadSpec(paths, "demo", KindMixin); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestLoadSpecNotFoundIsErrNotExist(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	if _, err := LoadSpec(paths, "does-not-exist", KindSandbox); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist for missing spec, got %v", err)
	}
}

// TestWarnUnknownFilesEntries confirms files/home and files/workspace are
// honored silently while any other files/ entry is flagged.
func TestWarnUnknownFilesEntries(t *testing.T) {
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	for _, sub := range []string{"home", "workspace", "bogus"} {
		if err := os.MkdirAll(filepath.Join(filesDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(filesDir, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray: %v", err)
	}

	var msgs []string
	warnUnknownFilesEntries(filesDir, "demo", func(m string) { msgs = append(msgs, m) })

	joined := strings.Join(msgs, "\n")
	for _, honored := range []string{"home", "workspace"} {
		if strings.Contains(joined, filepath.Join(filesDir, honored)) {
			t.Fatalf("honored files/%s must not warn, got:\n%s", honored, joined)
		}
	}
	for _, unknown := range []string{"bogus", "stray.txt"} {
		if !strings.Contains(joined, unknown) {
			t.Fatalf("expected a warning mentioning %q, got:\n%s", unknown, joined)
		}
	}
	if len(msgs) != 2 {
		t.Fatalf("expected exactly 2 warnings (bogus + stray.txt), got %d:\n%s", len(msgs), joined)
	}
}

// TestWarnUnknownFilesEntriesNoDir confirms a missing files/ dir is silent.
func TestWarnUnknownFilesEntriesNoDir(t *testing.T) {
	var msgs []string
	warnUnknownFilesEntries(filepath.Join(t.TempDir(), "files"), "demo", func(m string) { msgs = append(msgs, m) })
	if len(msgs) != 0 {
		t.Fatalf("missing files/ dir must be silent, got %v", msgs)
	}
}
