// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package arch enforces the isolation-backend boundary: session code must
// reach Docker only through internal/backend; direct internal/docker use is
// limited to the Docker backend and approved integration paths.
package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const dockerImport = "enclave/internal/docker"

// dockerImportAllowlist lists the only non-test files (by repo-relative path
// or directory prefix) that may import internal/docker.
var dockerImportAllowlist = []string{
	// The Docker CLI wrapper itself.
	"internal/docker/",
	// The Docker backend implementation behind the neutral seam.
	"internal/backend/docker/",
	// Build and cleanup remain app-level Docker mechanics. The gateway sidecar
	// and devcontainer support are Docker-specific.
	"internal/app/build.go",
	"internal/app/build_preflight.go",
	"internal/app/cleanup.go",
	"internal/devcontainer/devcontainer.go",
	"internal/gateway/gateway.go",
}

func TestDockerImportsConfinedToBackendAndCarveOuts(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	var violations []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "frontend" || name == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isAllowedDockerImporter(rel) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, `"`) == dockerImport {
				violations = append(violations, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("files import %s outside the Docker backend and approved integration paths:\n  %s\nRoute session/storage operations through internal/backend instead, or update dockerImportAllowlist intentionally.",
			dockerImport, strings.Join(violations, "\n  "))
	}
}

// TestDockerBackendDoesNotImportRuntime keeps the layering one-directional:
// the runtime declares intent through internal/backend; the Docker backend
// must not call back into runtime code.
func TestDockerBackendDoesNotImportRuntime(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "backend", "docker")

	fset := token.NewFileSet()
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob backend/docker: %v", err)
	}
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "enclave/internal/runtime" {
				t.Fatalf("%s imports internal/runtime; the backend must stay below the runtime layer", path)
			}
		}
	}
}

func isAllowedDockerImporter(rel string) bool {
	for _, allowed := range dockerImportAllowlist {
		if strings.HasSuffix(allowed, "/") {
			if strings.HasPrefix(rel, allowed) {
				return true
			}
			continue
		}
		if rel == allowed {
			return true
		}
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}
