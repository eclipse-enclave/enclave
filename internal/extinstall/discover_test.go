// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"path/filepath"
	"testing"

	"enclave/internal/model"
)

func TestCandidateDirsFindsSpecDirectories(t *testing.T) {
	files := []string{
		"README.md",
		"extensions/features/foo/spec.yaml",
		"extensions/features/foo/install.sh",
		"extensions/tools/bar/spec.json",
		"website/index.html",
	}
	got, err := candidateDirs(files, "")
	if err != nil {
		t.Fatalf("candidateDirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want two", got)
	}
	if got[0].Dir != "extensions/features/foo" || got[0].Name != "foo" {
		t.Errorf("first candidate = %+v", got[0])
	}
	if got[1].Dir != "extensions/tools/bar" || got[1].Name != "bar" {
		t.Errorf("second candidate = %+v", got[1])
	}
}

func TestCandidateDirsRepositoryRoot(t *testing.T) {
	got, err := candidateDirs([]string{"spec.yaml", "install.sh"}, "")
	if err != nil {
		t.Fatalf("candidateDirs: %v", err)
	}
	if len(got) != 1 || got[0].Dir != "" {
		t.Fatalf("candidates = %+v, want the repository root", got)
	}
}

func TestCandidateDirsHonorsSubpath(t *testing.T) {
	files := []string{
		"extensions/features/foo/spec.yaml",
		"extensions/features/bar/spec.yaml",
	}
	got, err := candidateDirs(files, "extensions/features/bar")
	if err != nil {
		t.Fatalf("candidateDirs: %v", err)
	}
	if len(got) != 1 || got[0].Name != "bar" {
		t.Fatalf("candidates = %+v, want only bar", got)
	}
}

func TestCandidateDirsSubpathWithoutSpec(t *testing.T) {
	files := []string{"extensions/features/foo/spec.yaml"}
	if _, err := candidateDirs(files, "extensions/features/nope"); err == nil {
		t.Fatal("candidateDirs accepted a subpath with no spec document")
	}
}

func TestCandidateDirsDepthBoundExcludesDeeperThanBound(t *testing.T) {
	files := []string{"a/b/c/d/e/f/g/h/i/spec.yaml"} // one segment past maxCandidateDepth
	got, err := candidateDirs(files, "")
	if err != nil {
		t.Fatalf("candidateDirs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("candidates = %+v, want none for spec deeper than depth bound", got)
	}
}

func TestCandidateDirsDepthBoundRespectedForExplicitSubpath(t *testing.T) {
	files := []string{"a/b/c/d/e/f/g/h/i/spec.yaml"} // one segment past maxCandidateDepth
	got, err := candidateDirs(files, "a/b/c/d/e/f/g/h/i")
	if err != nil {
		t.Fatalf("candidateDirs: %v", err)
	}
	if len(got) != 1 || got[0].Dir != "a/b/c/d/e/f/g/h/i" {
		t.Fatalf("candidates = %+v, want the deep spec when named explicitly", got)
	}
}

func TestCandidateDirsDepthBoundIncludesAtBound(t *testing.T) {
	files := []string{"a/b/c/d/e/f/g/h/spec.yaml"} // exactly maxCandidateDepth
	got, err := candidateDirs(files, "")
	if err != nil {
		t.Fatalf("candidateDirs: %v", err)
	}
	if len(got) != 1 || got[0].Dir != "a/b/c/d/e/f/g/h" {
		t.Fatalf("candidates = %+v, want the spec at the depth boundary", got)
	}
}

func TestClassifyAndSelectKind(t *testing.T) {
	repo := t.TempDir()
	writeFixture(t, filepath.Join(repo, "extensions", "features", "foo", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: mixin\nname: foo\n", 0o644)
	writeFixture(t, filepath.Join(repo, "extensions", "tools", "bar", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: sandbox\nname: bar\n", 0o644)
	writeFixture(t, filepath.Join(repo, "extensions", "features", "renamed", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: mixin\nname: different\n", 0o644)
	writeFixture(t, filepath.Join(repo, "extensions", "features", "broken", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: mixin\nname: broken\nbogus: 1\n", 0o644)

	candidates := []candidate{
		{Dir: "extensions/features/foo", Name: "foo"},
		{Dir: "extensions/tools/bar", Name: "bar"},
		{Dir: "extensions/features/renamed", Name: "renamed"},
		{Dir: "extensions/features/broken", Name: "broken"},
	}
	classified := classify(repo, candidates)
	match, other, skipped := selectKind(classified, model.KindFeature)

	if len(match) != 1 || match[0].Name != "foo" {
		t.Fatalf("match = %+v, want only foo", match)
	}
	if len(other) != 1 || other[0].Name != "bar" {
		t.Fatalf("other = %+v, want only bar", other)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %+v, want the mismatched and the malformed dirs", skipped)
	}
	for _, entry := range skipped {
		if entry.Skip == "" {
			t.Fatalf("skipped entry %q has no reason", entry.Name)
		}
	}
}
