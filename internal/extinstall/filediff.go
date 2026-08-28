// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"
	"io"
	"sort"
)

// maxChangedFilesShown bounds how many changed paths update prints before
// falling back to a count, so a large refactor in the source does not turn
// the trust summary into pages of paths.
const maxChangedFilesShown = 10

// changedFile is one path that differs between the installed ("before") and
// staged ("after") extension trees.
type changedFile struct {
	Path   string
	Status string // "added", "removed", or "modified"
}

// treeChanges reports what the staged tree changes about the installed one,
// along with the staged tree's manifest so its hash can be reused instead of
// read again. The changed-file list is advisory output, so an unreadable tree
// (a nil before, or a failed read of afterDir) yields no changes rather than
// failing the install.
func treeChanges(before *treeManifest, afterDir string) ([]changedFile, treeManifest) {
	after, err := readTreeManifest(afterDir)
	if err != nil || before == nil {
		return nil, treeManifest{}
	}
	return diffManifests(*before, after), after
}

// diffManifests compares two tree manifests by content and mode.
func diffManifests(before treeManifest, after treeManifest) []changedFile {
	var changes []changedFile
	for rel, digest := range after.Files {
		beforeDigest, ok := before.Files[rel]
		switch {
		case !ok:
			changes = append(changes, changedFile{Path: rel, Status: "added"})
		case beforeDigest != digest:
			changes = append(changes, changedFile{Path: rel, Status: "modified"})
		}
	}
	for rel := range before.Files {
		if _, ok := after.Files[rel]; !ok {
			changes = append(changes, changedFile{Path: rel, Status: "removed"})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

// printChangedFiles renders the file-level diff, capping the listed paths at
// maxChangedFilesShown and printing a remainder count beyond that so a large
// change stays readable.
func printChangedFiles(w io.Writer, changes []changedFile) {
	if len(changes) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "%s%d file(s) changed:\n", bodyIndent, len(changes))
	shown := changes
	if len(shown) > maxChangedFilesShown {
		shown = shown[:maxChangedFilesShown]
	}
	for _, change := range shown {
		_, _ = fmt.Fprintf(w, "%s  %-8s %s\n", bodyIndent, change.Status, change.Path)
	}
	if remaining := len(changes) - len(shown); remaining > 0 {
		_, _ = fmt.Fprintf(w, "%s  … and %d more\n", bodyIndent, remaining)
	}
	_, _ = fmt.Fprintln(w)
}
