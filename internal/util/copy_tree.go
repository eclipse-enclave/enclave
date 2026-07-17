// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"errors"
	"os"
	"path/filepath"
)

// TreeFileCopyFunc copies a non-directory source entry into dst using the
// provided file mode permissions.
type TreeFileCopyFunc func(src string, dst string, mode os.FileMode) error

// CopyTree recursively copies src into dst.
// Directory structure and permissions are recreated via os.Stat/os.MkdirAll,
// and file copying is delegated to copyFile.
func CopyTree(src string, dst string, copyFile TreeFileCopyFunc) error {
	if copyFile == nil {
		return errors.New("copy file function is required")
	}
	return copyTree(src, dst, copyFile)
}

func copyTree(src string, dst string, copyFile TreeFileCopyFunc) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			childSrc := filepath.Join(src, entry.Name())
			childDst := filepath.Join(dst, entry.Name())
			if err := copyTree(childSrc, childDst, copyFile); err != nil {
				return err
			}
		}
		return nil
	}
	return copyFile(src, dst, info.Mode().Perm())
}
