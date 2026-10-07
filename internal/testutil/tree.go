// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package testutil holds helpers shared by the tests of several packages.
package testutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// PinTree backdates every entry under root and returns a snapshot of the tree.
// Creating or removing an entry updates its directory's mtime, so a later
// snapshot also exposes writes that were undone again, such as a writability
// probe.
func PinTree(t testing.TB, root string) map[string]string {
	t.Helper()
	pinned := time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, pinned, pinned) // #nosec G122 -- root is a test-owned temporary tree.
	})
	if err != nil {
		t.Fatalf("pin %s: %v", root, err)
	}
	return snapshotTree(t, root)
}

// AssertTreeUnchanged fails the test if anything under root was created,
// removed, or modified since PinTree returned before.
func AssertTreeUnchanged(t testing.TB, root string, before map[string]string) {
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

func snapshotTree(t testing.TB, root string) map[string]string {
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
