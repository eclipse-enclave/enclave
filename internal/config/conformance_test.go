// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

// TestExtensionSurfaceGolden pins the resolved public surface (model.Profile
// for tools, model.Extension for features) of every built-in extension in
// extensions/. It is a drift detector: any change to a tool's or feature's
// resolved fields (command, secrets, providers, env, etc.) will fail this
// test until the golden files are regenerated with -update-golden and the
// diff is reviewed.
var updateGolden = flag.Bool("update-golden", false, "regenerate golden surface snapshots")

func TestExtensionSurfaceGolden(t *testing.T) {
	paths := realRepoPaths(t)

	tools, err := ListTools(paths)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("ListTools returned no tools; expected the real extensions/tools tree")
	}
	for _, name := range tools {
		p, err := LoadProfile(paths, name)
		if err != nil {
			t.Fatalf("LoadProfile(%s): %v", name, err)
		}
		assertGolden(t, "tool-"+name, p)

		// Also pin the tool's resolved model.Extension. The Profile snapshot
		// above does not cover tool-level Extension fields such as
		// default_included / default_enabled / priority, so a regression
		// there (e.g. flipping an opt-in tool to opt-in-by-default) would
		// otherwise slip through the fidelity gate.
		ext, err := LoadToolExtension(paths, name)
		if err != nil {
			t.Fatalf("LoadToolExtension(%s): %v", name, err)
		}
		assertGolden(t, "tool-ext-"+name, ext)
	}

	feats, err := ListFeatures(paths)
	if err != nil {
		t.Fatalf("ListFeatures: %v", err)
	}
	if len(feats) == 0 {
		t.Fatal("ListFeatures returned no features; expected the real extensions/features tree")
	}
	for _, ext := range feats {
		assertGolden(t, "feature-"+ext.Name, ext)
	}
}

// realRepoPaths resolves the paths.model.Paths for the actual repo checkout
// (not a temp fixture) by pointing ENCLAVE_HOME at the repo root discovered
// from the test's working directory, then delegating to the same
// ResolvePaths() the application uses. `go test` runs with the package
// directory as its working directory, and the compiled test binary lives
// outside the repo (e.g. under a temp build dir), so ResolvePaths() cannot
// find the app root via os.Executable() alone in this environment.
func realRepoPaths(t *testing.T) model.Paths {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, ok := findAppRootFromDir(wd)
	if !ok {
		t.Skipf("could not locate repo app root (Dockerfile, entrypoint.sh, extensions/, ...) walking up from %s; skipping golden conformance test", wd)
	}

	t.Setenv(model.EnvHome, root)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	// Conformance covers the built-in tree only; user extensions vary by host.
	paths.UserExtensionsDir = ""
	paths.UserToolsDir = ""
	paths.UserFeaturesDir = ""
	return paths
}

func assertGolden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	got = append(got, '\n')

	path := filepath.Join("testdata", "golden", name+".json")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update-golden to create it): %v", name, err)
	}
	if string(got) != string(want) {
		t.Fatalf("surface drift for %s (rerun with -update-golden after reviewing the diff):\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// TestExtensionSurfaceGolden_ReservedFieldsWarn is the "reserved fields warn"
// leg of the conformance contract: a spec that sets reserved fields must
// produce warnings via warnReservedFields, so the golden surface above can
// never silently absorb a reserved field's behavior. This reuses the same
// mustDoc test fixture helper as spec_warn_test.go, kept minimal since the
// exhaustive per-field coverage already lives there.
func TestExtensionSurfaceGolden_ReservedFieldsWarn(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: mixin
name: demo
agentContext: "hi"
`)
	var msgs []string
	warnReservedFields(doc, "demo", func(m string) { msgs = append(msgs, m) })
	if len(msgs) == 0 {
		t.Fatal("expected warnReservedFields to warn about reserved agentContext field")
	}
	if !strings.Contains(msgs[0], "agentContext") {
		t.Fatalf("expected warning to mention agentContext, got: %v", msgs)
	}
}
