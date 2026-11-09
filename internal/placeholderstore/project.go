// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/model"
)

type View struct {
	Dir      string
	Bindings Bindings
	Releases []model.SecretReleaseEntry
}

// Project reads only declared native files and atomically publishes a sanitized
// view. The caller holds the real store lock for the entire operation.
func Project(realDir, viewsDir, session string, profile model.Profile, configured []model.SecretReleaseEntry) (View, error) {
	var view View
	if session == "" || filepath.Base(session) != session || session == "." || session == ".." {
		return view, fmt.Errorf("invalid placeholder session")
	}
	bindings, err := LoadBindings(realDir)
	if err != nil {
		return view, err
	}
	root, err := openStoreRoot(realDir)
	if err != nil {
		return view, err
	}
	defer func() { _ = root.Close() }()
	if err := os.MkdirAll(viewsDir, 0o700); err != nil {
		return view, err
	}
	info, err := os.Lstat(viewsDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return view, fmt.Errorf("placeholder parent must be a private directory")
	}
	stage, err := os.MkdirTemp(viewsDir, "."+session+"-")
	if err != nil {
		return view, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	type projectedFile struct {
		name     string
		document map[string]any
	}
	var files []projectedFile
	var credentials []string
	for _, entry := range configured {
		if entry.Value != "" {
			credentials = append(credentials, entry.Value)
		}
	}
	for _, provider := range profile.Providers {
		for _, file := range provider.AuthSecrets {
			data, err := readRegular(root, file.File)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return View{}, fmt.Errorf("read native credentials %s: %w", file.File, err)
			}
			document, releases, values, err := ProjectFile(provider.Name, file, data, &bindings, profile.Secrets, configured)
			if err != nil {
				return View{}, fmt.Errorf("project native credentials %s: %w", file.File, err)
			}
			files = append(files, projectedFile{name: file.File, document: document})
			credentials = append(credentials, values...)
			view.Releases = append(view.Releases, releases...)
		}
	}
	// Every known credential must be absent from every projected document,
	// regardless of which file declared it or the declaration order.
	for _, file := range files {
		for _, credential := range credentials {
			if containsCredential(file.document, credential) {
				return View{}, fmt.Errorf("project native credentials %s: credential value remains outside its declared field", file.name)
			}
		}
		projected, err := json.Marshal(file.document)
		if err != nil {
			return View{}, err
		}
		target := filepath.Join(stage, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return View{}, err
		}
		if err := os.WriteFile(target, projected, 0o600); err != nil {
			return View{}, err
		}
	}
	if err := SaveBindings(realDir, bindings); err != nil {
		return View{}, err
	}
	view.Dir = filepath.Join(viewsDir, strings.TrimPrefix(filepath.Base(stage), "."))
	if err := os.Rename(stage, view.Dir); err != nil {
		return View{}, err
	}
	view.Bindings = bindings
	return view, nil
}

// ProjectFile renders one native document without writing it or the bindings.
// Callers check all known values across their complete output before publishing.
func ProjectFile(provider string, file model.AuthFileSecret, data []byte, bindings *Bindings, secrets map[string]model.SecretConfig, configured []model.SecretReleaseEntry) (map[string]any, []model.SecretReleaseEntry, []string, error) {
	doc, values, err := decodeAuthFile(file, data, projectedView)
	if err != nil {
		return nil, nil, nil, err
	}
	var rawValues []string
	var releases []model.SecretReleaseEntry
	for _, native := range values {
		field, pointer, value := native.field, native.field.Pointer, native.value
		var release model.SecretReleaseEntry
		if field.Kind == "apiKey" {
			for _, entry := range configured {
				if entry.SecretID == field.Release {
					release = entry
					break
				}
			}
			if release.Placeholder == "" {
				return nil, nil, nil, fmt.Errorf("credential field %s requires configured secret %s", pointer, field.Release)
			}
		} else if strings.Contains(value, model.SecretPlaceholderMarker) {
			return nil, nil, nil, fmt.Errorf("real native credential field %s contains a placeholder", pointer)
		}
		binding, err := bindings.Bind(provider, file.File, pointer, field.Kind, field.Placeholder, release.Placeholder)
		if err != nil {
			return nil, nil, nil, err
		}
		native.replace(binding.Placeholder)
		if !strings.Contains(value, model.SecretPlaceholderMarker) {
			rawValues = append(rawValues, value)
		}
		if field.Release != "" {
			if field.Kind != "apiKey" {
				secret := secrets[field.Release]
				if secret.Release == nil || secret.Release.HTTP == nil {
					return nil, nil, nil, fmt.Errorf("credential field %s has no HTTP release", pointer)
				}
				rule := secret.Release.HTTP
				release = model.SecretReleaseEntry{SecretID: field.Release, Value: value, Hosts: rule.Hosts, Header: rule.Header, Format: rule.Format}
			}
			release.Placeholder = binding.Placeholder
			releases = append(releases, release)
		}
	}
	return doc, releases, rawValues, nil
}

func containsCredential(value any, credential string) bool {
	switch node := value.(type) {
	case string:
		return strings.Contains(node, credential)
	case map[string]any:
		for key, child := range node {
			if strings.Contains(key, credential) || containsCredential(child, credential) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if containsCredential(child, credential) {
				return true
			}
		}
	}
	return false
}
