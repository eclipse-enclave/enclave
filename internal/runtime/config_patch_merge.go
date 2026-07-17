// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"enclave/internal/util"
)

type configPatch struct {
	sourcePath   string
	relativePath string
}

func hasConfigPatchFiles(patchDir string) bool {
	patches, err := findConfigPatches(patchDir)
	return err != nil || len(patches) > 0
}

func isIgnoredConfigPatchArtifact(name string) bool {
	lowerName := strings.ToLower(name)
	return name == ".DS_Store" ||
		strings.HasPrefix(name, ".#") ||
		(strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#")) ||
		strings.HasSuffix(name, "~") ||
		strings.HasSuffix(lowerName, ".swp") ||
		strings.HasSuffix(lowerName, ".swo") ||
		strings.HasSuffix(lowerName, ".swx")
}

func findConfigPatches(patchDir string) ([]configPatch, error) {
	if !util.PathExists(patchDir) {
		return nil, nil
	}
	rootDir, err := filepath.EvalSymlinks(patchDir)
	if err != nil {
		return nil, fmt.Errorf("resolve config patch directory %q: %w", patchDir, err)
	}
	rootInfo, err := os.Stat(rootDir)
	if err != nil {
		return nil, fmt.Errorf("stat config patch directory %q: %w", rootDir, err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("config patch path %q is not a directory", patchDir)
	}

	var patches []configPatch
	if err := filepath.WalkDir(rootDir, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || isIgnoredConfigPatchArtifact(entry.Name()) {
			return nil
		}
		info, err := os.Stat(sourcePath)
		if err != nil {
			return fmt.Errorf("stat config patch %q: %w", sourcePath, err)
		}
		if entry.Type()&os.ModeSymlink != 0 && info.IsDir() {
			return fmt.Errorf("symlinked config patch directory %q is not supported", sourcePath)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("config patch %q is not a regular file", sourcePath)
		}
		relativePath, err := filepath.Rel(rootDir, sourcePath)
		if err != nil {
			return fmt.Errorf("resolve config patch path %q: %w", sourcePath, err)
		}
		switch strings.ToLower(filepath.Ext(relativePath)) {
		case ".json", ".toml":
		default:
			return fmt.Errorf("unsupported config patch format for %q", relativePath)
		}
		patches = append(patches, configPatch{sourcePath: sourcePath, relativePath: relativePath})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read config patches from %q: %w", patchDir, err)
	}
	sort.Slice(patches, func(i int, j int) bool {
		return patches[i].relativePath < patches[j].relativePath
	})
	return patches, nil
}

func mergeConfigPatch(basePath string, patchPath string) error {
	var (
		content []byte
		err     error
	)
	switch strings.ToLower(filepath.Ext(basePath)) {
	case ".json":
		content, err = mergeJSONConfig(basePath, patchPath)
	case ".toml":
		content, err = mergeTOMLConfig(basePath, patchPath)
	default:
		return fmt.Errorf("unsupported config patch format for %q", basePath)
	}
	if err != nil {
		return fmt.Errorf("merge config patch %s: %w", patchPath, err)
	}

	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(basePath); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := util.WriteFileAtomic(basePath, content, mode); err != nil {
		return fmt.Errorf("write patched config %s: %w", basePath, err)
	}
	return nil
}

func mergeJSONConfig(basePath string, patchPath string) ([]byte, error) {
	baseValue, err := readJSONValue(basePath)
	if err != nil {
		return nil, fmt.Errorf("read base JSON %s: %w", basePath, err)
	}
	patchValue, err := readJSONValue(patchPath)
	if err != nil {
		return nil, fmt.Errorf("read patch JSON %s: %w", patchPath, err)
	}

	content, err := json.MarshalIndent(mergeJSONValue(baseValue, patchValue), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode merged JSON: %w", err)
	}
	return append(content, '\n'), nil
}

func readJSONValue(path string) (any, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is resolved from trusted host config or patch directories.
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func mergeJSONValue(base any, patch any) any {
	baseMap, baseIsMap := base.(map[string]any)
	patchMap, patchIsMap := patch.(map[string]any)
	if baseIsMap && patchIsMap {
		merged := make(map[string]any, len(baseMap))
		for key, value := range baseMap {
			merged[key] = value
		}
		for key, patchValue := range patchMap {
			if patchValue == nil {
				delete(merged, key)
				continue
			}
			baseValue, exists := merged[key]
			if !exists {
				baseValue = nil
			}
			merged[key] = mergeJSONValue(baseValue, patchValue)
		}
		return merged
	}
	return patch
}

func mergeTOMLConfig(basePath string, patchPath string) ([]byte, error) {
	baseValue, err := readTOMLMap(basePath)
	if err != nil {
		return nil, fmt.Errorf("read base TOML %s: %w", basePath, err)
	}
	patchValue, err := readTOMLMap(patchPath)
	if err != nil {
		return nil, fmt.Errorf("read patch TOML %s: %w", patchPath, err)
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(mergeTOMLMap(baseValue, patchValue)); err != nil {
		return nil, fmt.Errorf("encode merged TOML: %w", err)
	}
	return buf.Bytes(), nil
}

func readTOMLMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is resolved from trusted host config or patch directories.
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if _, err := toml.Decode(string(data), &value); err != nil {
		return nil, err
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, nil
}

func mergeTOMLMap(base map[string]any, patch map[string]any) map[string]any {
	merged := make(map[string]any, len(base))
	for key, value := range base {
		merged[key] = value
	}
	for key, patchValue := range patch {
		if baseValue, exists := merged[key]; exists {
			baseMap, baseIsMap := baseValue.(map[string]any)
			patchMap, patchIsMap := patchValue.(map[string]any)
			if baseIsMap && patchIsMap {
				merged[key] = mergeTOMLMap(baseMap, patchMap)
				continue
			}
		}
		merged[key] = patchValue
	}
	return merged
}
