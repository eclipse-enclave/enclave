// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"fmt"
	"io"
	"os"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

// parseEnvLines parses KEY=VALUE pairs from a reader.
func parseEnvLines(r io.Reader) (map[string]string, error) {
	return util.ParseEnv(r)
}

// ReadAllEnvFromFile reads all KEY=VALUE pairs from a .env file.
// Returns nil map and no error if the file does not exist.
func ReadAllEnvFromFile(path string) (map[string]string, error) {
	// #nosec G304 -- path is resolved from trusted secrets file locations.
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return parseEnvLines(file)
}

// SecretsLayer is one secrets file together with the values it contributes.
type SecretsLayer struct {
	Path   string
	Values map[string]string
}

// SecretsLayerPaths lists the secrets files a session of tool reads under
// scope, lowest precedence first:
//
//  1. global.env: every tool, every project (always read)
//  2. global/<tool>.env: one tool, every project ("global" or "both")
//  3. projects/<hash>.env: every tool, one project ("project" or "both")
//  4. projects/<hash>/<tool>.env: one tool, one project ("project" or "both")
func SecretsLayerPaths(home string, projectHash string, tool string, scope string) []string {
	paths := []string{config.HostSecretsGlobalSharedFile(home)}
	if scope == model.SecretsScopeGlobal || scope == model.SecretsScopeBoth {
		paths = append(paths, config.HostSecretsGlobalFile(home, tool))
	}
	if scope == model.SecretsScopeProject || scope == model.SecretsScopeBoth {
		paths = append(paths,
			config.HostSecretsProjectSharedFile(home, projectHash),
			config.HostSecretsProjectFile(home, projectHash, tool))
	}
	return paths
}

// ResolveSecretsLayers reads the secrets files of SecretsLayerPaths in order.
// Later layers override earlier ones. Files that are missing or empty are
// omitted.
func ResolveSecretsLayers(home string, projectHash string, tool string, scope string) ([]SecretsLayer, error) {
	paths := SecretsLayerPaths(home, projectHash, tool, scope)
	layers := make([]SecretsLayer, 0, len(paths))
	for _, path := range paths {
		values, err := ReadAllEnvFromFile(path)
		if err != nil {
			return nil, fmt.Errorf("read secrets file %s: %w", path, err)
		}
		if len(values) == 0 {
			continue
		}
		layers = append(layers, SecretsLayer{Path: path, Values: values})
	}
	return layers, nil
}
