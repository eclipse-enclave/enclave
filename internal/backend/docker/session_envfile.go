// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"enclave/internal/backend"
	"enclave/internal/logx"
)

// secretEnvFile holds environment entries that must stay off the engine argv
// (the A2A tokens); the engine CLI reads it while creating the container.
type secretEnvFile struct {
	path string
	once sync.Once
}

func writeSecretEnvFile(entries []backend.EnvVar) (*secretEnvFile, error) {
	var content strings.Builder
	for _, entry := range entries {
		if entry.Name == "" {
			continue
		}
		// --env-file has no quoting: one KEY=VALUE per line.
		if strings.ContainsAny(entry.Name, "=\n\r") || strings.ContainsAny(entry.Value, "\n\r") {
			return nil, fmt.Errorf("environment entry %q cannot go through an env file", entry.Name)
		}
		content.WriteString(entry.Name + "=" + entry.Value + "\n")
	}
	if content.Len() == 0 {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "enclave-env-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &secretEnvFile{path: path}, nil
}

// remove deletes the file and its private directory; safe to call repeatedly.
func (f *secretEnvFile) remove() {
	if f == nil {
		return
	}
	f.once.Do(func() {
		if err := os.RemoveAll(filepath.Dir(f.path)); err != nil {
			logx.Warnf("Failed to remove session env file %s: %v", f.path, err)
		}
	})
}
