// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package appassets

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
)

var embedded struct {
	sync.Mutex
	files fs.FS
	once  sync.Once
	key   string
	err   error
}

// Register installs the assets embedded by the repository-root package. The
// CLI imports that package for its initialization side effect, while binaries
// that only use internal packages (such as the gateway proxy) remain small.
func Register(files fs.FS) {
	if files == nil {
		panic("register nil embedded asset filesystem")
	}
	embedded.Lock()
	defer embedded.Unlock()
	if embedded.files != nil {
		panic("embedded asset filesystem registered more than once")
	}
	embedded.files = files
}

// Embedded returns the registered asset filesystem and its deterministic key.
func Embedded() (fs.FS, string, error) {
	embedded.Lock()
	files := embedded.files
	embedded.Unlock()
	if files == nil {
		return nil, "", fmt.Errorf("embedded assets are unavailable")
	}

	embedded.once.Do(func() {
		embedded.key, embedded.err = ContentHash(files)
	})
	return files, embedded.key, embedded.err
}

// ContentHash hashes the paths, normalized extraction modes, and contents of
// every embedded asset. Framing each field avoids ambiguous concatenations.
func ContentHash(files fs.FS) (string, error) {
	if files == nil {
		return "", fmt.Errorf("asset filesystem is nil")
	}

	h := sha256.New()
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}

		kind := byte('f')
		if entry.IsDir() {
			kind = 'd'
		} else if !entry.Type().IsRegular() {
			return fmt.Errorf("embedded asset %s is not a regular file or directory", name)
		}
		if err := writeHashField(h, []byte{kind}); err != nil {
			return err
		}
		if err := writeHashField(h, []byte(name)); err != nil {
			return err
		}
		mode := uint32(0o755)
		if !entry.IsDir() {
			mode = uint32(FileMode(name))
		}
		if err := binary.Write(h, binary.BigEndian, mode); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		file, err := files.Open(name)
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		return writeHashField(h, content)
	})
	if err != nil {
		return "", fmt.Errorf("hash embedded assets: %w", err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func writeHashField(w io.Writer, value []byte) error {
	if err := binary.Write(w, binary.BigEndian, uint64(len(value))); err != nil {
		return err
	}
	_, err := w.Write(value)
	return err
}

// FileMode returns the normalized mode used when extracting an asset. Embed
// files do not retain source modes, so executable runtime contracts are listed
// explicitly and all other files are read-only build inputs.
func FileMode(name string) fs.FileMode {
	name = path.Clean(strings.ReplaceAll(name, `\`, "/"))
	if name == "entrypoint.sh" || name == "gateway-entrypoint.sh" {
		return 0o755
	}
	if name == "runtime-assets/microvm/alpine/build-bundle.sh" || name == "runtime-assets/microvm/alpine/init" {
		return 0o755
	}
	if strings.HasPrefix(name, "runtime-assets/build-scripts/") {
		base := path.Base(name)
		if strings.HasSuffix(base, ".sh") || strings.Contains(name, "/bin/") {
			return 0o755
		}
	}
	if strings.HasPrefix(name, "extensions/") && path.Base(name) == "install.sh" {
		return 0o755
	}
	return 0o644
}
