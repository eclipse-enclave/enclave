// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseGatewayProxyBuildInputsRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data string
	}{
		{name: "duplicate", data: "go.mod\ngo.mod\n"},
		{name: "parent traversal", data: "../go.mod\n"},
		{name: "absolute", data: "/go.mod\n"},
		{name: "unclean", data: "internal/../go.mod\n"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseGatewayProxyBuildInputs(tc.data); err == nil {
				t.Fatalf("expected parse error for %q", tc.data)
			}
		})
	}
}

// TestGatewayProxyBuildInputsCoverInternalDeps guards against the manifest
// drifting out of sync with the proxy's real dependency tree. `make install`
// and debian/rules stage ONLY the packages listed here and then compile the
// gateway proxy from that staged subset, so a transitive internal dependency
// that is missing from the manifest builds fine from the repo but fails the
// install-time gateway build (this is exactly how internal/secretfile slipped
// through). Every enclave/internal/* package the proxy links must be covered
// by some manifest entry.
func TestGatewayProxyBuildInputsCoverInternalDeps(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate gateway proxy build inputs test file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))

	cmd := exec.Command("go", "list", "-deps", "./cmd/enclave-gateway-proxy")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	const modulePrefix = "enclave/"
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg := strings.TrimSpace(line)
		if !strings.HasPrefix(pkg, modulePrefix+"internal/") {
			continue
		}
		relDir := strings.TrimPrefix(pkg, modulePrefix) // e.g. internal/secretfile
		if !coveredByBuildInput(relDir) {
			t.Fatalf("gateway proxy depends on %s but no gateway_proxy_build_inputs.txt entry stages it; add %q", pkg, relDir)
		}
	}
}

// coveredByBuildInput reports whether relDir (a package dir like
// internal/secretfile) is staged by some manifest entry — either listed
// directly or nested under a listed directory (entries are copied recursively).
func coveredByBuildInput(relDir string) bool {
	for _, entry := range gatewayProxyBuildInputs {
		if entry == relDir || strings.HasPrefix(relDir, entry+"/") {
			return true
		}
	}
	return false
}

func TestGatewayProxyBuildInputsPathsExist(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate gateway proxy build inputs test file")
	}
	gatewayDir := filepath.Dir(filename)
	repoRoot := filepath.Clean(filepath.Join(gatewayDir, "..", ".."))

	for _, relPath := range gatewayProxyBuildInputs {
		fullPath := filepath.Join(repoRoot, filepath.FromSlash(relPath))
		if _, err := os.Stat(fullPath); err != nil {
			t.Fatalf("gateway proxy build input %s missing from repo: %v", relPath, err)
		}
	}
}
