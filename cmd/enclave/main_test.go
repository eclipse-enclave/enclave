// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows && !enclave_no_embed

package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"enclave/internal/app"
	"enclave/internal/appassets"
	"enclave/internal/config"
	"enclave/internal/model"
)

// Completion runs before the root guard, so it must not extract the embedded
// assets; it lists tools only once a regular command has extracted them. This
// package is the only one whose tests run with the real assets registered.
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

	before := pinTree(t, home)
	if out := completeTool(t); strings.Contains(out, "claude") {
		t.Errorf("completion listed tools before the assets were extracted: %q", out)
	}
	assertTreeUnchanged(t, home, before)

	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatalf("extract assets: %v", err)
	}
	if !strings.HasPrefix(paths.AppRoot, home+string(filepath.Separator)) {
		t.Fatalf("assets extracted outside HOME: %s", paths.AppRoot)
	}

	before = pinTree(t, home)
	if out := completeTool(t); !strings.Contains(out, "claude") {
		t.Errorf("completion did not list tools from the extracted assets: %q", out)
	}
	assertTreeUnchanged(t, home, before)
}

// completeTool sends the shell's completion request for --tool and returns
// the candidates.
func completeTool(t *testing.T) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdout := os.Stdout
	os.Stdout = writer
	code := app.Run([]string{"__complete", "run", "--tool", ""})
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

// pinTree backdates every entry under root and returns a snapshot of the tree.
// Creating or removing an entry updates its directory's mtime, so a later
// snapshot also exposes writes that were undone again, such as a writability
// probe.
func pinTree(t *testing.T, root string) map[string]string {
	t.Helper()
	pinned := time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, pinned, pinned)
	})
	if err != nil {
		t.Fatalf("pin %s: %v", root, err)
	}
	return snapshotTree(t, root)
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[rel] = fmt.Sprintf("%v %d %s", info.Mode(), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snapshot
}

func assertTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotTree(t, root)
	var changes []string
	for path, state := range after {
		if was, ok := before[path]; !ok {
			changes = append(changes, path+" was created")
		} else if was != state {
			changes = append(changes, fmt.Sprintf("%s changed: %s -> %s", path, was, state))
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changes = append(changes, path+" was removed")
		}
	}
	if len(changes) == 0 {
		return
	}
	sort.Strings(changes)
	if len(changes) > 5 {
		changes = append(changes[:5], fmt.Sprintf("and %d more", len(changes)-5))
	}
	t.Errorf("%s was written to:\n%s", root, strings.Join(changes, "\n"))
}
