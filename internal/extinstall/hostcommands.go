// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"enclave/internal/model"
)

// hostCommandNames lists the enclave verbs extDir contributes: the executable
// files directly inside its commands/host/, in os.ReadDir's sorted order. This
// is the single definition of what counts, shared by the capability summary,
// the inventory, and remove, so all three agree with the set internal/usercmd
// will actually register.
//
// A missing commands/host/ yields nothing, not an error. Links are resolved
// because usercmd resolves them: copyExtensionTree cannot produce one, but a
// directory the user created by hand can. Anything nested deeper, and any
// non-executable file (a README or a data file beside the scripts), is ignored.
func hostCommandNames(extDir string) ([]string, error) {
	dir := filepath.Join(extDir, model.CommandsDirName, model.CommandsHostDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		info, statErr := os.Stat(filepath.Join(dir, entry.Name()))
		if statErr != nil {
			// A broken link is not a verb. usercmd reports it where it would
			// otherwise have run it.
			continue
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
