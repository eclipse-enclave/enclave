// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"enclave/internal/model"
)

// walkExtensionEntries walks dir and reports every entry below it (files and
// directories, dir itself excluded) with its path relative to dir,
// slash-separated regardless of OS.
func walkExtensionEntries(dir string, fn func(rel string, path string, entry fs.DirEntry) error) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel), path, entry)
	})
}

// walkExtensionFiles reports every non-directory entry below dir, excluding the
// provenance sidecar. The sidecar is present in an installed tree and never in
// a staged one, so counting or hashing it would report every update as a
// content change, and would let writing provenance invalidate the tree hash
// that provenance itself records.
func walkExtensionFiles(dir string, fn func(rel string, path string, entry fs.DirEntry) error) error {
	return walkExtensionEntries(dir, func(rel string, path string, entry fs.DirEntry) error {
		if entry.IsDir() || rel == model.ExtensionSourceFilename {
			return nil
		}
		return fn(rel, path, entry)
	})
}

// fileDigest is one file's entry in a tree manifest.
type fileDigest struct {
	Mode   string
	Size   int64
	Sha256 string
}

// treeManifest is one read of an extension tree, viewed two ways: per-file
// digests for the changed-file list an update prints, and the aggregate tree
// hash recorded in the provenance sidecar.
type treeManifest struct {
	Files map[string]fileDigest
	Hash  string
}

// readTreeManifest reads every file under extDir once, digesting it into both
// its own entry and the aggregate tree hash. The provenance sidecar is
// excluded; a missing extDir is an error.
//
// The aggregate hash is persisted in the sidecar and compared against on the
// next update, so its construction — files in sorted path order, each
// contributing path, normalized mode, length, and content — is a compatibility
// contract: changing it makes every installed extension report as locally
// modified.
func readTreeManifest(extDir string) (treeManifest, error) {
	type entry struct {
		rel  string
		mode string
		path string
	}
	var entries []entry
	err := walkExtensionFiles(extDir, func(rel string, path string, d fs.DirEntry) error {
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, entry{rel: rel, mode: normalizedModeString(info.Mode()), path: path})
		return nil
	})
	if err != nil {
		return treeManifest{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	manifest := treeManifest{Files: make(map[string]fileDigest, len(entries))}
	tree := sha256.New()
	for _, item := range entries {
		digest, err := digestFile(item.path, item.rel, item.mode, tree)
		if err != nil {
			return treeManifest{}, err
		}
		manifest.Files[item.rel] = digest
	}
	manifest.Hash = "sha256:" + hex.EncodeToString(tree.Sum(nil))
	return manifest, nil
}

// digestFile hashes one file's content into its own digest and into the running
// tree hash, reading it once for both.
func digestFile(path string, rel string, mode string, tree io.Writer) (digest fileDigest, err error) {
	// #nosec G304 -- path came from walking the caller's extension dir.
	file, err := os.Open(path)
	if err != nil {
		return fileDigest{}, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return fileDigest{}, err
	}
	if _, err = fmt.Fprintf(tree, "%s\n%s\n%d\n", rel, mode, info.Size()); err != nil { // #nosec G705
		return fileDigest{}, err
	}
	content := sha256.New()
	if _, err = io.Copy(io.MultiWriter(tree, content), file); err != nil {
		return fileDigest{}, err
	}
	return fileDigest{Mode: mode, Size: info.Size(), Sha256: hex.EncodeToString(content.Sum(nil))}, nil
}

// TreeHash digests the installed content of extDir so a later update can tell
// whether the user edited it by hand.
func TreeHash(extDir string) (string, error) {
	manifest, err := readTreeManifest(extDir)
	if err != nil {
		return "", err
	}
	return manifest.Hash, nil
}

// normalizedModeString collapses a file mode to the two values the installer
// ever writes, so an unrelated permission bit cannot look like a local edit.
func normalizedModeString(mode os.FileMode) string {
	if mode.Perm()&0o111 != 0 {
		return "0755"
	}
	return "0644"
}
