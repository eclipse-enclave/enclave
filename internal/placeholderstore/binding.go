// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package placeholderstore projects native credentials into private,
// per-session views. Native files remain the authoritative credential store.
package placeholderstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"enclave/internal/auth"
	"enclave/internal/model"
	"enclave/internal/util"
)

const BindingsFile = "bindings.json"
const maxFileBytes = 1 << 20

// Bindings contains only placeholder identifiers, never real credential values.
type Bindings struct {
	Schema   int       `json:"schema"`
	Bindings []Binding `json:"bindings"`
}

type Binding struct {
	Provider    string `json:"provider"`
	File        string `json:"file"`
	Pointer     string `json:"pointer"`
	Kind        string `json:"kind"`
	Placeholder string `json:"placeholder,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

func (b Binding) Matches(value string) bool {
	if b.Signature != "" {
		parts := strings.Split(value, ".")
		return len(parts) == 3 && parts[2] == b.Signature
	}
	return value == b.Placeholder && value != ""
}

func (b Bindings) Claims(value string) bool {
	for _, binding := range b.Bindings {
		if binding.Matches(value) {
			return true
		}
	}
	return false
}

func LoadBindings(dir string) (Bindings, error) {
	b := Bindings{Schema: 1, Bindings: []Binding{}}
	root, err := openStoreRoot(dir)
	if err != nil {
		return b, err
	}
	defer func() { _ = root.Close() }()
	data, err := readRegular(root, BindingsFile)
	if errors.Is(err, fs.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSONValue(decoder, 0); err != nil {
		return b, fmt.Errorf("invalid native credential bindings")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	b = Bindings{}
	if err := decoder.Decode(&b); err != nil || b.Schema != 1 || b.Bindings == nil {
		return b, fmt.Errorf("invalid native credential bindings")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return b, fmt.Errorf("invalid native credential bindings")
	}
	seen := map[string]bool{}
	for _, binding := range b.Bindings {
		key := binding.Provider + "\x00" + binding.File + "\x00" + binding.Pointer
		_, pointerErr := model.AuthPointerSegments(binding.Pointer)
		value := binding.Placeholder
		if binding.Signature != "" {
			value = binding.Signature
		}
		if binding.Provider == "" || !slices.Contains([]string{"access", "refresh", "id", "apiKey"}, binding.Kind) || !model.ValidAuthFilePath(binding.File) || pointerErr != nil || seen[key] || (binding.Placeholder == "") == (binding.Signature == "") || !strings.Contains(value, model.SecretPlaceholderMarker) {
			return b, fmt.Errorf("invalid native credential binding")
		}
		seen[key] = true
	}
	return b, nil
}

// SaveBindings is called while the caller holds the real store's lock.
func SaveBindings(dir string, bindings Bindings) error {
	data, err := json.Marshal(bindings)
	if err != nil {
		return err
	}
	if err := util.WriteFileAtomic(filepath.Join(dir, BindingsFile), data, 0o600); err != nil {
		return err
	}
	return util.SyncDir(dir)
}

func (b *Bindings) field(provider, file, pointer, kind string, shape *model.SecretPlaceholderConfig, real, apiPlaceholder string) (Binding, error) {
	if shape != nil && shape.Shape == "jwt" && len(strings.Split(real, ".")) != 3 {
		return Binding{}, fmt.Errorf("native credential is not JWT-shaped")
	}
	for _, binding := range b.Bindings {
		if binding.Provider == provider && binding.File == file && binding.Pointer == pointer {
			if binding.Kind != kind || (binding.Signature != "") != (shape != nil && shape.Shape == "jwt") {
				return Binding{}, fmt.Errorf("native credential binding changed; reset the identity")
			}
			return binding, nil
		}
	}
	binding := Binding{Provider: provider, File: file, Pointer: pointer, Kind: kind}
	var err error
	switch {
	case kind == "apiKey":
		if !strings.Contains(apiPlaceholder, model.SecretPlaceholderMarker) {
			return binding, fmt.Errorf("native API key requires a recognizable env placeholder")
		}
		binding.Placeholder = apiPlaceholder
	case shape != nil && shape.Shape == "jwt":
		binding.Signature, err = auth.NewPlaceholderResolver().ResolvePlaceholder(pointer, &model.SecretPlaceholderConfig{Prefix: model.SecretPlaceholderMarker, Random: model.SecretPlaceholderRandom{Encoding: "base64url", Length: 28}})
	default:
		if shape != nil {
			copy := *shape
			copy.Prefix += model.SecretPlaceholderMarker
			if copy.Random.Encoding == "base64url" {
				copy.Random.Length -= len(model.SecretPlaceholderMarker)
			}
			shape = &copy
		}
		binding.Placeholder, err = auth.NewPlaceholderResolver().ResolvePlaceholder(pointer, shape)
	}
	if err != nil {
		return binding, err
	}
	b.Bindings = append(b.Bindings, binding)
	return binding, nil
}

func (b Binding) project(real string) string {
	if b.Signature == "" {
		return b.Placeholder
	}
	parts := strings.Split(real, ".")
	return parts[0] + "." + parts[1] + "." + b.Signature
}

// readRegular rejects symlinks in every component and verifies the opened inode.
func readRegular(root *os.Root, name string) ([]byte, error) {
	if !model.ValidAuthFilePath(name) {
		return nil, fmt.Errorf("invalid native credential file %q", name)
	}
	parts := strings.Split(name, "/")
	for i := 1; i <= len(parts); i++ {
		info, err := root.Lstat(strings.Join(parts[:i], "/"))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts) && !info.IsDir()) || (i == len(parts) && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("native credential file %q is not a regular confined file", name)
		}
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > maxFileBytes {
		return nil, fmt.Errorf("native credential file %q changed or exceeds its size limit", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("native credential file %q exceeds its size limit", name)
	}
	return data, err
}

func openStoreRoot(dir string) (*os.Root, error) {
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("native store root is not a regular directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, fmt.Errorf("native store root changed")
	}
	return root, nil
}
