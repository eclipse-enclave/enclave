// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows && !enclave_no_embed

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/app"
	"enclave/internal/appassets"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/testutil"
)

// Completion runs before the root guard, so it must not extract the embedded
// assets: until a regular command has extracted them, it reads the built-in
// names from the binary. This package is the only one whose tests run with the
// real assets registered.
func TestCompletionDoesNotExtractEmbeddedAssets(t *testing.T) {
	if _, _, err := appassets.Embedded(); err != nil {
		t.Fatalf("embedded assets: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{model.EnvHome, "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(key, "")
	}
	if paths, err := config.ResolvePathsReadOnly(); err == nil {
		t.Skipf("the test binary sits in an app root, so there is nothing to extract: %s", paths.AppRoot)
	}
	userTool := filepath.Join(config.HostExtensionsDir(home), model.KindTool.DirName(), "my-tool")
	if err := os.MkdirAll(userTool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userTool, config.SpecFilename), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	assertCompletions := func(stage string) {
		t.Helper()
		before := testutil.PinTree(t, home)
		if out := complete(t, "run", "--tool", ""); !strings.Contains(out, "claude\n") || !strings.Contains(out, "my-tool\n") {
			t.Errorf("%s: --tool did not list built-in and user tools: %q", stage, out)
		}
		if out := complete(t, "tools", "remove", ""); strings.Contains(out, "claude\n") || !strings.Contains(out, "my-tool\n") {
			t.Errorf("%s: tools remove did not list only the user tool: %q", stage, out)
		}
		testutil.AssertTreeUnchanged(t, home, before)
	}

	assertCompletions("before extraction")

	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatalf("extract assets: %v", err)
	}
	if !strings.HasPrefix(paths.AppRoot, home+string(filepath.Separator)) {
		t.Fatalf("assets extracted outside HOME: %s", paths.AppRoot)
	}

	assertCompletions("after extraction")
}

// complete sends the shell's completion request for args and returns the
// candidates.
func complete(t *testing.T, args ...string) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdout := os.Stdout
	os.Stdout = writer
	code := app.Run(append([]string{"__complete"}, args...))
	os.Stdout = stdout
	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read completion: %v", err)
	}
	if code != 0 {
		t.Fatalf("completion returned %d: %s", code, out)
	}
	return string(out)
}
