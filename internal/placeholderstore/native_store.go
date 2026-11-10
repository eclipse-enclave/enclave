// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"crypto/rand"
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
	if !model.ValidAuthFilePath(name) {
		return fmt.Errorf("invalid native credential path")
	}
	parentName := filepath.Dir(name)
	parentInfo, err := root.Stat(".")
	if parentName != "." {
		parentInfo, err = nativePathInfo(root, parentName)
	}
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("native credential parent is not a regular directory")
	}
	parent, err := root.OpenRoot(parentName)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	parentDir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = parentDir.Close() }()
	anchored, err := parentDir.Stat()
	if err != nil || !os.SameFile(parentInfo, anchored) {
		return fmt.Errorf("native credential parent changed")
	}
	leaf := filepath.Base(name)
	before, err := parent.Lstat(leaf)
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
	temp := ".native-" + rand.Text()
	tmp, err := parent.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close(); _ = parent.Remove(temp) }()
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
	if err := renameNative(parentDir, temp, leaf); err != nil {
		return err
	}
	// The anchored directory is also the durability boundary. Do not swallow
	// sync errors: callers must not treat an uncommitted rename as success.
	return parentDir.Sync()
}
