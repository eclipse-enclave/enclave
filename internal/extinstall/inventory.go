// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"

	"enclave/internal/config"
	"enclave/internal/model"
)

// Managed describes one discovered extension: where its files come from, and
// its provenance when the installer put them there.
type Managed struct {
	Name   string
	Source string
	Origin *Origin
	// Modified reports content edited by hand since the install. It is only
	// filled in at InventoryModifications detail, since answering it costs a
	// full read of the extension's content.
	Modified bool
	// Problem is a compact, single-line description of a provenance read
	// failure for this extension (e.g. a corrupt sidecar). It never aborts
	// Inventory: a listing command must list. When set, Origin is nil and
	// Modified is false.
	Problem string
}

// InventoryDetail selects how much Inventory reports about each extension;
// every level costs more I/O than the one before it.
type InventoryDetail int

const (
	// InventoryNames resolves each extension's built-in and user directories
	// and classifies its Source. Nothing inside an extension is opened.
	InventoryNames InventoryDetail = iota
	// InventoryProvenance also reads each user extension's provenance sidecar
	// into Origin: one small JSON document per extension, no content.
	InventoryProvenance
	// InventoryModifications also hashes each user extension's whole tree to
	// report Modified.
	InventoryModifications
)

// Inventory reports extensions of kind visible to paths, keyed by name. A nil
// names enumerates every extension directory of that kind, including one whose
// spec no longer loads, so a management command can still reach it; a caller
// that has already resolved the names passes them to avoid a second
// enumeration.
//
// Provenance is read from the user directory only: a built-in never carries a
// sidecar.
func Inventory(paths model.Paths, kind model.ExtensionKind, names []string, detail InventoryDetail) (map[string]Managed, error) {
	if names == nil {
		var err error
		if names, err = config.ListExtensionDirNames(paths, kind); err != nil {
			return nil, err
		}
	}

	inventory := make(map[string]Managed, len(names))
	for _, name := range names {
		builtinDir, userDir := config.ResolveExtensionDirs(paths, kind, name)
		entry := Managed{Name: name, Source: config.SourceLabel(builtinDir, userDir)}
		if detail > InventoryNames && userDir != "" {
			origin, readErr := readOrigin(userDir)
			switch {
			case readErr != nil:
				entry.Problem = fmt.Sprintf("%s %q: %v", kind.Label(), name, readErr)
			case origin != nil:
				entry.Origin = origin
				if detail >= InventoryModifications {
					modified, modErr := isLocallyModified(userDir, *origin)
					if modErr != nil {
						entry.Origin = nil
						entry.Problem = fmt.Sprintf("%s %q: %v", kind.Label(), name, modErr)
						break
					}
					entry.Modified = modified
				}
			}
		}
		inventory[name] = entry
	}
	return inventory, nil
}
