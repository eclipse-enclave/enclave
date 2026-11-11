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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"enclave/internal/model"
)

const BindingsFile = "bindings.json"
const maxFileBytes = 1 << 20

// Bindings contains only placeholder identifiers, never real credential values.
type Bindings struct {
	Schema   int       `json:"schema"`
	Bindings []Binding `json:"bindings"`
}

type Binding struct {
	Shape       string `json:"shape,omitempty"`
	Provider    string `json:"provider"`
	File        string `json:"file"`
	Pointer     string `json:"pointer"`
	Kind        string `json:"kind"`
	Placeholder string `json:"placeholder,omitempty"`
}

func (b Binding) Matches(value string) bool {
	if b.Shape == "jwt" {
		parts := strings.Split(value, ".")
		return len(parts) == 3 && parts[2] == b.Placeholder
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
		if (binding.Shape != "" && binding.Shape != "jwt") || (binding.Shape == "jwt" && ((binding.Kind != "access" && binding.Kind != "id") || !model.MatchesSecretPlaceholder(binding.Placeholder, nil))) || binding.Provider == "" || !slices.Contains([]string{"access", "refresh", "id", "apiKey"}, binding.Kind) || !model.ValidAuthFilePath(binding.File) || pointerErr != nil || seen[key] || binding.Placeholder == "" || !strings.Contains(value, model.SecretPlaceholderMarker) {
			return b, fmt.Errorf("invalid native credential binding")
		}
		seen[key] = true
	}
	return b, nil
}

// SaveBindings is called while the caller holds the real store's lock.
func SaveBindings(dir string, bindings Bindings) error {
	data, _ := json.Marshal(bindings)
	return WriteNative(dir, BindingsFile, data)
}

func credentialAlias(provider, pointer string) string {
	if provider == "github-copilot" && (pointer == "/github-copilot/access" || pointer == "/github-copilot/refresh") {
		return "/github-copilot"
	}
	if provider == "github" && strings.HasPrefix(pointer, "/authTokens/") {
		return strings.TrimSuffix(strings.TrimSuffix(pointer, "/token"), ":github")
	}
	return pointer
}

// Bind returns the stable opaque placeholder for a declared credential field.
func (b *Bindings) Bind(provider, file, pointer, kind string, shape *model.SecretPlaceholderConfig, apiPlaceholder string) (Binding, error) {
	for _, binding := range b.Bindings {
		if binding.Provider == provider && binding.File == file && binding.Pointer == pointer {
			if binding.Kind != kind || (shape != nil && binding.Shape != shape.Shape) || (shape == nil && binding.Shape != "") {
				return Binding{}, fmt.Errorf("native credential binding changed; reset the identity")
			}
			return binding, nil
		}
	}
	binding := Binding{Provider: provider, File: file, Pointer: pointer, Kind: kind}
	for _, known := range b.Bindings {
		if known.Provider == provider && known.File == file && credentialAlias(provider, known.Pointer) == credentialAlias(provider, pointer) {
			binding.Placeholder = known.Placeholder
			break
		}
	}
	var err error
	if kind == "apiKey" {
		if !strings.Contains(apiPlaceholder, model.SecretPlaceholderMarker) {
			return binding, fmt.Errorf("native API key requires a recognizable env placeholder")
		}
		binding.Placeholder = apiPlaceholder
	} else if binding.Placeholder == "" {
		if shape != nil && shape.Shape == "jwt" {
			binding.Shape, shape = "jwt", nil
		}
		if shape != nil {
			copy := *shape
			copy.Prefix += model.SecretPlaceholderMarker
			if copy.Random.Encoding == "base64url" {
				copy.Random.Length -= len(model.SecretPlaceholderMarker)
			}
			shape = &copy
		}
		binding.Placeholder, err = model.NewSecretPlaceholder(shape)
	}
	if err != nil {
		return binding, err
	}
	b.Bindings = append(b.Bindings, binding)
	return binding, nil
}

// readRegular rejects symlinks in every component and verifies the opened inode.
func readRegular(root *os.Root, name string) ([]byte, error) {
	before, err := nativePathInfo(root, name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("native credential file %q is not a regular confined file", name)
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

// nativePathInfo is shared by classification and regular-file reads. Only the
// leaf may be a symlink; classification checks its exact target without following.
func nativePathInfo(root *os.Root, name string) (os.FileInfo, error) {
	if !model.ValidAuthFilePath(name) {
		return nil, fmt.Errorf("invalid native credential file %q", name)
	}
	parts := strings.Split(name, "/")
	var info os.FileInfo
	for i := 1; i <= len(parts); i++ {
		var err error
		info, err = root.Lstat(strings.Join(parts[:i], "/"))
		if err != nil {
			return nil, err
		}
		if i < len(parts) && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return nil, fmt.Errorf("native credential file %q has a symlinked or non-directory parent", name)
		}
	}
	return info, nil
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

// JWTClaims leaves validation of provider signatures to the upstream provider.
func JWTClaims(value string) (map[string]any, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return nil, fmt.Errorf("invalid native JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid native JWT claims")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var claims map[string]any
	if err := decoder.Decode(&claims); err != nil || claims == nil {
		return nil, fmt.Errorf("invalid native JWT claims")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing native JWT claims")
	}
	return claims, nil
}

// Render updates readable JWT claims while retaining the identity's bound signature.
func (b Binding) Render(value string) (string, error) {
	if b.Shape != "jwt" {
		return b.Placeholder, nil
	}
	if _, err := JWTClaims(value); err != nil {
		return "", err
	}
	parts := strings.Split(value, ".")
	return parts[0] + "." + parts[1] + "." + b.Placeholder, nil
}

func bindingProvider(provider string, file model.AuthFileSecret, pointer string) string {
	if file.Codec == "provider-map" {
		parts, _ := model.AuthPointerSegments(pointer)
		return parts[0]
	}
	return provider
}
