// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/config"
)

// Overlay validates the candidate before clearing the live directory. The
// directory inode stays stable for existing bind mounts. Once publication
// starts, any error retains a complete backup and reports its recovery path.
func Overlay(targetDir, sourceDir string, preserve []string) (result error) {
	target, err := OpenFiles(targetDir)
	if err != nil {
		return err
	}
	defer func() { _ = target.Close() }()
	source, err := OpenFiles(sourceDir)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	recovery, err := os.MkdirTemp("", "enclave-config-recovery-")
	if err != nil {
		return err
	}
	publishing := false
	defer func() {
		if result != nil && publishing {
			result = fmt.Errorf("%w; original config recoverable at %s", result, filepath.Join(recovery, "backup"))
		} else {
			_ = os.RemoveAll(recovery)
		}
	}()
	work, err := OpenFiles(recovery)
	if err != nil {
		return err
	}
	defer func() { _ = work.Close() }()
	if err := work.Mkdir("backup", 0o700); err != nil {
		return err
	}
	if err := work.Mkdir("candidate", 0o700); err != nil {
		return err
	}
	if err := Copy(target, ".", work, "backup", 0); err != nil {
		return err
	}
	if err := Copy(source, ".", work, "candidate", 0); err != nil {
		return err
	}
	backup, err := work.OpenRoot("backup")
	if err != nil {
		return err
	}
	defer func() { _ = backup.Close() }()
	original := &Files{Root: backup, name: filepath.Join(recovery, "backup")}
	var preserved []string
	err = fs.WalkDir(original.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		for _, pattern := range preserve {
			if !config.HostConfigPathMatches(name, pattern) {
				continue
			}
			dest := filepath.Join("candidate", name)
			// Preserved state takes precedence over generated state, including type changes.
			if err := work.RemoveAll(dest); err != nil {
				return err
			}
			if err := work.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			if err := Copy(original, name, work, dest, 0); err != nil {
				return err
			}
			preserved = append(preserved, name)
			if entry.IsDir() {
				return fs.SkipDir
			}
			break
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Preserve ancestor directory metadata for nested selections.
	err = fs.WalkDir(original.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." || !entry.IsDir() {
			return nil
		}
		for _, path := range preserved {
			if strings.HasPrefix(path, name+"/") {
				info, err := original.Lstat(name)
				if err != nil {
					return err
				}
				if err := work.Chmod(filepath.Join("candidate", name), info.Mode().Perm()); err != nil {
					return err
				}
				if err := work.Chtimes(filepath.Join("candidate", name), info.ModTime()); err != nil {
					return err
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	info, err := original.Stat(".")
	if err != nil {
		return err
	}
	if err := work.Chmod("candidate", info.Mode().Perm()); err != nil {
		return err
	}
	publishing = true
	entries, err := target.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := target.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	return Copy(work, "candidate", target, ".", 0)
}
