// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"enclave/internal/model"
)

type harvestDocument struct {
	provider string
	file     model.AuthFileSecret
	document map[string]any
}

// Harvest merges only client metadata. The caller holds config then native locks.
// Invalid sources are skipped and reported, never copied into canonical files.
func Harvest(realDir, viewDir, configDir string, profile model.Profile) error {
	bindings, err := LoadBindings(realDir)
	if err != nil || len(bindings.Bindings) == 0 {
		return err
	}
	var files []harvestDocument
	var credentials []string
	for _, provider := range profile.Providers {
		for _, file := range provider.AuthSecrets {
			if file.CheckOnly {
				continue
			}
			native, err := ReadNative(realDir, file)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			for _, value := range native.Values {
				credentials = append(credentials, value)
			}
			files = append(files, harvestDocument{provider: provider.Name, file: file, document: native.Document})
		}
	}
	var warnings error
	view, err := readHarvestSource(viewDir, files, bindings, true)
	warnings = errors.Join(warnings, err)
	local, err := readHarvestSource(configDir, files, bindings, false)
	warnings = errors.Join(warnings, err)
	for _, file := range files {
		native := file.document
		if native == nil {
			native = map[string]any{}
		}
		before, _ := json.Marshal(native)
		for _, source := range append(view, local...) {
			if source.provider != file.provider || source.file.File != file.file.File {
				continue
			}
			metadata := source.document
			for _, field := range file.file.Fields {
				parts, _ := model.AuthPointerSegments(field.Pointer)
				if len(parts) == 1 {
					delete(metadata, parts[0])
				} else if object, ok := metadata[parts[0]].(map[string]any); ok {
					delete(object, parts[1])
				}
			}
			if file.file.Codec == "claude-credentials" {
				if oauth, ok := metadata["claudeAiOauth"].(map[string]any); ok {
					delete(oauth, "expiresAt")
					delete(oauth, "scopes")
				}
			}
			contaminated := ContainsCredential(metadata, model.SecretPlaceholderMarker)
			for _, credential := range credentials {
				contaminated = contaminated || ContainsCredential(metadata, credential)
			}
			if contaminated {
				warnings = errors.Join(warnings, fmt.Errorf("skipping secret-bearing metadata in %s", file.file.File))
				continue
			}
			mergeMetadata(native, metadata)
		}
		data, _ := json.Marshal(native)
		if bytes.Equal(before, data) {
			continue
		}
		warnings = errors.Join(warnings, WriteNative(realDir, file.file.File, data))
	}
	return warnings
}

// The private view is one identity-proven document set. A regular native-path
// sibling needs its own proof: no shared-path mutation occurs at session end.
func readHarvestSource(dir string, files []harvestDocument, bindings Bindings, whole bool) ([]harvestDocument, error) {
	root, err := openStoreRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var documents []harvestDocument
	var warnings error
	matched := false
	for _, file := range files {
		info, err := nativePathInfo(root, file.file.File)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil && !whole && !info.Mode().IsRegular() {
			continue
		}
		data, err := readRegular(root, file.file.File)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var values []nativeValue
		if err == nil {
			file.document, values, err = decodeAuthFile(file.file, data, projectedView)
		}
		count := 0
		if err == nil {
			for _, value := range values {
				bound := false
				for _, binding := range bindings.Bindings {
					if binding.Provider == file.provider && binding.File == file.file.File && binding.Pointer == value.field.Pointer && binding.Matches(value.value) {
						bound = true
						break
					}
				}
				if !bound {
					err = fmt.Errorf("unbound credential in harvest source %s", file.file.File)
					break
				}
				count++
			}
		}
		if err != nil {
			if whole {
				return nil, err
			}
			warnings = errors.Join(warnings, err)
			continue
		}
		if count > 0 {
			matched = true
		}
		if whole || count > 0 {
			documents = append(documents, file)
		}
	}
	if whole && !matched {
		return nil, warnings
	}
	return documents, warnings
}

func mergeMetadata(target, source map[string]any) {
	for key, value := range source {
		if object, ok := value.(map[string]any); ok {
			if len(object) == 0 {
				continue
			}
			child, ok := target[key].(map[string]any)
			if !ok {
				child = map[string]any{}
				target[key] = child
			}
			mergeMetadata(child, object)
		} else {
			target[key] = value
		}
	}
}
