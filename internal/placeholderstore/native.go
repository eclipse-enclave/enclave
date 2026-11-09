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
	"path"
	"path/filepath"
	"strings"

	"enclave/internal/model"
)

// NativeOperation is rechecked by the entrypoint before it touches a shared path.
type NativeOperation struct {
	File   string `json:"file"`
	Verb   string `json:"verb"`
	Digest string `json:"digest,omitempty"`
}

// ClassifyNative never modifies the config store. The caller holds its store lock
// and supplies a current, complete holder/identity snapshot. Unknown engine or
// identity state must fail before calling this function, not masquerade as idle.
func ClassifyNative(configDir, expectedAuthDir string, profile model.Profile, current Bindings, other []Bindings, idle bool) ([]NativeOperation, error) {
	root, err := openStoreRoot(configDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var operations []NativeOperation
	for _, provider := range profile.Providers {
		for _, file := range provider.AuthSecrets {
			if !model.ValidAuthFilePath(file.File) {
				return nil, fmt.Errorf("invalid native path")
			}
			parts := strings.Split(file.File, "/")
			missingParent := false
			for i := 1; i < len(parts); i++ {
				info, err := root.Lstat(strings.Join(parts[:i], "/"))
				if errors.Is(err, fs.ErrNotExist) {
					missingParent = true
					break
				}
				if err != nil {
					return nil, err
				}
				if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return nil, nativeRefusal(file.File)
				}
			}
			op := NativeOperation{File: file.File, Verb: "create"}
			if missingParent {
				operations = append(operations, op)
				continue
			}
			info, err := root.Lstat(file.File)
			if errors.Is(err, fs.ErrNotExist) {
				operations = append(operations, op)
				continue
			}
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(filepath.Join(configDir, filepath.FromSlash(file.File)))
				if err != nil || target != path.Join(expectedAuthDir, file.File) {
					return nil, nativeRefusal(file.File)
				}
				op.Verb = "keep"
			} else {
				data, err := readRegular(root, file.File)
				if err != nil {
					return nil, nativeRefusal(file.File)
				}
				digest := sha256.Sum256(data)
				op.Digest = hex.EncodeToString(digest[:])
				values, err := nativeValues(file, data)
				if err != nil {
					return nil, nativeRefusal(file.File)
				}
				unbound := false
				for pointer, value := range values {
					if !strings.Contains(value, model.SecretPlaceholderMarker) {
						return nil, nativeRefusal(file.File)
					}
					bound := false
					for _, binding := range current.Bindings {
						if binding.Provider == provider.Name && binding.File == file.File && binding.Pointer == pointer && binding.Matches(value) {
							bound = true
							break
						}
					}
					if bound {
						continue
					}
					unbound = true
					for _, identity := range append([]Bindings{current}, other...) {
						if identity.Claims(value) {
							return nil, nativeRefusal(file.File)
						}
					}
				}
				op.Verb = "keep"
				if idle && len(values) > 0 {
					op.Verb = "link"
				} else if unbound {
					return nil, nativeRefusal(file.File)
				}
			}
			operations = append(operations, op)
		}
	}
	return operations, nil
}

func nativeRefusal(file string) error {
	return fmt.Errorf("refusing protected native path %s: end the ordinary session and let auth reconciliation link it, or use enclave auth reset for a crash leftover", file)
}

func nativeValues(file model.AuthFileSecret, data []byte) (map[string]string, error) {
	doc, pointers, err := decodeNative(file.Codec, data)
	if err != nil {
		return nil, err
	}
	declared := map[string]bool{}
	for _, field := range file.Fields {
		declared[field.Pointer] = true
	}
	for _, pointer := range pointers {
		if !declared[pointer] {
			return nil, fmt.Errorf("undeclared native credential field")
		}
	}
	values := map[string]string{}
	for _, field := range file.Fields {
		parts, err := model.AuthPointerSegments(field.Pointer)
		if err != nil {
			return nil, err
		}
		if err := visitFields(doc, parts, "", func(pointer, value string, _ func(string)) error { values[pointer] = value; return nil }); err != nil {
			return nil, err
		}
	}
	return values, nil
}
