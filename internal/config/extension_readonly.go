// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"io/fs"
	"path"
	"sort"

	"enclave/internal/appassets"
	"enclave/internal/model"
)

// ListExtensionNamesReadOnly returns the sorted names of the built-in and
// user extensions of kind that carry a spec document, without writing. It is
// for shell completion, which runs before the root guard: when the embedded
// assets have not been extracted yet, it reads the built-in names from the
// binary instead of extracting them.
func ListExtensionNamesReadOnly(kind model.ExtensionKind) ([]string, error) {
	paths, err := ResolvePathsReadOnly()
	if err == nil {
		return listSpecNames(paths, kind.SpecKind())
	}
	files, _, embedErr := appassets.Embedded()
	if embedErr != nil {
		return nil, err
	}
	builtin, err := embeddedSpecNames(files, kind)
	if err != nil {
		return nil, err
	}
	user, err := listSpecNames(userExtensionPathsReadOnly(), kind.SpecKind())
	if err != nil {
		return nil, err
	}
	return mergeNames(builtin, user), nil
}

// ListUserExtensionDirNamesReadOnly returns the sorted names of the extension
// directories of kind under the user root, without writing and without
// resolving the built-in tree.
func ListUserExtensionDirNamesReadOnly(kind model.ExtensionKind) ([]string, error) {
	_, userRoot := ExtensionRoots(userExtensionPathsReadOnly(), kind)
	return listExtensionDirNames(userRoot, "")
}

// userExtensionPathsReadOnly names only the user extension roots. The
// built-in roots stay empty, which every lookup skips.
func userExtensionPathsReadOnly() model.Paths {
	var paths model.Paths
	if home, err := resolveHostHome(true); err == nil {
		resolveUserExtensionPaths(&paths, home)
	}
	return paths
}

// embeddedSpecNames lists the built-in extensions of kind in the embedded
// asset tree that carry a spec document.
func embeddedSpecNames(files fs.FS, kind model.ExtensionKind) ([]string, error) {
	root := path.Join(appRootExtensionsRel, kind.DirName())
	entries, err := fs.ReadDir(files, root)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !IsExtensionDir(entry) {
			continue
		}
		for _, specName := range []string{SpecFilename, SpecFilenameJSON} {
			if _, err := fs.Stat(files, path.Join(root, entry.Name(), specName)); err == nil {
				names = append(names, entry.Name())
				break
			}
		}
	}
	return names, nil
}

func mergeNames(lists ...[]string) []string {
	seen := map[string]struct{}{}
	var merged []string
	for _, list := range lists {
		for _, name := range list {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			merged = append(merged, name)
		}
	}
	sort.Strings(merged)
	return merged
}
