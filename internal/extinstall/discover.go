// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/config"
	"enclave/internal/model"
)

// maxCandidateDepth bounds how deep a spec document may sit below the
// repository root, so a pathological tree cannot turn discovery into a crawl.
const maxCandidateDepth = 8

// candidate is a directory in a fetched repository that holds a spec document.
type candidate struct {
	Dir  string // slash path relative to the repository root; "" is the root
	Name string // base name of Dir, or the repository root's own name
}

// classified is a candidate whose spec has been parsed. Skip carries the reason
// a directory cannot be installed; such entries are reported, never silently
// dropped.
type classified struct {
	candidate
	SpecKind string
	Skip     string
}

// candidateDirs finds directories containing a spec document in a repository
// file listing. It reads the listing rather than a checkout, so no file content
// has to be downloaded to discover what a repository offers. With subpath set,
// only that directory is considered and its absence is an error.
func candidateDirs(files []string, subpath string) ([]candidate, error) {
	dirs := map[string]struct{}{}
	for _, file := range files {
		base := path.Base(file)
		if base != config.SpecFilename && base != config.SpecFilenameJSON {
			continue
		}
		dir := path.Dir(file)
		if dir == "." {
			dir = ""
		}
		// Apply depth bound only for unscoped discovery; explicit subpaths are
		// resolved regardless of depth.
		if subpath == "" && dir != "" && len(strings.Split(dir, "/")) > maxCandidateDepth {
			continue
		}
		dirs[dir] = struct{}{}
	}

	if subpath != "" {
		if _, ok := dirs[subpath]; !ok {
			return nil, fmt.Errorf("no %s in %s", config.SpecFilename, subpath)
		}
		return []candidate{{Dir: subpath, Name: path.Base(subpath)}}, nil
	}

	candidates := make([]candidate, 0, len(dirs))
	for dir := range dirs {
		name := path.Base(dir)
		if dir == "" {
			name = ""
		}
		candidates = append(candidates, candidate{Dir: dir, Name: name})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Dir < candidates[j].Dir })
	return candidates, nil
}

// classify parses each candidate's spec document from a materialized checkout.
// A candidate whose spec cannot be read, or whose declared name disagrees with
// its directory name, is marked with a Skip reason: the extension loader
// resolves extensions by directory name, so such a directory could never load.
func classify(repoDir string, candidates []candidate) []classified {
	entries := make([]classified, 0, len(candidates))
	for _, cand := range candidates {
		entry := classified{candidate: cand}
		summary, err := config.SummarizeSpecDir(filepath.Join(repoDir, filepath.FromSlash(cand.Dir)))
		if err != nil {
			entry.Skip = fmt.Sprintf("cannot read %s: %v", config.SpecFilename, err)
			entries = append(entries, entry)
			continue
		}
		entry.SpecKind = summary.Kind
		switch {
		case summary.Name == "":
			entry.Skip = fmt.Sprintf("%s declares no name", config.SpecFilename)
		case cand.Name == "":
			// Repository-root extension: the directory name is the repository's,
			// so the spec name is authoritative and there is nothing to compare.
			entry.Name = summary.Name
		case summary.Name != cand.Name:
			entry.Skip = fmt.Sprintf("%s declares name %q but its directory is %q", config.SpecFilename, summary.Name, cand.Name)
		}
		entries = append(entries, entry)
	}
	return entries
}

// selectKind partitions classified candidates into those matching kind, those
// of the opposite kind (so the caller can name the right verb), and those that
// cannot be installed at all.
func selectKind(entries []classified, kind model.ExtensionKind) (match []classified, other []classified, skipped []classified) {
	for _, entry := range entries {
		switch {
		case entry.Skip != "":
			skipped = append(skipped, entry)
		case entry.SpecKind == kind.SpecKind():
			match = append(match, entry)
		case entry.SpecKind == kind.Other().SpecKind():
			other = append(other, entry)
		default:
			entry.Skip = fmt.Sprintf("unknown kind %q", entry.SpecKind)
			skipped = append(skipped, entry)
		}
	}
	return match, other, skipped
}
