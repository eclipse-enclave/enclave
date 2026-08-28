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
	"io/fs"
	"os"
	"path/filepath"

	"enclave/internal/util"
)

// copyLimits bounds what a remote extension may write onto the host.
type copyLimits struct {
	MaxFiles int
	MaxBytes int64
}

// defaultLimits are generous for real extensions (scripts, specs, templates,
// skills) and tight enough that a mistargeted source cannot fill the disk.
func defaultLimits() copyLimits {
	return copyLimits{MaxFiles: 1000, MaxBytes: 10 << 20}
}

// copyExtensionTree copies srcDir to dstDir under strict rules: regular files
// and directories only, no symlinks or other special entries, nothing escaping
// the destination, .git skipped, modes normalized, and the caps enforced.
// Extension content is untrusted, so every rejection names the offending path.
// On failure, partially copied files may remain in dstDir; callers must discard
// the destination wholesale if the copy fails.
func copyExtensionTree(srcDir string, dstDir string, limits copyLimits) error {
	// Lstat, not Stat: a symlinked source root must not be treated as a real
	// directory just because it resolves to one. Every path check below this
	// point (HasPathTraversal, PathStrictlyWithin) assumes srcDir itself is a
	// genuine directory inside the checkout, not a link elsewhere.
	info, err := os.Lstat(srcDir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", srcDir)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", srcDir)
	}
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return err
	}

	files := 0
	var totalBytes int64
	return filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == srcDir {
			return nil
		}
		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil {
			return relErr
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			// A gitlink file (submodule marker) at this name is not a real
			// extension file either; skip it the same as the directory form.
			return nil
		}
		if util.HasPathTraversal(rel) {
			return fmt.Errorf("refusing %s: path escapes the extension directory", rel)
		}
		target := filepath.Join(dstDir, rel)
		if !util.PathStrictlyWithin(dstDir, target) {
			return fmt.Errorf("refusing %s: resolves outside %s", rel, dstDir)
		}

		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("refusing %s: only regular files and directories are installed (found %s)", rel, entry.Type())
		}

		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		files++
		totalBytes += fileInfo.Size()
		if files > limits.MaxFiles {
			return fmt.Errorf("extension has more than %d files", limits.MaxFiles)
		}
		if totalBytes > limits.MaxBytes {
			return fmt.Errorf("extension exceeds %d bytes", limits.MaxBytes)
		}
		return copySanitizedFile(path, target, fileInfo.Mode())
	})
}

func copySanitizedFile(src string, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	// #nosec G304 -- src comes from walking the caller's staging directory.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	perm := os.FileMode(0o640)
	if mode.Perm()&0o111 != 0 {
		// Extension scripts must stay executable: install.sh and entrypoint.d
		// hooks are executed inside the image.
		perm = 0o750
	}
	// #nosec G304 -- dst is constructed and checked by the caller with PathStrictlyWithin.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
