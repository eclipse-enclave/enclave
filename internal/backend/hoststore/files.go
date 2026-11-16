// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"enclave/internal/backend"
	"enclave/internal/config"
)

// Files pins a directory inode. All descendant operations are root-relative;
// preflight symlink checks alone cannot contain a concurrent container writer.
type Files struct {
	*os.Root
	name string
}

// Name is the logical directory supplied by our constructors. os.Root.Name
// for OpenRoot children differs between Go 1.24 and newer toolchains.
// Filesystem authority always stays on the opened descriptor, not this label.
func (r *Files) Name() string { return r.name }

func OpenFiles(dir string) (*Files, error) {
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	root, err := parent.OpenRoot(filepath.Base(dir))
	if err != nil {
		return nil, err
	}
	return &Files{Root: root, name: filepath.Clean(dir)}, nil
}

func (r *Files) MkdirAll(name string, mode fs.FileMode) error {
	if name == "." {
		return nil
	}
	if !filepath.IsLocal(name) {
		return fmt.Errorf("invalid relative path %q", name)
	}
	if err := r.MkdirAll(filepath.Dir(name), mode); err != nil {
		return err
	}
	err := r.Mkdir(name, mode)
	if errors.Is(err, os.ErrExist) {
		info, statErr := r.Lstat(name)
		if statErr != nil {
			return statErr
		}
		if info.IsDir() {
			return nil
		}
	}
	return err
}

func (r *Files) RemoveAll(name string) error {
	info, err := r.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		child, err := r.OpenRoot(name)
		if err != nil {
			return err
		}
		nested := &Files{Root: child, name: filepath.Join(r.name, name)}
		entries, err := nested.ReadDir(".")
		if err == nil {
			for _, entry := range entries {
				if err = nested.RemoveAll(entry.Name()); err != nil {
					break
				}
			}
		}
		_ = child.Close()
		if err != nil {
			return err
		}
	}
	return r.Remove(name)
}

// Copy copies nodes without dereferencing symlinks in either tree.
func Copy(src *Files, from string, dst *Files, to string, mode fs.FileMode) error {
	return copyNode(src, from, dst, to, mode, nil)
}

// copyNode is Copy, except that a special node (FIFO, socket, device), a
// regular file, or a directory the caller may not read is left out instead of
// failing when droppable reports true for its source path.
func copyNode(src *Files, from string, dst *Files, to string, mode fs.FileMode, droppable func(name string, dir bool) bool) error {
	if info, err := dst.Lstat(to); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlinked copy destination %s", to)
	}
	info, err := src.Lstat(from)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		link, err := src.Readlink(from)
		if err != nil {
			return err
		}
		return dst.Symlink(link, to)
	case info.IsDir():
		entries, err := src.ReadDir(from)
		if errors.Is(err, fs.ErrPermission) && droppable != nil && droppable(from, true) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := dst.MkdirAll(to, info.Mode().Perm()); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyNode(src, filepath.Join(from, entry.Name()), dst, filepath.Join(to, entry.Name()), mode, droppable); err != nil {
				return err
			}
		}
	default:
		if !info.Mode().IsRegular() {
			if droppable != nil && droppable(from, false) {
				return nil
			}
			return fmt.Errorf("unsupported store node %s", from)
		}
		data, err := src.ReadFile(from)
		if errors.Is(err, fs.ErrPermission) && droppable != nil && droppable(from, false) {
			return nil
		}
		if err != nil {
			return err
		}
		if mode == 0 {
			mode = info.Mode().Perm()
		}
		if err := dst.WriteFile(to, data, mode); err != nil {
			return err
		}
	}
	if info.IsDir() {
		if err := dst.Chmod(to, info.Mode().Perm()); err != nil {
			return err
		}
	}
	if to == "." {
		return nil
	}
	return dst.Chtimes(to, info.ModTime())
}

func OpenStore(home string, key backend.StoreKey, kind backend.StoreKind, create bool) (*Files, error) {
	dir, err := ResolveDir(home, key, kind)
	if err != nil {
		return nil, err
	}
	stateDir := config.HostStateRootDir(home)
	if create {
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return nil, err
		}
	}
	state, err := os.OpenRoot(stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = state.Close() }()
	rel, err := filepath.Rel(stateDir, dir)
	if err != nil {
		return nil, err
	}
	if create {
		if err := (&Files{Root: state, name: stateDir}).MkdirAll(rel, 0o700); err != nil {
			return nil, err
		}
	}
	root, err := state.OpenRoot(rel)
	if err != nil {
		return nil, err
	}
	return &Files{Root: root, name: filepath.Clean(dir)}, nil
}
