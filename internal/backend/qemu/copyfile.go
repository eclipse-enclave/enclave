// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func removeCopyDestination(dst string) error {
	if _, err := os.Lstat(dst); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.RemoveAll(dst)
}

func copyFile(src string, dst string, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	in, err := os.Open(src) // #nosec G304 -- caller validates source.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := removeCopyDestination(dst); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode.Perm()) // #nosec G304 -- dst is constrained by caller.
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode.Perm())
}
