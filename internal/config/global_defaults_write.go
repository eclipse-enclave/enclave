// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteGlobalDefault sets one top-level string key in the global config.json,
// creating the file when it does not exist and preserving every other key.
// It returns the path written.
func WriteGlobalDefault(key string, value string) (string, error) {
	path, err := GlobalConfigPath()
	if err != nil {
		return "", err
	}
	doc := map[string]json.RawMessage{}
	// #nosec G304 -- path is the application's own global config file.
	data, err := os.ReadFile(path)
	switch {
	case err == nil && len(strings.TrimSpace(string(data))) > 0:
		if err := json.Unmarshal(data, &doc); err != nil {
			return path, fmt.Errorf("parse %s: %w", path, err)
		}
	case err != nil && !os.IsNotExist(err):
		return path, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return path, err
	}
	doc[key] = raw
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, err
	}
	return path, os.WriteFile(path, append(out, '\n'), 0o600)
}
