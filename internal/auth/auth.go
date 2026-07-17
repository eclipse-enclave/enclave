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

// ResolveLayeredSecrets reads secrets files in layer order and merges them.
// Layer 1: global.env (always read). Layer 2: global/<tool>.env (when scope
// is "global" or "both"). Layer 3: projects/<hash>/<tool>.env (when scope is
// "project" or "both"). Later layers override earlier ones.
func ResolveLayeredSecrets(home string, projectHash string, tool string, scope string) (map[string]string, error) {
	result := map[string]string{}

	globalShared, err := ReadAllEnvFromFile(config.HostSecretsGlobalSharedFile(home))
	if err != nil {
		return nil, fmt.Errorf("read global secrets: %w", err)
	}
	for k, v := range globalShared {
		result[k] = v
	}

	if scope == model.SecretsScopeGlobal || scope == model.SecretsScopeBoth {
		perTool, err := ReadAllEnvFromFile(config.HostSecretsGlobalFile(home, tool))
		if err != nil {
			return nil, fmt.Errorf("read global per-tool secrets: %w", err)
		}
		for k, v := range perTool {
			result[k] = v
		}
	}

	if scope == model.SecretsScopeProject || scope == model.SecretsScopeBoth {
		projectTool, err := ReadAllEnvFromFile(config.HostSecretsProjectFile(home, projectHash, tool))
		if err != nil {
			return nil, fmt.Errorf("read project per-tool secrets: %w", err)
		}
		for k, v := range projectTool {
			result[k] = v
		}
	}

	return result, nil
}
