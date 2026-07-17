// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/model"
)

type authFileCache struct {
	data []byte
	ok   bool
}

func (m authManager) checkSessionFromAuthFiles(authStorage *backend.StoreRef, authFiles []string) bool {
	if authStorage == nil || len(authFiles) == 0 {
		return false
	}
	storageHasSession := false
	if err := m.withStoreLock(authStorage, func() error {
		authFiles, err := backend.ValidateAuthFilePaths(authFiles)
		if err != nil {
			return err
		}
		for _, authFile := range authFiles {
			if m.authStorageFileExists(authStorage, authFile) {
				storageHasSession = true
				break
			}
		}
		return nil
	}); err != nil {
		logx.Warnf("Failed to read auth session files: %v", err)
	}
	return storageHasSession
}

func (m authManager) checkSessionFromConfig(authStorage *backend.StoreRef, cfg model.AuthSessionConfig) (bool, error) {
	if authStorage == nil {
		return false, nil
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = model.AuthSessionModeAny
	}
	if mode != model.AuthSessionModeAny && mode != model.AuthSessionModeAll {
		return false, fmt.Errorf("auth_session.mode must be \"any\" or \"all\"")
	}
	if len(cfg.Checks) == 0 {
		return false, fmt.Errorf("auth_session.checks is empty")
	}

	cache := map[string]authFileCache{}
	var result bool
	if err := m.withStoreLock(authStorage, func() error {
		ok, err := m.evalAuthSessionChecks(authStorage, cfg.Checks, mode, cache)
		if err != nil {
			return err
		}
		result = ok
		return nil
	}); err != nil {
		return false, err
	}
	return result, nil
}

func (m authManager) evalAuthSessionChecks(authStorage *backend.StoreRef, checks []model.AuthSessionCheck, mode string, cache map[string]authFileCache) (bool, error) {
	for i, check := range checks {
		file, err := cleanAuthSessionFilePath(check.File)
		if err != nil {
			return false, fmt.Errorf("auth_session.checks[%d]: %w", i, err)
		}
		ok, err := m.evalAuthSessionCheck(authStorage, file, check, cache, i)
		if err != nil {
			return false, err
		}
		if mode == model.AuthSessionModeAny && ok {
			return true, nil
		}
		if mode == model.AuthSessionModeAll && !ok {
			return false, nil
		}
	}
	return mode == model.AuthSessionModeAll, nil
}

func (m authManager) evalAuthSessionCheck(authStorage *backend.StoreRef, file string, check model.AuthSessionCheck, cache map[string]authFileCache, index int) (bool, error) {
	checkType := strings.ToLower(strings.TrimSpace(check.Type))
	if checkType == "" {
		checkType = "file_exists"
	}
	switch checkType {
	case "file_exists":
		return m.authStorageFileExists(authStorage, file), nil
	case "json_pointer":
		pointer := strings.TrimSpace(check.Pointer)
		if pointer == "" {
			return false, fmt.Errorf("auth_session.checks[%d]: pointer is required for json_pointer", index)
		}
		data, ok := m.readAuthStorageFile(authStorage, file, cache)
		if !ok {
			return false, nil
		}
		var payload any
		if err := json.Unmarshal(data, &payload); err != nil {
			return false, nil
		}
		return jsonPointerExists(payload, pointer), nil
	case "json_pointer_non_null":
		pointer := strings.TrimSpace(check.Pointer)
		if pointer == "" {
			return false, fmt.Errorf("auth_session.checks[%d]: pointer is required for json_pointer_non_null", index)
		}
		data, ok := m.readAuthStorageFile(authStorage, file, cache)
		if !ok {
			return false, nil
		}
		var payload any
		if err := json.Unmarshal(data, &payload); err != nil {
			return false, nil
		}
		return jsonPointerNonNull(payload, pointer), nil
	default:
		return false, fmt.Errorf("auth_session.checks[%d]: unknown type %q", index, check.Type)
	}
}

func cleanAuthSessionFilePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("file is empty")
	}
	cleaned, err := backend.ValidateAuthFilePaths([]string{path})
	if err != nil {
		return "", err
	}
	return cleaned[0], nil
}

func (m authManager) authStorageFileExists(authStorage *backend.StoreRef, relPath string) bool {
	store := m.storage()
	if store == nil || authStorage == nil {
		return false
	}
	_, err := store.ReadFile(context.Background(), authStorage.Key, authStorage.Kind, relPath)
	return err == nil
}

func (m authManager) readAuthStorageFile(authStorage *backend.StoreRef, relPath string, cache map[string]authFileCache) ([]byte, bool) {
	if cached, ok := cache[relPath]; ok {
		return cached.data, cached.ok
	}
	store := m.storage()
	if store == nil || authStorage == nil {
		cache[relPath] = authFileCache{ok: false}
		return nil, false
	}
	data, err := store.ReadFile(context.Background(), authStorage.Key, authStorage.Kind, relPath)
	if err != nil {
		cache[relPath] = authFileCache{ok: false}
		return nil, false
	}
	cache[relPath] = authFileCache{data: data, ok: true}
	return data, true
}

func jsonPointerExists(value any, pointer string) bool {
	_, ok := jsonPointerLookup(value, pointer)
	return ok
}

func jsonPointerNonNull(value any, pointer string) bool {
	v, ok := jsonPointerLookup(value, pointer)
	if !ok {
		return false
	}
	return v != nil
}

func jsonPointerLookup(value any, pointer string) (any, bool) {
	if pointer == "" {
		return value, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := value
	parts := strings.Split(pointer[1:], "/")
	for _, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}
