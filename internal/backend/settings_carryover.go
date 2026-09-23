// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"

	"enclave/internal/logx"
	"enclave/internal/util"
)

// OverlayConfigSettings carries tool edits to the declared settings file over
// an otherwise destructive config-store overlay. The snapshot always contains
// the generated input, not the merged file that the tool receives.
func OverlayConfigSettings(storeDir string, spec ConfigOverlaySpec, overlay func() error) error {
	if spec.SettingsPath == "" || spec.BasePath == "" {
		return overlay()
	}
	storePath, err := settingsPath(storeDir, spec.SettingsPath)
	if err != nil {
		logx.Warnf("Cannot carry in-tool settings edits for %s: %v; using generated settings", spec.SettingsPath, err)
		storePath = ""
	}
	generatedPath, err := settingsPath(spec.SourceDir, spec.SettingsPath)
	if err != nil {
		return err
	}
	generated, generatedMode, generatedOK := readSettingsFile(generatedPath, "generated")
	base, _, baseOK := readSettingsFile(spec.BasePath, "snapshot")
	var store []byte
	var storeOK bool
	if storePath != "" {
		store, _, storeOK = readSettingsFile(storePath, "store")
	}

	var merged []byte
	if generatedOK && baseOK && storeOK {
		merged, err = mergeSettingsFile(base, store, generated, filepath.Ext(spec.SettingsPath))
		if err != nil {
			logx.Warnf("Cannot carry in-tool settings edits for %s: %v; using generated settings", spec.SettingsPath, err)
			merged = nil
		}
	}
	// Invalidate before touching the store. If a later step fails, the next
	// launch has no base and cannot mistake generated state for a tool edit.
	if err := os.Remove(spec.BasePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove settings snapshot %s: %w", spec.BasePath, err)
	}
	if err := overlay(); err != nil {
		return err
	}
	if merged != nil && !bytes.Equal(merged, generated) {
		storePath, err = settingsPath(storeDir, spec.SettingsPath)
		if err != nil {
			return err
		}
		info, statErr := os.Lstat(storePath)
		if statErr != nil {
			return fmt.Errorf("stat overlaid settings %s: %w", storePath, statErr)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("overlaid settings path %s is not a regular file", storePath)
		}
		if err := util.WriteFileAtomic(storePath, merged, generatedMode); err != nil {
			return fmt.Errorf("write merged settings %s: %w", storePath, err)
		}
	}
	if !generatedOK {
		return nil
	}
	snapshotErr := os.MkdirAll(filepath.Dir(spec.BasePath), 0o700)
	if snapshotErr == nil {
		snapshotErr = util.WriteFileAtomic(spec.BasePath, generated, 0o600)
	}
	if snapshotErr != nil {
		logx.Warnf("Cannot save settings snapshot %s: %v; in-tool edits may be lost on the next launch", spec.BasePath, snapshotErr)
	}
	return nil
}

// settingsPath rejects traversal and symlinked ancestors in either the old
// tool-writable store or the freshly generated source.
func settingsPath(root string, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || util.HasPathTraversal(relative) {
		return "", fmt.Errorf("invalid settings path %q", relative)
	}
	cleaned := filepath.Clean(relative)
	if cleaned == "." {
		return "", fmt.Errorf("invalid settings path %q", relative)
	}
	current := root
	for _, part := range strings.Split(filepath.Dir(cleaned), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("settings parent %s is not a directory", current)
		}
	}
	return filepath.Join(root, cleaned), nil
}

func readSettingsFile(path string, label string) ([]byte, os.FileMode, bool) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false
	}
	if err != nil || !info.Mode().IsRegular() {
		logx.Warnf("Cannot carry in-tool settings edits: %s settings file %s is not a readable regular file", label, path)
		return nil, 0, false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is a validated settings path in a managed store or generated tree.
	if err != nil {
		logx.Warnf("Cannot read %s settings file %s: %v", label, path, err)
		return nil, 0, false
	}
	return data, info.Mode().Perm(), true
}

func mergeSettingsFile(base []byte, store []byte, generated []byte, ext string) ([]byte, error) {
	decode := func(data []byte) (any, error) {
		switch strings.ToLower(ext) {
		case ".json":
			var value any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return nil, err
			}
			var extra any
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("extra JSON content: %v", err)
			}
			return value, nil
		case ".toml":
			var value map[string]any
			_, err := toml.Decode(string(data), &value)
			return value, err
		default:
			return nil, fmt.Errorf("unsupported settings format %q", ext)
		}
	}
	baseValue, err := decode(base)
	if err != nil {
		return nil, fmt.Errorf("parse settings snapshot: %w", err)
	}
	storeValue, err := decode(store)
	if err != nil {
		return nil, fmt.Errorf("parse store settings: %w", err)
	}
	generatedValue, err := decode(generated)
	if err != nil {
		return nil, fmt.Errorf("parse generated settings: %w", err)
	}
	if settingsEqual(baseValue, storeValue) {
		return generated, nil
	}
	merged, _ := mergeSettingsValue(baseValue, true, storeValue, true, generatedValue, true)
	if settingsEqual(merged, generatedValue) {
		return generated, nil
	}
	switch strings.ToLower(ext) {
	case ".json":
		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(merged); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case ".toml":
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(merged); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	default:
		return nil, fmt.Errorf("unsupported settings format %q", ext)
	}
}

func mergeSettingsValue(base any, hasBase bool, store any, hasStore bool, generated any, hasGenerated bool) (any, bool) {
	storeMap, storeIsMap := store.(map[string]any)
	generatedMap, generatedIsMap := generated.(map[string]any)
	baseMap, baseIsMap := base.(map[string]any)
	if hasStore && hasGenerated && storeIsMap && generatedIsMap && (baseIsMap || !hasBase) {
		keys := make(map[string]struct{}, len(baseMap)+len(storeMap)+len(generatedMap))
		for key := range baseMap {
			keys[key] = struct{}{}
		}
		for key := range storeMap {
			keys[key] = struct{}{}
		}
		for key := range generatedMap {
			keys[key] = struct{}{}
		}
		merged := make(map[string]any, len(keys))
		for key := range keys {
			baseChild, hasBaseChild := baseMap[key]
			storeChild, hasStoreChild := storeMap[key]
			generatedChild, hasGeneratedChild := generatedMap[key]
			value, exists := mergeSettingsValue(baseChild, hasBaseChild, storeChild, hasStoreChild, generatedChild, hasGeneratedChild)
			if exists {
				merged[key] = value
			}
		}
		return merged, true
	}
	if hasGenerated != hasBase || (hasGenerated && !settingsEqual(generated, base)) {
		return generated, hasGenerated
	}
	if hasStore != hasBase || (hasStore && !settingsEqual(store, base)) {
		return store, hasStore
	}
	return generated, hasGenerated
}

func settingsEqual(a any, b any) bool {
	if aNumber, ok := a.(json.Number); ok {
		bNumber, ok := b.(json.Number)
		if !ok {
			return false
		}
		if aNumber == bNumber {
			return true
		}
		var aRat, bRat big.Rat
		_, aOK := aRat.SetString(string(aNumber))
		_, bOK := bRat.SetString(string(bNumber))
		return aOK && bOK && aRat.Cmp(&bRat) == 0
	}
	if aMap, ok := a.(map[string]any); ok {
		bMap, ok := b.(map[string]any)
		if !ok || len(aMap) != len(bMap) {
			return false
		}
		for key, value := range aMap {
			other, exists := bMap[key]
			if !exists || !settingsEqual(value, other) {
				return false
			}
		}
		return true
	}
	if aArray, ok := a.([]any); ok {
		bArray, ok := b.([]any)
		if !ok || len(aArray) != len(bArray) {
			return false
		}
		for i := range aArray {
			if !settingsEqual(aArray[i], bArray[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
