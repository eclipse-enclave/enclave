// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"enclave/internal/model"
)

// NativeFile is a confined canonical snapshot. Unknown native keys are preserved;
// only projection is allowed to strip them.
type NativeFile struct {
	Document    map[string]any
	Values      map[string]string
	Fingerprint string
}

func ReadNative(dir string, file model.AuthFileSecret) (NativeFile, error) {
	root, err := openStoreRoot(dir)
	if err != nil {
		return NativeFile{}, err
	}
	defer func() { _ = root.Close() }()
	data, err := readRegular(root, file.File)
	if err != nil {
		return NativeFile{}, err
	}
	doc, values, err := decodeAuthFile(file, data, canonicalNative)
	if err != nil {
		return NativeFile{}, err
	}
	fingerprint := sha256.Sum256(data)
	result := NativeFile{Document: doc, Values: map[string]string{}, Fingerprint: hex.EncodeToString(fingerprint[:])}
	for _, value := range values {
		result.Values[value.field.Pointer] = value.value
	}
	return result, nil
}

// WriteNative performs a durable rename without changing canonical ownership or
// permissions. The process must already be the store owner; no privilege fallback.
// The caller holds the canonical hoststore lock.
func WriteNative(dir, name string, data []byte) error {
	if len(data) > maxFileBytes {
		return fmt.Errorf("native credential write exceeds its size limit")
	}
	root, err := openStoreRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	before, err := nativePathInfo(root, name)
	mode := os.FileMode(0o600)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if before != nil {
		if !before.Mode().IsRegular() {
			return fmt.Errorf("native target must be regular")
		}
		mode = before.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	} else {
		before, err = root.Stat(".")
		if err != nil {
			return err
		}
	}
	parent := filepath.Join(dir, filepath.Dir(name))
	tmp, err := os.CreateTemp(parent, ".native-*")
	if err != nil {
		return err
	}
	temp := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(temp) }()
	if err := preserveOwner(tmp, before); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, filepath.Join(dir, name)); err != nil {
		return err
	}
	// Unlike the general-purpose helper, a failure to sync this directory is not
	// tolerated: an exchange intent must be durable before upstream dispatch.
	parentDir, err := root.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer func() { _ = parentDir.Close() }()
	return parentDir.Sync()
}
