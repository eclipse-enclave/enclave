// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
)

func removeModeFiles(home, dir string, holder ModeHolder) error {
	if !holder.Protected {
		return nil
	}
	key := backend.StoreKey{Owner: holder.Tool, ProjectHash: holder.ProjectHash, Suffix: filepath.Base(dir)}
	expected, err := ResolveDir(home, key, backend.StoreKindConfig)
	if err != nil || expected != dir {
		return fmt.Errorf("cannot identify protected session config store")
	}
	views := filepath.Join(config.HostProjectToolDir(home, holder.ProjectHash, holder.Tool), "auth-views")
	if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), views, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(views)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), holder.ID+"-") || strings.HasPrefix(entry.Name(), "."+holder.ID+"-") {
			if err := os.RemoveAll(filepath.Join(views, entry.Name())); err != nil {
				return err
			}
		}
	}
	if holder.Ephemeral {
		native, err := ResolveDir(home, backend.StoreKey{Owner: holder.Tool, Suffix: "ephemeral-" + key.Suffix}, backend.StoreKindAuth)
		if err != nil {
			return err
		}
		return WithLock(home, native, func() error { return os.RemoveAll(native) })
	}
	return nil
}
